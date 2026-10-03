package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/view"
)

func unwrap[T any](t *testing.T, raw string) T {
	t.Helper()
	var env struct {
		Result T `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	return env.Result
}

func stateOf(t *testing.T, raw string) clusterState {
	t.Helper()
	info := unwrap[clusterInfo](t, raw)
	return clusterState{mode: modeDistributed, info: info, names: peerNames(info)}
}

func section(t *testing.T, v view.View, title string) view.View {
	t.Helper()
	for _, s := range v.(view.Sections).Items {
		if s.Title == title {
			return s.View
		}
	}
	t.Fatalf("no section %q", title)
	return nil
}

func hasSection(v view.View, title string) bool {
	s, ok := v.(view.Sections)
	if !ok {
		return false
	}
	for _, it := range s.Items {
		if it.Title == title {
			return true
		}
	}
	return false
}

func pair(kv view.KeyValue, key string) string {
	for _, p := range kv.Pairs {
		if p.Key == key {
			return p.Value
		}
	}
	return ""
}

func distributedRoutes() map[string]string {
	return map[string]string{
		"/":                          fxRoot,
		"/collections":               fxCollections,
		"/collections/docs3":         fxInfo,
		"/cluster":                   fxClusterPeerDown,
		"/collections/docs3/cluster": fxShardsDead,
	}
}

func TestAnOverviewOfAClusterShowsEveryPeerAndWhichReplicasServe(t *testing.T) {
	f := newFakeQdrant(t, distributedRoutes())
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}

	status := section(t, v, "status").(view.KeyValue)
	if got := pair(status, "cluster"); got != "distributed — 3 peers, this one (dbwp-oth-qd1) is leader" {
		t.Errorf("cluster = %q", got)
	}

	peers := section(t, v, "peers").(view.Table).Rows
	if len(peers) != 3 || peers[0][0] != "dbwp-oth-qd1 (this peer)" {
		t.Fatalf("peers = %v", peers)
	}
	// This peer's own consensus state, in full.
	if r := peers[0]; r[2] != "leader" || r[4] != "1" || r[5] != "38" || r[6] != "0" || r[7] != "ok" {
		t.Errorf("this peer = %v", r)
	}
	// The others: a role from the leader this peer names, and blank numbers,
	// which are theirs to report.
	if r := peers[1]; r[0] != "dbwp-oth-qd2" || r[2] != "follower" || r[4] != "-" || r[5] != "-" {
		t.Errorf("another peer = %v", r)
	}

	// Dead replicas on qd3, grouped as the one line a peer going down is.
	coll := section(t, v, "collections").(view.Table)
	if coll.Columns[len(coll.Columns)-1].Name != "Replicas" || coll.Columns[len(coll.Columns)-1].Kind != view.KindStatus {
		t.Fatalf("columns = %v", coll.Columns)
	}
	if got := coll.Rows[0][5]; got != "fail — 7 of 9 replicas active: shards 0, 2 on dbwp-oth-qd3 are Dead" {
		t.Errorf("replicas = %q", got)
	}
}

// The counter never resets, so a peer that failed an hour ago and has been
// fine since must not read as down, and one failing this second must.
func TestAPeerIsGradedByWhetherItIsFailingNotByWhetherItEverFailed(t *testing.T) {
	cs := stateOf(t, fxClusterPeerDown)
	failedAt := time.Date(2026, 10, 3, 0, 31, 4, 516454383, time.UTC)

	now := peersTable(cs, failedAt.Add(2*time.Second)).Rows[2]
	if !strings.HasPrefix(now[7], "fail — cannot be messaged: 18 failed messages to it, the last: The service is currently unavailable: Failed to connect, error: transport error") {
		t.Errorf("failing now = %q", now[7])
	}
	past := peersTable(cs, failedAt.Add(2*time.Hour)).Rows[2]
	if past[7] != "ok — 18 failed messages in the past, the last 2h ago" {
		t.Errorf("failed in the past = %q", past[7])
	}

	// A server that does not date its failures: the safer reading.
	undated := cs
	undated.info.SendFailures = map[string]struct {
		Count       uint64 `json:"count"`
		LatestError string `json:"latest_error"`
		At          string `json:"latest_error_timestamp"`
	}{"http://dbwp-oth-qd3:6335/": {Count: 1, LatestError: "transport error"}}
	if got := peersTable(undated, failedAt).Rows[2][7]; got != "warn — 1 failed message to it, the last: transport error" {
		t.Errorf("undated = %q", got)
	}
}

// Asked of a follower, the same table puts the leader's row where it names it
// and keeps the numbers on the follower's own: each peer describes itself.
func TestAFollowerDescribesItselfAndNamesTheLeader(t *testing.T) {
	rows := peersTable(stateOf(t, fxClusterFollower), time.Now()).Rows
	if len(rows) != 2 || rows[0][0] != "dbwp-oth-qd2 (this peer)" || rows[0][2] != "follower" || rows[0][5] != "6" {
		t.Errorf("this peer = %v", rows)
	}
	if rows[1][0] != "dbwp-oth-qd1" || rows[1][2] != "leader" || rows[1][5] != "-" {
		t.Errorf("the leader = %v", rows[1])
	}

	// A follower sends to the leader and to no other peer, so its failure list
	// says nothing about a third one: asked of qd2 with qd3 stopped it read "ok".
	// The three-peer capture taken at qd1, answered as qd2 would have.
	cs := stateOf(t, fxClusterPeerDown)
	cs.info.PeerID = 5204490162315831
	cs.info.RaftInfo.Role = "Follower"
	third := peersTable(cs, time.Now()).Rows[2]
	if third[0] != "dbwp-oth-qd3" || !strings.HasPrefix(third[7], "info — a follower messages only the leader") {
		t.Errorf("a peer a follower cannot see = %v", third)
	}
}

// The consensus thread, a missing leader and an election each have their own
// words, and an election is amber because it ends. Each is the captured
// leader's answer with one value changed.
func TestThePeerAskedGradesItsOwnConsensus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*clusterInfo)
		want   string
	}{
		{"working", func(*clusterInfo) {}, "ok"},
		{"the thread stopped with an error", func(i *clusterInfo) {
			i.Consensus.State, i.Consensus.Err = "stopped_with_err", "raft: disk full"
		}, "fail — the consensus thread stopped: raft: disk full"},
		{"the thread stopped", func(i *clusterInfo) { i.Consensus.State = "stopped" }, "fail — the consensus thread is stopped"},
		{"no leader", func(i *clusterInfo) { i.RaftInfo.Leader = nil }, "fail — no leader: nothing can be committed until an election finishes"},
		{"a candidate", func(i *clusterInfo) { i.RaftInfo.Role = "Candidate" }, "warn — an election is under way"},
	} {
		cs := stateOf(t, fxClusterHealthy)
		tc.change(&cs.info)
		if got := peersTable(cs, time.Now()).Rows[0][7]; got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	cs := stateOf(t, fxClusterHealthy)
	voter := false
	cs.info.RaftInfo.IsVoter = &voter
	if got := peersTable(cs, time.Now()).Rows[0][2]; got != "learner" {
		t.Errorf("a peer that does not vote reads %q", got)
	}
}

func TestReplicasAreGradedByWhatStateTheyAreIn(t *testing.T) {
	names := stateOf(t, fxClusterPeerDown).label
	for _, tc := range []struct{ name, raw, want string }{
		{"all serving", fxShardsActive, "ok — 9 of 9 replicas active"},
		{"missed writes", fxShardsDead, "fail — 7 of 9 replicas active: shards 0, 2 on dbwp-oth-qd3 are Dead"},
		// A Dead replica among them makes it red; the one being rebuilt is listed
		// beside it.
		{"coming back", fxShardsRecovering, "fail — 7 of 9 replicas active: shard 1 on dbwp-oth-qd3 is Recovery; shard 2 on dbwp-oth-qd3 is Dead"},
	} {
		if got := replicasHealth(unwrap[collectionCluster](t, tc.raw), names, nil); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}

	// The recovery capture with its Dead replica brought back, which is what
	// the next second of it reads: amber, with the transfer named.
	cc := unwrap[collectionCluster](t, fxShardsRecovering)
	for i := range cc.RemoteShards {
		if cc.RemoteShards[i].State == "Dead" {
			cc.RemoteShards[i].State = "Active"
		}
	}
	want := "pending — 8 of 9 replicas active: shard 1 on dbwp-oth-qd3 is Recovery, 1 shard transfer under way"
	if got := replicasHealth(cc, names, nil); got != want {
		t.Errorf("rebuilding = %q, want %q", got, want)
	}
	// A transfer with nothing wrong beside it is still a thing in flight.
	cc.RemoteShards[2].State = "Active"
	if got := replicasHealth(cc, names, nil); got != "pending — 9 of 9 replicas active, 1 shard transfer under way" {
		t.Errorf("transfer only = %q", got)
	}
}

func TestAnUnknownStateIsAmberNotRed(t *testing.T) {
	cc := unwrap[collectionCluster](t, fxShardsActive)
	cc.RemoteShards[0].State = "SomeFutureState"
	got := replicasHealth(cc, func(id uint64) string { return "p" }, nil)
	if !strings.HasPrefix(got, "pending — 8 of 9 replicas active") {
		t.Errorf("replicas = %q", got)
	}
}

// Consensus keeps a replica Active until a write fails on it, so a peer that
// went down under a collection nobody wrote to since left every replica on it
// reading active beside a search that fails. The leader knows which peers it
// cannot message, and the replicas on them are not counted as serving.
func TestAnActiveReplicaOnAPeerTheLeaderCannotMessageIsNotCountedAsServing(t *testing.T) {
	cs := stateOf(t, fxClusterPeerDown)
	failedAt := time.Date(2026, 10, 3, 0, 31, 4, 516454383, time.UTC)
	down := cs.peersDown(failedAt.Add(2 * time.Second))

	// Three copies of each shard: the other two serve, so it is amber.
	cc := unwrap[collectionCluster](t, fxShardsActive)
	want := "warn — 6 of 9 replicas active: shards 0, 1, 2 on dbwp-oth-qd3 are active, but the peer cannot be messaged"
	if got := replicasHealth(cc, cs.label, down); got != want {
		t.Errorf("another copy serves = %q, want %q", got, want)
	}
	for _, r := range shardsTable(cc, cs.label, down).Rows {
		if (r[1] == "dbwp-oth-qd3") != strings.HasPrefix(r[2], "warn — active, but the peer") {
			t.Errorf("shard row %v: exactly the replicas on qd3 are cut off", r)
		}
	}

	// One copy of the shard, on the peer that is down: nothing serves it.
	const solo = `{"result":{"peer_id":5520940345939595,"shard_count":1,"local_shards":[],` +
		`"remote_shards":[{"shard_id":0,"peer_id":1212972222451972,"state":"Active"}],"shard_transfers":[]}}`
	want = "fail — 0 of 1 replicas active: shard 0 on dbwp-oth-qd3 is active, but the peer cannot be messaged"
	if got := replicasHealth(unwrap[collectionCluster](t, solo), cs.label, down); got != want {
		t.Errorf("no copy serves = %q, want %q", got, want)
	}

	// A peer that failed an hour ago is back, and a follower cannot tell.
	if cs.peersDown(failedAt.Add(time.Hour))(1212972222451972) {
		t.Error("a peer that has not failed for an hour is still down")
	}
	if stateOf(t, fxClusterFollower).peersDown(failedAt) != nil {
		t.Error("a follower, which messages only the leader, claimed to know which peers are down")
	}
}

func TestAStandaloneInstanceHasNoReplicaColumnAndSaysWhy(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/": fxRoot, "/collections": fxCollections, "/collections/docs3": fxInfo,
		"/cluster": fxClusterDisabled, "/collections/docs3/cluster": fxShardsSolo,
	})
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := pair(section(t, v, "status").(view.KeyValue), "cluster"); !strings.HasPrefix(got, "standalone — cluster mode is off") {
		t.Errorf("cluster = %q", got)
	}
	if hasSection(v, "peers") {
		t.Error("a standalone instance has no peers to list")
	}
	for _, c := range section(t, v, "collections").(view.Table).Columns {
		if c.Name == "Replicas" {
			t.Error("a standalone collection has no replicas to be behind")
		}
	}
	for _, r := range f.bodies {
		if r.path == "/collections/docs3/cluster" {
			t.Error("asked for a collection's shards on an instance that is not a cluster")
		}
	}
}

// A server too old to have /cluster, or a proxy that drops it, still lists
// its collections.
func TestAServerWithoutTheClusterEndpointStillListsItsCollections(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/": fxRoot, "/collections": fxCollections, "/collections/docs3": fxInfo,
	})
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := pair(section(t, v, "status").(view.KeyValue), "cluster"); got != "not reported by this server" {
		t.Errorf("cluster = %q", got)
	}
}
