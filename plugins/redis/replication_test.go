package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Every fixture below is the Replication section of a real redis 7.4.11,
// captured from containers set up as the test says. Nothing is edited but the
// one line a test names.

// A primary and its three replicas, one of them still in its full sync.
const infoPrimaryThreeReplicas = "# Replication\r\nrole:master\r\nconnected_slaves:3\r\n" +
	"slave0:ip=172.20.0.9,port=6379,state=online,offset=197085,lag=1\r\n" +
	"slave1:ip=172.20.0.3,port=6379,state=online,offset=197085,lag=1\r\n" +
	"slave2:ip=172.20.0.10,port=6379,state=wait_bgsave,offset=0,lag=0\r\n" +
	"master_failover_state:no-failover\r\nmaster_replid:ffd4b443b40f29e7f5a0f45722bc5a4bf3ffdd4e\r\n" +
	"master_replid2:0000000000000000000000000000000000000000\r\nmaster_repl_offset:197085\r\nsecond_repl_offset:-1\r\n" +
	"repl_backlog_active:1\r\nrepl_backlog_size:1048576\r\nrepl_backlog_first_byte_offset:1\r\nrepl_backlog_histlen:197085\r\n"

// A replica that stopped answering while 60 KiB were written, behind a 16 KiB
// backlog: its offset 358 is long gone from the window starting at 40881.
const infoPrimaryPastBacklog = "# Replication\r\nrole:master\r\nconnected_slaves:1\r\n" +
	"slave0:ip=172.20.0.3,port=6379,state=online,offset=358,lag=12\r\nmaster_failover_state:no-failover\r\n" +
	"master_replid:967868a588395341af8c7fff434b1f37c297b2e9\r\nmaster_replid2:0000000000000000000000000000000000000000\r\n" +
	"master_repl_offset:63237\r\nsecond_repl_offset:-1\r\nrepl_backlog_active:1\r\nrepl_backlog_size:16384\r\n" +
	"repl_backlog_first_byte_offset:40881\r\nrepl_backlog_histlen:22357\r\n"

// The same stalled replica before the window had moved past it: still behind,
// still able to resume.
const infoPrimaryBehindInBacklog = "# Replication\r\nrole:master\r\nconnected_slaves:1\r\n" +
	"slave0:ip=172.20.0.3,port=6379,state=online,offset=358,lag=1\r\nmaster_failover_state:no-failover\r\n" +
	"master_replid:967868a588395341af8c7fff434b1f37c297b2e9\r\nmaster_replid2:0000000000000000000000000000000000000000\r\n" +
	"master_repl_offset:27302\r\nsecond_repl_offset:-1\r\nrepl_backlog_active:1\r\nrepl_backlog_size:16384\r\n" +
	"repl_backlog_first_byte_offset:1\r\nrepl_backlog_histlen:27302\r\n"

const infoReplicaUp = "# Replication\r\nrole:slave\r\nmaster_host:dbwp-oth-redis-p\r\nmaster_port:6379\r\n" +
	"master_link_status:up\r\nmaster_last_io_seconds_ago:4\r\nmaster_sync_in_progress:0\r\n" +
	"slave_read_repl_offset:50\r\nslave_repl_offset:50\r\nslave_priority:100\r\nslave_read_only:1\r\n" +
	"replica_announced:1\r\nconnected_slaves:0\r\nmaster_failover_state:no-failover\r\n" +
	"master_replid:967868a588395341af8c7fff434b1f37c297b2e9\r\nmaster_replid2:0000000000000000000000000000000000000000\r\n" +
	"master_repl_offset:50\r\nsecond_repl_offset:-1\r\nrepl_backlog_active:1\r\nrepl_backlog_size:1048576\r\n" +
	"repl_backlog_first_byte_offset:51\r\nrepl_backlog_histlen:0\r\n"

