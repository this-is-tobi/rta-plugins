package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The compact form of pg.overview is what a dashboard tile shows (its first
// eight lines) and what an agent reads by default, so what these hold is the
// order and the number of its rows: the verdict first, the state of the
// service before what it is called, and no more rows than a tile has lines.

// glanceRows is the compact form's rows in the order they are drawn.
func glanceRows(t *testing.T, v view.View, err error) []view.Pair {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("view is %T, want key/value", v)
	}
	return kv.Pairs
}

func keysOf(rows []view.Pair) string {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.Key
	}
	return strings.Join(keys, ",")
}

func valueOf(rows []view.Pair, key string) string {
	for _, r := range rows {
		if r.Key == key {
			return r.Value
		}
	}
	return "<no such row>"
}

// A server that has been up for 14 days and 2 hours, as the clock the view
// reads says.
func fixedClock(t *testing.T) {
	t.Helper()
	was := clock
	clock = func() time.Time { return started.Add(14*24*time.Hour + 2*time.Hour + 5*time.Minute) }
	t.Cleanup(func() { clock = was })
}

func databaseAnswer(db, size string, databases int64) answer {
	return answer{match: "pg_size_pretty(pg_database_size(current_database()))", rows: [][]any{{db, size, databases}}}
}

type sessionRow struct {
	total, active, idleInTx, waiting, max int64
	seesAll                               bool
	oldest                                any
	oldestState                           any
}

func sessionsAnswer(s sessionRow) answer {
	return answer{match: "pg_stat_activity", rows: [][]any{
		{s.total, s.active, s.idleInTx, s.waiting, s.max, s.seesAll, s.oldest, s.oldestState},
	}}
}

type archiverRow struct {
	mode       string
	recovering bool
	archived   int64
	lastWAL    string
	lastAt     any
	failed     int64
	failedWAL  string
	failedAt   any
}

func archiverAnswer(a archiverRow) answer {
	return answer{match: "pg_stat_archiver", rows: [][]any{
		{a.mode, a.recovering, a.archived, a.lastWAL, a.lastAt, a.failed, a.failedWAL, a.failedAt},
	}}
}

func archivedAgo(d time.Duration) archiverRow {
	return archiverRow{mode: "on", archived: 40, lastWAL: "000000010000000000000003", lastAt: time.Now().Add(-d)}
}

func cacheAnswer(ratio float64, resetAt any) answer {
	return answer{match: "pg_statio_user_tables", rows: [][]any{{ratio, resetAt}}}
}

// A standby the way pg_stat_replication gives it to a role that may read it,
// as many bytes behind as asked and with the replay lag the server measured.
func streamingAnswer(behind int64, replayLag any) answer {
	return answer{match: "s.sync_state", rows: [][]any{{
		int32(101), "walreceiver", "10.0.0.2", "streaming", "async", int32(0),
		"0/3000148", "0/3000148", "0/3000148", "0/3000148", behind, 0.01, 0.01, replayLag, nil,
	}}}
}

var typical = sessionRow{total: 12, active: 3, idleInTx: 2, waiting: 1, max: 40, seesAll: true,
	oldest: int64(250), oldestState: "idle in transaction"}

// primaryScript is a primary that is fine, with each figure the glance reads.
func primaryScript(extra ...answer) asking {
	answers := append([]answer{
		databaseAnswer("app", "8 MB", 3),
		typical.answer(),
		archiverAnswer(archivedAgo(2 * time.Minute)),
		cacheAnswer(99.5, time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)),
		serverAnswer("170006", false), primaryAnswer("0/3000148", "000000010000000000000003"),
		streamingAnswer(16<<20, 1.2), {match: "r.slot_type"},
	}, extra...)
	return askingScript(answers...)
}

func (s sessionRow) answer() answer { return sessionsAnswer(s) }

func glance(t *testing.T, q asking) []view.Pair {
	t.Helper()
	fixedClock(t)
	v, err := compactOverview(context.Background(), q, reqFor(t, "pg.overview", nil))
	return glanceRows(t, v, err)
}

