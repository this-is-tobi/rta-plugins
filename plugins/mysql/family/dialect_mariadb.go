package family

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// mariadbDialect is MariaDB's answer to each question the shared code cannot
// answer once for both forks (dialect); dialect_mysql.go holds MySQL's.
type mariadbDialect struct{}

// versionString undoes the prefix MariaDB puts in front of its own number for
// the sake of a replication handshake older clients cannot parse: with
// replication-version-hack on, VERSION() reads 5.5.5-10.11.6-MariaDB, and
// read as it stands that is a 5.5 server that knows none of these statements.
func (mariadbDialect) versionString(raw string) string {
	if rest, ok := strings.CutPrefix(raw, "5.5.5-"); ok && strings.Contains(rest, "MariaDB") {
		return rest
	}
	return raw
}

// MariaDB spells the replication statements both ways and has since 10.5, and
// reads every named connection of a multi-source replica through the ALL form
// where the plain one reads only the default. Each list is the spelling this
// version answers to first and the others after it, and firstSupported moves
// on only for a syntax error.
func (mariadbDialect) replicaStatusStatements(v serverVersion) []string {
	if v.atLeast(10, 5, 1) {
		return []string{"SHOW ALL REPLICAS STATUS", "SHOW ALL SLAVES STATUS", "SHOW SLAVE STATUS"}
	}
	return []string{"SHOW ALL SLAVES STATUS", "SHOW SLAVE STATUS", "SHOW ALL REPLICAS STATUS"}
}

func (mariadbDialect) binlogStatusStatements(v serverVersion) []string {
	if v.atLeast(10, 5, 2) {
		return []string{"SHOW BINLOG STATUS", "SHOW MASTER STATUS"}
	}
	return []string{"SHOW MASTER STATUS", "SHOW BINLOG STATUS"}
}

func (mariadbDialect) replicaHostsStatements(v serverVersion) []string {
	if v.atLeast(10, 5, 1) {
		return []string{"SHOW REPLICA HOSTS", "SHOW SLAVE HOSTS"}
	}
	return []string{"SHOW SLAVE HOSTS", "SHOW REPLICA HOSTS"}
}

func (mariadbDialect) variablesStatement() string {
	return `SHOW GLOBAL VARIABLES WHERE Variable_name IN ` +
		`('server_id','read_only','log_bin','gtid_domain_id','gtid_binlog_pos','gtid_current_pos','gtid_slave_pos')`
}

// privilegeFor names what the server wants for each statement. MariaDB split
// REPLICATION CLIENT into narrower privileges in 10.5.2 and gave replica
// status its own in 10.5.9; the old name is still what a server older than
// that checks, and what a grant of it is translated into on a newer one.
func (mariadbDialect) privilegeFor(s section, v serverVersion) string {
	switch s {
	case sectionHosts:
		if v.atLeast(10, 5, 2) {
			return "REPLICATION MASTER ADMIN"
		}
		return "REPLICATION SLAVE"
	case sectionBinlog:
		if v.atLeast(10, 5, 2) {
			return "BINLOG MONITOR"
		}
	default:
		if v.atLeast(10, 5, 9) {
			return "REPLICA MONITOR"
		}
	}
	return "REPLICATION CLIENT"
}

// The connected replicas are behind an administrative privilege, not a
// monitoring one: REPLICATION MASTER ADMIN also lets an account change
// replication settings on the source. It is named so that nobody grants it to
// a monitoring account without knowing that.
func (mariadbDialect) privilegeNote(s section) string {
	if s == sectionHosts {
		return " (an administrative privilege, more than monitoring needs — every other part of this answer works without it)"
	}
	return ""
}

// grantScope is what a grant is made ON. Every privilege here is global.
func (mariadbDialect) grantScope(section) string { return "*.*" }

func (mariadbDialect) vendorPairs(vars map[string]string) []view.Pair {
	var pairs []view.Pair
	for _, p := range [][2]string{
		{"gtid domain", "gtid_domain_id"},
		{"gtid binlog pos", "gtid_binlog_pos"},
		{"gtid current pos", "gtid_current_pos"},
		{"gtid slave pos", "gtid_slave_pos"},
	} {
		if v := vars[p[1]]; v != "" {
			pairs = append(pairs, view.Pair{Key: p[0], Value: clip(v)})
		}
	}
	return pairs
}

