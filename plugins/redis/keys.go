package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The keyspace, and the line drawn through it.
//
// redis.key.list and redis.key.tree return key names, types and TTLs.
// redis.key.get returns what a key holds, and it is the only capability here
// that does — so it is a write, and it needs a grant naming the key.
//
// Names are walked with SCAN, never KEYS: KEYS blocks the server for the
// whole walk, which on a large keyspace is an outage a listing caused.

const (
	maxKeys      = 1000
	maxTreeNodes = 500
	// scanBatch is the COUNT hint per SCAN call. Redis treats it as a hint
	// and may return more or fewer; the bound that matters is maxKeys.
	scanBatch = 200
	// maxValueItems bounds a collection value. A list with a million entries
	// is a value, and it is not an answer.
	maxValueItems = 100
)

func patternField() plugin.Field {
	return plugin.Field{Name: "pattern", Type: plugin.String, Positional: true, Default: "*",
		Help: "glob to match key names against (user:* , *:session); * walks the whole keyspace"}
}

func keyListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "redis.key.list",
		Summary:    "Key names matching a pattern — never their contents",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Names, types and time to live for every key matching a glob, walked with " +
			"SCAN so the server keeps answering everybody else while it runs.\n\n" +
			"Never a value: this is the read tier, and redis.key.get is where contents live. " +
			"Bounded, and it says when it stopped — a listing that quietly ended at a " +
			"thousand reads exactly like a keyspace with a thousand keys in it.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *client) (view.View, error) {
				return keyListView(ctx, c, req)
			})
		},
	}, patternField(),
		plugin.Field{Name: "limit", Type: plugin.Int, Config: "limit", Default: 200, Min: 1, Max: maxKeys,
			Help: "how many keys to return"})
}

// scanKeys walks SCAN until the cursor comes back to 0 or limit is reached,
// and reports which of the two stopped it.
func scanKeys(ctx context.Context, c *client, pattern string, limit int) (keys []string, truncated bool, verr *view.Error) {
	cursor := "0"
	for {
		r, err := c.do(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(scanBatch))
		if err != nil {
			return nil, false, classify(err, c.addr)
		}
		if len(r.items) != 2 {
			return nil, false, view.Errorf("redis.scan.malformed", "%s answered SCAN with %d items, want 2", c.addr, len(r.items))
		}
		for _, k := range r.items[1].strings() {
			if len(keys) == limit {
				return keys, true, nil
			}
			keys = append(keys, k)
		}
		cursor = r.items[0].text()
		if cursor == "0" {
			sort.Strings(keys)
			return keys, false, nil
		}
	}
}

func keyListView(ctx context.Context, c *client, req plugin.Request) (view.View, error) {
	limit := req.Int("limit")
	keys, truncated, verr := scanKeys(ctx, c, req.String("pattern"), limit)
	if verr != nil {
		return nil, verr
	}
	t := view.Table{Columns: []view.Column{
		{Name: "Key"},
		{Name: "Type"},
		{Name: "TTL"},
	}}
	for _, k := range keys {
		typ, err := c.do(ctx, "TYPE", k)
		if err != nil {
			return nil, classify(err, c.addr)
		}
		ttl, err := c.do(ctx, "TTL", k)
		if err != nil {
			return nil, classify(err, c.addr)
		}
		t.Rows = append(t.Rows, []string{k, typ.text(), ttlText(ttl.num)})
	}
	t.Total = len(t.Rows)
	if len(t.Rows) == 0 {
		return view.Text{Body: fmt.Sprintf("No keys match %q on %s.", req.String("pattern"), c.addr)}, nil
	}
	if truncated {
		// A listing that quietly ended at the limit reads exactly like a
		// keyspace that size. The last row says so, the way etcd's tree does.
		t.Rows = append(t.Rows, []string{"…", "-", fmt.Sprintf("stopped at %d keys; narrow the pattern or raise --limit", limit)})
	}
	return t, nil
}

func ttlText(ttl int64) string {
	switch {
	case ttl == -1:
		return "no expiry"
	case ttl == -2:
		return "gone"
	case ttl < 0:
		return "-"
	default:
		return span(time.Duration(ttl) * time.Second)
	}
}

func keyTreeCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "redis.key.tree",
		Summary:    "The shape of the keyspace in one call — names only",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Redis keys are flat, and everybody names them as paths anyway: " +
			"user:42:session is a hierarchy that only the separator makes visible. This " +
			"walks a pattern once and draws it, with a count at every level.\n\n" +
			"Names and counts only, never a value. Same read tier as redis.key.list.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *client) (view.View, error) {
				return keyTreeView(ctx, c, req)
			})
		},
	}, patternField(),
		plugin.Field{Name: "separator", Type: plugin.String, Config: "separator", Default: ":",
			Help: "the character that separates levels in key names"},
		plugin.Field{Name: "depth", Type: plugin.Int, Config: "depth", Default: 4, Min: 1, Max: 20,
			Help: "how many levels to expand"},
		plugin.Field{Name: "limit", Type: plugin.Int, Config: "limit", Default: maxKeys, Min: 1, Max: 100000,
			Help: "how many keys to walk before stopping"})
}

func keyTreeView(ctx context.Context, c *client, req plugin.Request) (view.View, error) {
	limit := req.Int("limit")
	keys, truncated, verr := scanKeys(ctx, c, req.String("pattern"), limit)
	if verr != nil {
		return nil, verr
	}
	sep := req.String("separator")
	if sep == "" {
		sep = ":"
	}
	root := &treeNode{}
	for _, k := range keys {
		root.insert(splitKey(k, sep))
	}
	w := &treeRender{maxDepth: req.Int("depth"), sep: sep}
	children := w.expand(root, 1)
	detail := fmt.Sprintf("%d keys", root.keys)
	switch {
	case truncated:
		detail += fmt.Sprintf(" — stopped at %d; narrow the pattern or raise --limit", limit)
	case w.stopped != "":
		detail += " — " + w.stopped
	}
	return view.Tree{Roots: []view.Node{{Label: req.String("pattern"), Detail: detail, Children: children}}}, nil
}

func splitKey(key, sep string) []string {
	parts := strings.Split(key, sep)
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

type treeNode struct {
	children map[string]*treeNode
	keys     int
	leaf     bool
}

func (n *treeNode) insert(parts []string) {
	n.keys++
	if len(parts) == 0 {
		return
	}
	if n.children == nil {
		n.children = map[string]*treeNode{}
	}
	c, ok := n.children[parts[0]]
	if !ok {
		c = &treeNode{}
		n.children[parts[0]] = c
	}
	if len(parts) == 1 {
		c.leaf = true
	}
	c.insert(parts[1:])
}

type treeRender struct {
	maxDepth int
	sep      string
	nodes    int
	stopped  string
}

func (w *treeRender) expand(n *treeNode, depth int) []view.Node {
	names := make([]string, 0, len(n.children))
	for name := range n.children {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := n.children[names[i]], n.children[names[j]]
		if a.leaf != b.leaf {
			return b.leaf
		}
		return names[i] < names[j]
	})
	var out []view.Node
	for _, name := range names {
		if w.nodes >= maxTreeNodes {
			w.stopped = fmt.Sprintf("stopped at %d nodes; narrow the pattern", maxTreeNodes)
			return append(out, view.Node{Label: "…", Detail: w.stopped})
		}
		w.nodes++
		c := n.children[name]
		if c.leaf && len(c.children) == 0 {
			out = append(out, view.Node{Label: name})
			continue
		}
		node := view.Node{Label: name + w.sep, Detail: fmt.Sprintf("%d keys", c.keys)}
		if depth >= w.maxDepth {
			node.Detail += " — not expanded, raise --depth"
		} else {
			node.Children = w.expand(c, depth+1)
		}
		out = append(out, node)
	}
	return out
}

func keyGetCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:      "redis.key.get",
		Summary: "What one key holds",
		// Write, and it needs a grant naming it, for what it discloses rather
		// than what it changes: a session store keeps tokens, a cache keeps
		// whatever the application put there, and "read any key" is "read
		// any of that". NeedsGrant is available because this names exactly
		// one key, so a grant can name it too.
		Safety:     plugin.Write,
		NeedsGrant: true,
		Idempotent: true,
		Scope:      "key",
		Description: "The value at one key, whatever its type: a string as itself, a hash as " +
			"its fields, a list, set or sorted set as its members — bounded, and it says " +
			"when it stopped.\n\n" +
			"**Classified write for what it discloses, not what it changes.** A session store " +
			"keeps tokens and a cache keeps whatever the application cached, so reading an " +
			"arbitrary key can be reading somebody's session. It needs a grant naming the key: " +
			"`rta grant allow redis.key.get user:42:session` is a consent somebody can read.\n\n" +
			"The read tier — redis.key.list and redis.key.tree — shows names, types and TTLs, " +
			"which is usually the question and costs none of this.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *client) (view.View, error) {
				return keyGetView(ctx, c, req)
			})
		},
	}, plugin.Field{Name: "key", Type: plugin.String, Positional: true, Required: true,
		Help: "the exact key to read"})
}

