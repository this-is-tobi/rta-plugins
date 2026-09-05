package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "redis.overview",
		Summary:    "Whether this server is healthy, and what it is made of",
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Description: "INFO, read once and graded: memory against maxmemory and what happens at " +
			"the ceiling, when the last RDB was written and how many writes it does not " +
			"cover, whether AOF is on and whether its last rewrite succeeded, the " +
			"replication role and every replica's link, and each database's key count.\n\n" +
			"The memory row is the one to watch. A server at maxmemory with `noeviction` " +
			"refuses every write while answering reads, which looks like a working cache " +
			"from anywhere except here; one with an eviction policy quietly loses keys " +
			"instead, and the evicted count is where that shows.\n\n" +
			"--detail adds the raw INFO sections, for the field this page does not show.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *client) (view.View, error) {
				return overviewView(ctx, c, req)
			})
		},
	})
}

// info is INFO parsed: every `key:value` line under its `# Section` heading.
type info struct {
	sections map[string]map[string]string
	order    []string
}

func (i info) get(key string) string {
	for _, s := range i.sections {
		if v, ok := s[key]; ok {
			return v
		}
	}
	return ""
}

func (i info) int(key string) int64 {
	n, _ := strconv.ParseInt(i.get(key), 10, 64)
	return n
}

func (i info) float(key string) float64 {
	f, _ := strconv.ParseFloat(i.get(key), 64)
	return f
}

func parseInfo(raw string) info {
	out := info{sections: map[string]map[string]string{}}
	section := "Other"
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
		case strings.HasPrefix(line, "# "):
			section = strings.TrimPrefix(line, "# ")
			if _, ok := out.sections[section]; !ok {
				out.sections[section] = map[string]string{}
				out.order = append(out.order, section)
			}
		default:
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			if _, ok := out.sections[section]; !ok {
				out.sections[section] = map[string]string{}
				out.order = append(out.order, section)
			}
			out.sections[section][k] = v
		}
	}
	return out
}

func fetchInfo(ctx context.Context, c *client) (info, *view.Error) {
	r, err := c.do(ctx, "INFO", "all")
	if err != nil {
		return info{}, classify(err, c.addr)
	}
	return parseInfo(r.text()), nil
}

func overviewView(ctx context.Context, c *client, req plugin.Request) (view.View, error) {
	in, verr := fetchInfo(ctx, c)
	if verr != nil {
		return nil, verr
	}
	now := time.Now()

	p := plugin.NewPage(ctx, req)
	p.Put("server", view.KeyValue{Pairs: []view.Pair{
		{Key: "address", Value: c.addr},
		{Key: "version", Value: in.get("redis_version")},
		{Key: "mode", Value: in.get("redis_mode")},
		{Key: "uptime", Value: span(time.Duration(in.int("uptime_in_seconds")) * time.Second)},
		{Key: "clients", Value: in.get("connected_clients") + " connected, " + in.get("blocked_clients") + " blocked"},
		{Key: "ops/s", Value: in.get("instantaneous_ops_per_sec")},
		{Key: "hit ratio", Value: hitRatio(in.int("keyspace_hits"), in.int("keyspace_misses"))},
	}})
	p.Put("memory", memoryTable(in))
	p.Put("persistence", persistenceTable(in, now))
	p.Put("replication", replicationTable(in))
	p.Put("keyspace", keyspaceTable(in))
	if req.Bool("detail") {
		for _, name := range in.order {
			p.Put("info "+strings.ToLower(name), sectionPairs(in.sections[name]))
		}
	}
	return p.View(), nil
}

// memoryTable grades used memory against maxmemory, and says what the server
// does at the ceiling — the fact that decides whether "95%" is a warning or
// an outage in progress.
//
// **Use % is blank when maxmemory is 0.** That is the default, and it means
// no ceiling: the server takes what the OS gives until the OS kills it. A
// share of zero would render as 0%, the colour of a server with nothing in
// it, on exactly the server whose memory nobody is watching.
func memoryTable(in info) view.Table {
	used, max := in.int("used_memory"), in.int("maxmemory")
	use := "-"
	if share, ok := usageShare(used, max); ok {
		use = strconv.FormatFloat(share, 'f', 1, 64) + "%"
	}
	maxText := "not set"
	if max > 0 {
		maxText = format.Bytes(uint64(max))
	}
	policy := in.get("maxmemory_policy")
	if max == 0 {
		policy += " (no ceiling to apply it at)"
	}
	return view.Table{
		Columns: []view.Column{
			{Name: "Used", Kind: view.KindBytes},
			{Name: "Max", Kind: view.KindBytes},
			{Name: "Use %", Kind: view.KindUsage},
			{Name: "RSS", Kind: view.KindBytes},
			{Name: "Fragmentation"},
			{Name: "At the ceiling"},
			{Name: "Evicted", Kind: view.KindNumber},
		},
		Rows: [][]string{{
			format.Bytes(uint64(used)), maxText, use,
			format.Bytes(uint64(in.int("used_memory_rss"))),
			strconv.FormatFloat(in.float("mem_fragmentation_ratio"), 'f', 2, 64),
			policy,
			in.get("evicted_keys"),
		}},
		Total: 1,
	}
}

