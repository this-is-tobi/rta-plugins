package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// pg.overview composes what the other four capabilities already know how to
// answer into one screen, through the one connection its own Run already
// opened — the reason its sections are called directly rather than through
// plugin.Page.AddAs, which invokes a full Handler and would open a separate
// connection per section for what is supposed to be a single glance.
//
// It stays NoPreview like every capability here (see cap's comment): the
// automatic dashboard must not decide, on its own, that a database is worth
// polling every five seconds. An operator who has looked at their own setup
// and decided otherwise can still say so explicitly — `dashboard.tiles`
// accepts it regardless, because naming a capability in a config file is
// the asking.

// overviewTableLimit and overviewActivityLimit are the counts pg.overview
// asks for from pg.table.list and pg.activity — narrower than each
// capability's own default, because five sections share one screen and this
// one is a glance, not the report `rta pg table list` gives on its own.
const (
	overviewTableLimit    = 5
	overviewActivityLimit = 10
)

// cacheHitFloor is the textbook rule of thumb for pg_statio's cache hit
// ratio: below it, more of shared_buffers usually pays for itself. Not a
// verdict on any particular workload — a cold cache after a restart reads
// identically to one that is genuinely starved, which is why the line names
// what to check rather than declaring the number wrong.
const cacheHitFloor = 90.0

// compactOverview is the form a dashboard tile and an agent read by default,
// and a tile shows the first eight lines of it, so what it says is ordered for
// someone with five seconds: the verdict, then how the service is doing
// (sessions, the oldest transaction, replication), then when it last started,
// and what it is called only after that. The WAL position, which only grows
// and wraps on a tile, and the pointer to the replication page are in the
// detail.
//
// Each figure is read on its own, in the same "add what answered" style as
// builtin/sys's runOverview: a read that fails costs the reader that one row,
// said as a row that could not be read and named in the verdict, and never the
// page. Only when nothing at all could be read is it an error.
//
// **It asks the server no more statements than it did**, eight on a primary and
// nine on a standby: the role and the uptime come from the replication
// reading's own first statement, which spares the one that asked for the role
// alone; the database count and the cache's reset date ride on the statements
// of their neighbours; and the archiver is the one statement that is new, in the
// place the role's was.
func compactOverview(ctx context.Context, conn querier, req plugin.Request) (view.View, error) {
	var (
		findings   []finding
		unreadable []string
		read       int
	)
	unread := func(name string, err error, keys ...string) []view.Pair {
		unreadable = append(unreadable, name)
		why := whyUnread(err, req)
		rows := make([]view.Pair, len(keys))
		for i, key := range keys {
			rows[i] = view.Pair{Key: key, Value: why}
		}
		return rows
	}
	note := func(f finding, ok bool) {
		if ok {
			findings = append(findings, f)
		}
	}

	d, dbErr := readDatabase(ctx, conn)
	f, replErr := readReplication(ctx, conn)
	s, sessErr := readSessions(ctx, conn)
	a, archErr := readArchiver(ctx, conn)
	cache, cacheErr := cacheView(ctx, conn, req)

	// The service's own state first: what is wrong or changed.
	var rows []view.Pair
	switch {
	case sessErr != nil:
		rows = append(rows, unread("sessions", sessErr, "sessions", "oldest xact")...)
	default:
		read++
		if s.seesAll {
			note(connectionsFinding(s))
		} else {
			unreadable = append(unreadable, "sessions")
		}
		rows = append(rows, sessionPairs(s)...)
	}

	serverRead := f.server.versionNum != 0
	switch {
	case !serverRead:
		rows = append(rows, unread("replication", replErr, "replication", "up since")...)
	case f.server.versionNum < minReplicationVersion:
		read++
		unreadable = append(unreadable, "replication")
		rows = append(rows,
			view.Pair{Key: "replication", Value: view.CouldNotRead("replication is read from PostgreSQL 14 and newer")},
			view.Pair{Key: "up since", Value: upSince(f.server.started)})
	case replErr != nil:
		read++
		rows = append(rows, unread("replication", replErr, "replication")...)
		rows = append(rows, view.Pair{Key: "up since", Value: upSince(f.server.started)})
	default:
		read++
		note(attentionFinding(f))
		unreadable = append(unreadable, hiddenFrom(f)...)
		rows = append(rows,
			view.Pair{Key: "replication", Value: roleWord(f) + ", " + replicationLine(f)},
			view.Pair{Key: "up since", Value: upSince(f.server.started)})
	}

	// Then what it is called.
	if dbErr != nil {
		rows = append(rows, unread("database", dbErr, "database")...)
	} else {
		read++
		rows = append(rows, view.Pair{Key: "database", Value: d.text()})
	}

	switch {
	case archErr != nil:
		rows = append(rows, unread("archiver", archErr, "archiver")...)
	default:
		read++
		if a.archives() && a.failing() {
			findings = append(findings, finding{gradeFail, "WAL archiving is failing"})
		}
		if a.shown() {
			rows = append(rows, view.Pair{Key: "archiver", Value: a.text()})
		}
	}

	if cacheErr != nil {
		rows = append(rows, unread("cache", cacheErr, "cache")...)
	} else if kv, ok := cache.(view.KeyValue); ok && len(kv.Pairs) > 0 {
		read++
		rows = append(rows, view.Pair{Key: "cache", Value: kv.Pairs[0].Value})
	}

	if read == 0 {
		return nil, view.Errorf("pg.overview.unavailable", "no figure could be read")
	}
	status := view.Pair{Key: "status", Value: statusText(findings, unreadable)}
	return view.KeyValue{Pairs: append([]view.Pair{status}, rows...)}, nil
}

