package main

import (
	"strings"
	"testing"
)

// The rules, stated against a row a server really sent with the one column a
// rule reads changed. A captured row is the base so that every other column is
// what a server writes; the grading tests change only what they are about.

func replicaRow(t *testing.T, fixture string, change map[string]string) map[string]string {
	t.Helper()
	row := map[string]string{}
	for k, v := range stateFrom(t, fixture, "root").channels[0] {
		row[k] = v
	}
	for k, v := range change {
		row[k] = v
	}
	return row
}

func TestHowFarBehindDecidesTheGradeBeyondAnyDelay(t *testing.T) {
	base := fixtureNames(t, "replica")[0]
	for _, tc := range []struct {
		name   string
		change map[string]string
		want   string
	}{
		{"caught up", map[string]string{"seconds_behind_source": "0"}, "ok"},
		{"a few seconds is not a finding", map[string]string{"seconds_behind_source": "59"}, "ok"},
		{"a minute", map[string]string{"seconds_behind_source": "60"}, "warn — 1m behind"},
		{"nine minutes", map[string]string{"seconds_behind_source": "599"}, "warn — 9m59s behind"},
		{"ten minutes", map[string]string{"seconds_behind_source": "600"}, "fail — 10m behind"},
		{"an hour", map[string]string{"seconds_behind_source": "3600"}, "fail — 1h behind"},
		// A replica kept an hour behind on purpose is on time at an hour and
		// a minute behind only once it is a minute past its delay.
		{"inside its delay", map[string]string{"seconds_behind_source": "3600", "sql_delay": "3600"}, "ok"},
		{"before it reaches its delay", map[string]string{"seconds_behind_source": "30", "sql_delay": "3600"}, "ok"},
		{"a minute past its delay", map[string]string{"seconds_behind_source": "3660", "sql_delay": "3600"}, "warn — 1m behind beyond its 1h delay"},
	} {
		if got := assessChannel(replicaRow(t, base, tc.change)).status; got != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Running with no lag figure is not the same as caught up: the server writes
// NULL when it cannot tell, and a NULL read as 0 is how a replica that has
// lost its source reads as healthy.
func TestNoLagFigureIsAWarningNotACaughtUpReplica(t *testing.T) {
	base := fixtureNames(t, "replica")[0]
	c := assessChannel(replicaRow(t, base, map[string]string{"seconds_behind_source": ""}))
	if !strings.HasPrefix(c.status, "warn") || c.behind != "-" {
		t.Errorf("status = %q, behind = %q, want a warning and no figure", c.status, c.behind)
	}
}

func TestEveryThreadStateHasAGrade(t *testing.T) {
	base := fixtureNames(t, "replica")[0]
	for _, tc := range []struct {
		name   string
		change map[string]string
		want   string
	}{
		{"both stopped", map[string]string{"replica_io_running": "No", "replica_sql_running": "No"},
			"fail — IO and SQL threads stopped, no error recorded"},
		{"sql stopped by hand", map[string]string{"replica_sql_running": "No"}, "fail — SQL thread stopped, no error recorded"},
		{"io connecting", map[string]string{"replica_io_running": "Connecting"}, "warn — IO thread connecting"},
		{"io connecting and refused", map[string]string{"replica_io_running": "Connecting", "last_io_errno": "2003"},
			"fail — IO thread cannot reach the source; IO errno 2003 (cannot connect to the source)"},
		{"an errno this does not know stays a number", map[string]string{"replica_sql_running": "No", "last_sql_errno": "1690"},
			"fail — SQL thread stopped; SQL errno 1690"},
	} {
		if got := assessChannel(replicaRow(t, base, tc.change)).status; got != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A monitoring grant reads the replica threads and the binary log and not the
// connected replicas, whose privilege is a different and broader one. That is
// the usual monitoring account, so it must not read as a failure on every
// poll — and it must not read as "no replicas" either, which is what an
// unread list would otherwise turn into. The threads it could read say what
// this server is; the list says so in a warning that heads nothing as partial.
func TestAnAccountThatSeesThreadsButNotReplicasSaysWhatItDoesKnow(t *testing.T) {
	for _, name := range fixtureNames(t, "monitor") {
		st := stateFrom(t, name, "mon")
		sum := summarise(st, nil)
		if !strings.Contains(sum.role, "not a replica") || sum.status != "none" {
			t.Errorf("%s: summary = %+v, want what was read, graded as nothing to grade", name, sum)
		}
		if !strings.Contains(sum.detail, "whether replicas are connected is not readable") {
			t.Errorf("%s: detail = %q, want it to say the replicas are unknown, not absent", name, sum.detail)
		}
		if _, ok := st.unread[sectionReplica]; ok {
			t.Errorf("%s: the replica threads were readable and are reported as not", name)
		}
		warnings := replicationView(st).Warnings
		if len(warnings) != 1 || !warnings[0].Advisory || warnings[0].Code != "mysql.replication.denied" {
			t.Errorf("%s: warnings = %+v, want the one advisory denial", name, warnings)
		}
	}
}

func TestBacklogIsBytesWithinOneFileAndFilesAcrossThem(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  map[string]string
		want string
	}{
		{"same file", map[string]string{"source_log_file": "bin.000007", "relay_source_log_file": "bin.000007",
			"read_source_log_pos": "2048", "exec_source_log_pos": "1024"}, "1.0 KiB"},
		{"nothing pending", map[string]string{"source_log_file": "bin.000007", "relay_source_log_file": "bin.000007",
			"read_source_log_pos": "500", "exec_source_log_pos": "500"}, "0 B"},
		{"across files", map[string]string{"source_log_file": "bin.000009", "relay_source_log_file": "bin.000007",
			"read_source_log_pos": "10", "exec_source_log_pos": "9000"}, "across 3 binary log files"},
		{"a name with no sequence", map[string]string{"source_log_file": "a", "relay_source_log_file": "b"}, "-"},
		{"not reported", map[string]string{}, "-"},
	} {
		if got := backlog(tc.row); got != tc.want {
			t.Errorf("%s: backlog = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A replica kept an hour behind on purpose reads ok, and says why it is an
// hour behind: "1h behind" and ok with no reason beside it reads like a status
// somebody forgot to grade.
func TestADelayedReplicaSaysItKeepsItsDelay(t *testing.T) {
	base := fixtureNames(t, "replica")[0]
	c := assessChannel(replicaRow(t, base, map[string]string{"seconds_behind_source": "3640", "sql_delay": "3600"}))
	f := replicaFacet([]channelRow{c})
	if f.status != "ok" || !strings.Contains(f.detail, "1h behind, keeping a 1h delay") {
		t.Errorf("facet = %+v, want ok and the delay named beside the lag", f)
	}
	undelayed := replicaFacet([]channelRow{assessChannel(replicaRow(t, base, nil))})
	if strings.Contains(undelayed.detail, "delay") {
		t.Errorf("detail = %q, want no delay mentioned on a replica that keeps none", undelayed.detail)
	}
}
