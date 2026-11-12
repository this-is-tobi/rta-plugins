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
		ID:       "redis.key.list",
		Summary:  "Key names matching a pattern — never their contents",
		Keywords: []string{"keys", "scan", "glob", "ls"},
		Examples: []plugin.Example{
			{Title: "the session keys", Inputs: map[string]any{"pattern": "session:*", "limit": 50}},
		},
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
		plugin.Field{Name: "limit", Short: "n", Type: plugin.Int, Config: "limit", Default: 200, Min: 1, Max: maxKeys,
			Help: "how many keys to return"})
}

// scanKeys walks SCAN until the cursor comes back to 0 or limit is reached,
// and reports which of the two stopped it.
//
// A key SCAN returns again is skipped: what SCAN promises is that every key
// present for the whole walk comes back at least once, and a repeat counted
// as a key of its own inflated every count drawn from it and spent the limit
// on keys already held. The keys are sorted whichever stopped the walk, so a
// list cut short reads in the order a whole one does.
func scanKeys(ctx context.Context, c *client, pattern string, limit int) (keys []string, truncated bool, verr *view.Error) {
	cursor := "0"
	seen := map[string]bool{}
	for {
		r, err := c.do(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(scanBatch))
		if err != nil {
			return nil, false, classify(err, c.addr, c.req)
		}
		if len(r.items) != 2 {
			return nil, false, view.Errorf("redis.scan.malformed", "%s answered SCAN with %s, want 2", c.reached, format.CountOf(len(r.items), "item"))
		}
		for _, k := range r.items[1].strings() {
			if seen[k] {
				continue
			}
			if len(keys) == limit {
				sort.Strings(keys)
				return keys, true, nil
			}
			seen[k] = true
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
			return nil, classify(err, c.addr, c.req)
		}
		ttl, err := c.do(ctx, "TTL", k)
		if err != nil {
			return nil, classify(err, c.addr, c.req)
		}
		t.Rows = append(t.Rows, []string{plugin.ListedName(k), typ.text(), ttlText(ttl.num)})
	}
	t.Total = len(t.Rows)
	t.Empty = "no keys match " + quotedName(req.String("pattern"))
	if truncated {
		// A listing that quietly ended at the limit reads exactly like a
		// keyspace that size. The last row says so, the way etcd's tree does.
		t.Rows = append(t.Rows, []string{"…", "-", "stopped at " + format.CountOf(limit, "key") + "; narrow the pattern or raise " +
			req.Surface().InputName("limit")})
	}
	return t, nil
}

// quotedName is a key as a message names it: in quotes, as %q always put it,
// and in ListedName's spelling when the key holds a character a reader would
// not see as itself — a Braille blank or a zero-width mark, which %q leaves
// as they are, so that "prod" and a "prod" with one after it read alike.
func quotedName(name string) string {
	if listed := plugin.ListedName(name); listed != name {
		return listed
	}
	return strconv.Quote(name)
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
		ID:       "redis.key.tree",
		Summary:  "The shape of the keyspace in one call — names only",
		Keywords: []string{"hierarchy", "namespaces", "structure", "ls"},
		Examples: []plugin.Example{
			{Title: "the keyspace two levels deep", Inputs: map[string]any{"depth": 2}},
		},
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
		plugin.Field{Name: "depth", Short: "d", Type: plugin.Int, Config: "depth", Default: 4, Min: 1, Max: 20,
			Help: "how many levels to expand"},
		plugin.Field{Name: "limit", Short: "n", Type: plugin.Int, Config: "limit", Default: maxKeys, Min: 1, Max: 100000,
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
	w := &treeRender{maxDepth: req.Int("depth"), sep: sep, sf: req.Surface()}
	children := w.expand(root, 1)
	detail := format.CountOf(root.keys, "key")
	switch {
	case truncated:
		detail += fmt.Sprintf(" — stopped at %d; narrow the pattern or raise %s", limit, req.Surface().InputName("limit"))
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
	children map[segment]*treeNode
	keys     int
}

// segment is one level's name, and whether a key ends there. A key that is
// also a level (user:42 beside user:42:sessions) is two children of one name: keyed by
// name alone they were one node, drawn as the level only, which counted the
// key among the keys under it.
type segment struct {
	name string
	leaf bool
}

func (n *treeNode) insert(parts []string) {
	n.keys++
	if len(parts) == 0 {
		return
	}
	if n.children == nil {
		n.children = map[segment]*treeNode{}
	}
	s := segment{name: parts[0], leaf: len(parts) == 1}
	c, ok := n.children[s]
	if !ok {
		c = &treeNode{}
		n.children[s] = c
	}
	c.insert(parts[1:])
}

type treeRender struct {
	maxDepth int
	sep      string
	nodes    int
	stopped  string
	sf       plugin.Surface
}

func (w *treeRender) expand(n *treeNode, depth int) []view.Node {
	segs := make([]segment, 0, len(n.children))
	for s := range n.children {
		segs = append(segs, s)
	}
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].leaf != segs[j].leaf {
			return segs[j].leaf
		}
		return segs[i].name < segs[j].name
	})
	var out []view.Node
	for _, s := range segs {
		if w.nodes >= maxTreeNodes {
			w.stopped = fmt.Sprintf("stopped at %d nodes; narrow the pattern", maxTreeNodes)
			return append(out, view.Node{Label: "…", Detail: w.stopped})
		}
		w.nodes++
		c := n.children[s]
		// The segment is the name a stranger chose and the separator is the
		// caller's, so only the segment is quoted: a folder reads `"odd\x1b":`
		// and stays a folder, as fs's tree draws one.
		listed := plugin.ListedName(s.name)
		if s.leaf {
			out = append(out, view.Node{Label: listed})
			continue
		}
		node := view.Node{Label: listed + w.sep, Detail: format.CountOf(c.keys, "key")}
		if depth >= w.maxDepth {
			node.Detail += " — not expanded, raise " + w.sf.InputName("depth")
		} else {
			node.Children = w.expand(c, depth+1)
		}
		out = append(out, node)
	}
	return out
}

func keyGetCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:       "redis.key.get",
		Summary:  "What one key holds",
		Keywords: []string{"value", "hash", "string", "session", "secret"},
		Examples: []plugin.Example{
			{Title: "one key's value", Inputs: map[string]any{"key": "session:42"}},
		},
		// Write, and it needs a grant naming it, for what it discloses rather
		// than what it changes: a session store keeps tokens, a cache keeps
		// whatever the application put there, and "read any key" is "read
		// any of that". NeedsGrant is available because this names exactly
		// one key, so a grant can name it too.
		Safety:     plugin.Write,
		NeedsGrant: true,
		Idempotent: true,
		Scope:      "key",
		// The call whose answer is the value: it says so, and the value comes
		// back as stored. The grant naming the key is the control, and a mask on
		// top of it was a grant that bought an agent ••••••.
		Reveals: true,
		Description: "The value at one key, whatever its type: a string as itself, a hash as " +
			"its fields, a list, set or sorted set as its members — bounded, and it says " +
			"when it stopped.\n\n" +
			"**What it holds comes back as stored, on every surface** — a hash's field values and a " +
			"collection's members included, none of it masked, and an agent holding a grant for " +
			"the key has it in its context from then on. The read tier — redis.key.list and " +
			"redis.key.tree — shows names, types and TTLs instead, which is usually the question " +
			"and costs none of this.\n\n" +
			"**Classified write for what it discloses, not what it changes.** A session store " +
			"keeps tokens and a cache keeps whatever the application cached, so reading an " +
			"arbitrary key can be reading somebody's session.",
		Agent: "The value at one key, whatever its type: a string as itself, a hash as its fields, a list, set " +
			"or sorted set as its members, bounded, and it says when it stopped. `key` is the exact key. " +
			"Needs a grant naming the key.",
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
		return nil, classify(err, c.addr, c.req)
	}
	ttl, err := c.do(ctx, "TTL", key)
	if err != nil {
		return nil, classify(err, c.addr, c.req)
	}
	pairs := []view.Pair{
		{Key: "key", Value: plugin.ListedName(key)},
		{Key: "type", Value: typ.text()},
		{Key: "ttl", Value: ttlText(ttl.num)},
	}
	var value string
	switch typ.text() {
	case "none":
		return nil, view.Errorf("redis.key.notfound", "no key %s on %s", quotedName(key), c.reached).
			WithHint("`" + c.sf.Call("redis.key.list", append([]plugin.Arg{
				{Name: "pattern", Value: plugin.Placeholder("pattern"), Positional: true}}, reachArgs(req)...)...) +
				"` shows what exists").WithInput("key")
	case "string":
		r, err := c.do(ctx, "GET", key)
		if err != nil {
			return nil, classify(err, c.addr, c.req)
		}
		value = r.text()
		pairs = append(pairs, view.Pair{Key: "size", Value: format.Bytes(len(value))})
	case "hash":
		r, err := c.do(ctx, "HGETALL", key)
		if err != nil {
			return nil, classify(err, c.addr, c.req)
		}
		kv := r.pairs()
		pairs = append(pairs, view.Pair{Key: "fields", Value: strconv.Itoa(len(kv))})
		for i, p := range kv {
			if i == maxValueItems {
				pairs = append(pairs, view.Pair{Key: "…", Value: format.CountOf(len(kv)-maxValueItems, "more field") + " not shown"})
				break
			}
			pairs = append(pairs, view.Pair{Key: "field " + plugin.ListedName(p[0]), Value: p[1]})
		}
		return view.KeyValue{Pairs: pairs}, nil
	case "list":
		r, err := c.do(ctx, "LRANGE", key, "0", strconv.Itoa(maxValueItems))
		if err != nil {
			return nil, classify(err, c.addr, c.req)
		}
		n, _ := c.do(ctx, "LLEN", key)
		return collectionView(pairs, r.strings(), n.num), nil
	case "set":
		r, err := c.do(ctx, "SRANDMEMBER", key, strconv.Itoa(maxValueItems+1))
		if err != nil {
			return nil, classify(err, c.addr, c.req)
		}
		n, _ := c.do(ctx, "SCARD", key)
		return collectionView(pairs, r.strings(), n.num), nil
	case "zset":
		r, err := c.do(ctx, "ZRANGE", key, "0", strconv.Itoa(maxValueItems), "WITHSCORES")
		if err != nil {
			return nil, classify(err, c.addr, c.req)
		}
		items := make([]string, 0, len(r.items)/2)
		for _, p := range r.pairs() {
			items = append(items, p[0]+" ("+p[1]+")")
		}
		n, _ := c.do(ctx, "ZCARD", key)
		return collectionView(pairs, items, n.num), nil
	default:
		return nil, view.Errorf("redis.key.type", "%s is a %s, which this does not render", quotedName(key), typ.text()).
			WithHint("streams and modules' own types need their own client")
	}
	pairs = append(pairs, view.Pair{Key: "value", Value: value})
	return view.KeyValue{Pairs: pairs}, nil
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
