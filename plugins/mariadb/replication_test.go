package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Each test below runs against the answers real servers of every version this
// plugin is verified on gave in one state, so a rule that holds for one
// server's wording and not another's fails here rather than in somebody's
// terminal. The account named is the one the capture was made as.

func summaryRow(t *testing.T, name, user string) (row []string, tables func(string) ([][]string, bool), st state) {
	t.Helper()
	st = stateFrom(t, name, user)
	v := replicationView(st)
	sum, ok := tableOf(v, "summary")
	if !ok || len(sum.Rows) != 1 {
		t.Fatalf("%s: summary is not one row: %+v", name, sum)
	}
	return sum.Rows[0], func(id string) ([][]string, bool) {
		tb, ok := tableOf(v, id)
		return tb.Rows, ok
	}, st
}

func TestASourceNamesItsReplicas(t *testing.T) {
	for _, name := range fixtureNames(t, "source") {
		row, tables, st := summaryRow(t, name, "root")
		if !strings.HasPrefix(row[0], "source of 1 replica") || row[1] != "ok" {
			t.Errorf("%s: summary = %v, want a source of one replica that is ok", name, row)
		}
		if hosts, ok := tables("replicas"); !ok || len(hosts) != 1 {
			t.Errorf("%s: connected replicas = %v", name, hosts)
		}
		if _, ok := tables("sources"); ok {
			t.Errorf("%s: a source that follows nothing shows a replicating-from table", name)
		}
		if got := pairsOf(replicationView(st), "server")["binary log"]; got == "" || got == "off" {
			t.Errorf("%s: binary log = %q, want its file and position", name, got)
		}
		if len(st.unread) != 0 {
			t.Errorf("%s: unread parts on an account that may read everything: %v", name, st.unread)
		}
	}
}

func TestACaughtUpReplicaIsOkAndShowsWhereItIs(t *testing.T) {
	for _, name := range fixtureNames(t, "replica") {
		row, tables, _ := summaryRow(t, name, "root")
		if !strings.HasPrefix(row[0], "replica of ") || row[1] != "ok" {
			t.Errorf("%s: summary = %v, want a replica that is ok", name, row)
		}
		sources, ok := tables("sources")
		if !ok || len(sources) != 1 {
			t.Fatalf("%s: sources = %v", name, sources)
		}
		s := sources[0]
		// Channel, Source, IO, SQL, Behind, Read, Applied, Backlog, Status.
		if s[2] != "yes" || s[3] != "yes" || s[4] != "0s" || s[8] != "ok" {
			t.Errorf("%s: sources row = %v, want both threads running, 0s behind, ok", name, s)
		}
		if s[5] == "-" || s[5] != s[6] {
			t.Errorf("%s: read %q and applied %q, want the same position when caught up", name, s[5], s[6])
		}
		if s[7] != "0 B" {
			t.Errorf("%s: backlog = %q, want 0 B", name, s[7])
		}
		// Received and applied are both read, and a caught-up replica has nothing
		// between them. An applied position that fails to be read shows as a dash
		// and as everything pending, which is how a column read under the wrong
		// spelling would look.
		if g, ok := tables("gtid"); ok && (len(g) != 1 || g[0][3] == "-" || g[0][4] != "0") {
			t.Errorf("%s: transactions = %v, want received and applied read and nothing pending", name, g)
		}
	}
}

// **The trap the lag figure sets.** A replica whose applier cannot keep up
// still reads as running and as seconds behind; what shows it is the distance
// between what was read and what was applied, and the transactions received
// and not yet executed.
func TestABlockedApplierShowsItsBacklogAndPendingTransactions(t *testing.T) {
	for _, name := range fixtureNames(t, "backlog") {
		_, tables, _ := summaryRow(t, name, "root")
		sources, _ := tables("sources")
		if len(sources) != 1 || sources[0][7] == "0 B" || sources[0][7] == "-" {
			t.Errorf("%s: backlog = %v, want bytes between read and applied", name, sources)
		}
		if sources[0][5] == sources[0][6] {
			t.Errorf("%s: read and applied are both %q on a replica that is behind", name, sources[0][5])
		}
		gtid, ok := tables("gtid")
		if !ok || len(gtid) != 1 || gtid[0][4] == "0" || gtid[0][4] == "-" {
			t.Errorf("%s: pending transactions = %v, want a count of what was received and not applied", name, gtid)
		}
	}
}

func TestAReceiverStoppedByHandIsAFailureNotACaughtUpReplica(t *testing.T) {
	for _, name := range fixtureNames(t, "io-stopped") {
		row, tables, _ := summaryRow(t, name, "root")
		if row[1] != "fail" || !strings.Contains(row[2], "IO thread stopped") || !strings.Contains(row[2], "no error recorded") {
			t.Errorf("%s: summary = %v, want a failure naming the IO thread and that no error was recorded", name, row)
		}
		sources, _ := tables("sources")
		if !strings.HasPrefix(sources[0][8], "fail — IO thread stopped") || sources[0][2] != "no" {
			t.Errorf("%s: sources row = %v", name, sources[0])
		}
	}
}