// Four seconds after its primary was stopped.
const infoReplicaLinkDown = "# Replication\r\nrole:slave\r\nmaster_host:dbwp-oth-redis-p\r\nmaster_port:6379\r\n" +
	"master_link_status:down\r\nmaster_last_io_seconds_ago:-1\r\nmaster_sync_in_progress:0\r\n" +
	"slave_read_repl_offset:63330\r\nslave_repl_offset:63330\r\nmaster_link_down_since_seconds:4\r\n" +
	"slave_priority:100\r\nslave_read_only:1\r\nreplica_announced:1\r\nconnected_slaves:0\r\n" +
	"master_failover_state:no-failover\r\nmaster_replid:967868a588395341af8c7fff434b1f37c297b2e9\r\n" +
	"master_replid2:0000000000000000000000000000000000000000\r\nmaster_repl_offset:63330\r\nsecond_repl_offset:-1\r\n" +
	"repl_backlog_active:1\r\nrepl_backlog_size:1048576\r\nrepl_backlog_first_byte_offset:51\r\nrepl_backlog_histlen:63280\r\n"

// A fresh replica while its primary is still writing the RDB: the link is
// down, the sync is in progress, and the primary has not said how large the
// transfer is, so the total is -1 and the share -0.00.
const infoReplicaFullSync = "# Replication\r\nrole:slave\r\nmaster_host:dbwp-oth-redis-p\r\nmaster_port:6379\r\n" +
	"master_link_status:down\r\nmaster_last_io_seconds_ago:-1\r\nmaster_sync_in_progress:1\r\n" +
	"slave_read_repl_offset:1\r\nslave_repl_offset:1\r\nmaster_sync_total_bytes:-1\r\nmaster_sync_read_bytes:0\r\n" +
	"master_sync_left_bytes:-1\r\nmaster_sync_perc:-0.00\r\nmaster_sync_last_io_seconds_ago:1\r\n" +
	"master_link_down_since_seconds:-1\r\nslave_priority:100\r\nslave_read_only:1\r\nreplica_announced:1\r\n" +
	"connected_slaves:0\r\nmaster_failover_state:no-failover\r\nmaster_replid:d4539e89d503243627ce577959a9da4f480c3368\r\n" +
	"master_replid2:0000000000000000000000000000000000000000\r\nmaster_repl_offset:0\r\nsecond_repl_offset:-1\r\n" +
	"repl_backlog_active:0\r\nrepl_backlog_size:1048576\r\nrepl_backlog_first_byte_offset:0\r\nrepl_backlog_histlen:0\r\n"

// A server nothing replicates to or from.
const infoStandalone = "# Replication\r\nrole:master\r\nconnected_slaves:0\r\nmaster_failover_state:no-failover\r\n" +
	"master_replid:610847f779c93681d5d5760609bfc38355c3f958\r\nmaster_replid2:0000000000000000000000000000000000000000\r\n" +
	"master_repl_offset:0\r\nsecond_repl_offset:-1\r\nrepl_backlog_active:0\r\nrepl_backlog_size:1048576\r\n" +
	"repl_backlog_first_byte_offset:0\r\nrepl_backlog_histlen:0\r\n"

// A replica after REPLICAOF NO ONE: a new history, remembering the old id up
// to the offset where the two part.
const infoPromoted = "# Replication\r\nrole:master\r\nconnected_slaves:0\r\nmaster_failover_state:no-failover\r\n" +
	"master_replid:df469ea5e6ae525b8b1585cf478e348fcf7f9d86\r\nmaster_replid2:ffd4b443b40f29e7f5a0f45722bc5a4bf3ffdd4e\r\n" +
	"master_repl_offset:197099\r\nsecond_repl_offset:197100\r\nrepl_backlog_active:1\r\nrepl_backlog_size:1048576\r\n" +
	"repl_backlog_first_byte_offset:15\r\nrepl_backlog_histlen:197085\r\n"

