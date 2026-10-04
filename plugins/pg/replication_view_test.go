package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func primaryFacts(standbys []standbyRow, slots []slotRow) replicationFacts {
	return replicationFacts{
		server: serverFacts{versionNum: 170006, version: "17.6", walLevel: "replica", senders: 10, syncCommit: "on",
			started: time.Date(2026, 10, 2, 23, 58, 24, 0, time.UTC)},
		primary:  primaryPosition{lsn: "0/A56F6A8", walFile: "00000001000000000000000A", timeline: "1"},
		standbys: standbys, slots: slots,
	}
}

// What a standby as a role in pg_monitor read: the WAL receiver streaming, and
// 106077496 bytes of the primary's WAL received and not replayed, replay
// paused.
func pausedStandbyFacts() replicationFacts {
	return replicationFacts{
		server:  serverFacts{versionNum: 170006, version: "17.6", recovering: true, walLevel: "replica"},
		standby: standbyPosition{receiveLSN: str("0/A5A8000"), replayLSN: str("0/4045970"), gap: i64(106106880), paused: yes(), timeline: "1"},
		receiver: receiverRow{present: true, status: "streaming", host: str("dbwp-pg-primary-17"),
			port: i32(5432), slot: str("slot_replica_17"), endLSN: str("0/A5A8000"), behindEnd: i64(106106880)},
	}
}

func TestTheSummaryReadsEachShapeOfServer(t *testing.T) {
	minimal := primaryFacts(nil, nil)
	minimal.server.walLevel = "minimal"
	noSenders := primaryFacts(nil, nil)
	noSenders.server.senders = 0
	backup := caughtUpStandby()
	backup.state = str("backup")
	catching := caughtUpStandby()
	catching.state = str("catchup")
	notStreaming := pausedStandbyFacts()
	notStreaming.receiver = receiverRow{}
	hidden := pausedStandbyFacts()
	hidden.receiver = receiverRow{present: true, hidden: true}

	for _, tc := range []struct {
		name  string
		facts replicationFacts
		want  string
	}{
		{"wal_level minimal", minimal, "off — wal_level is minimal"},
		{"no senders", noSenders, "off — max_wal_senders is 0"},
		{"nothing connected", primaryFacts(nil, nil), "no standby is connected and there are no replication slots"},
		{"a slot nobody reads", primaryFacts(nil, []slotRow{extendedSlot()}),
			"no standby is connected (1 replication slot defined)"},
		{"one caught up", primaryFacts([]standbyRow{caughtUpStandby()}, nil), "1 standby streaming, furthest behind 0 B"},
		{"drift", primaryFacts([]standbyRow{caughtUpStandby(), driftingStandby()}, nil),
			"2 standbys streaming, furthest behind 101.2 MiB"},
		{"a base backup", primaryFacts([]standbyRow{backup}, nil), "1 taking a base backup, furthest behind 0 B"},
		{"catching up", primaryFacts([]standbyRow{catching}, nil), "1 catching up, furthest behind 0 B"},
		{"a standby, paused", pausedStandbyFacts(),
			"streaming from dbwp-pg-primary-17, replay paused, 101.2 MiB behind its upstream"},
		{"a standby with no receiver", notStreaming,
			"not streaming — no WAL receiver is running, replay paused, 101.2 MiB received but not replayed"},
		{"a standby whose receiver is hidden", hidden,
			"upstream hidden from this role, replay paused, 101.2 MiB received but not replayed"},
	} {
		if got := replicationSummary(tc.facts); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: summary = %q, want it to start %q", tc.name, got, tc.want)
		}
	}
}

// **Hidden is not zero, in any cell and in the sentence.** The role that
// cannot see a standby's positions gets the row, says so, and is shown no
// number that could be read as caught up.
func TestAStandbyTheRoleCannotSeeIsNeverShownAsZero(t *testing.T) {
	f := primaryFacts([]standbyRow{hiddenStandby()}, nil)

	summary := replicationSummary(f)
	if !strings.Contains(summary, "1 hidden from this role") || strings.Contains(summary, "0 B") ||
		strings.Contains(summary, "behind") {
		t.Errorf("summary = %q, want the standby hidden and no distance claimed", summary)
	}
	row := standbysTable(f).Rows[0]
	for i, cell := range row {
		col := standbysTable(f).Columns[i].Name
		switch col {
		case "Standby", "Slot":
		case "State":
			if cell != "hidden" {
				t.Errorf("State = %q, want hidden", cell)
			}
		case "Health":
			if cell != gradeUnknown {
				t.Errorf("Health = %q, want unknown", cell)
			}
		default:
			if cell != "-" {
				t.Errorf("%s = %q, want - for a column this role is not shown", col, cell)
			}
		}
	}
	if rows := attentionTable(f).Rows; len(rows) != 0 {
		t.Errorf("attention = %v, want nothing: no finding can be made from nothing", rows)
	}
}

