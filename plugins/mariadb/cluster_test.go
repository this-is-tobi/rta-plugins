package main

import (
	"strings"
	"testing"
)

// The Galera rules, against what nodes of a real three-node cluster said in
// each state — and, for the states one cluster cannot be put in on demand, the
// healthy node's answer with the one variable a rule reads changed, so every
// other value is what a node writes.

func galeraVars(t *testing.T, fixture string) map[string]string {
	t.Helper()
	db := fixtureOpen(t, fixture)
	vars, err := wsrepVars(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	return vars
}

func withVars(vars map[string]string, change map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range vars {
		out[k] = v
	}
	for k, v := range change {
		out[k] = v
	}
	return out
}

func TestAHealthyNodeIsOkAndShowsTheClusterItIsIn(t *testing.T) {
	for _, name := range fixtureNames(t, "galera-synced") {
		st := stateFrom(t, name, "root")
		sum := summarise(st, nil)
		if sum.status != "ok" || sum.role != "Galera node (cluster of 3)" {
			t.Errorf("%s: summary = %+v, want an ok node of a cluster of three", name, sum)
		}
		if !strings.Contains(sum.detail, "synced with 3 members") {
			t.Errorf("%s: detail = %q", name, sum.detail)
		}
		pairs := pairsOf(replicationView(st), "galera")
		if pairs["cluster uuid"] == "" || pairs["cluster uuid"] != pairs["node uuid"] {
			t.Errorf("%s: the cluster's and the node's state uuids are not side by side and equal: %v", name, pairs)
		}
		if n := strings.Count(pairs["members"], ",") + 1; n != 3 {
			t.Errorf("%s: members = %q, want the three the cluster reports", name, pairs["members"])
		}
		if pairs["last committed"] == "" || pairs["cluster conf id"] == "" {
			t.Errorf("%s: a node's place in the cluster's history is missing: %v", name, pairs)
		}
	}
}

// **The failure this exists for.** A Galera node that has lost quorum still
// accepts connections and still answers SELECT — it just stops being part of
// the cluster. Nothing else about the server looks wrong while it is
// happening, so the verdict has to say it in words.
func TestANodeCutOffFromTheClusterIsASplitBrainRisk(t *testing.T) {
	st := stateFrom(t, "mariadb-11.4-galera-split", "root")
	sum := summarise(st, nil)
	if sum.status != "fail" || !strings.Contains(sum.detail, "split-brain risk") ||
		!strings.Contains(sum.detail, "must not be written to") {
		t.Errorf("summary = %+v, want a failure that says not to write to it", sum)
	}
	if !strings.Contains(sum.detail, "non-Primary") {
		t.Errorf("detail = %q, want the cluster status the node reported", sum.detail)
	}
}

// The combination that gets misread at three in the morning: connected,
// ready, in the primary component, and not caught up.
func TestADesyncedNodeIsAWarningNotHealthy(t *testing.T) {
	sum := summarise(stateFrom(t, "mariadb-11.4-galera-desynced", "root"), nil)
	if sum.status != "warn" || !strings.Contains(sum.detail, "Donor/Desynced") {
		t.Errorf("summary = %+v, want a warning naming the state", sum)
	}
}

func TestANodeWhoseHistoryIsNotTheClustersIsOutOfStep(t *testing.T) {
	vars := withVars(galeraVars(t, "mariadb-11.4-galera-synced"),
		map[string]string{"wsrep_local_state_uuid": "00000000-0000-0000-0000-000000000000"})
	f := galeraFacet(vars)
	if f.status != "fail" || !strings.Contains(f.detail, "out of step") {
		t.Errorf("facet = %+v, want a failure calling the node out of step", f)
	}
}

func TestFlowControlGradesByHowMuchOfTheTimeANodeIsHeldBack(t *testing.T) {
	base := galeraVars(t, "mariadb-11.4-galera-synced")
	for _, tc := range []struct{ paused, want string }{
		{"0.000000", "ok"},
		{"0.099999", "ok"},
		{"0.100000", "warn"},
		{"0.499999", "warn"},
		{"0.500000", "fail"},
		{"0.970000", "fail"},
	} {
		f := galeraFacet(withVars(base, map[string]string{"wsrep_flow_control_paused": tc.paused}))
		if f.status != tc.want {
			t.Errorf("paused %s: status = %q, want %q (%s)", tc.paused, f.status, tc.want, f.detail)
		}
	}
	f := galeraFacet(withVars(base, map[string]string{"wsrep_flow_control_paused": "0.250000"}))
	if !strings.Contains(f.detail, "25%") {
		t.Errorf("detail = %q, want the share of time spelled as a percentage", f.detail)
	}
}

// A node blocked behind a global read lock while the rest of the cluster
// writes reads Synced, connected and ready, and is a long way behind: the
// queue of writesets waiting to be applied is the only thing that says so.
func TestANodeFallingBehindIsAWarningByItsReceiveQueue(t *testing.T) {
	sum := summarise(stateFrom(t, "mariadb-11.4-galera-flow-n3", "root"), nil)
	// The global read lock also desyncs the node, which is why it reads
	// Donor/Desynced; both findings are kept.
	if sum.status != "warn" || !strings.Contains(sum.detail, "waiting to be applied") ||
		!strings.Contains(sum.detail, "Donor/Desynced") {
		t.Errorf("summary = %+v, want a warning naming the queue and the state", sum)
	}
	base := galeraVars(t, "mariadb-11.4-galera-synced")
	for queue, want := range map[string]string{"0": "ok", "99": "ok", "100": "warn"} {
		if f := galeraFacet(withVars(base, map[string]string{"wsrep_local_recv_queue": queue})); f.status != want {
			t.Errorf("queue %s: status = %q, want %q", queue, f.status, want)
		}
	}
}

// A node that is up and refusing cluster traffic is not a healthy one, and
// neither flag alone is the whole of it.
func TestAnUnreadyOrDisconnectedNodeFails(t *testing.T) {
	base := galeraVars(t, "mariadb-11.4-galera-synced")
	for _, change := range []map[string]string{{"wsrep_ready": "OFF"}, {"wsrep_connected": "OFF"}} {
		if f := galeraFacet(withVars(base, change)); f.status != "fail" {
			t.Errorf("%v: facet = %+v, want a failure", change, f)
		}
	}
}

// A standalone MariaDB is a normal thing to run. A cluster section for one
// would read exactly like a cluster that has fallen apart.
func TestAServerWithoutGaleraHasNoClusterSection(t *testing.T) {
	for _, vars := range []map[string]string{
		{},                              // no wsrep variables at all
		{"wsrep_provider_name": "none"}, // compiled in, not configured
		{"wsrep_provider_name": ""},     // present and empty
		{"wsrep_cluster_size": "0"},     // wsrep variables, no provider
	} {
		if part := clusterFrom(vars); part.facet != nil || part.section != nil {
			t.Errorf("vars %v: a cluster part for a server that is not clustered: %+v", vars, part)
		}
	}
	for _, name := range fixtureNames(t, "standalone") {
		st := stateFrom(t, name, "root")
		if st.cluster.facet != nil || st.cluster.section != nil {
			t.Errorf("%s: a standalone server has a cluster part", name)
		}
	}
}

// The members are the addresses the cluster reports, each set by its own node's
// operator: a list that holds one that does not read as itself is shown quoted,
// and an ordinary list as it always was.
func TestTheClustersMembersShowAnOddAddressQuotedAndAnOrdinaryListAsItIs(t *testing.T) {
	members := func(addresses string) string {
		for _, p := range galeraPairs(map[string]string{"wsrep_incoming_addresses": addresses}).Pairs {
			if p.Key == "members" {
				return p.Value
			}
		}
		t.Fatal("no members pair")
		return ""
	}
	shownAs(t, "an odd address", members(oddName+":3306,10.0.0.2:3306"), `"esc\x1b[31mred\nline:3306,10.0.0.2:3306"`)
	shownAs(t, "an ordinary list", members("10.0.0.1:3306,"+ordinaryName+":3306"), "10.0.0.1:3306,"+ordinaryName+":3306")
}
