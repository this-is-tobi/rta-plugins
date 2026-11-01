package family

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The compact form of mysql.overview is what a dashboard tile shows (its first
// eight lines) and what an agent reads by default, so what these hold is its
// order and its number of rows: the verdict first, how the server is doing
// before what it is called, and no more rows than a tile has lines. Each is run
// against the answers real servers gave in each state (testdata/replication),
// with the overview's own statements answered by the test.

func TestTheCompactFormOfAHealthyReplicaLeadsWithItsVerdict(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		f := newOverviewFake(t, scenario("replica"))
		f.counters["Threads_running"] = "4"
		f.counters["Slow_queries"] = "12"
		rows := f.compact(t)
		if got, want := keysOf(rows), "status,up since,replication,connections,lock waits,running,server"; got != want {
			t.Fatalf("rows = %s, want %s", got, want)
		}
		for key, want := range map[string]string{
			"status":      "ok",
			"up since":    "2026-10-04 21:55 UTC (14d 2h)",
			"replication": "ok — replica of " + captured().source + ", 0s behind",
			"connections": "12 of 151",
			"lock waits":  "none",
			"running":     "3 threads running, 12 slow queries since start",
			"server":      "localhost:3306 · " + captured().server,
		} {
			if got := valueOf(rows, key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
	})
}

// The aggregation over INFORMATION_SCHEMA totals every table of every database
// for a table the compact form does not show, so it is the detail's alone.
func TestTheCompactFormDoesNotRunTheDatabasesAggregationAndTheDetailDoes(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		f := newOverviewFake(t, scenario("replica"))
		f.compact(t)
		if f.askedFor("INFORMATION_SCHEMA.SCHEMATA") || f.askedFor("INFORMATION_SCHEMA.PROCESSLIST") {
			t.Errorf("the compact form asked for what only the detail shows:\n%s", strings.Join(f.asked, "\n--\n"))
		}
		compact := len(f.asked)

		d := newOverviewFake(t, scenario("replica"))
		fixedClock(t)
		if _, err := detailedOverview(t.Context(), d.open(t), req(t, engine.NS+".overview", nil)); err != nil {
			t.Fatal(err)
		}
		if !d.askedFor("INFORMATION_SCHEMA.SCHEMATA") || !d.askedFor("INFORMATION_SCHEMA.PROCESSLIST") {
			t.Errorf("the detail left out the databases or the sessions:\n%s", strings.Join(d.asked, "\n--\n"))
		}
		if len(d.asked) <= compact {
			t.Errorf("the detail asked %d statements and the compact form %d, want the detail's own on top", len(d.asked), compact)
		}
	})
}

// The lock waits and the slow queries are names in the statement that already
// read the uptime and the threads, not a statement of their own.
func TestTheLockWaitsAndTheSlowQueriesRideOnTheStatementThatWasThere(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		f := newOverviewFake(t, scenario("standalone"))
		f.compact(t)
		var counters []string
		for _, q := range f.asked {
			if strings.Contains(q, "SHOW GLOBAL STATUS WHERE") {
				counters = append(counters, q)
			}
		}
		if len(counters) != 1 {
			t.Fatalf("%d statements read the status variables, want the one there was:\n%s", len(counters), strings.Join(counters, "\n"))
		}
		for _, name := range []string{"Uptime", "Threads_connected", "Threads_running", "Innodb_row_lock_current_waits", "Slow_queries"} {
			if !strings.Contains(counters[0], "'"+name+"'") {
				t.Errorf("the statement does not ask for %s: %s", name, counters[0])
			}
		}
	})
}

func TestAStoppedSQLThreadIsTheVerdictAndTheReplicationRowSaysWhichThread(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		rows := newOverviewFake(t, scenario("sql-error")).compact(t)
		if got, want := valueOf(rows, "status"), "fail — replication: SQL thread stopped"; got != want {
			t.Errorf("status = %q, want %q", got, want)
		}
		if got, want := valueOf(rows, "replication"), "fail — SQL thread stopped; SQL errno 1062"; got != want {
			t.Errorf("replication = %q, want %q", got, want)
		}
	})
}