func TestEveryRowHasACellPerColumn(t *testing.T) {
	f := primaryFacts([]standbyRow{hiddenStandby(), driftingStandby()}, []slotRow{lostSlot(), extendedSlot()})
	for name, tbl := range map[string]view.Table{
		"standbys": standbysTable(f), "slots": slotsTable(f), "attention": attentionTable(f),
	} {
		for _, row := range tbl.Rows {
			if len(row) != len(tbl.Columns) {
				t.Errorf("%s: row %v has %d cells for %d columns", name, row, len(row), len(tbl.Columns))
			}
		}
	}
}

// The lag columns are durations and Health is the one that is graded: State
// carries PostgreSQL's own words, which the status vocabulary does not know,
// and declaring it a status would put no colour on the word that matters.
func TestOnlyTheGradeColumnsAreStatus(t *testing.T) {
	f := primaryFacts([]standbyRow{driftingStandby()}, []slotRow{extendedSlot()})
	for _, tbl := range []view.Table{standbysTable(f), slotsTable(f)} {
		for _, c := range tbl.Columns {
			if (c.Kind == view.KindStatus) != (c.Name == "Health") {
				t.Errorf("column %s has kind %q", c.Name, c.Kind)
			}
		}
	}
}

func TestTheAttentionListIsWorstFirstAndLeavesOutWhatIsFine(t *testing.T) {
	f := primaryFacts([]standbyRow{caughtUpStandby(), driftingStandby()}, []slotRow{lostSlot(), extendedSlot()})
	rows := attentionTable(f).Rows
	if len(rows) != 3 {
		t.Fatalf("attention = %v, want the drifting standby and both slots, not the caught-up one", rows)
	}
	if rows[0][1] != gradeFail {
		t.Errorf("first row is %v, want the failure first", rows[0])
	}
	if rows[2][1] != gradeWarn {
		t.Errorf("last row is %v, want a warning last", rows[2])
	}
}

func TestAPausedStandbyIsAFinding(t *testing.T) {
	rows := attentionTable(pausedStandbyFacts()).Rows
	if len(rows) != 1 || rows[0][1] != gradeWarn || !strings.Contains(rows[0][2], "replay is paused") {
		t.Errorf("attention = %v, want one warning that replay is paused", rows)
	}
}

// Commits that wait are the one thing here an application feels, and the one
// that cannot be judged from a row the role cannot read.
func TestSynchronousCommitWithNoSynchronousStandbyFails(t *testing.T) {
	f := primaryFacts([]standbyRow{caughtUpStandby()}, nil)
	f.server.syncNames = "FIRST 1 (ghost)"
	if g := gradeSynchronous(f); g.status != gradeFail {
		t.Errorf("no sync standby: %q, want fail", g.status)
	}
	quorum := caughtUpStandby()
	quorum.syncKind, quorum.priority = str("quorum"), i32(1)
	f.standbys = []standbyRow{quorum}
	if g := gradeSynchronous(f); g.status != gradeOK {
		t.Errorf("a quorum standby: %q, want ok", g.status)
	}
	f.standbys = []standbyRow{hiddenStandby()}
	if g := gradeSynchronous(f); g.status != gradeOK {
		t.Errorf("a hidden standby: %q, want no finding rather than a guess", g.status)
	}
	f.server.syncNames = ""
	f.standbys = nil
	if g := gradeSynchronous(f); g.status != gradeOK {
		t.Errorf("synchronous commit off: %q, want ok", g.status)
	}
}

// A commit waits for a standby only under the settings that say it should.
// With synchronous_commit off or local the names are a standing instruction
// that nothing acts on, and a fail would be a page for a database that is not
// hanging.
func TestSynchronousStandbyNamesAreNotAFindingWhereCommitsDoNotWait(t *testing.T) {
	for _, setting := range []string{"off", "local"} {
		f := primaryFacts(nil, nil)
		f.server.syncNames, f.server.syncCommit = "FIRST 1 (ghost)", setting
		if g := gradeSynchronous(f); g.status != gradeOK {
			t.Errorf("synchronous_commit %s: %q, want ok", setting, g.status)
		}
	}
	f := primaryFacts(nil, nil)
	f.server.syncNames, f.server.syncCommit = "FIRST 1 (ghost)", "remote_apply"
	if g := gradeSynchronous(f); g.status != gradeFail {
		t.Errorf("synchronous_commit remote_apply: %q, want fail", g.status)
	}
}

