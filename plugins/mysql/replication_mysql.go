package main

import (
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/view"
)

// What replication.go needs from the server it is talking to and cannot say
// once for both forks: the spelling of each statement, the privilege each one
// takes, and the vocabulary of a replica's transaction positions. plugins/mariadb
// carries the same functions with MariaDB's answers; everything else in
// replication*.go is one source tree twice over, and the drift gate holds it
// to that.

func versionString(raw string) string { return raw }

const capabilitySummary = "Replication and Group Replication state in one place: role, lag, positions, replicas, group"

// clusterNote is what the description adds for a clustering layer this plugin
// reads.
const clusterNote = "\n\nOn a Group Replication member it adds the group as this server sees it: each " +
	"member's state, role and applier queue, and whether this server still has a majority — a member " +
	"that has lost it keeps answering SELECT while the group stops committing writes. The group's tables " +
	"take SELECT on performance_schema, asked only of a server configured for a group, with the grant " +
	"named when it is missing."

// MySQL renamed the replication statements in two steps, and removed the old
// spelling in 8.4: SHOW REPLICA STATUS and SHOW REPLICAS arrived in 8.0.22,
// SHOW BINARY LOG STATUS in 8.2.0. Each list is the spelling this version
// answers to first and the other after it — a server that does not know the
// first is told so with a syntax error, and firstSupported moves on only for
// that.
func replicaStatusStatements(v serverVersion) []string {
	if v.atLeast(8, 0, 22) {
		return []string{"SHOW REPLICA STATUS", "SHOW SLAVE STATUS"}
	}
	return []string{"SHOW SLAVE STATUS", "SHOW REPLICA STATUS"}
}

func binlogStatusStatements(v serverVersion) []string {
	if v.atLeast(8, 2, 0) {
		return []string{"SHOW BINARY LOG STATUS", "SHOW MASTER STATUS"}
	}
	return []string{"SHOW MASTER STATUS", "SHOW BINARY LOG STATUS"}
}

func replicaHostsStatements(v serverVersion) []string {
	if v.atLeast(8, 0, 22) {
		return []string{"SHOW REPLICAS", "SHOW SLAVE HOSTS"}
	}
	return []string{"SHOW SLAVE HOSTS", "SHOW REPLICAS"}
}

// variablesStatement filters on the server so that a name this version does
// not have — super_read_only before 5.7, gtid_mode before 5.6 — is simply
// absent from the answer rather than an error that costs the others.
func variablesStatement() string {
	return `SHOW GLOBAL VARIABLES WHERE Variable_name IN ` +
		`('server_id','server_uuid','read_only','super_read_only','log_bin','gtid_mode','gtid_executed')`
}

// privilegeFor names what the server wants for each statement. SHOW REPLICA
// STATUS and the binary log status take REPLICATION CLIENT; the connected
// replicas are a different, older privilege, REPLICATION SLAVE, which is the
// one a replica's own account holds — so an account that may monitor and not
// see its replicas is common and is what this names.
func privilegeFor(s section, _ serverVersion) string {
	switch s {
	case sectionHosts:
		return "REPLICATION SLAVE"
	case sectionCluster:
		return "SELECT"
	}
	return "REPLICATION CLIENT"
}

func privilegeNote(section) string { return "" }

// grantScope is what a grant is made ON. The replication privileges are
// global; the group's tables are read with a SELECT on the schema they are in.
func grantScope(s section) string {
	if s == sectionCluster {
		return "performance_schema.*"
	}
	return "*.*"
}

const standaloneDetail = "not replicating from anything, not in a replication group, and no replica is connected"

func vendorPairs(vars map[string]string) []view.Pair {
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
func gtidOf(row map[string]string) gtidState {
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
