package family

import (
	"strings"
	"testing"
)

func TestEachMySQLVersionAsksForTheStatementItsServerKnows(t *testing.T) {
	for _, tc := range []struct {
		version, replica, binlog, hosts string
	}{
		{"8.4.11", "SHOW REPLICA STATUS", "SHOW BINARY LOG STATUS", "SHOW REPLICAS"},
		{"9.5.0", "SHOW REPLICA STATUS", "SHOW BINARY LOG STATUS", "SHOW REPLICAS"},
		{"8.0.46", "SHOW REPLICA STATUS", "SHOW MASTER STATUS", "SHOW REPLICAS"},
		{"8.0.21", "SHOW SLAVE STATUS", "SHOW MASTER STATUS", "SHOW SLAVE HOSTS"},
		{"5.7.44-log", "SHOW SLAVE STATUS", "SHOW MASTER STATUS", "SHOW SLAVE HOSTS"},
		// A version nobody could read gets the modern spelling first, and the
		// refusal it earns on an old server moves on to the other.
		{"", "SHOW REPLICA STATUS", "SHOW BINARY LOG STATUS", "SHOW REPLICAS"},
	} {
		v := parseVersion(tc.version)
		if got := MySQL.replicaStatusStatements(v)[0]; got != tc.replica {
			t.Errorf("%q: replica status = %q, want %q", tc.version, got, tc.replica)
		}
		if got := MySQL.binlogStatusStatements(v)[0]; got != tc.binlog {
			t.Errorf("%q: binary log status = %q, want %q", tc.version, got, tc.binlog)
		}
		if got := MySQL.replicaHostsStatements(v)[0]; got != tc.hosts {
			t.Errorf("%q: replicas = %q, want %q", tc.version, got, tc.hosts)
		}
	}
}

func TestTransactionSetsSubtractAcrossIdentitiesAndTags(t *testing.T) {
	for _, tc := range []struct {
		have, other, want string
		count             int64
	}{
		{"u:1-10", "u:1-10", "", 0},
		{"u:1-10", "u:1-7", "u:8-10", 3},
		{"u:1-10", "u:1-3:6-8", "u:4-5:9-10", 4},
		{"a:1-5,b:1-5", "a:1-5", "b:1-5", 5},
		{"u:1-5", "", "u:1-5", 5},
		{"u:5", "u:1-4", "u:5", 1},
		// Two sets that differ only in tag are different transactions.
		{"u:tag:1-5", "u:1-5", "u:tag:1-5", 5},
		{"U:1-5", "u:1-5", "", 0},
	} {
		have, ok1 := parseGTIDSet(tc.have)
		other, ok2 := parseGTIDSet(tc.other)
		if !ok1 || !ok2 {
			t.Fatalf("%q / %q did not parse", tc.have, tc.other)
		}
		got := have.subtract(other)
		if got.String() != tc.want || got.count() != tc.count {
			t.Errorf("%q minus %q = %q (%d), want %q (%d)", tc.have, tc.other, got.String(), got.count(), tc.want, tc.count)
		}
	}
	if _, ok := parseGTIDSet(":1-5"); ok {
		t.Error("a set with no identifier parsed")
	}
}

// The tls input speaks go-sql-driver's vocabulary everywhere in this plugin;
// the child speaks --ssl-mode. The mapping is by meaning, and "preferred"
// passes nothing because it is the client's own default.
func TestMySQLTLSModesMapByMeaning(t *testing.T) {
	for tls, want := range map[string]string{
		"false":       "--ssl-mode=DISABLED",
		"true":        "--ssl-mode=VERIFY_IDENTITY",
		"skip-verify": "--ssl-mode=REQUIRED",
		"verify-ca":   "--ssl-mode=VERIFY_CA --ssl-ca=/etc/mysql/ca.pem",
	} {
		values := map[string]any{"database": "app", "tls": tls}
		if tls == "verify-ca" {
			values["ca-file"] = "/etc/mysql/ca.pem"
		}
		args := strings.Join(dumpArgs(req(t, "mysql.dump", values)), " ")
		if !strings.Contains(args, want) {
			t.Errorf("tls=%s: missing %q in %q", tls, want, args)
		}
	}
	preferred := strings.Join(dumpArgs(req(t, "mysql.dump", map[string]any{
		"database": "app", "tls": "preferred",
	})), " ")
	if strings.Contains(preferred, "--ssl-mode") {
		t.Errorf("tls=preferred passes a flag where the client default is the meaning: %q", preferred)
	}
}
