package main

import (
	"strings"
	"testing"
)

func TestEachVersionAsksForTheStatementItsServerKnows(t *testing.T) {
	for _, tc := range []struct {
		version, replica, binlog, hosts string
	}{
		{"11.8.2-MariaDB-ubu2404", "SHOW ALL REPLICAS STATUS", "SHOW BINLOG STATUS", "SHOW REPLICA HOSTS"},
		{"10.11.8-MariaDB-1:10.11.8+maria~ubu2204", "SHOW ALL REPLICAS STATUS", "SHOW BINLOG STATUS", "SHOW REPLICA HOSTS"},
		{"10.5.1-MariaDB", "SHOW ALL REPLICAS STATUS", "SHOW MASTER STATUS", "SHOW REPLICA HOSTS"},
		{"10.4.34-MariaDB", "SHOW ALL SLAVES STATUS", "SHOW MASTER STATUS", "SHOW SLAVE HOSTS"},
		// What VERSION() reads with the replication handshake's version hack on.
		{"5.5.5-10.11.6-MariaDB-log", "SHOW ALL REPLICAS STATUS", "SHOW BINLOG STATUS", "SHOW REPLICA HOSTS"},
		{"", "SHOW ALL REPLICAS STATUS", "SHOW BINLOG STATUS", "SHOW REPLICA HOSTS"},
	} {
		v := parseVersion(tc.version)
		if got := replicaStatusStatements(v)[0]; got != tc.replica {
			t.Errorf("%q: replica status = %q, want %q", tc.version, got, tc.replica)
		}
		if got := binlogStatusStatements(v)[0]; got != tc.binlog {
			t.Errorf("%q: binary log status = %q, want %q", tc.version, got, tc.binlog)
		}
		if got := replicaHostsStatements(v)[0]; got != tc.hosts {
			t.Errorf("%q: replicas = %q, want %q", tc.version, got, tc.hosts)
		}
	}
}

// The plain statement is always the last resort: it is the only one a server
// older than the ALL form answers, and it reads the default connection, which
// is the whole answer for a replica of one source.
func TestEveryStatementListEndsWhereAnOldServerAnswers(t *testing.T) {
	for _, v := range []string{"11.4.2-MariaDB", "10.4.1-MariaDB", ""} {
		list := replicaStatusStatements(parseVersion(v))
		found := false
		for _, s := range list {
			found = found || s == "SHOW SLAVE STATUS" || s == "SHOW ALL SLAVES STATUS"
		}
		if !found {
			t.Errorf("%q: %v has no spelling a pre-10.5 server knows", v, list)
		}
	}
}

// The privilege named in a hint is the one this server's version checks: the
// narrow ones MariaDB split REPLICATION CLIENT into, and the old name on a
// server too old to have them.
func TestTheHintNamesThePrivilegeThisVersionChecks(t *testing.T) {
	for _, tc := range []struct {
		version string
		which   section
		want    string
	}{
		{"11.4.13-MariaDB", sectionReplica, "REPLICA MONITOR"},
		{"11.4.13-MariaDB", sectionBinlog, "BINLOG MONITOR"},
		{"11.4.13-MariaDB", sectionHosts, "REPLICATION MASTER ADMIN"},
		{"10.5.8-MariaDB", sectionReplica, "REPLICATION CLIENT"},
		{"10.5.9-MariaDB", sectionReplica, "REPLICA MONITOR"},
		{"10.4.34-MariaDB", sectionBinlog, "REPLICATION CLIENT"},
		{"10.4.34-MariaDB", sectionHosts, "REPLICATION SLAVE"},
	} {
		if got := privilegeFor(tc.which, parseVersion(tc.version)); got != tc.want {
			t.Errorf("%s %s: privilege = %q, want %q", tc.version, tc.which, got, tc.want)
		}
	}
	if note := privilegeNote(sectionHosts); !strings.Contains(note, "administrative") {
		t.Errorf("the connected replicas' privilege is administrative and the hint must say so: %q", note)
	}
	if privilegeNote(sectionReplica) != "" {
		t.Error("a monitoring privilege needs no warning")
	}
}

func TestPositionsPendingAreCountedWithinEachDomain(t *testing.T) {
	for _, tc := range []struct {
		received, applied, mode, want string
	}{
		{"0-1-2", "0-1-2", "slave_pos", "0"},
		{"0-1-12", "0-1-9", "slave_pos", "3 (domain 0: 3)"},
		// Another server wrote the applied position: a sequence only grows within a
		// domain, so what counts is how far apart the sequences are.
		{"0-2-12", "0-1-9", "slave_pos", "3 (domain 0: 3)"},
		{"0-1-10,1-1-5", "0-1-10,1-1-2", "current_pos", "3 (domain 1: 3)"},
		{"0-1-10,1-1-5", "0-1-10", "current_pos", "5 (domain 1: 5)"},
		{"0-1-5", "0-1-9", "slave_pos", "0"},
		{"", "", "slave_pos", ""},
		{"0-1-x", "0-1-2", "slave_pos", "?"},
	} {
		g := gtidOf(map[string]string{"gtid_io_pos": tc.received, "gtid_replica_pos": tc.applied, "using_gtid": tc.mode})
		if g.pending != tc.want {
			t.Errorf("%q behind %q: pending = %q, want %q", tc.received, tc.applied, g.pending, tc.want)
		}
	}
	if g := gtidOf(map[string]string{"gtid_io_pos": "", "gtid_replica_pos": "", "using_gtid": "No"}); g.mode != "" {
		t.Errorf("a replica following by position with no GTIDs shows a transactions row: %+v", g)
	}
	if g := gtidOf(map[string]string{"gtid_io_pos": "0-1-2", "gtid_replica_pos": "0-1-2", "using_gtid": "No"}); g.mode != "by file position" {
		t.Errorf("mode = %q, want by file position", g.mode)
	}
}
