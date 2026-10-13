package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The position line is one more reading on calls whose job is something else
// — whether the server answers, a glance at it — so what these hold is that it
// is the same reading pg.replication makes, that it is complete when it
// appears, and that its absence is quiet.

// asking is a script that records what was asked of it, and can be made to
// refuse one statement the way a server does a role it will not answer.
type asking struct {
	script
	asked  *[]string
	refuse string
	err    error
}

func (q asking) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	*q.asked = append(*q.asked, sql)
	if q.refuse != "" && strings.Contains(sql, q.refuse) {
		return valueRow{err: q.err}
	}
	return q.script.QueryRow(ctx, sql, args...)
}

func (q asking) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	*q.asked = append(*q.asked, sql)
	return q.script.Query(ctx, sql, args...)
}

func (q asking) askedFor(want string) bool {
	for _, sql := range *q.asked {
		if strings.Contains(sql, want) {
			return true
		}
	}
	return false
}

var started = time.Date(2026, 10, 2, 23, 58, 24, 0, time.UTC)

func statusAnswer() answer {
	return answer{match: "version()", rows: [][]any{{"PostgreSQL 17.6", "app", "mon", "8 MB"}}}
}

func serverAnswer(versionNum string, recovering bool) answer {
	return answer{match: "server_version_num", rows: [][]any{
		{versionNum, "17.6", recovering, started, "replica", "10", "", "on"},
	}}
}

func primaryAnswer(lsn, file string) answer {
	return answer{match: "pg_walfile_name", rows: [][]any{{lsn, file}}}
}

// A standby's row as standbySQL returns it: the receive and replay positions,
// the bytes between them, the last replayed commit, and whether replay is
// paused. Any position may be NULL, which the script says as nil.
func standbyAnswer(receive, replay any) answer {
	return answer{match: "pg_is_wal_replay_paused", rows: [][]any{{receive, replay, nil, nil, false}}}
}

func askingScript(a ...answer) asking { return asking{script: script(a), asked: new([]string)} }

func pairsOf(t *testing.T, v view.View, err error) map[string]string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("view is %T, want key/value", v)
	}
	out := map[string]string{}
	for _, p := range kv.Pairs {
		out[p.Key] = p.Value
	}
	return out
}

func lastKey(t *testing.T, v view.View) string {
	t.Helper()
	pairs := v.(view.KeyValue).Pairs
	return pairs[len(pairs)-1].Key
}

func TestAPrimarysStatusEndsWithItsWritePositionTimelineAndFile(t *testing.T) {
	q := askingScript(statusAnswer(), serverAnswer("170006", false),
		primaryAnswer("0/3000148", "000000010000000000000003"))
	v, err := statusView(context.Background(), q, reqFor(t, "pg.status", nil))
	pairs := pairsOf(t, v, err)
	if got, want := pairs["WAL position"], "0/3000148, timeline 1, file 000000010000000000000003"; got != want {
		t.Errorf("WAL position = %q, want %q", got, want)
	}
	if lastKey(t, v) != "WAL position" {
		t.Errorf("the line is not the last: %v", v)
	}
	// The four lines that were there before are unchanged.
	for key, want := range map[string]string{
		"server": "PostgreSQL 17.6", "database": "app", "connected as": "mon", "size": "8 MB",
	} {
		if pairs[key] != want {
			t.Errorf("%s = %q, want %q", key, pairs[key], want)
		}
	}
}

// A timeline past 9 is two hex digits, and the file name's first eight are the
// timeline in hex: the line says it in decimal, which is how every other
// surface here and psql's own pg_control_checkpoint say it.
func TestTheTimelineOfTheStatusLineIsReadFromTheFileNameInDecimal(t *testing.T) {
	q := askingScript(statusAnswer(), serverAnswer("170006", false),
		primaryAnswer("A/B374D848", "0000000A0000000A000000B3"))
	v, err := statusView(context.Background(), q, reqFor(t, "pg.status", nil))
	if got, want := pairsOf(t, v, err)["WAL position"], "A/B374D848, timeline 10, file 0000000A0000000A000000B3"; got != want {
		t.Errorf("WAL position = %q, want %q", got, want)
	}
}

