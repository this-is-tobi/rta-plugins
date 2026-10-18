package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	stdnet "net"
	"os"
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
		Keywords:   []string{"info", "stats", "version", "replication", "uptime"},
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Description: "The state of this server in a few rows, worst first. `status` leads and is `ok`, or " +
			"`warn` or `fail` followed by the worst fact: memory against maxmemory and what happens at " +
			"the ceiling, clients against maxclients, whether the last RDB save and the last AOF " +
			"rewrite succeeded, and the replication link. Then each of those in a line, the " +
			"evictions and the slow log's length when either is not zero, and last the role, the " +
			"version and the uptime. It is the default sections of INFO and the length of the slow " +
			"log: a user without `+slowlog|len` gets that row saying so, and the rest.\n\n" +
			"Memory is the row to watch. A server at maxmemory with `noeviction` refuses every " +
			"write while answering reads, which looks like a working cache from anywhere except " +
			"here; one with an eviction policy quietly loses keys instead, and the evicted count is " +
			"where that shows. A server with no maxmemory has no ceiling to be near, and the row " +
			"says what it uses.\n\n" +
			"`detail` adds INFO in full, graded: the server's figures, memory with its policy and " +
			"fragmentation, persistence, each database's key count and the raw sections. Against a " +
			"primary the replication table has a row per replica: its acknowledged offset, the bytes " +
			"of the stream it has not acknowledged, and whether a reconnect now could still resume. " +
			"A replica behind the replication backlog is graded, because its next dropped " +
			"connection costs a full copy of the dataset, and so is one silent for 10 seconds, the " +
			"lag at which a primary with min-replicas-to-write refuses writes. A replica shows its " +
			"link and the progress of a full sync; how far behind it is, only its primary can say. " +
			"The stream section holds the replication id, which a promotion changes, and the " +
			"backlog. The hit ratio counts since the server started, or its counters were reset.",
		Agent: "A few rows, worst first: `status` is `ok`, or `warn` or `fail` and the worst fact. Then " +
			"memory against maxmemory, clients against maxclients, the last RDB save and AOF rewrite, the " +
			"replication link, evictions and the slow log's length when not zero, and the role, version " +
			"and uptime. Memory is the row to watch: at maxmemory with `noeviction` every write is " +
			"refused while reads answer; with an eviction policy keys are lost quietly. `detail` adds " +
			"INFO in full, graded: a row per replica (offset, bytes unacknowledged, whether it could " +
			"resume), the hit ratio since the server started, key counts and the raw sections.",
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

// fetchInfo is INFO, in its default sections for the compact answer and in all
// of them for the detail. The default sections hold every figure the compact
// rows read and weigh a tenth of what INFO all does on a busy server, where the
// per-command statistics and the latency histograms are most of the text.
func fetchInfo(ctx context.Context, c *client, all bool) (info, *view.Error) {
	args := []string{"INFO"}
	if all {
		args = append(args, "all")
	}
	r, err := c.do(ctx, args...)
	if err != nil {
		return info{}, classify(err, c.addr, c.req)
	}
	return parseInfo(r.text()), nil
}

// versionText is the server's version as it names itself. Valkey keeps
// redis_version at the 7.2.4 it forked from, for the clients that gate on one,
// and puts its own in valkey_version.
func versionText(in info) string {
	if v := in.get("valkey_version"); v != "" {
		return "valkey " + v + " (answers as redis " + in.get("redis_version") + ")"
	}
	return in.get("redis_version")
}

// modeText is standalone, sentinel or cluster, under the key redis gives it,
// or under Valkey's own.
func modeText(in info) string {
	if m := in.get("redis_mode"); m != "" {
		return m
	}
	return in.get("server_mode")
}

