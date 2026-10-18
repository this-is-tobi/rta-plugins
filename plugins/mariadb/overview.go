package main

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func statusCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "mariadb.status",
		Summary:    "Whether the database answers, and what it is",
		Keywords:   []string{"ping", "alive", "version", "connection"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "The cheapest possible call: connect, ask the server what it is, disconnect. " +
			"Useful on its own as a reachability check, and it is the call whose failure carries " +
			"the classified hint for every connection problem the others would hit later.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withDB(ctx, req, func(ctx context.Context, db *sql.DB) (view.View, error) {
				return statusView(ctx, db, req)
			})
		},
	})
}

// serverInfo is the handful of scalars every glance here starts from. Read in
// one round trip rather than four, because a status call that costs four
// round trips against a loaded server is a status call people stop making.
type serverInfo struct {
	version  string
	flavour  string
	uptime   time.Duration
	threads  int64
	running  int64
	maxConns int64
	// lockWaits is the transactions waiting on an InnoDB row lock right now, and
	// is known only where the server reports it: one without InnoDB does not.
	// slowQueries counts the statements slower than long_query_time since the
	// server started, whether or not the slow query log is on.
	lockWaits      int64
	lockWaitsKnown bool
	slowQueries    int64
}

// readServerInfo is the two statements a glance starts from. They are read
// apart for the overview, whose rows each cost only themselves when the other
// half cannot be read.
func readServerInfo(ctx context.Context, db *sql.DB) (serverInfo, error) {
	info, err := readIdentity(ctx, db)
	if err != nil {
		return info, err
	}
	return readCounters(ctx, db, info)
}

// readIdentity is what answered and how many connections it will take.
func readIdentity(ctx context.Context, db *sql.DB) (serverInfo, error) {
	var info serverInfo
	row := db.QueryRowContext(ctx, `SELECT VERSION(), @@max_connections`)
	if err := row.Scan(&info.version, &info.maxConns); err != nil {
		return info, err
	}
	info.flavour = flavourOf(info.version)
	return info, nil
}

// readCounters adds the server's own counters to what readIdentity found.
//
// SHOW GLOBAL STATUS rather than performance_schema: it is available to
// an unprivileged user and on every version back to 5.x, where the
// performance_schema tables need a grant somebody has to have thought to
// give. A description of the server should not be the call that needs the
// most privilege.
func readCounters(ctx context.Context, db *sql.DB, info serverInfo) (serverInfo, error) {
	rows, err := db.QueryContext(ctx, `SHOW GLOBAL STATUS WHERE Variable_name IN `+
		`('Uptime','Threads_connected','Threads_running','Innodb_row_lock_current_waits','Slow_queries')`)
	if err != nil {
		return info, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return info, err
		}
		n, _ := strconv.ParseInt(value, 10, 64)
		switch name {
		case "Uptime":
			info.uptime = time.Duration(n) * time.Second
		case "Threads_connected":
			info.threads = n
		case "Threads_running":
			info.running = n
		case "Innodb_row_lock_current_waits":
			info.lockWaits, info.lockWaitsKnown = n, true
		case "Slow_queries":
			info.slowQueries = n
		}
	}
	return info, rows.Err()
}

// flavourOf reads the fork out of the version string, which is the only place
// it is stated. MariaDB reports something like "11.4.2-MariaDB"; MySQL does
// not carry the word at all. Worth surfacing because the two have diverged
// enough that somebody debugging needs to know which one answered — and
// because the answer decides whether plugins/mariadb has anything to add.
func flavourOf(version string) string {
	lower := strings.ToLower(version)
	for _, marker := range []string{"MariaDB", "Percona"} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return marker
		}
	}
	return "MySQL"
}

func statusView(ctx context.Context, db *sql.DB, req plugin.Request) (view.View, error) {
	info, err := readServerInfo(ctx, db)
	if err != nil {
		return nil, classify(err, req)
	}
	return view.KeyValue{Pairs: statusPairs(info, req)}, nil
}

// statusPairs is the status page. The server is named as its reader reaches it
// again (Request.Reached): through a forward the address is 127.0.0.1 and a
// port that closed with the call, which a page kept or pasted into a ticket
// named and the reader could do nothing with.
func statusPairs(info serverInfo, req plugin.Request) []view.Pair {
	pairs := []view.Pair{
		{Key: "server", Value: req.Reached(address(req))},
		{Key: "flavour", Value: info.flavour},
		{Key: "version", Value: info.version},
		{Key: "uptime", Value: uptime(info.uptime)},
		{Key: "connections", Value: fmt.Sprintf("%d of %d", info.threads, info.maxConns)},
		{Key: "running", Value: strconv.FormatInt(info.running, 10)},
	}
	if dbName := req.String("database"); dbName != "" {
		pairs = append(pairs, view.Pair{Key: "database", Value: dbName})
	}
	return pairs
}

