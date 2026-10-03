package main

import (
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/view"
)

// peerRoutes is a follower of a three-peer cluster, with the telemetry it
// answered; the collection routes are the ones the other tests use, whose
// ids are another cluster's, and are not what is under test.
func peerRoutes(telemetry string) map[string]string {
	routes := distributedRoutes()
	routes["/cluster"] = fxClusterAtFollower
	if telemetry != "" {
		routes["/cluster/telemetry"] = telemetry
	}
	return routes
}

// Asked of a follower, the peers table names every peer's version, term and
// commit, which are each peer's own, from what the peer asked reports of them,
// and says of the stopped one that it does not answer: the leader's
// send-failure list that graded it before is empty on a follower.
func TestThePeersTableReportsEachPeerFromTheTelemetryOfThePeerAsked(t *testing.T) {
	f := newFakeQdrant(t, peerRoutes(fxTelemetryDown))
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	peers := section(t, v, "peers").(view.Table)
	if peers.Columns[3].Name != "Version" {
		t.Fatalf("columns = %v", peers.Columns)
	}
	byName := map[string][]string{}
	for _, r := range peers.Rows {
		byName[strings.TrimSuffix(r[0], " (this peer)")] = r
	}
	if r := byName["dbwp-r12plug-qd2"]; r[3] != "1.19.1" || r[4] != "1" || r[5] != "13" || r[7] != "ok" {
		t.Errorf("this peer = %v", r)
	}
	if r := byName["dbwp-r12plug-qd1"]; r[2] != "leader" || r[3] != "1.19.1" || r[4] != "1" || r[5] != "13" ||
		r[6] != "0" || r[7] != "ok" {
		t.Errorf("the leader, from the follower = %v", r)
	}
	if r := byName["dbwp-r12plug-qd3"]; r[3] != "-" || r[4] != "-" || r[5] != "-" ||
		!strings.HasPrefix(r[7], "fail — does not answer the peer asked") {
		t.Errorf("the stopped peer = %v", r)
	}
	status := section(t, v, "status").(view.KeyValue)
	if got := pair(status, "cluster"); strings.Contains(got, "versions") {
		t.Errorf("cluster = %q, names versions when every peer runs one", got)
	}
}

// A cluster in the middle of a rolling upgrade says so in the line that
// describes it, with the versions it runs. One value of the capture is changed:
// qd1's version.
func TestAClusterOnSeveralVersionsSaysSo(t *testing.T) {
	skewed := strings.Replace(fxTelemetryUp, `"version":"1.19.1"`, `"version":"1.18.0"`, 1)
	f := newFakeQdrant(t, peerRoutes(skewed))
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	got := pair(section(t, v, "status").(view.KeyValue), "cluster")
	if !strings.HasSuffix(got, ", on 2 versions (1.18.0, 1.19.1)") {
		t.Errorf("cluster = %q", got)
	}
	// A third peer a follower cannot message itself answers the peer asked,
	// which is the evidence the follower's own failure list could not give.
	for _, r := range section(t, v, "peers").(view.Table).Rows {
		if r[0] == "dbwp-r12plug-qd3" && r[7] != "ok — answers the peer asked" {
			t.Errorf("a third peer = %v", r)
		}
	}
}

// A server older than the endpoint answers 404, and a credential scoped to
// collections is refused it: the page is whole without it, as it was.
func TestAServerWithNoPeerTelemetryIsGradedAsItWas(t *testing.T) {
	f := newFakeQdrant(t, peerRoutes(""))
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range section(t, v, "peers").(view.Table).Rows {
		if r[3] != "-" {
			t.Errorf("%s reports version %q with no telemetry", r[0], r[3])
		}
		if strings.HasPrefix(r[0], "dbwp-oth-qd3") && !strings.HasPrefix(r[7], "info — a follower messages only the leader") {
			t.Errorf("a peer a follower cannot see = %v", r)
		}
	}
}

// The replicas on a peer are graded by whether it answers, from any peer once
// the server reports its peers' telemetry, where only the leader's own
// failures said so before.
func TestAFollowerKnowsWhichPeersAreDownFromTheirTelemetry(t *testing.T) {
	cs := stateOf(t, fxClusterAtFollower)
	cs.telemetry = fetchTelemetryFrom(t, fxTelemetryDown)
	down := cs.peersDown(time.Now())
	if down == nil {
		t.Fatal("a follower with telemetry cannot say which peers are down")
	}
	for id, want := range map[uint64]bool{333551949039778: true, 1575883389625398: false, 3702969928492873: false} {
		if got := down(id); got != want {
			t.Errorf("peer %d down = %v, want %v", id, got, want)
		}
	}
	if cs.telemetry = nil; cs.peersDown(time.Now()) != nil {
		t.Error("a follower with no telemetry claims to know which peers are down")
	}
}

func fetchTelemetryFrom(t *testing.T, body string) map[uint64]peerTelemetry {
	t.Helper()
	f := newFakeQdrant(t, map[string]string{"/cluster/telemetry": body})
	got := fetchTelemetry(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{}))
	if got == nil {
		t.Fatal("the telemetry was not read")
	}
	return got
}