// overviewView is the compact rows, or with detail the page that opens with
// them: a tile is the first eight lines of the compact answer, and a tile
// cannot ask for the detail, so what has to be seen at a glance is in the rows
// and everything else is a request for it.
func overviewView(ctx context.Context, c *client, req plugin.Request) (view.View, error) {
	detail := req.Bool("detail")
	in, verr := fetchInfo(ctx, c, detail)
	if verr != nil {
		return nil, verr
	}
	now := time.Now()
	summary := compactOverview(in, now, readSlowLog(ctx, c))
	if !detail {
		return summary, nil
	}

	p := plugin.NewPage(ctx, req)
	p.Put("summary", summary)
	p.Put("server", view.KeyValue{Pairs: []view.Pair{
		{Key: "address", Value: req.Reached(c.addr)},
		{Key: "version", Value: versionText(in)},
		{Key: "mode", Value: modeText(in)},
		{Key: "uptime", Value: uptime(serverUptime(in))},
		{Key: "clients", Value: in.get("connected_clients") + " connected, " + in.get("blocked_clients") + " blocked"},
		{Key: "ops/s", Value: in.get("instantaneous_ops_per_sec")},
		{Key: "hit ratio", Value: hitRatio(in.int("keyspace_hits"), in.int("keyspace_misses")) + countingSince(in, now)},
	}})
	p.Put("memory", memoryTable(in))
	p.Put("persistence", persistenceTable(in, now))
	p.Put("replication", replicationTable(in))
	if stream, ok := streamPairs(in, c.sf); ok {
		p.Put("replication stream", stream)
	}
	p.Put("keyspace", keyspaceTable(in))
	for _, name := range in.order {
		p.Put("info "+strings.ToLower(name), sectionPairs(in.sections[name]))
	}
	return p.View(), nil
}

// grade is how bad a fact is, in the words the status row leads with and the
// theme colours: nothing, warn, fail.
type grade int

const (
	_ grade = iota
	gradeWarn
	gradeFail
)

func (g grade) word() string {
	switch g {
	case gradeWarn:
		return "warn"
	case gradeFail:
		return "fail"
	}
	return "ok"
}

// finding is one thing wrong, in a few words, for the status row.
type finding struct {
	grade grade
	text  string
}

// verdict is the status row: ok, or the worst finding with its grade. Of
// several equally bad, the first stands, and the rows are built in the order
// that puts loading, then capacity, then durability, then replication first.
func verdict(found []finding) string {
	var worst *finding
	for i := range found {
		if worst == nil || found[i].grade > worst.grade {
			worst = &found[i]
		}
	}
	if worst == nil {
		return "ok"
	}
	return worst.grade.word() + " — " + worst.text
}

// slowLog is the length of the slow log, or why it could not be read.
type slowLog struct {
	entries int64
	unread  string
}

// readSlowLog asks for the slow log's length, which is a count and none of the
// command lines the entries hold (redis.slowlog is a write for those). It is
// the one request the compact answer makes beyond INFO, and a user without
// +slowlog|len is told so in a row of its own rather than given a page that
// reads as though the log were empty.
func readSlowLog(ctx context.Context, c *client) slowLog {
	r, err := c.do(ctx, "SLOWLOG", "LEN")
	if err != nil {
		return slowLog{unread: unreadReason(err)}
	}
	return slowLog{entries: r.num}
}

// unreadReason is why a read failed, short enough for a row: the answer a
// server gives for a refusal is a sentence naming the user and the command.
func unreadReason(err error) string {
	var srv *serverError
	if asServerError(err, &srv) {
		switch code, _, _ := strings.Cut(srv.msg, " "); code {
		case "NOPERM":
			return "the ACL user may not run SLOWLOG LEN"
		case "LOADING":
			return "the server is still loading its dataset"
		}
		head, rest := format.Head(srv.msg, 48)
		if rest > 0 {
			head += "…"
		}
		return head
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return "no answer in time"
	}
	return "the connection failed"
}

// compactOverview is the answer a tile and an agent read by default, built from
// INFO and the slow log's length alone.
//
// **The order is the order of a glance.** The verdict, then what is the case
// of the server's capacity and durability, then what changed, and what the
// server is last — a tile cuts from the bottom, and the version of a server
// that is out of memory is the last thing its reader needs.
//
// **A capacity is graded only where it has a wall.** A server at maxmemory with
// noeviction refuses every write and is a failure; one with an eviction policy
// is a cache working as configured at any fullness, and its evictions are in
// the row below for whoever wants to know. A server with no maxmemory has no
// ceiling to be near and is not graded on its size.
func compactOverview(in info, now time.Time, slow slowLog) view.KeyValue {
	var found []finding
	var rows []view.Pair
	add := func(key, value string, fs ...finding) {
		if value == "" {
			return
		}
		rows = append(rows, view.Pair{Key: key, Value: value})
		found = append(found, fs...)
	}

	if in.get("loading") == "1" {
		found = append(found, finding{gradeWarn, "loading its dataset"})
	}
	memory, fs := memoryRow(in)
	add("memory", memory, fs...)
	clients, fs := clientsRow(in)
	add("clients", clients, fs...)
	persistence, fs := persistenceRow(in, now)
	add("persistence", persistence, fs...)
	replication, fs := replicationRow(in)
	add("replication", replication, fs...)
	pressure, unread := pressureRows(in, slow)
	add("pressure", pressure)
	if unread != "" {
		add("slow log", "could not be read — "+unread, finding{gradeWarn, "slow log could not be read"})
	}
	add("state", stateRow(in))

	return view.KeyValue{Pairs: append([]view.Pair{{Key: "status", Value: verdict(found)}}, rows...)}
}

