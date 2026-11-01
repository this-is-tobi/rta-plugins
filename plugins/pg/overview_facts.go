package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What the overview's glance reads beyond the replication facts, each as a
// small type of its own so that a read that fails costs its rows and not the
// page, and so that the compact form and the detailed page say the same thing
// from the same reading.

// clock is the moment an uptime is measured against, a variable so that a test
// can say what time it is.
var clock = time.Now

// upSince is when a server started and how long ago that was, the pair a
// reader wants from "what changed": a restart or a failover is the first
// answer, and it is the date that matches it to a log.
func upSince(started time.Time) string {
	return started.UTC().Format("2006-01-02 15:04 UTC") + " (" + format.Uptime(clock().Sub(started)) + ")"
}

// connectionsWarn and connectionsFail are the shares of max_connections taken
// at which the glance warns and fails. Rules of thumb, like the replication
// bands: past the first a deploy or a retry storm exhausts the rest, and past
// the second what is left is what superuser_reserved_connections holds back on
// a default server, so an ordinary role is about to be refused.
const (
	connectionsWarn = 85
	connectionsFail = 95
)

// databaseFacts are the connected database, its size and how many databases
// the server holds. The count is of the ones a person connects to, the way
// pg.database.list counts them: templates are not databases anyone has.
type databaseFacts struct {
	name, size string
	databases  int64
}

const databaseSQL = `
	select current_database(), pg_size_pretty(pg_database_size(current_database())),
	       (select count(*) from pg_database where not datistemplate)`

func readDatabase(ctx context.Context, conn querier) (databaseFacts, error) {
	var d databaseFacts
	err := conn.QueryRow(ctx, databaseSQL).Scan(&d.name, &d.size, &d.databases)
	return d, err
}

func (d databaseFacts) count() string {
	return format.Count(int(d.databases), "database", "databases")
}

func (d databaseFacts) text() string {
	return plugin.ListedName(d.name) + " · " + d.size + ", " + d.count()
}

// sessionFacts are the other sessions of the server, counted as
// max_connections counts them: every client of every database, and not this
// one, which is the session counting. The oldest transaction is the one that
// holds back vacuum and the locks it took, whatever its session is doing now.
//
// **A number a role cannot make is not given.** pg_stat_activity shows a role
// every session's row, and fills in database, type, state, wait event and
// transaction start only for its own unless it holds pg_read_all_stats: the
// sessions of others are rows with nothing in them, a count of the client ones
// among them is a count of this role's own, and it reads as a quiet server when
// it is a blind one. Such a role is told what it cannot see, and not given a
// figure to read as the server's connections.
//
// Asked as USAGE, the privileges the role has, which is how pg_stat_activity
// decides what to show; MEMBER is true of a role that belongs to
// pg_read_all_stats without inheriting its rights, and is shown the same
// blanks. A superuser has every privilege, so the one question covers it.
type sessionFacts struct {
	total, active, idleInTx, waiting, max int64
	seesAll                               bool
	oldest                                *int64
	oldestState                           *string
}

const sessionsSQL = `
	with s as (
		select state, wait_event_type, xact_start
		from pg_stat_activity
		where backend_type = 'client backend' and pid <> pg_backend_pid()
	)
	select count(*),
	       count(*) filter (where state = 'active'),
	       count(*) filter (where state like 'idle in transaction%'),
	       count(*) filter (where wait_event_type = 'Lock'),
	       current_setting('max_connections')::bigint,
	       pg_has_role(current_user, 'pg_read_all_stats', 'USAGE'),
	       (select extract(epoch from now() - xact_start)::bigint from s
	         where xact_start is not null order by xact_start limit 1),
	       (select state from s where xact_start is not null order by xact_start limit 1)
	from s`

func readSessions(ctx context.Context, conn querier) (sessionFacts, error) {
	var s sessionFacts
	err := conn.QueryRow(ctx, sessionsSQL).
		Scan(&s.total, &s.active, &s.idleInTx, &s.waiting, &s.max, &s.seesAll, &s.oldest, &s.oldestState)
	return s, err
}

// share is how much of max_connections is taken, floored so that a server at
// 94.9% never reads as the 95% that fails.
func (s sessionFacts) share() int64 {
	if s.max <= 0 {
		return 0
	}
	return s.total * 100 / s.max
}

const noStatsRole = "role sees only its own (no pg_read_all_stats)"

func (s sessionFacts) text() string {
	if !s.seesAll {
		return view.CouldNotRead(noStatsRole)
	}
	line := fmt.Sprintf("%d of %d", s.total, s.max)
	parts := []string{fmt.Sprintf("%d active", s.active)}
	if s.idleInTx > 0 {
		parts = append(parts, fmt.Sprintf("%d idle in transaction", s.idleInTx))
	}
	if s.waiting > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting on a lock", s.waiting))
	}
	return line + " (" + strings.Join(parts, ", ") + ")"
}

func (s sessionFacts) oldestText() string {
	if !s.seesAll {
		return view.CouldNotRead(noStatsRole)
	}
	if s.oldest == nil {
		return "none"
	}
	line := format.Duration(time.Duration(*s.oldest) * time.Second)
	if s.oldestState != nil && *s.oldestState != "" {
		line += ", " + *s.oldestState
	}
	return line
}