// usageShare rounds once so the number printed is the number graded.
func usageShare(used, max int64) (float64, bool) {
	if max <= 0 || used < 0 {
		return 0, false
	}
	return math.Round(float64(used)/float64(max)*1000) / 10, true
}

// persistenceTable is the honest half of "is this backed up": where the last
// RDB went and how old it is, how many writes it does not cover, and whether
// AOF is on. A client cannot pull the file; it can say whether one exists.
func persistenceTable(in info, now time.Time) view.Table {
	lastSave := time.Unix(in.int("rdb_last_save_time"), 0)
	age := "-"
	if in.int("rdb_last_save_time") > 0 {
		age = span(now.Sub(lastSave).Truncate(time.Second)) + " ago"
	}
	rdbStatus := in.get("rdb_last_bgsave_status")
	if in.get("rdb_bgsave_in_progress") == "1" {
		rdbStatus = "in progress"
	}
	aof := "off"
	aofStatus := "-"
	if in.get("aof_enabled") == "1" {
		aof = "on"
		aofStatus = in.get("aof_last_bgrewrite_status")
		if in.get("aof_rewrite_in_progress") == "1" {
			aofStatus = "rewriting"
		}
	}
	return view.Table{
		Columns: []view.Column{
			{Name: "Last RDB"},
			{Name: "Writes since", Kind: view.KindNumber},
			{Name: "RDB status", Kind: view.KindStatus},
			{Name: "AOF"},
			{Name: "AOF status", Kind: view.KindStatus},
			{Name: "Loading", Kind: view.KindStatus},
		},
		Rows: [][]string{{
			age, in.get("rdb_changes_since_last_save"), rdbStatus, aof, aofStatus, loadingText(in),
		}},
		Total: 1,
	}
}

func loadingText(in info) string {
	if in.get("loading") == "1" {
		return "loading"
	}
	return "ok"
}

// replicationTable is the role and, on a primary, every replica's link; on a
// replica, the primary it follows and whether the link is up.
func replicationTable(in info) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Role"},
		{Name: "Peer"},
		{Name: "Link", Kind: view.KindStatus},
		{Name: "Offset", Kind: view.KindNumber},
		{Name: "Lag"},
	}}
	role := in.get("role")
	switch role {
	case "slave", "replica":
		link := in.get("master_link_status")
		lag := "-"
		if s := in.get("master_last_io_seconds_ago"); s != "" {
			lag = s + "s since last I/O"
		}
		t.Rows = append(t.Rows, []string{"replica",
			in.get("master_host") + ":" + in.get("master_port"), link, in.get("slave_repl_offset"), lag})
	default:
		n := int(in.int("connected_slaves"))
		if n == 0 {
			t.Rows = append(t.Rows, []string{"primary", "no replicas", "-", in.get("master_repl_offset"), "-"})
		}
		for i := 0; i < n; i++ {
			raw := in.get("slave" + strconv.Itoa(i))
			f := fieldsOf(raw)
			t.Rows = append(t.Rows, []string{"primary",
				f["ip"] + ":" + f["port"], f["state"], f["offset"], f["lag"] + "s"})
		}
	}
	t.Total = len(t.Rows)
	return t
}

// fieldsOf reads the `k=v,k=v` shape INFO uses for replicas and databases.
func fieldsOf(raw string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		if k, v, ok := strings.Cut(part, "="); ok {
			out[k] = v
		}
	}
	return out
}

func keyspaceTable(in info) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "DB"},
		{Name: "Keys", Kind: view.KindNumber},
		{Name: "With TTL", Kind: view.KindNumber},
		{Name: "Avg TTL"},
	}}
	ks := in.sections["Keyspace"]
	names := make([]string, 0, len(ks))
	for name := range ks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := fieldsOf(ks[name])
		ttl := "-"
		if ms, err := strconv.ParseInt(f["avg_ttl"], 10, 64); err == nil && ms > 0 {
			ttl = span(time.Duration(ms) * time.Millisecond)
		}
		t.Rows = append(t.Rows, []string{name, f["keys"], f["expires"], ttl})
	}
	t.Total = len(t.Rows)
	if len(t.Rows) == 0 {
		t.Rows = [][]string{{"-", "0", "0", "-"}}
		t.Total = 1
	}
	return t
}