func keyGetView(ctx context.Context, c *client, req plugin.Request) (view.View, error) {
	key := req.String("key")
	typ, err := c.do(ctx, "TYPE", key)
	if err != nil {
		return nil, classify(err, c.addr)
	}
	ttl, err := c.do(ctx, "TTL", key)
	if err != nil {
		return nil, classify(err, c.addr)
	}
	pairs := []view.Pair{
		{Key: "key", Value: key},
		{Key: "type", Value: typ.text()},
		{Key: "ttl", Value: ttlText(ttl.num)},
	}
	var value string
	var redacted []string
	switch typ.text() {
	case "none":
		return nil, view.Errorf("redis.key.notfound", "no key %q on %s", key, c.addr).
			WithHint("`rta redis key list <pattern>` shows what exists")
	case "string":
		r, err := c.do(ctx, "GET", key)
		if err != nil {
			return nil, classify(err, c.addr)
		}
		value = r.text()
		pairs = append(pairs, view.Pair{Key: "size", Value: format.Bytes(uint64(len(value)))})
	case "hash":
		r, err := c.do(ctx, "HGETALL", key)
		if err != nil {
			return nil, classify(err, c.addr)
		}
		kv := r.pairs()
		pairs = append(pairs, view.Pair{Key: "fields", Value: strconv.Itoa(len(kv))})
		for i, p := range kv {
			if i == maxValueItems {
				pairs = append(pairs, view.Pair{Key: "…", Value: fmt.Sprintf("%d more fields not shown", len(kv)-maxValueItems)})
				break
			}
			pairs = append(pairs, view.Pair{Key: "field " + p[0], Value: p[1]})
			redacted = append(redacted, "field "+p[0])
		}
		return view.KeyValue{Pairs: pairs, Redacted: redacted}, nil
	case "list":
		r, err := c.do(ctx, "LRANGE", key, "0", strconv.Itoa(maxValueItems))
		if err != nil {
			return nil, classify(err, c.addr)
		}
		n, _ := c.do(ctx, "LLEN", key)
		return collectionView(pairs, r.strings(), n.num), nil
	case "set":
		r, err := c.do(ctx, "SRANDMEMBER", key, strconv.Itoa(maxValueItems+1))
		if err != nil {
			return nil, classify(err, c.addr)
		}
		n, _ := c.do(ctx, "SCARD", key)
		return collectionView(pairs, r.strings(), n.num), nil
	case "zset":
		r, err := c.do(ctx, "ZRANGE", key, "0", strconv.Itoa(maxValueItems), "WITHSCORES")
		if err != nil {
			return nil, classify(err, c.addr)
		}
		items := make([]string, 0, len(r.items)/2)
		for _, p := range r.pairs() {
			items = append(items, p[0]+" ("+p[1]+")")
		}
		n, _ := c.do(ctx, "ZCARD", key)
		return collectionView(pairs, items, n.num), nil
	default:
		return nil, view.Errorf("redis.key.type", "%q is a %s, which this does not render", key, typ.text()).
			WithHint("streams and modules' own types need their own client")
	}
	pairs = append(pairs, view.Pair{Key: "value", Value: value})
	// The value is the whole point of the capability and still must not land
	// in a log or a terminal scrollback by accident. Redacted is what makes
	// every renderer mask it unless somebody asked for it.
	return view.KeyValue{Pairs: pairs, Redacted: []string{"value"}}, nil
}

// collectionView is a list, set or sorted set: the metadata pairs, then the
// members as rows, bounded and honest about the bound.
func collectionView(meta []view.Pair, items []string, total int64) view.View {
	truncated := false
	if len(items) > maxValueItems {
		items = items[:maxValueItems]
		truncated = true
	}
	meta = append(meta, view.Pair{Key: "members", Value: strconv.FormatInt(total, 10)})
	t := view.Table{Columns: []view.Column{{Name: "Member"}}}
	for _, it := range items {
		t.Rows = append(t.Rows, []string{it})
	}
	t.Total = int(total)
	if truncated {
		t.Rows = append(t.Rows, []string{fmt.Sprintf("… first %d of %d members", maxValueItems, total)})
	}
	return view.Sections{Items: []view.Section{
		{ID: "key", Title: "key", View: view.KeyValue{Pairs: meta}},
		{ID: "members", Title: "members", View: t},
	}}
}