// archiverFacts are what pg_stat_archiver says, which any role may read: the
// cheapest answer to whether the backup chain is alive. The counters are since
// the statistics were last reset, which is why the glance leans on the order of
// the last success and the last failure and not on a count.
type archiverFacts struct {
	mode       string
	archived   int64
	lastWAL    string
	lastAt     *time.Time
	failed     int64
	failedWAL  string
	failedAt   *time.Time
	recovering bool
}

const archiverSQL = `
	select current_setting('archive_mode'), pg_is_in_recovery(), archived_count,
	       coalesce(last_archived_wal, ''), last_archived_time,
	       failed_count, coalesce(last_failed_wal, ''), last_failed_time
	from pg_stat_archiver`

func readArchiver(ctx context.Context, conn querier) (archiverFacts, error) {
	var a archiverFacts
	err := conn.QueryRow(ctx, archiverSQL).
		Scan(&a.mode, &a.recovering, &a.archived, &a.lastWAL, &a.lastAt, &a.failed, &a.failedWAL, &a.failedAt)
	return a, err
}

// failing is whether the last thing the archiver did was fail: a failure that a
// later success followed is history, and one that nothing has followed is the
// archive command still not working, however long ago it was.
func (a archiverFacts) failing() bool {
	return a.failedAt != nil && (a.lastAt == nil || a.failedAt.After(*a.lastAt))
}

// shown is whether the glance has a row for the archiver: a standby archives
// only where archive_mode is always, and where it is on or off the archiver is
// the primary's business, so a row about it would read as an archive nobody is
// writing. The detail says so in words.
func (a archiverFacts) shown() bool { return !a.recovering || a.mode == "always" }

func (a archiverFacts) archives() bool { return a.mode != "off" && a.shown() }

func (a archiverFacts) text() string {
	switch {
	case a.mode == "off":
		return "off — archive_mode is off"
	case !a.shown():
		return "not archiving — a standby archives only with archive_mode always"
	case a.failing():
		return "failing — " + a.failedWAL + " failed " + format.Ago(*a.failedAt) + ", " +
			format.Count(int(a.failed), "failure", "failures")
	case a.lastAt != nil:
		return "archived " + a.lastWAL + ", " + format.Ago(*a.lastAt)
	}
	return "nothing archived yet"
}

// archiverPairs is the archiver on the detailed page: the row of the glance,
// and the counters behind it, which are since the statistics were last reset.
func archiverPairs(a archiverFacts) view.KeyValue {
	pairs := []view.Pair{{Key: "archiver", Value: a.text()}, {Key: "archive_mode", Value: a.mode}}
	if a.archives() {
		pairs = append(pairs, view.Pair{Key: "archived", Value: format.Count(int(a.archived), "WAL file", "WAL files")})
		if a.failed > 0 {
			pairs = append(pairs, view.Pair{Key: "failed", Value: format.Count(int(a.failed), "attempt", "attempts") +
				", the last of " + a.failedWAL + " " + format.Ago(*a.failedAt)})
		}
	}
	return view.KeyValue{Pairs: pairs}
}

// connectionsFinding and the other findings below are what the status row is
// made from: a grade, and the few words that name the fact.
type finding struct{ grade, text string }

func connectionsFinding(s sessionFacts) (finding, bool) {
	share := s.share()
	switch {
	case share >= connectionsFail:
		return finding{gradeFail, fmt.Sprintf("%d%% of max_connections", share)}, true
	case share >= connectionsWarn:
		return finding{gradeWarn, fmt.Sprintf("%d%% of max_connections", share)}, true
	}
	return finding{}, false
}

// attentionFinding is the worst thing replication's attention list holds,
// named by what it is about; the list itself is on the page and the count is in
// the replication row.
func attentionFinding(f replicationFacts) (finding, bool) {
	rows := attentionTable(f).Rows
	if len(rows) == 0 {
		return finding{}, false
	}
	return finding{rows[0][1], rows[0][0] + " needs attention"}, true
}

// hiddenFrom is what a role without pg_monitor is not shown of the replication
// it summarises. The summary already counts such a standby as hidden, and the
// status says it too: a row that is ok because nothing could be seen of it is
// the quiet half of a failure, which the attention list refuses to paint green
// for the same reason.
func hiddenFrom(f replicationFacts) []string {
	var what []string
	for _, s := range f.standbys {
		if s.hidden() {
			what = append(what, "standby state")
			break
		}
	}
	if f.receiver.hidden {
		what = append(what, "upstream")
	}
	return what
}

// statusText is the first row of the glance: ok, or the grade of the worst
// fact and the words for it. A fail outranks a warn whichever was noted first,
// and among equals the first noted stands, so the same server reads the same
// way twice. Rows that could not be read are a warning of their own, last,
// since a service that is failing in a way that was read says more than what
// could not be.
func statusText(findings []finding, unreadable []string) string {
	if len(unreadable) > 0 {
		findings = append(findings, finding{gradeWarn, strings.Join(unreadable, ", ") + " could not be read"})
	}
	var worst finding
	for _, f := range findings {
		if severity(f.grade) > severity(worst.grade) || worst.grade == "" {
			worst = f
		}
	}
	if worst.grade == "" {
		return "ok"
	}
	return worst.grade + " — " + worst.text
}

// whyUnread is the reason a row could not be read, in the words the
// capabilities beside it would have refused with.
func whyUnread(err error, req plugin.Request) string {
	return view.CouldNotRead(classify(err, req).Message)
}

func sessionPairs(s sessionFacts) []view.Pair {
	return []view.Pair{
		{Key: "sessions", Value: s.text()},
		{Key: "oldest xact", Value: s.oldestText()},
	}
}