func TestAPrimaryShowsEachReplicaAndHowFarBehindItIs(t *testing.T) {
	rows := replicationTable(parseInfo(infoPrimaryThreeReplicas)).Rows
	if len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}
	// Both replicas have acknowledged the whole stream.
	if got := rows[0]; got[2] != "ok" || got[3] != "197085" || got[4] != "0 B" || got[6] != "partial" {
		t.Errorf("an online replica 14 bytes behind = %v", got)
	}
	// A replica in its full sync is being rebuilt: no resync to judge, no
	// distance to a stream it does not follow yet, amber rather than red.
	if got := rows[2]; got[2] != "pending — full sync (wait_bgsave)" || got[4] != "-" || got[6] != "-" {
		t.Errorf("a replica in its full sync = %v", got)
	}
}

func TestAReplicaPastTheBacklogIsGradedForTheFullResyncItWouldCost(t *testing.T) {
	row := replicationTable(parseInfo(infoPrimaryPastBacklog)).Rows[0]
	if !strings.HasPrefix(row[2], "warn — behind the backlog") || row[6] != resyncFull {
		t.Errorf("link %q, resync %q", row[2], row[6])
	}
	if row[4] != "61.4 KiB" {
		t.Errorf("behind = %q, want the 62879 bytes between 358 and 63237", row[4])
	}

	// The same replica while the window still reached it is behind and fine:
	// distance alone is not the finding, the lost way back is.
	row = replicationTable(parseInfo(infoPrimaryBehindInBacklog)).Rows[0]
	if row[2] != "ok" || row[6] != resyncPartial {
		t.Errorf("behind but inside the backlog: link %q, resync %q", row[2], row[6])
	}
}

// Redis 8 appends io-thread to a replica's line and 6.2 does not have the
// fields 7.0 added; neither changes what the table reads. A replica that has
// not acknowledged anything yet is at offset 0 while the stream stands at 50,
// which is the honest distance until its first acknowledgement a second on.
func TestTheReplicaLineOfOtherVersionsReadsTheSame(t *testing.T) {
	const redis8 = "# Replication\r\nrole:master\r\nconnected_slaves:1\r\n" +
		"slave0:ip=172.20.0.22,port=6379,state=online,offset=0,lag=0,io-thread=0\r\n" +
		"master_failover_state:no-failover\r\nmaster_replid:a9b829d444fabb47ade422ad696cd24ace421c99\r\n" +
		"master_replid2:0000000000000000000000000000000000000000\r\nmaster_repl_offset:50\r\nsecond_repl_offset:-1\r\n" +
		"repl_backlog_active:1\r\nrepl_backlog_size:1048576\r\nrepl_backlog_first_byte_offset:1\r\nrepl_backlog_histlen:50\r\n"
	row := replicationTable(parseInfo(redis8)).Rows[0]
	if row[1] != "172.20.0.22:6379" || row[2] != "ok" || row[4] != "50 B" || row[6] != resyncPartial {
		t.Errorf("redis 8: %v", row)
	}

	const redis6Replica = "# Replication\r\nrole:slave\r\nmaster_host:dbwp-oth-r6p\r\nmaster_port:6379\r\n" +
		"master_link_status:up\r\nmaster_last_io_seconds_ago:0\r\nmaster_sync_in_progress:0\r\n" +
		"slave_read_repl_offset:50\r\nslave_repl_offset:50\r\nslave_priority:100\r\nslave_read_only:1\r\n" +
		"replica_announced:1\r\nconnected_slaves:0\r\nmaster_failover_state:no-failover\r\n" +
		"master_replid:af182afeb6310af2c055059c9d830b4903a4863d\r\nmaster_replid2:0000000000000000000000000000000000000000\r\n" +
		"master_repl_offset:50\r\nsecond_repl_offset:-1\r\nrepl_backlog_active:1\r\nrepl_backlog_size:1048576\r\n" +
		"repl_backlog_first_byte_offset:1\r\nrepl_backlog_histlen:50\r\n"
	row = replicationTable(parseInfo(redis6Replica)).Rows[0]
	if row[2] != "ok" || row[3] != "50" || row[5] != "0s ago" {
		t.Errorf("redis 6.2 replica: %v", row)
	}
}