// pg_is_wal_replay_paused is open to every role, so a standby whose receiver
// is hidden still says it is not applying WAL. Hidden is not a reason to stay
// quiet about a fact the role can read.
func TestAPausedReplayIsSaidEvenWhereTheReceiverIsHidden(t *testing.T) {
	f := pausedStandbyFacts()
	f.receiver = receiverRow{present: true, hidden: true}
	rows := attentionTable(f).Rows
	if len(rows) != 1 || rows[0][1] != gradeWarn || !strings.Contains(rows[0][2], "replay is paused") {
		t.Errorf("attention = %v, want one warning that replay is paused", rows)
	}
	f.standby.paused = nil
	if rows := attentionTable(f).Rows; len(rows) != 0 {
		t.Errorf("attention = %v, want nothing said about a link the role cannot see", rows)
	}
}

func TestALagReadsInTheUnitAPersonThinksIn(t *testing.T) {
	for seconds, want := range map[float64]string{
		0.0002: "<1 ms", 0.113: "113 ms", 1.02: "1s", 90: "1m30s", 3600: "1h",
	} {
		if got := lagText(seconds); got != want {
			t.Errorf("lagText(%v) = %q, want %q", seconds, got, want)
		}
	}
}

// The wording names what the surface's reader can reach: a flag at a
// terminal, the operator's setting over MCP, where there is no flag to type.
func TestTheHiddenWarningNamesTheRoleAndWhatGrantsIt(t *testing.T) {
	f := primaryFacts([]standbyRow{hiddenStandby(), hiddenStandby()}, nil)
	for _, tc := range []struct {
		surface plugin.Surface
		want    string
	}{
		{plugin.SurfaceCLI, "with --user"},
		{plugin.SurfaceMCP, "with the operator's `user` setting"},
	} {
		r := reqFor(t, "pg.replication", map[string]any{"user": "mon"}).WithSurface(tc.surface)
		w := hiddenWarning(f, r)
		if w == nil || w.Code != "pg.replication.hidden" {
			t.Fatalf("warning = %v, want pg.replication.hidden", w)
		}
		for _, want := range []string{`"mon"`, "2 connected standbys", "pg_monitor", tc.want} {
			if !strings.Contains(w.Message+"\n"+w.Hint, want) {
				t.Errorf("%s: warning %q lacks %q", tc.surface, w.Message+"\n"+w.Hint, want)
			}
		}
	}
	if w := hiddenWarning(primaryFacts([]standbyRow{caughtUpStandby()}, nil), reqFor(t, "pg.replication", nil)); w != nil {
		t.Errorf("a role that sees everything was warned: %v", w)
	}
}

// The statement the hint offers is one to paste, so the role is quoted the way
// SQL reads an identifier: a role holding a double quote doubles it, which a
// bare pair of quotes around the name did not, and the statement named another
// role or did not parse.
func TestTheGrantTheHiddenWarningOffersQuotesTheRoleAsSQLDoes(t *testing.T) {
	f := primaryFacts([]standbyRow{hiddenStandby()}, nil)
	for user, want := range map[string]string{
		"mon":        "`GRANT pg_monitor TO \"mon\"`",
		"app role é": "`GRANT pg_monitor TO \"app role é\"`",
		`we"ird`:     "`GRANT pg_monitor TO \"we\"\"ird\"`",
		`"leading`:   "`GRANT pg_monitor TO \"\"\"leading\"`",
		`back\slash`: "`GRANT pg_monitor TO \"back\\slash\"`",
	} {
		w := hiddenWarning(f, reqFor(t, "pg.replication", map[string]any{"user": user}))
		if w == nil || !strings.Contains(w.Hint, want) {
			t.Errorf("role %q: hint = %v, want %s in it", user, w, want)
		}
	}
}

func TestTheOverviewPointsAtThePageOnlyWhereThereIsOne(t *testing.T) {
	for _, tc := range []struct {
		surface plugin.Surface
		want    string
	}{
		{plugin.SurfaceCLI, "`rta pg replication`"},
		{plugin.SurfaceMCP, "`pg_replication`"},
	} {
		line := replicationLine(primaryFacts([]standbyRow{caughtUpStandby()}, nil), tc.surface)
		if !strings.Contains(line, tc.want) {
			t.Errorf("%s: %q does not point at %s", tc.surface, line, tc.want)
		}
	}
	if line := replicationLine(primaryFacts(nil, nil), plugin.SurfaceCLI); strings.Contains(line, "rta pg") {
		t.Errorf("a server with nothing to show points at a page: %q", line)
	}
}