func memoryRow(in info) (string, []finding) {
	used, max := in.int("used_memory"), in.int("maxmemory")
	share, ok := usageShare(used, max)
	if !ok {
		return format.Bytes(used) + " used, no maxmemory set", nil
	}
	policy := in.get("maxmemory_policy")
	text := strconv.FormatFloat(share, 'f', 1, 64) + "% of " + format.Bytes(max) + ", " + policy
	if policy != "noeviction" {
		return text, nil
	}
	switch {
	case share >= 100:
		return text, []finding{{gradeFail, "memory is full and noeviction refuses writes"}}
	case share >= view.UsageWarn:
		return text, []finding{{gradeWarn, "memory at " + strconv.FormatFloat(share, 'f', 1, 64) + "% of maxmemory, noeviction"}}
	}
	return text, nil
}

// clientsRow is the connections against the limit. maxclients is in INFO from
// redis 7.0; before it the row says what is connected and does not ask the
// server a second question to find the limit.
func clientsRow(in info) (string, []finding) {
	connected := in.int("connected_clients")
	text := strconv.FormatInt(connected, 10) + " connected"
	var found []finding
	if limit := in.int("maxclients"); limit > 0 {
		text = strconv.FormatInt(connected, 10) + " of " + strconv.FormatInt(limit, 10)
		if share, _ := usageShare(connected, limit); share >= view.UsageWarn {
			found = append(found, finding{gradeWarn, strconv.FormatFloat(share, 'f', 1, 64) + "% of maxclients"})
		}
	}
	if rejected := in.int("rejected_connections"); rejected > 0 {
		text += ", " + strconv.FormatInt(rejected, 10) + " rejected since start"
	}
	return text, found
}

// persistenceRow is how old the last RDB save is and whether it, and the last
// AOF rewrite, succeeded. The writes an RDB does not cover are said only when
// the save is failing: while it works they are the next save's.
func persistenceRow(in info, now time.Time) (string, []finding) {
	status := in.get("rdb_last_bgsave_status")
	text := "RDB " + status
	var found []finding
	if saved := in.int("rdb_last_save_time"); saved > 0 {
		text += " " + span(now.Sub(time.Unix(saved, 0)).Truncate(time.Second)) + " ago"
	} else {
		text += ", never saved"
	}
	if status == "err" {
		text += ", " + in.get("rdb_changes_since_last_save") + " writes since"
		found = append(found, finding{gradeFail, "the last RDB save failed"})
	}
	if in.get("rdb_bgsave_in_progress") == "1" {
		text += ", saving now"
	}
	if in.get("aof_enabled") == "1" {
		aof := in.get("aof_last_bgrewrite_status")
		text += ", AOF " + aof
		if aof == "err" {
			found = append(found, finding{gradeFail, "the last AOF rewrite failed"})
		}
	}
	return text, found
}

// replicationRow is the role, the link and how far the other end is, in the
// words the replication table grades with. Against a replica the lag is how
// long ago its primary last spoke; against a primary it is the worst of its
// replicas' acknowledgements, and the row counts the ones that need attention
// because the table that says which is in the detail.
func replicationRow(in info) (string, []finding) {
	if role := in.get("role"); role == "slave" || role == "replica" {
		return replicaRow(in)
	}
	n := int(in.int("connected_slaves"))
	if n == 0 {
		return "primary, no replicas", nil
	}
	var found []finding
	attention, worstLag := 0, int64(0)
	for i, row := range downstreamRows(in) {
		if lag, ok := parseInt(fieldsOf(in.get("slave" + strconv.Itoa(i)))["lag"]); ok {
			worstLag = max(worstLag, lag)
		}
		if row[2] == "ok" {
			continue
		}
		attention++
		_, why, _ := strings.Cut(row[2], " — ")
		found = append(found, finding{gradeWarn, "a replica: " + why})
	}
	text := "primary, " + format.CountOf(n, "replica")
	if attention > 0 {
		text += ", " + strconv.Itoa(attention) + " " + format.Plural(attention, "needs", "need") + " attention"
	}
	return text + ", worst lag " + span(secondsOf(worstLag)), found
}

