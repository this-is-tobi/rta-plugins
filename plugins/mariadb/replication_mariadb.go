package main

import (
	"sort"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/view"
)

// What replication.go needs from the server it is talking to and cannot say
// once for both forks: the spelling of each statement, the privilege each one
// takes, and the vocabulary of a replica's transaction positions. plugins/mysql
// carries the same functions with MySQL's answers; everything else in
// replication*.go is one source tree twice over, and the drift gate holds it
// to that.

// versionString undoes the prefix MariaDB puts in front of its own number for
// the sake of a replication handshake older clients cannot parse: with
// replication-version-hack on, VERSION() reads 5.5.5-10.11.6-MariaDB, and
// read as it stands that is a 5.5 server that knows none of these statements.
func versionString(raw string) string {
	if rest, ok := strings.CutPrefix(raw, "5.5.5-"); ok && strings.Contains(rest, "MariaDB") {
		return rest
	}
	return raw
}

const capabilitySummary = "Replication and Galera state in one place: role, lag, positions, replicas, cluster"

// clusterNote is what the description adds for a clustering layer this plugin
// reads.
const clusterNote = "\n\nOn a Galera node it adds the cluster's own view of the node: the cluster size and " +
	"state, whether this node is in the primary component, caught up and in step with the cluster's " +
	"state, and how much flow control is holding it back. A node that has lost quorum still accepts " +
	"connections and still answers SELECT — it just stops being part of the cluster, and nothing else " +
	"about the server looks wrong while that happens."

// clusterAgentNote is clusterNote for an agent: the same facts, without the
// reasons.
const clusterAgentNote = " On a Galera node it adds the cluster size and state, whether the node is in " +
	"the primary component and in step, and its flow control."

// MariaDB spells the replication statements both ways and has since 10.5, and
// reads every named connection of a multi-source replica through the ALL form
// where the plain one reads only the default. Each list is the spelling this
// version answers to first and the others after it, and firstSupported moves
// on only for a syntax error.
func replicaStatusStatements(v serverVersion) []string {
	if v.atLeast(10, 5, 1) {
		return []string{"SHOW ALL REPLICAS STATUS", "SHOW ALL SLAVES STATUS", "SHOW SLAVE STATUS"}
	}
	return []string{"SHOW ALL SLAVES STATUS", "SHOW SLAVE STATUS", "SHOW ALL REPLICAS STATUS"}
}

func binlogStatusStatements(v serverVersion) []string {
	if v.atLeast(10, 5, 2) {
		return []string{"SHOW BINLOG STATUS", "SHOW MASTER STATUS"}
	}
	return []string{"SHOW MASTER STATUS", "SHOW BINLOG STATUS"}
}

func replicaHostsStatements(v serverVersion) []string {
	if v.atLeast(10, 5, 1) {
		return []string{"SHOW REPLICA HOSTS", "SHOW SLAVE HOSTS"}
	}
	return []string{"SHOW SLAVE HOSTS", "SHOW REPLICA HOSTS"}
}

func variablesStatement() string {
	return `SHOW GLOBAL VARIABLES WHERE Variable_name IN ` +
		`('server_id','read_only','log_bin','gtid_domain_id','gtid_binlog_pos','gtid_current_pos','gtid_slave_pos')`
}

// privilegeFor names what the server wants for each statement. MariaDB split
// REPLICATION CLIENT into narrower privileges in 10.5.2 and gave replica
// status its own in 10.5.9; the old name is still what a server older than
// that checks, and what a grant of it is translated into on a newer one.
func privilegeFor(s section, v serverVersion) string {
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
func privilegeNote(s section) string {
	if s == sectionHosts {
		return " (an administrative privilege, more than monitoring needs — every other part of this answer works without it)"
	}
	return ""
}

// grantScope is what a grant is made ON. Every privilege here is global.
func grantScope(section) string { return "*.*" }

const standaloneDetail = "not replicating from anything, not part of a cluster, and no replica is connected"

func vendorPairs(vars map[string]string) []view.Pair {
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
func gtidOf(row map[string]string) gtidState {
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