func TestAReplicaBehindItsSourceWarnsWithHowFarBehind(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		for _, tc := range []struct {
			behind, status, replication string
		}{
			{"42", "ok", "ok — replica of " + captured().source + ", 42s behind"},
			{"90", "warn — replication: 1m30s behind", "warn — 1m30s behind"},
			{"3600", "fail — replication: 1h behind", "fail — 1h behind"},
		} {
			f := newOverviewFake(t, scenario("replica"))
			f.setCell(t, captured().replicaStatus, captured().lagColumn, tc.behind)
			rows := f.compact(t)
			if got := valueOf(rows, "status"); got != tc.status {
				t.Errorf("%ss behind: status = %q, want %q", tc.behind, got, tc.status)
			}
			if got := valueOf(rows, "replication"); got != tc.replication {
				t.Errorf("%ss behind: replication = %q, want %q", tc.behind, got, tc.replication)
			}
		}
	})
}

func TestTheOtherReplicationRolesEachReadAsOneLine(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		for state, want := range map[string]string{
			"source":     "ok — source of 1 replica",
			"standalone": "none — standalone",
			"monitor":    "none — not a replica, binary log on",
		} {
			if got := valueOf(newOverviewFake(t, scenario(state)).compact(t), "replication"); got != want {
				t.Errorf("%s: replication = %q, want %q", state, got, want)
			}
		}
	})
}

func TestAccountWithoutTheReplicationGrantIsToldWhatItNeedsAndTheStatusSaysSo(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		rows := newOverviewFake(t, scenario("denied")).compact(t)
		if got, want := valueOf(rows, "replication"), "could not be read — needs "+captured().grant; got != want {
			t.Errorf("replication = %q, want %q", got, want)
		}
		if got, want := valueOf(rows, "status"), "warn — replication could not be read"; got != want {
			t.Errorf("status = %q, want %q", got, want)
		}
		if got := valueOf(rows, "connections"); got != "12 of 151" {
			t.Errorf("connections = %q, a refused replication read took the other rows with it", got)
		}
	})
}

func TestConnectionsAreGradedWhereTheShareIsWorthAWord(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		for _, tc := range []struct {
			threads, status, connections string
		}{
			{"12", "ok", "12 of 151"},
			{"128", "ok", "128 of 151"},
			{"129", "warn — 85% of max_connections", "warn — 129 of 151 (85%)"},
			{"143", "warn — 94% of max_connections", "warn — 143 of 151 (94%)"},
			{"144", "fail — 95% of max_connections", "fail — 144 of 151 (95%)"},
			{"151", "fail — 100% of max_connections", "fail — 151 of 151 (100%)"},
		} {
			f := newOverviewFake(t, scenario("standalone"))
			f.counters["Threads_connected"] = tc.threads
			rows := f.compact(t)
			if got := valueOf(rows, "connections"); got != tc.connections {
				t.Errorf("%s threads: connections = %q, want %q", tc.threads, got, tc.connections)
			}
			if got := valueOf(rows, "status"); got != tc.status {
				t.Errorf("%s threads: status = %q, want %q", tc.threads, got, tc.status)
			}
		}
	})
}

func TestAFailBeatsAWarnWhicheverCameFirst(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		f := newOverviewFake(t, scenario("sql-error"))
		f.counters["Threads_connected"] = "140"
		if got, want := valueOf(f.compact(t), "status"), "fail — replication: SQL thread stopped"; got != want {
			t.Errorf("status = %q, want %q", got, want)
		}
		g := newOverviewFake(t, scenario("replica"))
		g.counters["Threads_connected"] = "150"
		g.setCell(t, captured().replicaStatus, captured().lagColumn, "90")
		if got, want := valueOf(g.compact(t), "status"), "fail — 99% of max_connections"; got != want {
			t.Errorf("status = %q, want the fail of the connections over the warn of the lag: %q", got, want)
		}
	})
}