func TestAStandbysStatusEndsWithWhatItHasReceivedAndReplayed(t *testing.T) {
	q := askingScript(statusAnswer(), serverAnswer("170006", true), standbyAnswer("0/3000148", "0/3000060"))
	v, err := statusView(context.Background(), q, reqFor(t, "pg.status", nil))
	if got, want := pairsOf(t, v, err)["WAL position"], "received 0/3000148, replayed 0/3000060"; got != want {
		t.Errorf("WAL position = %q, want %q", got, want)
	}
	// What a status call asks for is the position and the server's facts: the
	// receiver, the checkpoint, the standbys and the slots are the page's.
	for _, page := range []string{"pg_stat_wal_receiver", "pg_control_checkpoint", "pg_stat_replication", "pg_replication_slots"} {
		if q.askedFor(page) {
			t.Errorf("pg.status read %s for a line that needs only the position", page)
		}
	}
}

// The longest an LSN gets is FFFFFFFF/FFFFFFFF, and neither the line nor the
// two lines of the overview may cut one.
func TestALongPositionIsNeverCut(t *testing.T) {
	const longest = "FFFFFFFF/FFFFFFFF"
	q := askingScript(statusAnswer(), serverAnswer("170006", false), primaryAnswer(longest, "00000001FFFFFFFF000000FF"))
	v, err := statusView(context.Background(), q, reqFor(t, "pg.status", nil))
	if got := pairsOf(t, v, err)["WAL position"]; !strings.HasPrefix(got, longest+",") {
		t.Errorf("WAL position = %q, want %s whole", got, longest)
	}
	q = askingScript(statusAnswer(), serverAnswer("170006", true), standbyAnswer(longest, "FFFFFFFE/FFFFFF00"))
	v, err = statusView(context.Background(), q, reqFor(t, "pg.status", nil))
	if got, want := pairsOf(t, v, err)["WAL position"], "received "+longest+", replayed FFFFFFFE/FFFFFF00"; got != want {
		t.Errorf("WAL position = %q, want %q", got, want)
	}
}

// A tile that is one key/value view shortens, in the middle, a value that is a
// slash and no whitespace, because that is what a path looks like and its end
// is the part that names it — and a bare LSN is exactly that, so one that did
// not fit beside its key would lose its middle to an ellipsis in a box a third
// of the screen wide. The line is always words around the positions, which a
// tile wraps and does not cut.
func TestThePositionLineIsNeverABareLSN(t *testing.T) {
	for name, f := range map[string]replicationFacts{
		"a primary": {primary: primaryPosition{lsn: "16/B374D848", walFile: "0000000100000016000000B3", timeline: "1"}},
		"a standby": {server: serverFacts{recovering: true},
			standby: standbyPosition{receiveLSN: str("16/B374D848"), replayLSN: str("16/B3000060")}},
		"a standby that never streamed": {server: serverFacts{recovering: true},
			standby: standbyPosition{replayLSN: str("16/B3000060")}},
	} {
		if line := positionText(f); !strings.ContainsAny(line, " \t") {
			t.Errorf("%s: %q holds no whitespace, so a tile takes it for a path", name, line)
		}
	}
}

// A standby that never streamed has no receive position, which PostgreSQL says
// as NULL and not as zero. What it has replayed is still its progress, so that
// is the line; a standby with neither has none.
func TestAStandbyThatNeverStreamedSaysOnlyWhatItReplayed(t *testing.T) {
	q := askingScript(statusAnswer(), serverAnswer("170006", true), standbyAnswer(nil, "0/3000060"))
	v, err := statusView(context.Background(), q, reqFor(t, "pg.status", nil))
	if got, want := pairsOf(t, v, err)["WAL position"], "replayed 0/3000060"; got != want {
		t.Errorf("WAL position = %q, want %q", got, want)
	}
	q = askingScript(statusAnswer(), serverAnswer("170006", true), standbyAnswer(nil, nil))
	v, err = statusView(context.Background(), q, reqFor(t, "pg.status", nil))
	pairs := pairsOf(t, v, err)
	if _, there := pairs["WAL position"]; there {
		t.Errorf("a standby with no position was given a line: %q", pairs["WAL position"])
	}
	if len(pairs) != 4 {
		t.Errorf("pairs = %v, want the four lines status always had", pairs)
	}
}