func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "mariadb.overview",
		Summary:    "Everything about this connection at a glance",
		Keywords:   []string{"summary", "health", "dashboard"},
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Description: "The compact form is what a dashboard tile shows and what an agent reads by default, " +
			"ordered for five seconds: `status` first (ok, or warn or fail and the worst fact in a few " +
			"words), then `up since`, `replication` (led by ok, warn or fail, with the thread that " +
			"stopped or how far behind a replica is; mariadb.replication.status has the rest), " +
			"`connections` (of max_connections, led by warn past 85% and fail past 95%), `lock waits` " +
			"(transactions waiting on an InnoDB row lock), `running` (the other threads running a " +
			"statement and the slow queries since the server started, when there are any), `cluster` " +
			"(a Galera node's state, only where wsrep is on) and `server` (the " +
			"address and the version). A row the account cannot read says so, and the status names " +
			"it.\n\n" +
			"The full page (`detail`) is everything the compact form holds and what it leaves out: the " +
			"server's status, one line on replication, the largest databases on it, and what every " +
			"session is doing, without the statement text — state, time and command, which answers " +
			"\"is anything stuck\" and hands back nothing anybody stored. The statement text is " +
			"mariadb.activity, and it is a write for exactly that reason. The databases are an " +
			"aggregation over INFORMATION_SCHEMA that the compact form never runs.",
		Agent: "A status row first (ok, or warn or fail and the worst fact), then up since, replication " +
			"(led by ok, warn or fail, with the stopped thread or the lag), connections of " +
			"max_connections (warn past 85%, fail past 95%), InnoDB row lock waits, the other threads " +
			"running and the slow queries since start when there are any, the Galera node's state " +
			"where wsrep is on, and the address and version. A row the account " +
			"cannot read says so and the status names it. `detail` adds the largest databases and " +
			"what every session is doing, without statement text. States and counts only, never " +
			"stored values.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withDB(ctx, req, func(ctx context.Context, db *sql.DB) (view.View, error) {
				if req.Bool("detail") {
					return detailedOverview(ctx, db, req)
				}
				return compactOverview(ctx, db, req)
			})
		},
	})
}

// compactOverview is the form a dashboard tile and an agent read by default, and
// a tile shows the first eight lines of it, so what it says is ordered for
// someone with five seconds: the verdict, then how the server is doing (when it
// started, replication, connections, lock waits, what is running, a cluster),
// and what it is called last.
//
// Each part is read on its own, in the same "add what answered" style as
// builtin/sys's runOverview: a statement that fails costs the reader its rows,
// said as rows that could not be read and named in the verdict, and never the
// page. Only when nothing at all could be read is it an error, and it is the
// first statement's.
//
// **It asks the server for less than the page did**: the same statements, less
// the aggregation over INFORMATION_SCHEMA that totals every table of every
// database for a table the compact form does not show. The counters it adds to
// SHOW GLOBAL STATUS are names in the statement that was already there.
func compactOverview(ctx context.Context, db *sql.DB, req plugin.Request) (view.View, error) {
	id, idErr := readIdentity(ctx, db)
	info, cErr := readCounters(ctx, db, id)
	st, replErr := readState(ctx, db, req)
	if idErr != nil && cErr != nil && replErr != nil {
		return nil, classify(idErr, req)
	}

	var (
		rows       []view.Pair
		findings   []finding
		unreadable []string
	)
	add := func(r row) {
		rows = append(rows, r.pair)
		if r.finding.grade != "" {
			findings = append(findings, r.finding)
		}
		if r.unreadable != "" && !slices.Contains(unreadable, r.unreadable) {
			unreadable = append(unreadable, r.unreadable)
		}
	}
	plain := func(key, value string) { add(row{pair: view.Pair{Key: key, Value: value}}) }
	// A part that could not be read names itself once in the verdict, however
	// many rows are made from it.
	unread := func(name, key string, err error) {
		r := unreadRow(key, classify(err, req).Message)
		r.unreadable = name
		add(r)
	}

	if cErr != nil {
		unread("status variables", "up since", cErr)
	} else {
		plain("up since", upSince(info.uptime))
	}

	if replErr != nil {
		add(unreadRow("replication", replErr.Message))
	} else {
		add(replicationRowOf(st))
	}

	switch {
	case cErr != nil:
		unread("status variables", "connections", cErr)
	case idErr != nil:
		unread("max_connections", "connections", idErr)
	default:
		r := row{pair: view.Pair{Key: "connections", Value: info.connectionsText()}, finding: info.connectionsFinding()}
		add(r)
	}
	if cErr == nil && info.lockWaitsKnown {
		plain("lock waits", info.lockWaitsText())
	}
	if cErr == nil && info.loadText() != "" {
		plain("running", info.loadText())
	}
	if replErr == nil {
		if cluster := clusterRowOf(st); cluster != nil {
			add(*cluster)
		}
	}

	if idErr != nil {
		add(row{pair: view.Pair{Key: "server", Value: req.Reached(address(req)) + " · version could not be read"},
			unreadable: "version"})
	} else {
		plain("server", serverText(req.Reached(address(req)), info))
	}

	status := view.Pair{Key: "status", Value: statusText(findings, unreadable)}
	return view.KeyValue{Pairs: append([]view.Pair{status}, rows...)}, nil
}

