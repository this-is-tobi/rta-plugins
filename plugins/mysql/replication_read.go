package main

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What one connection can say about replication, read once and kept as the
// server spelled it. Everything the capability shows is derived from a state
// by the functions in replication.go, which is what lets their rules be tested
// against a server's captured answers instead of against a server.
//
// Each read stands alone. A statement that fails — a grant the connecting
// account does not hold, a spelling this server's version does not know —
// costs the view that one part and says so, because the alternative is a
// monitoring account that cannot see the connected replicas getting no answer
// about its own replica threads either, which is the opposite of what a view
// meant to be the one place to look is for.

// version is the server's own number, parsed far enough to choose between
// statement spellings. It is never shown: the version a person reads is
// whatever VERSION() said.
type serverVersion struct {
	major, minor, patch int
	known               bool
}

func parseVersion(raw string) serverVersion {
	raw = versionString(raw)
	end := 0
	for end < len(raw) && (unicode.IsDigit(rune(raw[end])) || raw[end] == '.') {
		end++
	}
	parts := strings.Split(strings.TrimRight(raw[:end], "."), ".")
	var n [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		v, err := strconv.Atoi(parts[i])
		if err != nil {
			return serverVersion{}
		}
		n[i] = v
	}
	if end == 0 {
		return serverVersion{}
	}
	return serverVersion{major: n[0], minor: n[1], patch: n[2], known: true}
}

// atLeast says whether the server is this version or newer. A version nobody
// could parse is assumed new: the statement lists below try the modern
// spelling first and fall back on a syntax error, so a wrong guess costs one
// refused statement and never a wrong answer.
func (v serverVersion) atLeast(major, minor, patch int) bool {
	if !v.known {
		return true
	}
	switch {
	case v.major != major:
		return v.major > major
	case v.minor != minor:
		return v.minor > minor
	}
	return v.patch >= patch
}

// section names a part of the answer that has a statement of its own, and so
// a privilege of its own, and so can be the one part that is missing.
type section string

const (
	sectionReplica section = "replica status"
	sectionBinlog  section = "binary log status"
	sectionHosts   section = "connected replicas"
	sectionVars    section = "server variables"
	sectionCluster section = "cluster state"
)

type state struct {
	version serverVersion
	// channels is one row of replica status per replication source this
	// server follows — one for an ordinary replica, several for a replica of
	// several sources, none for a server that follows nothing. Column names
	// are normalised (see columnName) so the rest of the code spells each
	// column one way.
	channels []map[string]string
	// binlog is what the binary log status statement said: empty when the
	// log is off or the statement could not be read.
	binlog map[string]string
	vars   map[string]string
	hosts  []map[string]string
	// unread is each part that could not be read, and why. A state with any
	// is a partial answer, and everything built from it must say so.
	unread  map[section]*view.Error
	cluster clusterPart
}

// clusterPart is what a clustering layer the dialect knows adds: nothing for
// a server without one. An error is the layer's state that could not be read,
// and is reported as an unread part like any other.
type clusterPart struct {
	facet   *facet
	section *view.Section
	err     error
}

// columnName gives the replica status column of every spelling this protocol
// has used one name. master/slave became source/replica in two different
// releases of each server, and a column read by only one spelling is how a
// replica comes to report both its threads as missing — which reads as
// stopped.
var columnSpelling = strings.NewReplacer("master", "source", "slave", "replica")

func columnName(raw string) string { return columnSpelling.Replace(strings.ToLower(raw)) }

