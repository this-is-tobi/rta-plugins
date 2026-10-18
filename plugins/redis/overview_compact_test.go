package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The compact form is what a dashboard tile shows and what an agent reads by
// default: the verdict first, then state, then capacity, then what changed, and
// the identity of the server last. A tile is the first eight lines of it, so a
// row that matters cannot sit below a row that does not.

// healthyInfo is the default sections of INFO from a redis 7.4 primary under
// load, with the lines the compact form reads. Each test names the lines it
// changes, and `extra` adds the ones a state needs that the base does not have.
func healthyInfo(changes map[string]string, extra ...string) string {
	base := []string{
		"# Server", "redis_version:7.4.2", "redis_mode:standalone", "uptime_in_seconds:360244",
		"# Clients", "connected_clients:212", "maxclients:10000", "blocked_clients:0",
		"# Memory", "used_memory:734003200", "used_memory_rss:801112064", "maxmemory:1073741824",
		"maxmemory_policy:allkeys-lru", "mem_fragmentation_ratio:1.09",
		"# Persistence", "loading:0", "rdb_changes_since_last_save:42", "rdb_bgsave_in_progress:0",
		"rdb_last_save_time:" + fmt.Sprint(time.Now().Add(-6*time.Minute).Unix()),
		"rdb_last_bgsave_status:ok", "aof_enabled:1", "aof_rewrite_in_progress:0", "aof_last_bgrewrite_status:ok",
		"# Stats", "instantaneous_ops_per_sec:3812", "rejected_connections:0", "evicted_keys:0",
		"keyspace_hits:90", "keyspace_misses:10",
		"# Replication", "role:master", "connected_slaves:0", "master_repl_offset:0",
		"# Keyspace", "db0:keys=1500,expires=200,avg_ttl=3600000",
	}
	for i, line := range base {
		if key, _, ok := strings.Cut(line, ":"); ok {
			if v, changed := changes[key]; changed {
				base[i] = key + ":" + v
			}
		}
	}
	return strings.Join(append(base, extra...), "\r\n") + "\r\n"
}

// compactOf runs the overview against a server that answers INFO with info and
// SLOWLOG LEN with slow (a RESP reply), and returns the rows it answered with.
func compactOf(t *testing.T, info, slow string) (view.KeyValue, *fakeServer) {
	t.Helper()
	srv := newFakeServer(t, map[string]string{"INFO": bulk(info), "SLOWLOG LEN": slow})
	v, err := run(t, "redis.overview", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("the compact overview is a %s, want the rows alone", view.TypeOf(v))
	}
	return kv, srv
}

func keysOf(kv view.KeyValue) []string {
	keys := make([]string, 0, len(kv.Pairs))
	for _, p := range kv.Pairs {
		keys = append(keys, p.Key)
	}
	return keys
}

// A tile a third of a screen wide still reads: no row is longer than a line of
// an 80-column terminal, and there are no more rows than a tile holds.
func assertFitsATile(t *testing.T, kv view.KeyValue) {
	t.Helper()
	if len(kv.Pairs) > 8 {
		t.Errorf("%d rows, a tile holds eight: %v", len(kv.Pairs), kv.Pairs)
	}
	for _, p := range kv.Pairs {
		if n := utf8.RuneCountInString(p.Key) + 2 + utf8.RuneCountInString(p.Value); n > 80 || strings.Contains(p.Value, "\n") {
			t.Errorf("row %q is %d columns or wraps: %q", p.Key, n, p.Value)
		}
	}
}