func TestLockWaitsAreCountedAndLeftOutWhereTheServerHasNone(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		f := newOverviewFake(t, scenario("standalone"))
		f.counters["Innodb_row_lock_current_waits"] = "1"
		if got, want := valueOf(f.compact(t), "lock waits"), "1 transaction waiting on a row lock"; got != want {
			t.Errorf("lock waits = %q, want %q", got, want)
		}
		f = newOverviewFake(t, scenario("standalone"))
		f.counters["Innodb_row_lock_current_waits"] = "7"
		if got, want := valueOf(f.compact(t), "lock waits"), "7 transactions waiting on a row lock"; got != want {
			t.Errorf("lock waits = %q, want %q", got, want)
		}
		f = newOverviewFake(t, scenario("standalone"))
		delete(f.counters, "Innodb_row_lock_current_waits")
		if got := valueOf(f.compact(t), "lock waits"); got != "<no such row>" {
			t.Errorf("lock waits = %q on a server that does not report them, want no row", got)
		}
	})
}

// Threads_running counts the session that is counting it, so an idle server
// said 1; the row is for the other threads and the slow queries, and is not
// there when there are none.
func TestTheRunningRowLeavesOutTheCountingThreadAndIsThereOnlyWhenThereIsSomething(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		for _, tc := range []struct {
			running, slow, want string
		}{
			{"1", "0", "<no such row>"},
			{"0", "0", "<no such row>"},
			{"2", "0", "1 thread running"},
			{"5", "0", "4 threads running"},
			{"1", "1", "1 slow query since start"},
			{"3", "250", "2 threads running, 250 slow queries since start"},
		} {
			f := newOverviewFake(t, scenario("standalone"))
			f.counters["Threads_running"], f.counters["Slow_queries"] = tc.running, tc.slow
			if got := valueOf(f.compact(t), "running"); got != tc.want {
				t.Errorf("running %s, slow %s: row = %q, want %q", tc.running, tc.slow, got, tc.want)
			}
		}
	})
}

func TestAnIdleStandaloneServerHasTheFewestRows(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		rows := newOverviewFake(t, scenario("standalone")).compact(t)
		if got, want := keysOf(rows), "status,up since,replication,connections,lock waits,server"; got != want {
			t.Errorf("rows = %s, want %s", got, want)
		}
		if got := valueOf(rows, "status"); got != "ok" {
			t.Errorf("status = %q, want ok", got)
		}
	})
}

func TestAGroupReplicationMemberHasARowOfItsOwnAndStandaloneIsNotItsReplicationRow(t *testing.T) {
	rows := newOverviewFake(t, scenario("group-primary")).compact(t)
	if got, want := valueOf(rows, "cluster"), "ok — online, and all 2 members are online"; got != want {
		t.Errorf("cluster = %q, want %q", got, want)
	}
	if got, want := valueOf(rows, "replication"), "none — not a replica"; got != want {
		t.Errorf("replication = %q, want %q", got, want)
	}
	if got, want := keysOf(rows), "status,up since,replication,connections,lock waits,cluster,server"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}

	rows = newOverviewFake(t, scenario("group-minority")).compact(t)
	if got := valueOf(rows, "cluster"); !strings.HasPrefix(got, "fail — sees 1 member of 2") {
		t.Errorf("cluster = %q, want the lost majority first", got)
	}
	if got, want := valueOf(rows, "status"), "fail — cluster: sees 1 member of 2"; got != want {
		t.Errorf("status = %q, want the lost majority as the verdict: %q", got, want)
	}

	rows = newOverviewFake(t, scenario("group-nogrant")).compact(t)
	if got, want := valueOf(rows, "cluster"), "could not be read — needs SELECT"; got != want {
		t.Errorf("cluster = %q, want %q", got, want)
	}
	if got, want := valueOf(rows, "status"), "warn — cluster could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// MariaDB's own clustering layer, beside MySQL's Group Replication above.