// span renders a duration the way a person reads one: the two largest units
// that matter, so "3d 4h" rather than "76h12m5s".
func span(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d >= 24*time.Hour:
		days := int(d / (24 * time.Hour))
		return fmt.Sprintf("%dd %dh", days, int(d%(24*time.Hour)/time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	case d >= time.Minute:
		return fmt.Sprintf("%dm %ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	case d >= time.Second:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d == 0:
		return "0s"
	case d >= time.Millisecond:
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	default:
		return fmt.Sprintf("%dµs", d/time.Microsecond)
	}
}

func hitRatio(hits, misses int64) string {
	if hits+misses == 0 {
		return "no lookups yet"
	}
	return fmt.Sprintf("%.1f%% (%d hits, %d misses)", float64(hits)/float64(hits+misses)*100, hits, misses)
}

func sectionPairs(s map[string]string) view.KeyValue {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kv := view.KeyValue{}
	for _, k := range keys {
		kv.Pairs = append(kv.Pairs, view.Pair{Key: k, Value: s[k]})
	}
	return kv
}

func clientListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "redis.client.list",
		Summary:    "Who is connected, from where, and what each connection is doing",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "CLIENT LIST as a table: address, name, age, idle time, the last command " +
			"and the database — the view that answers \"who is holding a thousand " +
			"connections open\" and \"what is that client blocked on\".",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *client) (view.View, error) {
				r, err := c.do(ctx, "CLIENT", "LIST")
				if err != nil {
					return nil, classify(err, c.addr)
				}
				return clientTable(r.text()), nil
			})
		},
	})
}

func clientTable(raw string) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "ID"},
		{Name: "Address"},
		{Name: "Name"},
		{Name: "Age", Kind: view.KindDuration},
		{Name: "Idle", Kind: view.KindDuration},
		{Name: "DB"},
		{Name: "Last command"},
	}}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		f := map[string]string{}
		for _, part := range strings.Fields(line) {
			if k, v, ok := strings.Cut(part, "="); ok {
				f[k] = v
			}
		}
		name := f["name"]
		if name == "" {
			name = "-"
		}
		t.Rows = append(t.Rows, []string{f["id"], f["addr"], name,
			seconds(f["age"]), seconds(f["idle"]), f["db"], f["cmd"]})
	}
	t.Total = len(t.Rows)
	return t
}

func seconds(s string) string {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return s
	}
	return span(time.Duration(n) * time.Second)
}

func clusterCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "redis.cluster",
		Summary:    "The cluster as this node sees it: state, slots, and every node's role and health",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "CLUSTER INFO and CLUSTER NODES from one node. A node that is not in a " +
			"cluster says so rather than failing.\n\n" +
			"The state row is the one that matters: `fail` means some slot has no reachable " +
			"primary and the cluster refuses writes to it, which is the outage the per-node " +
			"rows below explain.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *client) (view.View, error) {
				return clusterView(ctx, c, req)
			})
		},
	})
}

func clusterView(ctx context.Context, c *client, req plugin.Request) (view.View, error) {
	infoReply, err := c.do(ctx, "CLUSTER", "INFO")
	if err != nil {
		var srv *serverError
		if asServerError(err, &srv) && strings.Contains(srv.msg, "cluster support disabled") {
			return view.Text{Body: c.addr + " is a standalone server — cluster mode is off. " +
				"`rta redis overview` shows its replication instead."}, nil
		}
		return nil, classify(err, c.addr)
	}
	state := map[string]string{}
	for _, line := range strings.Split(infoReply.text(), "\n") {
		if k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), ":"); ok {
			state[k] = v
		}
	}
	p := plugin.NewPage(ctx, req)
	p.Put("state", view.Table{
		Columns: []view.Column{
			{Name: "State", Kind: view.KindStatus},
			{Name: "Slots assigned", Kind: view.KindNumber},
			{Name: "Slots ok", Kind: view.KindNumber},
			{Name: "Slots failing", Kind: view.KindNumber},
			{Name: "Known nodes", Kind: view.KindNumber},
			{Name: "Epoch", Kind: view.KindNumber},
		},
		Rows: [][]string{{state["cluster_state"], state["cluster_slots_assigned"], state["cluster_slots_ok"],
			state["cluster_slots_fail"], state["cluster_known_nodes"], state["cluster_current_epoch"]}},
		Total: 1,
	})

	nodesReply, err := c.do(ctx, "CLUSTER", "NODES")
	if err != nil {
		return nil, classify(err, c.addr)
	}
	p.Put("nodes", nodesTable(nodesReply.text()))
	return p.View(), nil
}

// nodesTable reads CLUSTER NODES: one line per node, space-separated —
// id, address, flags, primary id, ping sent, pong received, epoch, link
// state, then the slot ranges.
func nodesTable(raw string) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "ID"},
		{Name: "Address"},
		{Name: "Role"},
		{Name: "Health", Kind: view.KindStatus},
		{Name: "Replica of"},
		{Name: "Slots"},
	}}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		flags := strings.Split(f[2], ",")
		role, health := "replica", "ok"
		for _, fl := range flags {
			switch fl {
			case "master":
				role = "primary"
			case "fail":
				health = "fail"
			case "fail?":
				health = "pending — suspected failed"
			case "myself":
				role += " (this node)"
			}
		}
		if f[7] != "connected" {
			health = "down — link " + f[7]
		}
		replicaOf := "-"
		if f[3] != "-" {
			replicaOf = f[3][:min(8, len(f[3]))]
		}
		slots := "-"
		if len(f) > 8 {
			slots = strings.Join(f[8:], " ")
		}
		addr, _, _ := strings.Cut(f[1], "@")
		t.Rows = append(t.Rows, []string{f[0][:min(8, len(f[0]))], addr, role, health, replicaOf, slots})
	}
	t.Total = len(t.Rows)
	return t
}