func TestAReplicaThatWentQuietIsGradedAtTheLagAPrimaryActsOn(t *testing.T) {
	raw := strings.Replace(infoPrimaryBehindInBacklog, "lag=1", "lag=12", 1)
	if got := replicationTable(parseInfo(raw)).Rows[0][2]; got != "warn — silent for 12s" {
		t.Errorf("link = %q", got)
	}
}

func TestAReplicaOfAQuietPrimaryIsNotWarnedAtItsPingInterval(t *testing.T) {
	for last, want := range map[string]string{"10": "ok", "29": "ok", "31": "warn — silent for 31s"} {
		raw := strings.Replace(infoReplicaUp, "master_last_io_seconds_ago:4", "master_last_io_seconds_ago:"+last, 1)
		if got := replicationTable(parseInfo(raw)).Rows[0][2]; got != want {
			t.Errorf("last io %ss: link = %q, want %q", last, got, want)
		}
	}
}

func TestAReplicaNamesItsLinkAndNeverClaimsToKnowHowFarBehindItIs(t *testing.T) {
	for _, tc := range []struct {
		name, raw, link, contact string
	}{
		{"up", infoReplicaUp, "ok", "4s ago"},
		{"down since the primary stopped", infoReplicaLinkDown, "down — for 4s", "-"},
		{"in its full sync", infoReplicaFullSync, "pending — full sync", "-"},
	} {
		row := replicationTable(parseInfo(tc.raw)).Rows[0]
		if row[0] != "replica" || row[2] != tc.link || row[5] != tc.contact || row[4] != "-" {
			t.Errorf("%s: %v", tc.name, row)
		}
	}
}

// The share is printed only once the primary has said how large the transfer
// is; until then the total is -1 and a percentage would be a made-up number.
func TestAFullSyncShowsItsShareOnceThePrimaryHasSaidHowLargeItIs(t *testing.T) {
	raw := strings.NewReplacer("master_sync_total_bytes:-1", "master_sync_total_bytes:2000",
		"master_sync_read_bytes:0", "master_sync_read_bytes:500").Replace(infoReplicaFullSync)
	if got := replicationTable(parseInfo(raw)).Rows[0][2]; got != "pending — full sync 25%" {
		t.Errorf("link = %q", got)
	}
}

func TestAServerWithNoReplicationSaysSoAndHasNoStream(t *testing.T) {
	in := parseInfo(infoStandalone)
	rows := replicationTable(in).Rows
	if len(rows) != 1 || rows[0][1] != "no replicas" {
		t.Errorf("rows = %v", rows)
	}
	if _, ok := streamPairs(in, plugin.SurfaceCLI); ok {
		t.Error("a server nothing replicates has no stream to describe")
	}
}

func TestAPromotedNodeRemembersTheHistoryItLeft(t *testing.T) {
	kv, ok := streamPairs(parseInfo(infoPromoted), plugin.SurfaceCLI)
	if !ok {
		t.Fatal("a promoted node has a history to show")
	}
	prev := pairValue(kv, "previous id")
	if !strings.HasPrefix(prev, "ffd4b443b40f29e7f5a0f45722bc5a4bf3ffdd4e, the same stream up to offset 197100") {
		t.Errorf("previous id = %q", prev)
	}
}

// What a replica cannot know is said where to find, in the words of the
// surface asking: a flag-less tool name to an agent, a command to a person.
func TestAReplicaSaysWhereTheMissingDistanceIsRead(t *testing.T) {
	in := parseInfo(infoReplicaUp)
	cli, _ := streamPairs(in, plugin.SurfaceCLI)
	mcp, _ := streamPairs(in, plugin.SurfaceMCP)
	if got := pairValue(cli, "primary's offset"); !strings.Contains(got, "`rta redis overview` against the primary") {
		t.Errorf("cli: %q", got)
	}
	if got := pairValue(mcp, "primary's offset"); !strings.Contains(got, "`redis_overview` tool against the primary") {
		t.Errorf("mcp: %q", got)
	}
	if got := pairValue(cli, "backlog"); got != "0 B held from offset 51 (limit 1.0 MiB)" {
		t.Errorf("backlog = %q", got)
	}
}