func TestAGaleraNodeHasARowOfItsOwnAndOnlyWhereWsrepIsOn(t *testing.T) {
	rows := newOverviewFake(t, "mariadb-11.4-galera-synced").compact(t)
	if got, want := valueOf(rows, "cluster"), "ok — in the primary component and synced with 3 members"; got != want {
		t.Errorf("cluster = %q, want %q", got, want)
	}
	if got, want := valueOf(rows, "replication"), "none — not a replica"; got != want {
		t.Errorf("replication = %q, want %q: a node of a cluster is not a standalone server", got, want)
	}
	if got, want := keysOf(rows), "status,up since,replication,connections,lock waits,cluster,server"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}

	rows = newOverviewFake(t, "mariadb-11.4-standalone").compact(t)
	if got := valueOf(rows, "cluster"); got != "<no such row>" {
		t.Errorf("cluster = %q on a server without wsrep, want no row", got)
	}

	rows = newOverviewFake(t, "mariadb-11.4-galera-split").compact(t)
	if got := valueOf(rows, "cluster"); !strings.HasPrefix(got, "fail — not in the primary component") {
		t.Errorf("cluster = %q, want the split first", got)
	}
	if got, want := valueOf(rows, "status"), "fail — cluster: not in the primary component"; got != want {
		t.Errorf("status = %q, want the split as the verdict: %q", got, want)
	}

	rows = newOverviewFake(t, "mariadb-11.4-galera-desynced").compact(t)
	if got, want := valueOf(rows, "status"), "warn — cluster: state Donor/Desynced"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// The counters and the replication are separate reads: one that cannot be made
// costs its rows, says why, and is named in the verdict, and the others stay.
func TestARowTheServerRefusesSaysSoAndTheOthersAreStillThere(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		denied := &mysql.MySQLError{Number: 1227, Message: "Access denied; you need the PROCESS privilege"}
		f := newOverviewFake(t, scenario("replica"))
		f.cErr = denied
		rows := f.compact(t)
		if got, want := keysOf(rows), "status,up since,replication,connections,server"; got != want {
			t.Fatalf("rows = %s, want %s", got, want)
		}
		for _, key := range []string{"up since", "connections"} {
			if got := valueOf(rows, key); !strings.HasPrefix(got, "could not be read — ") {
				t.Errorf("%s = %q, want it to say it could not be read", key, got)
			}
		}
		if got, want := valueOf(rows, "status"), "warn — status variables could not be read"; got != want {
			t.Errorf("status = %q, want %q", got, want)
		}
		if got := valueOf(rows, "replication"); !strings.HasPrefix(got, "ok — replica of ") {
			t.Errorf("replication = %q, a refused counter read took the replication row with it", got)
		}

		g := newOverviewFake(t, scenario("replica"))
		g.idErr = errors.New("connection reset by peer")
		rows = g.compact(t)
		if got := valueOf(rows, "connections"); !strings.HasPrefix(got, "could not be read — ") {
			t.Errorf("connections = %q without max_connections", got)
		}
		if got := valueOf(rows, "server"); !strings.HasSuffix(got, "version could not be read") {
			t.Errorf("server = %q, want the address and that the version could not be read", got)
		}
		if got, want := valueOf(rows, "status"), "warn — max_connections, version could not be read"; got != want {
			t.Errorf("status = %q, want %q", got, want)
		}
		if got := valueOf(rows, "up since"); got == "<no such row>" || strings.HasPrefix(got, "could not") {
			t.Errorf("up since = %q, the counters were readable", got)
		}
	})
}

func TestAServerThatAnswersNothingIsAnErrorAndNotAPageOfRefusals(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		f := newOverviewFake(t, scenario("replica"))
		gone := errors.New("connection reset by peer")
		f.idErr, f.cErr = gone, gone
		f.fixture.Statements = map[string]fixtureAnswer{}
		fixedClock(t)
		_, err := compactOverview(t.Context(), f.open(t), req(t, engine.NS+".overview", nil))
		var verr *view.Error
		if !errors.As(err, &verr) || !strings.HasPrefix(verr.Code, engine.NS+".conn.") {
			t.Errorf("err = %v, want a mysql.conn error", err)
		}
	})
}