func roleWord(f replicationFacts) string {
	if f.server.recovering {
		return "standby"
	}
	return "primary"
}

// detailedOverview is the full page: status, replication, the sessions and
// the archiver, cache, the largest tables and current activity, each dropped
// independently on failure rather than sinking the whole page — the same
// contract plugin.Page documents, used here through Put/Warn since every
// section is already in hand rather than behind a Handler to invoke. It carries
// everything the compact form does, and the positions and tables it leaves out.
func detailedOverview(ctx context.Context, conn querier, req plugin.Request) (view.View, error) {
	p := plugin.NewPage(ctx, req)
	put := func(title string, v view.View, err error) {
		if err != nil {
			p.Warn(view.AsError(err, "page.section.failed"))
			return
		}
		p.Put(title, v)
	}

	status, err := serverStatus(ctx, conn, req)
	if err == nil {
		// A bonus beside the size, as the position is on pg.status: the glance says how
		// many databases the server holds, and the page that carries everything it
		// does must not be the one place that does not.
		if d, derr := readDatabase(ctx, conn); derr == nil {
			status.Pairs = append(status.Pairs, view.Pair{Key: "databases", Value: strconv.FormatInt(d.databases, 10)})
		}
	}
	put("status", status, err)

	if err := addReplication(ctx, p, conn, req); err != nil {
		p.Warn(view.AsError(err, "page.section.failed"))
	}

	s, err := readSessions(ctx, conn)
	if err != nil {
		put("sessions", nil, classify(err, req))
	} else {
		put("sessions", view.KeyValue{Pairs: sessionPairs(s)}, nil)
	}

	a, err := readArchiver(ctx, conn)
	if err != nil {
		put("archiver", nil, classify(err, req))
	} else {
		put("archiver", archiverPairs(a), nil)
	}

	v, err := cacheView(ctx, conn, req)
	put("cache", v, err)

	v, err = tableListView(ctx, conn, req.With(map[string]any{"limit": overviewTableLimit}), false)
	put("largest tables", v, err)

	// Without the query text, which is what lets this page stay Read: the
	// overview answers "is anything stuck" from state, duration and what a
	// session is waiting on, and none of that is a value anybody stored.
	// `rta pg activity` is where the query text lives, and it is Write for
	// exactly that reason.
	v, err = activityView(ctx, conn, req.With(map[string]any{"limit": overviewActivityLimit}), false, false)
	put("activity", v, err)

	if p.Empty() {
		return nil, view.Errorf("pg.overview.unavailable", "no section could be produced")
	}
	return p.View(), nil
}

// roleOf reports "primary" or "standby". pg_is_in_recovery is true for the
// lifetime of a standby, including one that has been promoted and is still
// catching up, which is exactly the moment this fact is worth having.
//
// Returns the raw driver error, uncoded: every caller already holds a
// plugin.Request and classifies it themselves, and roleOf has no request of
// its own to classify with. The overview does not call it: its replication
// reading already says whether the server is in recovery, in the one statement
// that also gives the uptime.
func roleOf(ctx context.Context, conn querier) (string, error) {
	var recovery bool
	if err := conn.QueryRow(ctx, `select pg_is_in_recovery()`).Scan(&recovery); err != nil {
		return "", err
	}
	if recovery {
		return "standby", nil
	}
	return "primary", nil
}

// cacheView renders pg_statio's buffer cache hit ratio, the textbook first
// question about a database that feels slow: a low ratio means the working
// set does not fit in shared_buffers, and everything else is downstream of
// that. -1 signals "nothing to divide by yet" from SQL rather than a NULL
// pgx has to be told how to scan, matching how pg.activity already turns
// query_start's possible NULL into 0 in the query itself.
//
// **A ratio of counters is a ratio since they were zeroed**, and read as
// current it is a figure about last month: the line says since when, from the
// database's own stats_reset, so that a hit ratio that has been dragged down
// by one bad hour three weeks ago reads as that.
func cacheView(ctx context.Context, conn querier, req plugin.Request) (view.View, error) {
	var ratio float64
	var since *time.Time
	err := conn.QueryRow(ctx, `
		select coalesce(round(100 * sum(heap_blks_hit) /
		       nullif(sum(heap_blks_hit) + sum(heap_blks_read), 0), 2), -1),
		       (select stats_reset from pg_stat_database where datname = current_database())
		from pg_statio_user_tables`).Scan(&ratio, &since)
	if err != nil {
		return nil, classify(err, req)
	}
	if ratio < 0 {
		return view.KeyValue{Pairs: []view.Pair{
			{Key: "cache hit ratio", Value: "no read activity recorded yet"},
		}}, nil
	}
	line := fmt.Sprintf("%.1f%% since the counters began", ratio)
	if since != nil {
		line = fmt.Sprintf("%.1f%% since %s", ratio, since.UTC().Format("2006-01-02"))
	}
	return view.KeyValue{Pairs: []view.Pair{
		{Key: "cache hit ratio", Value: fmt.Sprintf("%s (%s)", line, cacheVerdict(ratio))},
	}}, nil
}

// cacheVerdict is cacheView's ratio-to-word mapping, pulled out to where it
// can be tested against the boundary without a database — the same reason
// builtin/sys keeps loadVerdict and usageStatus as plain functions of a
// float rather than inline in the query that produces one.
func cacheVerdict(ratio float64) string {
	if ratio < cacheHitFloor {
		return "low — cold cache or small shared_buffers"
	}
	return "healthy"
}