// **The position never costs the status its answer.** Each way the line can
// be unavailable leaves the four lines that were always there, no error and no
// warning — and a server too old for pg.replication is not asked for a
// position at all.
func TestTheStatusKeepsAnsweringWhereThePositionCannotBeRead(t *testing.T) {
	refused := &pgconn.PgError{Code: "42501", Message: "permission denied for function pg_current_wal_lsn"}
	for _, tc := range []struct {
		name string
		q    asking
		// asks is what must not have been asked.
		neverAsked string
	}{
		{"a server older than 14", askingScript(statusAnswer(), serverAnswer("130023", false),
			primaryAnswer("0/3000148", "000000010000000000000003")), "pg_walfile_name"},
		{"a role the server refuses", asking{
			script: script{statusAnswer(), serverAnswer("170006", false),
				primaryAnswer("0/3000148", "000000010000000000000003")},
			asked: new([]string), refuse: "pg_walfile_name", err: refused,
		}, ""},
		{"a connection that fails after the status", askingScript(statusAnswer()), ""},
		{"a standby the server refuses", asking{
			script: script{statusAnswer(), serverAnswer("170006", true), standbyAnswer("0/3000148", "0/3000060")},
			asked:  new([]string), refuse: "pg_is_wal_replay_paused", err: refused,
		}, ""},
	} {
		v, err := statusView(context.Background(), tc.q, reqFor(t, "pg.status", nil))
		pairs := pairsOf(t, v, err)
		if _, there := pairs["WAL position"]; there || len(pairs) != 4 {
			t.Errorf("%s: pairs = %v, want the four lines and no position", tc.name, pairs)
		}
		if tc.neverAsked != "" && tc.q.askedFor(tc.neverAsked) {
			t.Errorf("%s: asked for a position it does not read", tc.name)
		}
	}
	// A status that fails at the first query is still a failure, with the
	// position never reached.
	q := askingScript()
	if _, err := statusView(context.Background(), q, reqFor(t, "pg.status", nil)); err == nil {
		t.Error("a status whose own query failed was answered")
	}
	if q.askedFor("pg_walfile_name") {
		t.Error("the position was read after the status had already failed")
	}
}

func TestTheGlanceCarriesThePositionBeforeTheReplicationLine(t *testing.T) {
	size := answer{match: "pg_size_pretty(pg_database_size(current_database()))", rows: [][]any{{"app", "8 MB"}}}
	q := askingScript(size, serverAnswer("170006", false), primaryAnswer("0/3000148", "000000010000000000000003"),
		answer{match: "s.sync_state"}, answer{match: "r.slot_type"})
	v, err := compactOverview(context.Background(), q, reqFor(t, "pg.overview", nil))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	pairs := map[string]string{}
	for _, p := range v.(view.KeyValue).Pairs {
		keys = append(keys, p.Key)
		pairs[p.Key] = p.Value
	}
	if got, want := pairs["WAL position"], "0/3000148, timeline 1, file 000000010000000000000003"; got != want {
		t.Errorf("WAL position = %q, want %q", got, want)
	}
	if strings.Join(keys, ",") != "database,WAL position,replication" {
		t.Errorf("lines = %v, want the position just before the replication line", keys)
	}
}