func readRows(ctx context.Context, db *sql.DB, statement string) ([]map[string]string, error) {
	rows, err := db.QueryContext(ctx, statement)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]string
	for rows.Next() {
		scan := make([]any, len(names))
		holders := make([]any, len(names))
		for i := range scan {
			holders[i] = &scan[i]
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, err
		}
		row := make(map[string]string, len(names))
		for i, n := range names {
			row[columnName(n)] = cell(scan[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func isSyntaxError(err error) bool {
	var myErr *mysql.MySQLError
	return errors.As(err, &myErr) && myErr.Number == 1064 // ER_PARSE_ERROR
}

func isDenied(err error) bool {
	var myErr *mysql.MySQLError
	if !errors.As(err, &myErr) {
		return false
	}
	switch myErr.Number {
	case 1227, 1142, 1044: // ER_SPECIFIC_ACCESS_DENIED_ERROR, ER_TABLEACCESS_DENIED_ERROR, ER_DBACCESS_DENIED_ERROR
		return true
	}
	return false
}

// firstSupported runs the spellings in order and moves on only when the server
// says it does not know the statement. Any other failure is the answer: trying
// the next spelling after a refused grant would bury the privilege error under
// a syntax error from a statement that was never the problem.
func firstSupported(ctx context.Context, db *sql.DB, statements []string) ([]map[string]string, error) {
	var err error
	for _, s := range statements {
		var rows []map[string]string
		if rows, err = readRows(ctx, db, s); err == nil || !isSyntaxError(err) {
			return rows, err
		}
	}
	return nil, err
}

// unreadable turns a refused statement into the warning that says which part
// is missing and what would restore it. Naming the privilege is the point: the
// server's own message names it too, but in words that vary by version and
// that an agent relaying it cannot act on.
func unreadable(err error, which section, v serverVersion, req plugin.Request) *view.Error {
	user := req.String("user")
	switch {
	case isDenied(err):
		priv := privilegeFor(which, v)
		return view.Errorf("mysql.replication.denied", "%q may not read the %s", user, which).
			WithHint("it needs " + priv + ": GRANT " + priv + " ON " + grantScope(which) + " TO '" + user + "'@'<host>'" +
				privilegeNote(which) + " — or " + req.Surface().SettingName("user") + " can name an account that has it")
	case isSyntaxError(err):
		return view.Errorf("mysql.replication.unsupported", "this server does not know how to report its %s", which).
			WithHint("it is older than any version this reads, or not the server it looks like; " +
				"version in " + req.Surface().CapabilityName("mysql.status") + " says which")
	}
	return classify(err, req)
}

func readState(ctx context.Context, db *sql.DB, req plugin.Request) (state, *view.Error) {
	st := state{unread: map[section]*view.Error{}}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&raw); err != nil {
		return st, classify(err, req)
	}
	st.version = parseVersion(raw)

	if vars, err := readVariables(ctx, db); err != nil {
		st.unread[sectionVars] = unreadable(err, sectionVars, st.version, req)
	} else {
		st.vars = vars
	}
	if rows, err := firstSupported(ctx, db, replicaStatusStatements(st.version)); err != nil {
		st.unread[sectionReplica] = unreadable(err, sectionReplica, st.version, req)
	} else {
		st.channels = rows
	}

	// A server with its binary log off has no position to report and no
	// replica able to connect to it, so neither statement is asked: the
	// answer is already known, and asking would demand a privilege to learn
	// nothing. A server whose variables could not be read is asked anyway.
	if logging := st.vars["log_bin"]; logging != "" && !isOn(logging) {
		return finish(ctx, db, req, st), nil
	}
	if rows, err := firstSupported(ctx, db, binlogStatusStatements(st.version)); err != nil {
		st.unread[sectionBinlog] = unreadable(err, sectionBinlog, st.version, req)
	} else if len(rows) > 0 {
		st.binlog = rows[0]
	}
	if rows, err := firstSupported(ctx, db, replicaHostsStatements(st.version)); err != nil {
		// Advisory, and kept out of the summary's gaps: the list says who is
		// connected and nothing about how they are doing — each replica's own
		// answer is where that is read — and its privilege is not one a
		// monitoring account is usually given. An answer that headed itself
		// partial, and a summary that warned, on every poll of every source
		// read that way would be noise nobody could silence.
		e := unreadable(err, sectionHosts, st.version, req)
		e.Advisory = true
		st.unread[sectionHosts] = e
	} else {
		st.hosts = rows
	}
	return finish(ctx, db, req, st), nil
}

func finish(ctx context.Context, db *sql.DB, req plugin.Request, st state) state {
	st.cluster = clusterOf(ctx, db)
	if st.cluster.err != nil {
		st.unread[sectionCluster] = unreadable(st.cluster.err, sectionCluster, st.version, req)
	}
	return st
}

func isOn(v string) bool {
	switch strings.ToUpper(v) {
	case "ON", "1", "YES", "TRUE":
		return true
	}
	return false
}

func readVariables(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := readRows(ctx, db, variablesStatement())
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[strings.ToLower(r["variable_name"])] = strings.Join(strings.Fields(r["value"]), "")
	}
	return out, nil
}