// detailedOverview is the full page: the server's status, replication, the
// largest databases, and what every session is doing, composed through the one
// connection its own Run already opened rather than through plugin.Page.AddAs,
// which would open a connection per section for what is meant to be a single
// glance. It carries everything the compact form does and the databases and
// sessions it leaves out.
func detailedOverview(ctx context.Context, db *sql.DB, req plugin.Request) (view.View, error) {
	info, err := readServerInfo(ctx, db)
	if err != nil {
		return nil, classify(err, req)
	}
	pairs := statusPairs(info, req)
	pairs = append(pairs, view.Pair{Key: "up since", Value: upSince(info.uptime)})
	if info.lockWaitsKnown {
		pairs = append(pairs, view.Pair{Key: "lock waits", Value: info.lockWaitsText()})
	}
	pairs = append(pairs, view.Pair{Key: "slow queries", Value: strconv.FormatInt(info.slowQueries, 10) + " since start"})
	p := plugin.NewPage(ctx, req)
	p.Put("status", view.KeyValue{Pairs: pairs})

	line, warnings := replicationSummary(ctx, db, req)
	p.Put("replication", line)
	for i := range warnings {
		p.Warn(&warnings[i])
	}

	databases, err := databaseTable(ctx, db, req, 10, false)
	if err != nil {
		return nil, err
	}
	p.Put("databases", databases)

	activity, err := activityView(ctx, db, req, false, false)
	if err != nil {
		return nil, err
	}
	p.Put("activity", activity)
	return p.View(), nil
}

func databaseListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "mariadb.database.list",
		Summary:    "List databases on this server, with their sizes",
		Keywords:   []string{"db", "catalog", "schemas"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Names, table counts and on-disk sizes. Sizes come from INFORMATION_SCHEMA " +
			"and are what the storage engine last reported rather than a live measurement — " +
			"close enough to find the big one, not close enough to bill on.\n\n" +
			"Only the databases this user may see: MariaDB filters INFORMATION_SCHEMA by grant, so " +
			"a short list here means a narrow grant and not an empty server.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withDB(ctx, req, func(ctx context.Context, db *sql.DB) (view.View, error) {
				return databaseTable(ctx, db, req, req.Int("limit"), true)
			})
		},
	},
		// The bound of every listing of what is on a server, which is the one
		// mysql.table.list's comment gives the reason for.
		plugin.Field{Name: "limit", Short: "n", Type: plugin.Int, Config: "limit", Default: 50, Min: 1, Max: 1000,
			Help: "how many databases to show"})
}

func databaseTable(ctx context.Context, db *sql.DB, req plugin.Request, limit int, say bool) (view.Table, error) {
	// Aggregated from INFORMATION_SCHEMA.TABLES rather than SHOW DATABASES,
	// because the size is the column that makes this worth running and SHOW
	// does not carry it. LEFT JOIN off SCHEMATA so a database holding no
	// tables still appears — an empty database is a fact, and dropping it
	// would make the list disagree with SHOW DATABASES for no stated reason.
	rows, err := db.QueryContext(ctx, `
		SELECT s.SCHEMA_NAME,
		       COUNT(t.TABLE_NAME),
		       COALESCE(SUM(t.DATA_LENGTH + t.INDEX_LENGTH), 0)
		  FROM INFORMATION_SCHEMA.SCHEMATA s
		  LEFT JOIN INFORMATION_SCHEMA.TABLES t ON t.TABLE_SCHEMA = s.SCHEMA_NAME
		 GROUP BY s.SCHEMA_NAME
		 ORDER BY 3 DESC
		 LIMIT ?`, limit+1)
	if err != nil {
		return view.Table{}, classify(err, req)
	}
	defer func() { _ = rows.Close() }()

	t := view.Table{Columns: []view.Column{
		{Name: "Database"},
		{Name: "Tables", Kind: view.KindNumber},
		{Name: "Size", Kind: view.KindBytes},
	}}
	for rows.Next() {
		var name string
		var tables int64
		var size any
		if err := rows.Scan(&name, &tables, &size); err != nil {
			return view.Table{}, classify(err, req)
		}
		t.Rows = append(t.Rows, []string{listed(name), strconv.FormatInt(tables, 10), bytesCell(size)})
	}
	if err := rows.Err(); err != nil {
		return view.Table{}, classify(err, req)
	}
	t = cutAtLimit(t, limit, "database", say, req.Surface())
	t.Total = len(t.Rows)
	t.Empty = "no databases"
	return t, nil
}