// Whatever the server's state, the compact form is at most eight rows, led by
// the status, and none is wider than the 80 columns of a terminal: the sweep is
// every state a real server was captured in.
func TestEveryCapturedStateFitsATileOfEightLinesAtEightyColumns(t *testing.T) {
	all, err := filepath.Glob(filepath.Join("testdata", "replication", "*.json"))
	if err != nil || len(all) == 0 {
		t.Fatalf("no scenarios (%v)", err)
	}
	for _, p := range all {
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		f := newOverviewFake(t, name)
		f.counters["Threads_connected"] = "150"
		f.counters["Threads_running"] = "9"
		f.counters["Slow_queries"] = "1234567"
		f.counters["Innodb_row_lock_current_waits"] = "12"
		rows := f.compact(t)
		if len(rows) > 8 {
			t.Errorf("%s: %d rows, want at most eight: %s", name, len(rows), keysOf(rows))
		}
		if rows[0].Key != "status" {
			t.Errorf("%s: first row is %s, want the status", name, rows[0].Key)
		}
		for _, r := range rows {
			if line := r.Key + strings.Repeat(" ", max(13-len(r.Key), 2)) + r.Value; len([]rune(line)) > 80 {
				t.Errorf("%s: %s is %d columns: %q", name, r.Key, len([]rune(line)), line)
			}
		}
	}
}

// A row that does not fit a line is shortened where a reader loses least: the
// parentheses that explain a number first, then the end of the sentence, at a
// word and not through one.
func TestAWordyRowIsShortenedWithoutItsParenthesesThenAtAWord(t *testing.T) {
	for _, tc := range []struct {
		text  string
		width int
		want  string
	}{
		{"SQL thread stopped", 59, "SQL thread stopped"},
		{"SQL thread stopped; SQL errno 1062 (a duplicate key on the replica)", 59, "SQL thread stopped; SQL errno 1062"},
		{"not in the primary component — a split-brain risk: it must not be written to", 59, "not in the primary component — a split-brain risk: it must…"},
		{"averyveryverylongwordwithnospacesatalltobecutbythenumberofcolumns", 20, "averyveryverylongwo…"},
	} {
		got := fit(tc.text, tc.width)
		if got != tc.want {
			t.Errorf("fit(%q, %d) = %q, want %q", tc.text, tc.width, got, tc.want)
		}
		if n := len([]rune(got)); n > tc.width {
			t.Errorf("fit(%q, %d) is %d columns", tc.text, tc.width, n)
		}
	}
}

// Everything the compact form says is said by the page that `detail` asks for,
// together with what the page said before it.
func TestTheDetailedPageKeepsTheOldSectionsAndCarriesTheCompactFormsFigures(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		f := newOverviewFake(t, scenario("replica"))
		f.counters["Innodb_row_lock_current_waits"] = "2"
		f.counters["Slow_queries"] = "12"
		fixedClock(t)
		v, err := detailedOverview(t.Context(), f.open(t), req(t, engine.NS+".overview", nil))
		if err != nil {
			t.Fatal(err)
		}
		sections, ok := v.(view.Sections)
		if !ok {
			t.Fatalf("view is %T, want sections", v)
		}
		var titles []string
		status := map[string]string{}
		for _, it := range sections.Items {
			titles = append(titles, it.Title)
			if kv, ok := it.View.(view.KeyValue); ok && it.Title == "status" {
				for _, p := range kv.Pairs {
					status[p.Key] = p.Value
				}
			}
		}
		if got, want := strings.Join(titles, ","), "status,replication,databases,activity"; got != want {
			t.Errorf("sections = %s, want %s", got, want)
		}
		for key, want := range map[string]string{
			"uptime":       "14d 2h",
			"up since":     "2026-10-04 21:55 UTC (14d 2h)",
			"connections":  "12 of 151",
			"lock waits":   "2 transactions waiting on a row lock",
			"slow queries": "12 since start",
			"version":      captured().version,
		} {
			if got := status[key]; got != want {
				t.Errorf("status %s = %q, want %q", key, got, want)
			}
		}
	})
}