func TestTheGlanceOfAStandbyCarriesWhatItReceivedAndReplayed(t *testing.T) {
	size := answer{match: "pg_size_pretty(pg_database_size(current_database()))", rows: [][]any{{"app", "8 MB"}}}
	receiver := answer{match: "from pg_stat_wal_receiver", rows: [][]any{
		{"streaming", int32(1), "primary", int32(5432), "slot_a", "0/3000148", started, int64(0)},
	}}
	q := askingScript(size, serverAnswer("170006", true), standbyAnswer("0/3000148", "0/3000060"), receiver,
		answer{match: "s.sync_state"}, answer{match: "r.slot_type"})
	v, err := compactOverview(context.Background(), q, reqFor(t, "pg.overview", nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range v.(view.KeyValue).Pairs {
		if p.Key == "WAL position" {
			if want := "received 0/3000148, replayed 0/3000060"; p.Value != want {
				t.Errorf("WAL position = %q, want %q", p.Value, want)
			}
			return
		}
	}
	t.Error("no WAL position line on a standby's glance")
}

// The detailed page's replication section opens with the same position, with
// the timeline and the upstream beside it, so the status section above it does
// not say it again.
func TestTheDetailedOverviewSaysThePositionOnce(t *testing.T) {
	q := askingScript(statusAnswer(), serverAnswer("170006", false), primaryAnswer("0/3000148", "000000010000000000000003"),
		answer{match: "s.sync_state"}, answer{match: "r.slot_type"})
	v, err := detailedOverview(context.Background(), q, reqFor(t, "pg.overview", nil))
	if err != nil {
		t.Fatal(err)
	}
	var where []string
	for _, s := range v.(view.Sections).Items {
		if kv, ok := s.View.(view.KeyValue); ok {
			for _, p := range kv.Pairs {
				if p.Key == "WAL position" {
					where = append(where, s.Title)
				}
			}
		}
	}
	if len(where) != 1 || where[0] != "replication" {
		t.Errorf("the position is said in %v, want only the replication section", where)
	}
}

// Both pages and the line read the position through the one helper: a
// standby's replication facts hold the positions the status line is made of,
// and the receiver and timeline that only the page reads.
func TestTheReplicationPageReadsAStandbyThroughTheSharedPosition(t *testing.T) {
	receiver := answer{match: "from pg_stat_wal_receiver", rows: [][]any{
		{"streaming", int32(3), "primary", int32(5432), "slot_a", "0/3000148", started, int64(0)},
	}}
	q := askingScript(serverAnswer("170006", true), standbyAnswer("0/3000148", "0/3000060"), receiver,
		answer{match: "s.sync_state"}, answer{match: "r.slot_type"})
	f, err := readReplication(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if f.standby.receiveLSN == nil || *f.standby.receiveLSN != "0/3000148" ||
		f.standby.replayLSN == nil || *f.standby.replayLSN != "0/3000060" {
		t.Errorf("standby = %+v, want the positions the line reads", f.standby)
	}
	if f.standby.timeline != "3" || !f.receiver.present || f.receiver.status != "streaming" {
		t.Errorf("standby timeline %q, receiver %+v: the upstream half was not read", f.standby.timeline, f.receiver)
	}
	if got, want := positionText(f), "received 0/3000148, replayed 0/3000060"; got != want {
		t.Errorf("positionText = %q, want %q", got, want)
	}
}

func TestAServerTooOldForTheReplicationPageIsNotAskedForItsStandbysOrSlots(t *testing.T) {
	q := askingScript(serverAnswer("130023", false))
	f, err := readReplication(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if f.server.versionNum != 130023 || f.primary.lsn != "" {
		t.Errorf("facts = %+v, want the server's and no position", f)
	}
	if len(*q.asked) != 1 {
		t.Errorf("asked %d statements of a server it will not read, want the one for its version", len(*q.asked))
	}
}

// pg_current_wal_lsn is a function a server evaluates each time it is named,
// and on a server that is writing two evaluations are two positions: the
// second can be past a segment boundary the first has not reached, which
// prints the name of the next WAL file beside a position in this one.
func TestThePrimaryPositionIsReadOnceAndItsFileNamedFromThatRead(t *testing.T) {
	if n := strings.Count(primarySQL, "pg_current_wal_lsn"); n != 1 {
		t.Errorf("primarySQL reads pg_current_wal_lsn %d times, want once:\n%s", n, primarySQL)
	}
	if !strings.Contains(primarySQL, "pg_walfile_name(cur.lsn)") {
		t.Errorf("primarySQL does not name the file from the position it read:\n%s", primarySQL)
	}
}

// The line is quiet about a position it could not read because it is a bonus;
// the page exists for the position, and a page that came back without one
// would read as a server that has none.
func TestTheReplicationPageStillFailsWhereItsPositionCannotBeRead(t *testing.T) {
	q := askingScript(serverAnswer("170006", false))
	if _, err := readReplication(context.Background(), q); err == nil {
		t.Fatal("a position that could not be read was passed over by the page")
	}
}