func TestAnAuthenticationFailureIsNamedByItsErrno(t *testing.T) {
	for _, name := range fixtureNames(t, "io-error") {
		row, _, _ := summaryRow(t, name, "root")
		if row[1] != "fail" || !strings.Contains(row[2], "IO errno 1045") || !strings.Contains(row[2], "refused the replication account") {
			t.Errorf("%s: summary = %v, want a failure naming errno 1045 and what it means", name, row)
		}
	}
}

func TestADuplicateKeyStopsTheSQLThreadAndIsNamedByItsErrno(t *testing.T) {
	for _, name := range fixtureNames(t, "sql-error") {
		row, _, _ := summaryRow(t, name, "root")
		if row[1] != "fail" || !strings.Contains(row[2], "SQL thread stopped") || !strings.Contains(row[2], "SQL errno 1062") {
			t.Errorf("%s: summary = %v, want a failure naming the SQL thread and errno 1062", name, row)
		}
	}
}

// A replica of several sources is one row per source, and the summary names
// the one that is failing rather than averaging it into the one that is fine.
func TestAReplicaOfSeveralSourcesIsOneRowEachAndNamesTheFailingOne(t *testing.T) {
	for _, name := range fixtureNames(t, "channels") {
		row, tables, _ := summaryRow(t, name, "root")
		if row[0] != "replica of 2 sources" || row[1] != "fail" || !strings.HasPrefix(row[2], "second: SQL thread stopped") {
			t.Errorf("%s: summary = %v, want two sources and the failing channel named", name, row)
		}
		sources, _ := tables("sources")
		if len(sources) != 2 || sources[0][0] != "(default)" || sources[0][8] != "ok" ||
			sources[1][0] != "second" || !strings.HasPrefix(sources[1][8], "fail") {
			t.Errorf("%s: sources = %v, want the default channel ok and the second failing", name, sources)
		}
		if g, _ := tables("gtid"); len(g) < 1 {
			t.Errorf("%s: no transactions row for a replica following by GTID", name)
		}
	}
}

// **The fact stays, the disclosure does not.** A replication error's text is
// the statement that failed, row values and all, and this capability is Read.
// Every captured replica row that carries one is checked: nothing the view
// renders may contain it, in any format an agent could be handed.
func TestNoReplicationErrorTextReachesTheReadTier(t *testing.T) {
	checked := 0
	for _, scenario := range []string{"io-error", "sql-error"} {
		for _, name := range fixtureNames(t, scenario) {
			st := stateFrom(t, name, "root")
			encoded, err := json.Marshal(replicationView(st))
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range st.channels {
				for _, col := range []string{"last_error", "last_io_error", "last_sql_error"} {
					text := row[col]
					if text == "" {
						continue
					}
					checked++
					if strings.Contains(string(encoded), text) {
						t.Errorf("%s: the text of %s reached the view: %q", name, col, text)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no captured replica carried an error text, so this proved nothing")
	}
}

// An account that may not read the replication statements gets an answer that
// says so — never the "standalone" that an empty result would be taken for,
// on exactly the server somebody is worried about.
func TestAnAccountWithoutTheGrantIsNeverToldTheServerIsStandalone(t *testing.T) {
	for _, name := range fixtureNames(t, "denied") {
		row, _, st := summaryRow(t, name, "nopriv")
		if row[0] == "standalone" || row[1] != "warn" {
			t.Errorf("%s: summary = %v, want an incomplete answer that is not standalone", name, row)
		}
		if len(st.unread) == 0 {
			t.Errorf("%s: nothing was reported as unreadable", name)
		}
		codes := codesOf(replicationView(st))
		if len(codes) == 0 {
			t.Errorf("%s: no warnings for an account that may read nothing", name)
		}
		for _, w := range replicationView(st).Warnings {
			if w.Code != "mariadb.replication.denied" || !strings.Contains(w.Hint, "GRANT ") {
				t.Errorf("%s: warning = %+v, want the denial with the grant that fixes it", name, w)
			}
		}
	}
}

func TestAServerWithNoReplicationSaysSoPlainly(t *testing.T) {
	for _, name := range fixtureNames(t, "standalone") {
		row, tables, st := summaryRow(t, name, "root")
		if row[0] != "standalone" || row[1] != "none" || !strings.Contains(row[2], "not replicating from anything") {
			t.Errorf("%s: summary = %v, want a plain standalone", name, row)
		}
		for _, id := range []string{"sources", "gtid", "replicas"} {
			if _, ok := tables(id); ok {
				t.Errorf("%s: a standalone server shows a %s table", name, id)
			}
		}
		if len(st.unread) != 0 {
			t.Errorf("%s: unread parts: %v", name, st.unread)
		}
	}
}