func TestTheCompactFormOfAHealthyPrimaryLeadsWithItsVerdictAndHasEightRows(t *testing.T) {
	rows := glance(t, primaryScript())
	if got, want := keysOf(rows), "status,sessions,oldest xact,replication,up since,database,archiver,cache"; got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	for key, want := range map[string]string{
		"status":      "ok",
		"sessions":    "12 of 40 (3 active, 2 idle in transaction, 1 waiting on a lock)",
		"oldest xact": "4m10s, idle in transaction",
		"replication": "primary, 1 standby streaming, furthest behind 16.0 MiB (lag 1s)",
		"up since":    "2026-10-02 23:58 UTC (14d 2h)",
		"database":    "app · 8 MB, 3 databases",
		"archiver":    "archived 000000010000000000000003, 2 minutes ago",
		"cache":       "99.5% since 2026-10-01 (healthy)",
	} {
		if got := valueOf(rows, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// A tile gives a row a line and the 80 columns of a terminal are what the
// longest of them is held to; the rows that carry the most words are the
// worst case, so they are the ones measured.
func TestNoRowOfTheCompactFormIsWiderThanAnEightyColumnLine(t *testing.T) {
	failing := archiverRow{mode: "on", archived: 40, lastWAL: "000000010000000000000003",
		lastAt: time.Now().Add(-48 * time.Hour), failed: 120, failedWAL: "000000010000000000000004", failedAt: time.Now().Add(-3 * time.Minute)}
	q := primaryScript()
	q.script = append(script{archiverAnswer(failing), cacheAnswer(81.2, time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC))}, q.script...)
	rows := glance(t, q)
	if len(rows) > 8 {
		t.Errorf("%d rows, want at most eight: %s", len(rows), keysOf(rows))
	}
	for _, r := range rows {
		if line := r.Key + "  " + r.Value; len([]rune(line)) > 80 {
			t.Errorf("%s is %d columns: %q", r.Key, len([]rune(line)), line)
		}
	}
}

// The glance made eight statements before it was reworked, and the rework is
// not a reason to ask a server for more on every refresh of every tile.
func TestTheCompactFormAsksNoMoreThanItDidBefore(t *testing.T) {
	q := primaryScript()
	glance(t, q)
	if n := len(*q.asked); n > 8 {
		t.Errorf("asked %d statements, want at most the 8 it asked before:\n%s", n, strings.Join(*q.asked, "\n--\n"))
	}
	if q.askedFor("select pg_is_in_recovery()") {
		t.Error("the role is asked for on its own, when the server facts already say it")
	}
}

func TestTheCompactFormOfAStandbyHasNoArchiverRowAndNoLagInSeconds(t *testing.T) {
	receiver := answer{match: "from pg_stat_wal_receiver", rows: [][]any{
		{"streaming", int32(1), "primary", int32(5432), "slot_a", "0/3000148", started, int64(4 << 20)},
	}}
	q := askingScript(databaseAnswer("app", "8 MB", 1), typical.answer(),
		archiverAnswer(archiverRow{mode: "on", recovering: true}), cacheAnswer(-1, nil),
		serverAnswer("170006", true), standbyAnswer("0/3000148", "0/3000060"), receiver,
		answer{match: "s.sync_state"}, answer{match: "r.slot_type"})
	rows := glance(t, q)
	if got, want := keysOf(rows), "status,sessions,oldest xact,replication,up since,database,cache"; got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	if got, want := valueOf(rows, "replication"), "standby, streaming from primary, 4.0 MiB behind its upstream"; got != want {
		t.Errorf("replication = %q, want %q", got, want)
	}
	if got, want := valueOf(rows, "cache"), "no read activity recorded yet"; got != want {
		t.Errorf("cache = %q, want %q", got, want)
	}
	if got := valueOf(rows, "status"); got != "ok" {
		t.Errorf("status = %q, want ok", got)
	}
}

func TestAnEmptyServerIsOkAndSaysSoInEveryRow(t *testing.T) {
	quiet := sessionRow{max: 100, seesAll: true}
	q := askingScript(databaseAnswer("postgres", "7 MB", 1), quiet.answer(),
		archiverAnswer(archiverRow{mode: "on"}), cacheAnswer(-1, nil),
		serverAnswer("170006", false), primaryAnswer("0/1000060", "000000010000000000000001"),
		answer{match: "s.sync_state"}, answer{match: "r.slot_type"})
	rows := glance(t, q)
	for key, want := range map[string]string{
		"status":      "ok",
		"sessions":    "0 of 100 (0 active)",
		"oldest xact": "none",
		"replication": "primary, no standby is connected and there are no replication slots",
		"database":    "postgres · 7 MB, 1 database",
		"archiver":    "nothing archived yet",
	} {
		if got := valueOf(rows, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestTheSessionsRowDropsTheCountsThatAreZeroAndNeverTheActiveOne(t *testing.T) {
	for _, tc := range []struct {
		s    sessionRow
		want string
	}{
		{sessionRow{total: 5, active: 2, max: 100, seesAll: true}, "5 of 100 (2 active)"},
		{sessionRow{total: 5, active: 0, idleInTx: 3, max: 100, seesAll: true}, "5 of 100 (0 active, 3 idle in transaction)"},
		{sessionRow{total: 5, active: 1, waiting: 4, max: 100, seesAll: true}, "5 of 100 (1 active, 4 waiting on a lock)"},
	} {
		q := primaryScript()
		q.script = append(script{tc.s.answer()}, q.script...)
		if got := valueOf(glance(t, q), "sessions"); got != tc.want {
			t.Errorf("sessions = %q, want %q", got, tc.want)
		}
	}
}

func TestTheOldestTransactionIsItsAgeAndWhatItsSessionIsDoing(t *testing.T) {
	for _, tc := range []struct {
		oldest, state any
		want          string
	}{
		{int64(4), "active", "4s, active"},
		{int64(250), "idle in transaction", "4m10s, idle in transaction"},
		{int64(7260), "idle in transaction (aborted)", "2h1m, idle in transaction (aborted)"},
		{nil, nil, "none"},
	} {
		s := typical
		s.oldest, s.oldestState = tc.oldest, tc.state
		q := primaryScript()
		q.script = append(script{s.answer()}, q.script...)
		if got := valueOf(glance(t, q), "oldest xact"); got != tc.want {
			t.Errorf("oldest xact of %v = %q, want %q", tc.oldest, got, tc.want)
		}
	}
}

func TestTheStatusNamesTheWorstFactAndAFailBeatsAWarn(t *testing.T) {
	failing := archiverRow{mode: "on", archived: 40, lastWAL: "000000010000000000000003",
		lastAt: time.Now().Add(-time.Hour), failed: 7, failedWAL: "000000010000000000000004", failedAt: time.Now().Add(-time.Minute)}
	crowded := func(total int64) sessionRow {
		s := typical
		s.total = total
		return s
	}
	for _, tc := range []struct {
		name  string
		front []answer
		want  string
	}{
		{"nine in ten connections taken", []answer{crowded(37).answer()}, "warn — 92% of max_connections"},
		{"the last connections taken", []answer{crowded(39).answer()}, "fail — 97% of max_connections"},
		{"85 percent is where it starts", []answer{crowded(34).answer()}, "warn — 85% of max_connections"},
		{"under that it is fine", []answer{crowded(33).answer()}, "ok"},
		{"archiving failing", []answer{archiverAnswer(failing)}, "fail — WAL archiving is failing"},
		{"a standby far behind", []answer{streamingAnswer(100<<20, 1.0)}, "warn — standby walreceiver needs attention"},
		{"a fail and a warn", []answer{archiverAnswer(failing), crowded(37).answer()}, "fail — WAL archiving is failing"},
	} {
		q := primaryScript()
		q.script = append(script(tc.front), q.script...)
		if got := valueOf(glance(t, q), "status"); got != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTheReplicationRowGivesTheLagInSecondsWhenTheStandbyReportsIt(t *testing.T) {
	for _, tc := range []struct {
		lag  any
		want string
	}{
		{1.2, "primary, 1 standby streaming, furthest behind 16.0 MiB (lag 1s)"},
		{0.004, "primary, 1 standby streaming, furthest behind 16.0 MiB (lag 4 ms)"},
		{nil, "primary, 1 standby streaming, furthest behind 16.0 MiB"},
	} {
		q := primaryScript()
		q.script = append(script{streamingAnswer(16<<20, tc.lag)}, q.script...)
		if got := valueOf(glance(t, q), "replication"); got != tc.want {
			t.Errorf("replication = %q, want %q", got, tc.want)
		}
	}
}

func TestTheArchiverRowSaysWhatTheLastAttemptWas(t *testing.T) {
	ago := func(d time.Duration) time.Time { return time.Now().Add(-d) }
	for _, tc := range []struct {
		name string
		a    archiverRow
		want string
	}{
		{"archiving", archivedAgo(2 * time.Minute), "archived 000000010000000000000003, 2 minutes ago"},
		{"it failed once and then worked", archiverRow{mode: "on", archived: 9, lastWAL: "000000010000000000000009", lastAt: ago(time.Minute),
			failed: 1, failedWAL: "000000010000000000000008", failedAt: ago(time.Hour)}, "archived 000000010000000000000009, 1 minute ago"},
		{"failing", archiverRow{mode: "on", archived: 9, lastWAL: "000000010000000000000009", lastAt: ago(time.Hour),
			failed: 7, failedWAL: "00000001000000000000000A", failedAt: ago(2 * time.Minute)},
			"failing — 00000001000000000000000A failed 2 minutes ago, 7 failures"},
		{"failing from the start", archiverRow{mode: "on", failed: 1, failedWAL: "000000010000000000000001", failedAt: ago(time.Minute)},
			"failing — 000000010000000000000001 failed 1 minute ago, 1 failure"},
		{"nothing yet", archiverRow{mode: "on"}, "nothing archived yet"},
		{"switched off", archiverRow{mode: "off"}, "off — archive_mode is off"},
	} {
		q := primaryScript()
		q.script = append(script{archiverAnswer(tc.a)}, q.script...)
		if got := valueOf(glance(t, q), "archiver"); got != tc.want {
			t.Errorf("%s: archiver = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A standby archives only where archive_mode is always; where it is on, the
// archiver is the primary's business and a row about it would read as an
// archive that is not being written.
func TestAStandbyIsAskedAboutItsArchiverOnlyWhereItArchives(t *testing.T) {
	for mode, want := range map[string]string{"on": "", "always": "nothing archived yet"} {
		receiver := answer{match: "from pg_stat_wal_receiver", rows: [][]any{
			{"streaming", int32(1), "primary", int32(5432), "slot_a", "0/3000148", started, int64(0)},
		}}
		q := askingScript(databaseAnswer("app", "8 MB", 1), typical.answer(),
			archiverAnswer(archiverRow{mode: mode, recovering: true}), cacheAnswer(99, nil),
			serverAnswer("170006", true), standbyAnswer("0/3000148", "0/3000060"), receiver,
			answer{match: "s.sync_state"}, answer{match: "r.slot_type"})
		rows := glance(t, q)
		got := valueOf(rows, "archiver")
		if want == "" {
			want = "<no such row>"
		}
		if got != want {
			t.Errorf("archive_mode %s on a standby: archiver = %q, want %q", mode, got, want)
		}
	}
}

func TestTheCacheRatioSaysSinceWhenItWasLastReset(t *testing.T) {
	for _, tc := range []struct {
		ratio float64
		reset any
		want  string
	}{
		{99.5, time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC), "99.5% since 2026-10-01 (healthy)"},
		{99.5, nil, "99.5% since the counters began (healthy)"},
		{81.2, time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC), "81.2% since 2026-10-01 (low — cold cache or small shared_buffers)"},
		{-1, time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC), "no read activity recorded yet"},
	} {
		q := primaryScript()
		q.script = append(script{cacheAnswer(tc.ratio, tc.reset)}, q.script...)
		if got := valueOf(glance(t, q), "cache"); got != tc.want {
			t.Errorf("cache of %v = %q, want %q", tc.ratio, got, tc.want)
		}
	}
}

// A row that cannot be read is a row that says so, with the reason, and a
// status that names it: a glance that quietly left it out would read as a
// service with nothing to say about it.
func TestARowTheServerRefusesSaysSoAndTheOthersAreStillThere(t *testing.T) {
	denied := &pgconn.PgError{Code: "42501", Message: "permission denied for function pg_stat_get_archiver"}
	q := primaryScript()
	q.refuse, q.err = "pg_stat_archiver", denied
	rows := glance(t, q)
	if got, want := keysOf(rows), "status,sessions,oldest xact,replication,up since,database,archiver,cache"; got != want {
		t.Fatalf("rows = %s, want all of them with the refused one saying so: %s", got, want)
	}
	if got := valueOf(rows, "archiver"); !strings.HasPrefix(got, "could not be read — ") {
		t.Errorf("archiver = %q, want it to say it could not be read", got)
	}
	if got, want := valueOf(rows, "status"), "warn — archiver could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := valueOf(rows, "sessions"); !strings.HasPrefix(got, "12 of 40") {
		t.Errorf("sessions = %q, a refused archiver took the other rows with it", got)
	}
}

func TestSeveralRowsThatCannotBeReadAreAllNamedInTheStatus(t *testing.T) {
	boom := errors.New("connection reset")
	q := primaryScript()
	q.refuse, q.err = "pg_stat", boom
	rows := glance(t, q)
	if got := valueOf(rows, "status"); got != "warn — sessions, archiver, cache could not be read" {
		t.Errorf("status = %q", got)
	}
	for _, key := range []string{"sessions", "oldest xact", "archiver", "cache"} {
		if got := valueOf(rows, key); !strings.HasPrefix(got, "could not be read — ") {
			t.Errorf("%s = %q, want it to say it could not be read", key, got)
		}
	}
	if got := valueOf(rows, "replication"); strings.HasPrefix(got, "could not be read") {
		t.Errorf("replication = %q, the server answered that", got)
	}
}

func TestAServerThatAnswersNothingIsAnErrorAndNotAPageOfRefusals(t *testing.T) {
	_, err := compactOverview(context.Background(), askingScript(), reqFor(t, "pg.overview", nil))
	var verr *view.Error
	if !errors.As(err, &verr) || verr.Code != "pg.overview.unavailable" {
		t.Errorf("err = %v, want pg.overview.unavailable", err)
	}
}

// A role without pg_monitor is shown a standby's row with nothing in it. An ok
// over that would be a verdict about a thing nobody could see, so the status
// says it could not be read, and the replication row keeps counting it.
func TestAStandbyTheRoleMayNotSeeIsNamedInTheStatusAndNeverPaintedOk(t *testing.T) {
	hidden := answer{match: "s.sync_state", rows: [][]any{
		{int32(101), "walreceiver", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil},
	}}
	q := primaryScript()
	q.script = append(script{hidden}, q.script...)
	rows := glance(t, q)
	if got, want := valueOf(rows, "status"), "warn — standby state could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := valueOf(rows, "replication"), "primary, 1 hidden from this role"; got != want {
		t.Errorf("replication = %q, want %q", got, want)
	}
}

func TestAnUpstreamTheRoleMayNotSeeIsNamedInTheStatus(t *testing.T) {
	receiver := answer{match: "from pg_stat_wal_receiver", rows: [][]any{
		{nil, nil, nil, nil, nil, nil, nil, nil},
	}}
	q := askingScript(databaseAnswer("app", "8 MB", 1), typical.answer(),
		archiverAnswer(archiverRow{mode: "on", recovering: true}), cacheAnswer(99, nil),
		serverAnswer("170006", true), standbyAnswer("0/3000148", "0/3000060"), receiver,
		answer{match: "pg_control_checkpoint", rows: [][]any{{int32(1)}}},
		answer{match: "s.sync_state"}, answer{match: "r.slot_type"})
	rows := glance(t, q)
	if got, want := valueOf(rows, "status"), "warn — upstream could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := valueOf(rows, "replication"), "standby, upstream hidden from this role"; got != want {
		t.Errorf("replication = %q, want %q", got, want)
	}
}

func TestAServerTooOldForReplicationSaysSoInsteadOfLeavingTheRowOut(t *testing.T) {
	q := askingScript(databaseAnswer("app", "8 MB", 1), typical.answer(),
		archiverAnswer(archivedAgo(time.Minute)), cacheAnswer(99, nil), serverAnswer("130023", false))
	rows := glance(t, q)
	if got := valueOf(rows, "replication"); !strings.HasPrefix(got, "could not be read — ") || !strings.Contains(got, "14") {
		t.Errorf("replication = %q, want it to say replication is read from 14 on", got)
	}
	if got := valueOf(rows, "up since"); got == "<no such row>" {
		t.Error("an old server still knows when it started")
	}
}

func TestUptimeIsDaysAndHoursThenHoursAndMinutesThenMinutes(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{14*24*time.Hour + 2*time.Hour + 59*time.Minute, "14d 2h"},
		{24 * time.Hour, "1d 0h"},
		{25 * time.Hour, "1d 1h"},
		{23*time.Hour + 59*time.Minute, "23h 59m"},
		{5*time.Hour + 12*time.Minute, "5h 12m"},
		{time.Hour, "1h 0m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{90 * time.Second, "1m"},
		{42 * time.Minute, "42m"},
		{30 * time.Second, "<1m"},
		{0, "<1m"},
		{-time.Minute, "<1m"},
	} {
		if got := uptime(tc.d); got != tc.want {
			t.Errorf("uptime(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// Everything the glance used to say is still said by the page that
// `detail` asks for, together with the figures the glance now leads with.
func TestTheDetailedOverviewKeepsThePositionAndAddsTheSessionsAndTheArchiver(t *testing.T) {
	fixedClock(t)
	q := primaryScript()
	q.script = append(script{
		statusAnswer(),
		answer{match: "pg_stat_user_tables", cols: []string{"schemaname", "relname", "n_live_tup", "size"}},
		answer{match: "extract(epoch from now() - query_start)", cols: []string{"pid", "usename", "application_name", "state", "seconds", "waiting"}},
	}, q.script...)
	v, err := detailedOverview(context.Background(), q, reqFor(t, "pg.overview", nil))
	if err != nil {
		t.Fatal(err)
	}
	pairs := func(title string) map[string]string {
		out := map[string]string{}
		for _, p := range section(t, v, title).(view.KeyValue).Pairs {
			out[p.Key] = p.Value
		}
		return out
	}
	if got := pairs("status")["databases"]; got != "3" {
		t.Errorf("databases = %q in the status section, the glance's count has nowhere to be read", got)
	}
	if got := pairs("replication")["WAL position"]; got != "0/3000148" {
		t.Errorf("WAL position = %q in the replication section", got)
	}
	if got := pairs("replication")["up since"]; got != "2026-10-02 23:58 UTC (14d 2h)" {
		t.Errorf("up since = %q, want the uptime spelled as the glance spells it", got)
	}
	if got := pairs("sessions")["sessions"]; got != "12 of 40 (3 active, 2 idle in transaction, 1 waiting on a lock)" {
		t.Errorf("sessions section says %q", got)
	}
	if got := pairs("archiver")["archiver"]; !strings.HasPrefix(got, "archived 000000010000000000000003") {
		t.Errorf("archiver section says %q", got)
	}
	if got := pairs("cache")["cache hit ratio"]; got != "99.5% since 2026-10-01 (healthy)" {
		t.Errorf("cache section says %q", got)
	}
}