func TestTheOverviewCountsWhatTheAttentionListHolds(t *testing.T) {
	healthy := replicationLine(primaryFacts([]standbyRow{caughtUpStandby()}, nil), plugin.SurfaceCLI)
	if strings.Contains(healthy, "attention") {
		t.Errorf("a healthy replication asks for attention: %q", healthy)
	}
	one := replicationLine(primaryFacts(nil, []slotRow{extendedSlot()}), plugin.SurfaceCLI)
	if !strings.Contains(one, ", 1 thing needs attention — `rta pg replication`") {
		t.Errorf("one finding: %q", one)
	}
	two := replicationLine(primaryFacts([]standbyRow{driftingStandby()}, []slotRow{lostSlot()}), plugin.SurfaceCLI)
	if !strings.Contains(two, ", 2 things need attention — ") {
		t.Errorf("two findings: %q", two)
	}
	sync := primaryFacts(nil, nil)
	sync.server.syncNames = "FIRST 1 (ghost)"
	if line := replicationLine(sync, plugin.SurfaceMCP); !strings.Contains(line, "`pg_replication`") {
		t.Errorf("a finding with no standby and no slot has nothing to point at: %q", line)
	}
}

func TestAServerTooOldIsRefusedByName(t *testing.T) {
	r := reqFor(t, "pg.replication", nil).WithSurface(plugin.SurfaceMCP)
	// server_version as postgres:13 reports it, banner and all: the message
	// names the release, not the build.
	verr := tooOldForReplication(serverFacts{versionNum: 130023, version: "13.23 (Debian 13.23-1.pgdg13+1)"}, r)
	if verr.Code != "pg.replication.version" || !strings.Contains(verr.Message, "PostgreSQL 13.23 ") ||
		strings.Contains(verr.Message, "Debian") || !strings.Contains(verr.Hint, "`pg_query`") {
		t.Errorf("error = %+v", verr)
	}
}

// **The read tier returns nothing anybody stored, and for this capability
// that is a property of its SQL.** Every relation any of the reads touches is
// a replication catalogue or a function of the log's position, and a table of
// the user's would not pass.
func TestTheReplicationReadsTouchOnlyReplicationCatalogues(t *testing.T) {
	allowed := map[string]bool{
		"pg_stat_replication": true, "pg_replication_slots": true,
		"pg_stat_wal_receiver": true, "cur": true,
	}
	from := regexp.MustCompile(`(?i)\b(?:from|join)\s+([a-z_]+)`)
	extract := regexp.MustCompile(`(?i)extract\([^)]*\)`)
	for _, version := range []int{140000, 160000, 170000} {
		for _, recovering := range []bool{false, true} {
			for _, sql := range []string{
				serverSQL, primarySQL, standbySQL, receiverSQL, checkpointSQL,
				standbysSQL(recovering), slotsSQL(serverFacts{versionNum: version, recovering: recovering}),
			} {
				// extract(epoch from col) is a function's own word, not a relation.
				for _, m := range from.FindAllStringSubmatch(extract.ReplaceAllString(sql, ""), -1) {
					if !allowed[m[1]] && !strings.HasPrefix(m[1], "pg_control") {
						t.Errorf("reads %q:\n%s", m[1], sql)
					}
				}
			}
		}
	}
}

// greatest(NULL, 0) is 0 — it ignores a NULL — and turned a standby the role
// could not see into one exactly caught up. The distance is a case so that
// an unknown position stays unknown.
func TestADistanceStaysUnknownWhenAPositionIs(t *testing.T) {
	d := distance("a", "b")
	if strings.Contains(strings.ToLower(d), "greatest") || !strings.Contains(d, "case when") {
		t.Errorf("distance = %q, want a case that keeps NULL", d)
	}
}

func TestASlotsInvalidationIsReadFromWhereThisVersionKeepsIt(t *testing.T) {
	for version, want := range map[int]string{
		140000: "null::text", 150000: "null::text",
		160000: "r.conflicting", 170000: "r.invalidation_reason", 180000: "r.invalidation_reason",
	} {
		if got := invalidationSQL(version); !strings.Contains(got, want) {
			t.Errorf("%d: %q, want %q", version, got, want)
		}
	}
}