func TestAHealthyServerIsOkFirstAndItsIdentityLast(t *testing.T) {
	kv, srv := compactOf(t, healthyInfo(nil), ":0\r\n")

	if got, want := keysOf(kv), []string{"status", "memory", "clients", "persistence", "replication", "state"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for key, want := range map[string]string{
		"status":      "ok",
		"memory":      "68.4% of 1.0 GiB, allkeys-lru",
		"clients":     "212 of 10000",
		"replication": "primary, no replicas",
		"state":       "primary · 7.4.2 · up 4d 4h",
	} {
		if got := pairValue(kv, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := pairValue(kv, "persistence"); !strings.HasPrefix(got, "RDB ok 6m ") || !strings.HasSuffix(got, " ago, AOF ok") {
		t.Errorf("persistence = %q, want the last save's age and both statuses", got)
	}
	assertFitsATile(t, kv)

	// Plain INFO is the default sections only; the commands and latency
	// histograms INFO all adds are most of its weight and no row reads them.
	if got, want := strings.Join(srv.seen, " | "), "PING | INFO | SLOWLOG LEN"; got != want {
		t.Errorf("requests = %q, want %q", got, want)
	}
}

// The status word is one the theme grades, so a tile colours it: ok, warn or
// fail, then the worst fact in a few words.
func TestTheStatusRowNamesTheWorstFactAndLeadsWithAGradedWord(t *testing.T) {
	for name, tc := range map[string]struct {
		info    string
		slow    string
		verdict string
	}{
		"full with noeviction refuses writes": {
			healthyInfo(map[string]string{"used_memory": "1073741824", "maxmemory_policy": "noeviction"}), ":0\r\n",
			"fail — memory is full and noeviction refuses writes"},
		"close to the ceiling with noeviction": {
			healthyInfo(map[string]string{"used_memory": "966367641", "maxmemory_policy": "noeviction"}), ":0\r\n",
			"warn — memory at 90.0% of maxmemory, noeviction"},
		"a cache at its ceiling is a cache": {
			healthyInfo(map[string]string{"used_memory": "1073741824"}), ":0\r\n", "ok"},
		"a failed RDB save": {
			healthyInfo(map[string]string{"rdb_last_bgsave_status": "err"}), ":0\r\n",
			"fail — the last RDB save failed"},
		"a failed AOF rewrite": {
			healthyInfo(map[string]string{"aof_last_bgrewrite_status": "err"}), ":0\r\n",
			"fail — the last AOF rewrite failed"},
		"nearly out of client slots": {
			healthyInfo(map[string]string{"connected_clients": "9870"}), ":0\r\n",
			"warn — 98.7% of maxclients"},
		"loading its dataset": {
			healthyInfo(map[string]string{"loading": "1"}), ":0\r\n",
			"warn — loading its dataset"},
		"a slow log that could not be read": {
			healthyInfo(nil), "-NOPERM this user has no permissions to run the 'slowlog|len' command\r\n",
			"warn — slow log could not be read"},
	} {
		t.Run(name, func(t *testing.T) {
			kv, _ := compactOf(t, tc.info, tc.slow)
			if got := pairValue(kv, "status"); got != tc.verdict {
				t.Errorf("status = %q, want %q", got, tc.verdict)
			}
			if kv.Pairs[0].Key != "status" {
				t.Errorf("the first row is %q, want status", kv.Pairs[0].Key)
			}
			assertFitsATile(t, kv)
		})
	}
}

// The worst fact wins, and among equals the one capacity and safety put first.
func TestTheWorstOfSeveralFactsIsTheVerdict(t *testing.T) {
	kv, _ := compactOf(t, healthyInfo(map[string]string{
		"used_memory": "966367641", "maxmemory_policy": "noeviction", "rdb_last_bgsave_status": "err",
	}), ":0\r\n")
	if got := pairValue(kv, "status"); got != "fail — the last RDB save failed" {
		t.Errorf("status = %q, want the failure over the warning", got)
	}
}

func TestWithoutAMaxmemoryTheMemoryRowSaysWhatIsUsedAndThatNothingBoundsIt(t *testing.T) {
	kv, _ := compactOf(t, healthyInfo(map[string]string{"maxmemory": "0", "used_memory": "1288490189"}), ":0\r\n")
	if got := pairValue(kv, "memory"); got != "1.2 GiB used, no maxmemory set" {
		t.Errorf("memory = %q", got)
	}
	if got := pairValue(kv, "status"); got != "ok" {
		t.Errorf("status = %q, want a server with no ceiling left ungraded rather than guessed at", got)
	}
}

// maxclients is in INFO from redis 7.0; before it the row says what it knows
// rather than asking the server a second question.
func TestAServerThatDoesNotReportMaxclientsIsNotAskedForIt(t *testing.T) {
	kv, srv := compactOf(t, strings.ReplaceAll(healthyInfo(nil), "maxclients:10000\r\n", ""), ":0\r\n")
	if got := pairValue(kv, "clients"); got != "212 connected" {
		t.Errorf("clients = %q", got)
	}
	for _, cmd := range srv.seen {
		if strings.HasPrefix(cmd, "CONFIG") {
			t.Errorf("asked %q for what INFO did not carry", cmd)
		}
	}
}

func TestRejectedConnectionsAreSaidBesideTheClients(t *testing.T) {
	kv, _ := compactOf(t, healthyInfo(map[string]string{"rejected_connections": "412"}), ":0\r\n")
	if got := pairValue(kv, "clients"); got != "212 of 10000, 412 rejected since start" {
		t.Errorf("clients = %q", got)
	}
}

func TestAFailedSaveShowsHowOldTheLastGoodOneIsAndWhatItDoesNotCover(t *testing.T) {
	kv, _ := compactOf(t, healthyInfo(map[string]string{
		"rdb_last_bgsave_status": "err", "rdb_changes_since_last_save": "48211233",
		"rdb_last_save_time": fmt.Sprint(time.Now().Add(-73 * time.Hour).Unix()),
	}), ":0\r\n")
	if got := pairValue(kv, "persistence"); !strings.HasPrefix(got, "RDB err 3d 1h ago, 48211233 writes since, AOF ok") {
		t.Errorf("persistence = %q", got)
	}
}

func TestAServerThatNeverSavedSaysNever(t *testing.T) {
	kv, _ := compactOf(t, healthyInfo(map[string]string{"rdb_last_save_time": "0", "aof_enabled": "0"}), ":0\r\n")
	if got := pairValue(kv, "persistence"); got != "RDB ok, never saved" {
		t.Errorf("persistence = %q", got)
	}
}

// Evictions and the slow log are one row, and only when there is something to
// say: a counter at zero is not news.
func TestEvictionsAndTheSlowLogShareARowWhenEitherIsNotZero(t *testing.T) {
	for name, tc := range map[string]struct {
		evicted, slow, want string
	}{
		"both":      {"1204", ":37\r\n", "1204 keys evicted since start · 37 slow log entries"},
		"evictions": {"1", ":0\r\n", "1 key evicted since start"},
		"slow log":  {"0", ":1\r\n", "1 slow log entry"},
	} {
		t.Run(name, func(t *testing.T) {
			kv, _ := compactOf(t, healthyInfo(map[string]string{"evicted_keys": tc.evicted}), tc.slow)
			if got := pairValue(kv, "pressure"); got != tc.want {
				t.Errorf("pressure = %q, want %q", got, tc.want)
			}
			if got := keysOf(kv); got[len(got)-1] != "state" || got[len(got)-2] != "pressure" {
				t.Errorf("rows = %v, want pressure just before the identity row", got)
			}
			assertFitsATile(t, kv)
		})
	}

	kv, _ := compactOf(t, healthyInfo(nil), ":0\r\n")
	if got := pairValue(kv, "pressure"); got != "" {
		t.Errorf("pressure = %q on a server with nothing evicted and nothing slow, want no row", got)
	}
}

// A role that may not read the slow log gets the row that says so, beside the
// evictions it could read, and the rest of the page is unharmed.
func TestASlowLogTheCredentialCannotReadIsSaidAndTheRestStands(t *testing.T) {
	for name, tc := range map[string]struct{ reply, why string }{
		"no ACL permission": {"-NOPERM this user has no permissions to run the 'slowlog|len' command\r\n", "the ACL user may not run SLOWLOG LEN"},
		"still loading":     {"-LOADING Redis is loading the dataset in memory\r\n", "the server is still loading its dataset"},
	} {
		t.Run(name, func(t *testing.T) {
			kv, _ := compactOf(t, healthyInfo(map[string]string{"evicted_keys": "9"}), tc.reply)
			if got, want := pairValue(kv, "slow log"), "could not be read — "+tc.why; got != want {
				t.Errorf("slow log = %q, want %q", got, want)
			}
			if got := pairValue(kv, "pressure"); got != "9 keys evicted since start" {
				t.Errorf("pressure = %q, want the evictions that could be read", got)
			}
			if pairValue(kv, "memory") == "" || pairValue(kv, "state") == "" {
				t.Errorf("rows = %v, want the others kept", kv.Pairs)
			}
			assertFitsATile(t, kv)
		})
	}
}

func TestAReplicaShowsItsLinkAndHowRecentlyItHeardFromThePrimary(t *testing.T) {
	kv, _ := compactOf(t, healthyInfo(map[string]string{"role": "slave"},
		"master_host:dbwp-oth-redis-p", "master_port:6379", "master_link_status:up",
		"master_last_io_seconds_ago:4", "master_sync_in_progress:0"), ":0\r\n")
	if got := pairValue(kv, "replication"); got != "replica of dbwp-oth-redis-p:6379, link up, last heard 4s ago" {
		t.Errorf("replication = %q", got)
	}
	if got := pairValue(kv, "state"); !strings.HasPrefix(got, "replica · 7.4.2 · up ") {
		t.Errorf("state = %q", got)
	}
	if got := pairValue(kv, "status"); got != "ok" {
		t.Errorf("status = %q", got)
	}
	assertFitsATile(t, kv)
}

func TestAReplicaWhoseLinkIsDownIsAFailure(t *testing.T) {
	kv, _ := compactOf(t, healthyInfo(map[string]string{"role": "slave"},
		"master_host:dbwp-oth-redis-p", "master_port:6379", "master_link_status:down",
		"master_last_io_seconds_ago:-1", "master_sync_in_progress:0", "master_link_down_since_seconds:4"), ":0\r\n")
	if got := pairValue(kv, "status"); got != "fail — the link to its primary is down" {
		t.Errorf("status = %q", got)
	}
	if got := pairValue(kv, "replication"); got != "replica of dbwp-oth-redis-p:6379, link down for 4s" {
		t.Errorf("replication = %q", got)
	}
}

func TestAReplicaInItsFullSyncIsRebuildingNotBroken(t *testing.T) {
	kv, _ := compactOf(t, healthyInfo(map[string]string{"role": "slave"},
		"master_host:dbwp-oth-redis-p", "master_port:6379", "master_link_status:down",
		"master_sync_in_progress:1", "master_sync_total_bytes:1000", "master_sync_read_bytes:420"), ":0\r\n")
	if got := pairValue(kv, "status"); got != "warn — link to its primary: full sync 42%" {
		t.Errorf("status = %q", got)
	}
}

func TestAPrimaryCountsItsReplicasAndSaysHowManyNeedAttention(t *testing.T) {
	kv, _ := compactOf(t, healthyInfo(map[string]string{"connected_slaves": "3"},
		"slave0:ip=172.20.0.9,port=6379,state=online,offset=197085,lag=1",
		"slave1:ip=172.20.0.3,port=6379,state=online,offset=197085,lag=12",
		"slave2:ip=172.20.0.10,port=6379,state=wait_bgsave,offset=0,lag=0",
		"master_repl_offset:197085", "repl_backlog_active:1", "repl_backlog_size:1048576",
		"repl_backlog_first_byte_offset:1", "repl_backlog_histlen:197085"), ":0\r\n")
	if got := pairValue(kv, "replication"); got != "primary, 3 replicas, 2 need attention, worst lag 12s" {
		t.Errorf("replication = %q", got)
	}
	if got := pairValue(kv, "status"); got != "warn — a replica: silent for 12s" {
		t.Errorf("status = %q", got)
	}
	kv, _ = compactOf(t, healthyInfo(map[string]string{"connected_slaves": "1"},
		"slave0:ip=172.20.0.3,port=6379,state=online,offset=358,lag=1", "master_repl_offset:63237",
		"repl_backlog_active:1", "repl_backlog_size:16384", "repl_backlog_first_byte_offset:40881",
		"repl_backlog_histlen:22357"), ":0\r\n")
	if got := pairValue(kv, "replication"); got != "primary, 1 replica, 1 needs attention, worst lag 1s" {
		t.Errorf("replication = %q", got)
	}
	if got := pairValue(kv, "status"); got != "warn — a replica: behind the backlog, a reconnect now means a full resync" {
		t.Errorf("status = %q", got)
	}
}

func TestAnEmptyServerIsOkAndSaysSo(t *testing.T) {
	kv, _ := compactOf(t, healthyInfo(map[string]string{
		"uptime_in_seconds": "3", "connected_clients": "1", "used_memory": "1048576", "maxmemory": "0",
		"rdb_last_save_time": "0", "aof_enabled": "0", "keyspace_hits": "0", "keyspace_misses": "0",
	}), ":0\r\n")
	if got := pairValue(kv, "status"); got != "ok" {
		t.Errorf("status = %q", got)
	}
	if got := pairValue(kv, "state"); got != "primary · 7.4.2 · up 3s" {
		t.Errorf("state = %q", got)
	}
	assertFitsATile(t, kv)
}

func TestValkeyIsNamedInTheIdentityRow(t *testing.T) {
	info := strings.Replace(healthyInfo(nil), "redis_version:7.4.2\r\nredis_mode:standalone",
		"redis_version:7.2.4\r\nserver_name:valkey\r\nvalkey_version:8.1.10\r\nserver_mode:cluster", 1)
	kv, _ := compactOf(t, info, ":0\r\n")
	if got := pairValue(kv, "state"); got != "primary · valkey 8.1.10 · up 4d 4h" {
		t.Errorf("state = %q", got)
	}
}

// A server that is not there, or that refuses the credential, is the error it
// always was: there is no page to put a verdict on.
func TestADegradedServerIsNotAPage(t *testing.T) {
	srv := newFakeServer(t, map[string]string{"INFO": "-NOPERM this user has no permissions to run the 'info' command\r\n"})
	_, err := run(t, "redis.overview", srv, nil)
	if ve := view.AsError(err, "x"); ve.Code != "redis.denied" {
		t.Errorf("a user without +info = %+v, want redis.denied", ve)
	}
}

// The detail page is everything the default page was, behind --detail, with
// the compact rows as its first section.
func TestTheDetailPageOpensWithTheCompactRowsAndKeepsEverythingElse(t *testing.T) {
	srv := newFakeServer(t, map[string]string{"INFO all": bulk(sampleInfo), "SLOWLOG LEN": ":0\r\n"})
	v, err := run(t, "redis.overview", srv, map[string]any{"detail": true})
	if err != nil {
		t.Fatal(err)
	}
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("detail is a %s, want a page", view.TypeOf(v))
	}
	var titles []string
	for _, it := range page.Items {
		titles = append(titles, it.Title)
	}
	want := []string{"summary", "server", "memory", "persistence", "replication", "replication stream", "keyspace"}
	if strings.Join(titles[:len(want)], ",") != strings.Join(want, ",") {
		t.Errorf("sections = %v, want %v then the raw INFO sections", titles, want)
	}
	if got := pairValue(sectionOf(t, page, "summary").(view.KeyValue), "status"); got == "" {
		t.Error("the summary section has no status row")
	}
	server := sectionOf(t, page, "server").(view.KeyValue)
	if pairValue(server, "address") == "" || pairValue(server, "version") != "7.2.4" || pairValue(server, "ops/s") != "120" {
		t.Errorf("server = %v, want the address and the figures the old default carried", server.Pairs)
	}
}

// A ratio that has run since the server started is not the last minute's, and
// the page says since when.
func TestTheHitRatioSaysSinceWhen(t *testing.T) {
	srv := newFakeServer(t, map[string]string{"INFO all": bulk(sampleInfo), "SLOWLOG LEN": ":0\r\n"})
	v, err := run(t, "redis.overview", srv, map[string]any{"detail": true})
	if err != nil {
		t.Fatal(err)
	}
	server := sectionOf(t, v.(view.Sections), "server").(view.KeyValue)
	start := time.Now().Add(-90061 * time.Second).Format("2006-01-02")
	want := "90.0% (90 hits, 10 misses) since " + start + " (server start; CONFIG RESETSTAT restarts the count)"
	if got := pairValue(server, "hit ratio"); got != want {
		t.Errorf("hit ratio = %q, want %q", got, want)
	}
}

// One way to spell how long a server has been up: days and hours, then hours
// and minutes, then minutes, so that 14d 2h on one plugin's tile is 14d 2h on
// the next.
func TestUptimeIsSpelledOneWay(t *testing.T) {
	for in, want := range map[time.Duration]string{
		3 * time.Second:                 "3s",
		59 * time.Second:                "59s",
		time.Minute:                     "1m",
		14*time.Minute + 59*time.Second: "14m",
		time.Hour:                       "1h 0m",
		5*time.Hour + 30*time.Minute:    "5h 30m",
		23*time.Hour + 59*time.Minute:   "23h 59m",
		24 * time.Hour:                  "1d 0h",
		14*24*time.Hour + 2*time.Hour + 5*time.Minute: "14d 2h",
	} {
		if got := uptime(in); got != want {
			t.Errorf("uptime(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestTheOverviewDeclaresADetailedPageAndSaysSoToAgents(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.ID != "redis.overview" {
			continue
		}
		if !c.Detailed {
			t.Error("redis.overview is not Detailed")
		}
		for name, text := range map[string]string{"description": c.Description, "agent": c.Agent} {
			if !strings.Contains(text, "`detail`") || !strings.Contains(text, "status") {
				t.Errorf("the %s text does not say what the default answer is and what `detail` adds: %q", name, text)
			}
		}
		return
	}
	t.Fatal("no redis.overview")
}