func replicaRow(in info) (string, []finding) {
	of := "replica of " + stdnet.JoinHostPort(in.get("master_host"), in.get("master_port"))
	link := upstreamLink(in)
	state, why, _ := strings.Cut(link, " — ")
	switch state {
	case "ok":
		return of + ", link up, last heard " + span(secondsOf(in.int("master_last_io_seconds_ago"))) + " ago", nil
	case "pending":
		return of + ", " + why, []finding{{gradeWarn, "link to its primary: " + why}}
	case "warn":
		return of + ", link up, " + why, []finding{{gradeWarn, "link to its primary: " + why}}
	}
	down := of + ", link down"
	if since, ok := parseInt(in.get("master_link_down_since_seconds")); ok && since >= 0 {
		down += " for " + span(secondsOf(since))
	}
	return down, []finding{{gradeFail, "the link to its primary is down"}}
}

// pressureRows are the evictions and the slow log's length as one row, only
// when either is not zero, and the reason the slow log could not be read as the
// second return. Both evictions and a long slow log are counted from the
// server's start or its last CONFIG RESETSTAT, which the row says.
func pressureRows(in info, slow slowLog) (row, unread string) {
	var parts []string
	if evicted := in.int("evicted_keys"); evicted > 0 {
		parts = append(parts, format.CountOf(int(evicted), "key")+" evicted since start")
	}
	if slow.entries > 0 {
		parts = append(parts, format.CountOf(int(slow.entries), "slow log entry"))
	}
	return strings.Join(parts, " · "), slow.unread
}

// stateRow is what the server is: its role, its version and how long it has
// been up. Last, because it is the one row that is the same on a healthy server
// and a dying one.
func stateRow(in info) string {
	role := "primary"
	if r := in.get("role"); r == "slave" || r == "replica" {
		role = "replica"
	}
	version := in.get("redis_version")
	if v := in.get("valkey_version"); v != "" {
		version = "valkey " + v
	}
	return role + " · " + version + " · up " + uptime(serverUptime(in))
}

func serverUptime(in info) time.Duration {
	return time.Duration(in.int("uptime_in_seconds")) * time.Second
}

// countingSince says since when the server's counters have run, which the
// hit ratio is a ratio of. INFO does not carry the moment CONFIG RESETSTAT last
// ran, so the date is the start of the server, which is when they began unless
// somebody reset them — and the text says which it knows.
func countingSince(in info, now time.Time) string {
	if in.int("keyspace_hits")+in.int("keyspace_misses") == 0 || in.int("uptime_in_seconds") == 0 {
		return ""
	}
	return " since " + now.Add(-serverUptime(in)).Format("2006-01-02") + " (server start; CONFIG RESETSTAT restarts the count)"
}

// uptime renders how long a server has been up as days and hours, then hours
// and minutes, then minutes: "14d 2h", "5h 30m", "14m". The same spelling in
// every plugin that shows one, so that a tile of one reads like a tile of the
// next.
func uptime(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
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
		maxText = format.Bytes(max)
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
			format.Bytes(used), maxText, use,
			format.Bytes(in.int("used_memory_rss")),
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
	// int is 64 bits on every target this plugin is released for, so a
	// counter that has run past two billion lookups converts whole.
	return fmt.Sprintf("%.1f%% (%s, %s)", float64(hits)/float64(hits+misses)*100,
		format.CountOf(int(hits), "hit"), format.CountOf(int(misses), "miss"))
}

func sectionPairs(s map[string]string) view.KeyValue {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kv := view.KeyValue{}
	for _, k := range keys {
		kv.Pairs = append(kv.Pairs, view.Pair{Key: plugin.ListedName(k), Value: s[k]})
	}
	return kv
}

func clientListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "redis.client.list",
		Summary:    "Who is connected, from where, and what each connection is doing",
		Keywords:   []string{"connections", "sessions", "monitor", "idle"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "CLIENT LIST as a table: address, name, age, idle time, the last command " +
			"and the database — the view that answers \"who is holding a thousand " +
			"connections open\" and \"what is that client blocked on\".",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *client) (view.View, error) {
				r, err := c.do(ctx, "CLIENT", "LIST")
				if err != nil {
					return nil, classify(err, c.addr, c.req)
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
		name := "-"
		if f["name"] != "" {
			name = plugin.ListedName(f["name"])
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
		Keywords:   []string{"slots", "shards", "masters", "replicas", "failover"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "CLUSTER INFO, CLUSTER NODES and CLUSTER SHARDS from one node. A node that " +
			"is not in a cluster says so rather than failing.\n\n" +
			"The state row is the one that matters: `fail` means some slot has no reachable " +
			"primary and the cluster refuses writes to it, which is the outage the per-node " +
			"rows below explain.\n\n" +
			"Each node's replication offset, and how many bytes each replica is behind its " +
			"primary, come from CLUSTER SHARDS (redis 7.0 and later; an ACL user needs " +
			"`+cluster|shards`). They are what the nodes learned of each other from the " +
			"cluster bus, a heartbeat old, so a few bytes is noise and a replica that is " +
			"thousands behind is the finding; `redis.overview` against a primary reads the " +
			"acknowledged offsets exactly. Where the subcommand is not available the table " +
			"is printed without those columns and says why.",
		Agent: "CLUSTER INFO, CLUSTER NODES and CLUSTER SHARDS from one node; a node that is not in a cluster " +
			"says so. The state row matters most: `fail` means some slot has no reachable primary and the " +
			"cluster refuses writes to it, which the per-node rows explain. Each node's replication offset " +
			"and each replica's bytes behind its primary come from CLUSTER SHARDS (redis 7.0 or later; an " +
			"ACL user needs `+cluster|shards`), a heartbeat old: a few bytes is noise, thousands is the " +
			"finding. Without that subcommand the columns are left out and the page says why.",
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
			return view.Text{Body: c.reached + " is a standalone server — cluster mode is off. " +
				nextCall(c.req, "redis.overview") + " shows its replication instead."}, nil
		}
		return nil, classify(err, c.addr, c.req)
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
		return nil, classify(err, c.addr, c.req)
	}
	positions, why := clusterPositions(ctx, c)
	p.Put("nodes", nodesTable(nodesReply.text(), positions))
	if why != "" {
		p.Put("offsets", view.Text{Body: why})
	}
	return p.View(), nil
}

// nodesTable reads CLUSTER NODES: one line per node, space-separated —
// id, address, flags, primary id, ping sent, pong received, epoch, link
// state, then the slot ranges. Where CLUSTER SHARDS answered, each node's
// offset and a replica's distance behind its primary sit beside them, and a
// node it reports loading is not "ok".
func nodesTable(raw string, positions map[string]position) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "ID"},
		{Name: "Address"},
		{Name: "Role"},
		{Name: "Health", Kind: view.KindStatus},
		{Name: "Replica of"},
		{Name: "Offset", Kind: view.KindNumber},
		{Name: "Behind", Kind: view.KindBytes},
		{Name: "Slots"},
	}}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		flags := strings.Split(f[2], ",")
		role, health := "replica", "ok"
		myself := false
		for _, fl := range flags {
			switch fl {
			case "master":
				role = "primary"
			case "fail":
				health = "fail"
			case "fail?":
				health = "pending — suspected failed"
			case "myself":
				myself = true
			}
		}
		// After the loop, because the flags come as "myself,master": marking
		// the role where `myself` was read lost the mark to the `master` after
		// it, so no primary was ever shown as the node being asked.
		if myself {
			role += " (this node)"
		}
		if f[7] != "connected" {
			health = "down — link " + f[7]
		}
		offset, behind := "-", "-"
		if pos, ok := positions[f[0]]; ok {
			offset = strconv.FormatInt(pos.offset, 10)
			if pos.replica && pos.behind >= 0 {
				behind = format.Bytes(pos.behind)
			}
			if health == "ok" && pos.health == "loading" {
				health = "pending — loading its dataset"
			}
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
		t.Rows = append(t.Rows, []string{f[0][:min(8, len(f[0]))], addr, role, health, replicaOf, offset, behind, slots})
	}
	t.Total = len(t.Rows)
	return t
}
