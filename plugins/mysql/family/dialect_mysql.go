package family

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// mysqlDialect is MySQL's answer to each question the shared code cannot
// answer once for both forks (dialect); dialect_mariadb.go holds MariaDB's.
type mysqlDialect struct{}

func (mysqlDialect) versionString(raw string) string { return raw }

// MySQL renamed the replication statements in two steps, and removed the old
// spelling in 8.4: SHOW REPLICA STATUS and SHOW REPLICAS arrived in 8.0.22,
// SHOW BINARY LOG STATUS in 8.2.0. Each list is the spelling this version
// answers to first and the other after it — a server that does not know the
// first is told so with a syntax error, and firstSupported moves on only for
// that.
func (mysqlDialect) replicaStatusStatements(v serverVersion) []string {
	if v.atLeast(8, 0, 22) {
		return []string{"SHOW REPLICA STATUS", "SHOW SLAVE STATUS"}
	}
	return []string{"SHOW SLAVE STATUS", "SHOW REPLICA STATUS"}
}

func (mysqlDialect) binlogStatusStatements(v serverVersion) []string {
	if v.atLeast(8, 2, 0) {
		return []string{"SHOW BINARY LOG STATUS", "SHOW MASTER STATUS"}
	}
	return []string{"SHOW MASTER STATUS", "SHOW BINARY LOG STATUS"}
}

func (mysqlDialect) replicaHostsStatements(v serverVersion) []string {
	if v.atLeast(8, 0, 22) {
		return []string{"SHOW REPLICAS", "SHOW SLAVE HOSTS"}
	}
	return []string{"SHOW SLAVE HOSTS", "SHOW REPLICAS"}
}

// variablesStatement filters on the server so that a name this version does
// not have — super_read_only before 5.7, gtid_mode before 5.6 — is simply
// absent from the answer rather than an error that costs the others.
func (mysqlDialect) variablesStatement() string {
	return `SHOW GLOBAL VARIABLES WHERE Variable_name IN ` +
		`('server_id','server_uuid','read_only','super_read_only','log_bin','gtid_mode','gtid_executed')`
}

// privilegeFor names what the server wants for each statement. SHOW REPLICA
// STATUS and the binary log status take REPLICATION CLIENT; the connected
// replicas are a different, older privilege, REPLICATION SLAVE, which is the
// one a replica's own account holds — so an account that may monitor and not
// see its replicas is common and is what this names.
func (mysqlDialect) privilegeFor(s section, _ serverVersion) string {
	switch s {
	case sectionHosts:
		return "REPLICATION SLAVE"
	case sectionCluster:
		return "SELECT"
	}
	return "REPLICATION CLIENT"
}

func (mysqlDialect) privilegeNote(section) string { return "" }

// grantScope is what a grant is made ON. The replication privileges are
// global; the group's tables are read with a SELECT on the schema they are in.
func (mysqlDialect) grantScope(s section) string {
	if s == sectionCluster {
		return "performance_schema.*"
	}
	return "*.*"
}

func (mysqlDialect) vendorPairs(vars map[string]string) []view.Pair {
	var pairs []view.Pair
	if u := vars["server_uuid"]; u != "" {
		pairs = append(pairs, view.Pair{Key: "server uuid", Value: u})
	}
	if m := vars["gtid_mode"]; m != "" {
		pairs = append(pairs, view.Pair{Key: "gtid mode", Value: strings.ToLower(m)})
	}
	if g := vars["gtid_executed"]; g != "" {
		pairs = append(pairs, view.Pair{Key: "gtid executed", Value: clip(g)})
	}
	return pairs
}

// gtidOf reads what a replica has received and what it has applied, and
// subtracts one from the other. The server could do the subtraction
// (GTID_SUBTRACT), but a statement per channel is a round trip for what two
// strings already hold, and a function of two strings is one a test can feed
// the sets a replica really reported.
func (mysqlDialect) gtidOf(row map[string]string) gtidState {
	received := strings.Join(strings.Fields(row["retrieved_gtid_set"]), "")
	applied := strings.Join(strings.Fields(row["executed_gtid_set"]), "")
	g := gtidState{received: received, applied: applied}
	if received == "" && applied == "" {
		return g
	}
	g.mode = "by file position"
	if row["auto_position"] == "1" {
		g.mode = "auto-position"
	}
	got, ok1 := parseGTIDSet(received)
	have, ok2 := parseGTIDSet(applied)
	if !ok1 || !ok2 {
		g.pending = "?"
		return g
	}
	missing := got.subtract(have)
	if n := missing.count(); n > 0 {
		g.pending = strconv.FormatInt(n, 10) + " (" + clip(missing.String()) + ")"
	} else {
		g.pending = "0"
	}
	return g
}

// tlsArgs maps the plugin's tls input — go-sql-driver's vocabulary, shared
// with every in-process capability here — onto mysqldump/mysql's --ssl-mode.
// The mapping is by *meaning*: "true" verifies the server is who it claims
// (VERIFY_IDENTITY, the driver's own behaviour for true), "verify-ca" its
// chain alone (VERIFY_CA, which is where the name came from), "skip-verify"
// encrypts without verifying (REQUIRED), "preferred" is the client default
// and passes nothing.
//
// ca-file goes with true and verify-ca as --ssl-ca, the CA the child verifies
// against, so the dump verifies the server the pre-flight connection did. It
// is only ever beside those two: tlsConfig refuses it beside the two modes
// that never verify, and verify-ca without it, before any child is described.
func (mysqlDialect) tlsArgs(req plugin.Request) []string {
	switch tlsMode(req) {
	case "false":
		return []string{"--ssl-mode=DISABLED"}
	case "true":
		if ca := caFile(req); ca != "" {
			return []string{"--ssl-mode=VERIFY_IDENTITY", "--ssl-ca=" + ca}
		}
		return []string{"--ssl-mode=VERIFY_IDENTITY"}
	case "verify-ca":
		return []string{"--ssl-mode=VERIFY_CA", "--ssl-ca=" + caFile(req)}
	case "skip-verify":
		return []string{"--ssl-mode=REQUIRED"}
	}
	return nil
}

// sourceNote adds nothing on MySQL, whose dump has never read the group's
// state; dialect_mariadb.go says why MariaDB's reads Galera's.
func (mysqlDialect) sourceNote(context.Context, *sql.DB, plugin.Surface) string { return "" }

// noCA answers MySQL's client refusing tls=true for want of a CA, which
// MariaDB's never does (classifyDump reads both). true is VERIFY_IDENTITY to
// the child, and the MySQL client, unlike the driver, never reads the
// machine's own trust store: with no --ssl-ca it will not connect at all. The pre-flight connection verified against that store and passed — a
// certificate nothing here trusts is refused there, naming ca-file — so this
// is a server the machine already vouches for, and the CA that does so is
// what the child needs named.
func noCA(sf plugin.Surface, line string) *view.Error {
	return view.Errorf(engine.NS+".tls.ca.required", "%s", line).
		WithHint("the MySQL client verifies only against a CA it is given, never the store this machine " +
			"trusted the server through — " + sf.SettingName("ca-file") + " names one: the CA that issued " +
			"the server's certificate, or the machine's CA bundle")
}