// gtidOf reads what a replica has received and what it has applied. MariaDB
// names a position by domain, server and sequence — 0-1-2345 — and a sequence
// only grows within a domain, so what is pending in a domain is how far the
// received sequence is past the applied one, whichever server wrote either.
//
// Columns are read under their normalised names (see columnName), which spell
// master as source and slave as replica everywhere: the applied position is
// the column the server calls Gtid_Slave_Pos and this reads gtid_replica_pos.
// The variable of the same name keeps its own spelling, being a value rather
// than a column.
func (mariadbDialect) gtidOf(row map[string]string) gtidState {
	received := strings.Join(strings.Fields(row["gtid_io_pos"]), "")
	applied := strings.Join(strings.Fields(row["gtid_replica_pos"]), "")
	g := gtidState{mode: strings.ToLower(row["using_gtid"]), received: received, applied: applied}
	if g.mode == "no" {
		g.mode = "by file position"
	}
	if received == "" && applied == "" {
		g.mode = ""
		return g
	}
	got, ok1 := parsePositions(received)
	have, ok2 := parsePositions(applied)
	if !ok1 || !ok2 {
		g.pending = "?"
		return g
	}
	var total int64
	var parts []string
	for domain, seq := range got {
		if behind := seq - have[domain]; behind > 0 {
			total += behind
			parts = append(parts, "domain "+strconv.FormatInt(domain, 10)+": "+strconv.FormatInt(behind, 10))
		}
	}
	sort.Strings(parts)
	g.pending = "0"
	if total > 0 {
		g.pending = strconv.FormatInt(total, 10) + " (" + strings.Join(parts, ", ") + ")"
	}
	return g
}

// parsePositions is the sequence reached in each domain of a list such as
// 0-1-2345,1-2-77.
func parsePositions(s string) (map[int64]int64, bool) {
	out := map[int64]int64{}
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			continue
		}
		f := strings.Split(part, "-")
		if len(f) != 3 {
			return nil, false
		}
		domain, err1 := strconv.ParseInt(f[0], 10, 64)
		seq, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, false
		}
		out[domain] = max(out[domain], seq)
	}
	return out, true
}

// tlsArgs maps the plugin's tls input — go-sql-driver's vocabulary, shared
// with every in-process capability here — onto the MariaDB client's own
// --ssl family. **Not --ssl-mode**, which is the MySQL 8 spelling: the
// --ssl/--ssl-verify-server-cert pair is what MariaDB's client has carried
// across every version this is likely to meet, and choosing the spelling the
// installed fork does not know turns a working dump into "unknown option".
//
// The mapping is by *meaning*, matching MySQL's: "true" both encrypts and
// verifies the server is who it claims (the driver's own behaviour for true),
// "verify-ca" its chain alone, "skip-verify" encrypts without verifying,
// "preferred" is the client's default and passes nothing.
//
// ca-file goes with true and verify-ca as --ssl-ca, the CA the child verifies
// against, so the dump verifies the server the pre-flight connection did. It
// is only ever beside those two: tlsConfig refuses it beside the two modes
// that never verify, and verify-ca without it, before any child is described.
// verify-ca has no word of its own in this client, and this spelling of it is
// the one a client before 11.4 reads as a chain check; pin.go says what a
// newer one is handed instead.
func (mariadbDialect) tlsArgs(req plugin.Request) []string {
	switch tlsMode(req) {
	case "false":
		return []string{"--skip-ssl"}
	case "true":
		if ca := caFile(req); ca != "" {
			return []string{"--ssl", "--ssl-verify-server-cert", "--ssl-ca=" + ca}
		}
		return []string{"--ssl", "--ssl-verify-server-cert"}
	case "verify-ca":
		return []string{"--ssl", "--ssl-ca=" + caFile(req), "--skip-ssl-verify-server-cert"}
	case "skip-verify":
		return []string{"--ssl"}
	}
	return nil
}

// sourceNote is the Galera question MySQL's dump has no reason to ask. A node
// that has lost quorum still accepts connections and still answers queries —
// from its own side of a partition — so a dump taken there is a backup of one
// side of a split brain, and nothing about the file says so afterwards. The
// receipt says it while somebody is still looking.
//
// Best effort and deliberately not fatal: a server built without wsrep answers
// with an empty provider (or nothing at all), and a standalone MariaDB is not
// a broken cluster — the same reading galera.go makes.
func (mariadbDialect) sourceNote(ctx context.Context, db *sql.DB, sf plugin.Surface) string {
	var name, status string
	if err := db.QueryRowContext(ctx,
		`show global status like 'wsrep_cluster_status'`).Scan(&name, &status); err != nil {
		return ""
	}
	return galeraNote(status, sf)
}

// galeraNote is what the receipt says of a node whose wsrep_cluster_status is
// status: nothing when it is no node at all, and the lost quorum loudest.
func galeraNote(status string, sf plugin.Surface) string {
	switch {
	case status == "":
		return ""
	case strings.EqualFold(status, "Primary"):
		return " — a Galera node holding quorum"
	}
	// The one that changes what the backup means, said while somebody is
	// looking at the file they just made rather than left for the day they
	// restore it.
	return fmt.Sprintf(" — **a Galera node whose cluster status is %s, not Primary**: "+
		"this node has lost quorum and is serving its own side of a partition, so this "+
		"dump is a backup of that side. %s has the whole picture",
		status, sf.CapabilityName("mariadb.replication.status"))
}
