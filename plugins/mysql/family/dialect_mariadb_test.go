package family

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

func TestEachMariaDBVersionAsksForTheStatementItsServerKnows(t *testing.T) {
	useEngine(t, MariaDB)
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
		if got := MariaDB.replicaStatusStatements(v)[0]; got != tc.replica {
			t.Errorf("%q: replica status = %q, want %q", tc.version, got, tc.replica)
		}
		if got := MariaDB.binlogStatusStatements(v)[0]; got != tc.binlog {
			t.Errorf("%q: binary log status = %q, want %q", tc.version, got, tc.binlog)
		}
		if got := MariaDB.replicaHostsStatements(v)[0]; got != tc.hosts {
			t.Errorf("%q: replicas = %q, want %q", tc.version, got, tc.hosts)
		}
	}
}

// The plain statement is always the last resort: it is the only one a server
// older than the ALL form answers, and it reads the default connection, which
// is the whole answer for a replica of one source.
func TestEveryStatementListEndsWhereAnOldServerAnswers(t *testing.T) {
	useEngine(t, MariaDB)
	for _, v := range []string{"11.4.2-MariaDB", "10.4.1-MariaDB", ""} {
		list := MariaDB.replicaStatusStatements(parseVersion(v))
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
	useEngine(t, MariaDB)
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
		if got := MariaDB.privilegeFor(tc.which, parseVersion(tc.version)); got != tc.want {
			t.Errorf("%s %s: privilege = %q, want %q", tc.version, tc.which, got, tc.want)
		}
	}
	if note := MariaDB.privilegeNote(sectionHosts); !strings.Contains(note, "administrative") {
		t.Errorf("the connected replicas' privilege is administrative and the hint must say so: %q", note)
	}
	if MariaDB.privilegeNote(sectionReplica) != "" {
		t.Error("a monitoring privilege needs no warning")
	}
}

func TestPositionsPendingAreCountedWithinEachDomain(t *testing.T) {
	useEngine(t, MariaDB)
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
		g := MariaDB.gtidOf(map[string]string{"gtid_io_pos": tc.received, "gtid_replica_pos": tc.applied, "using_gtid": tc.mode})
		if g.pending != tc.want {
			t.Errorf("%q behind %q: pending = %q, want %q", tc.received, tc.applied, g.pending, tc.want)
		}
	}
	if g := MariaDB.gtidOf(map[string]string{"gtid_io_pos": "", "gtid_replica_pos": "", "using_gtid": "No"}); g.mode != "" {
		t.Errorf("a replica following by position with no GTIDs shows a transactions row: %+v", g)
	}
	if g := MariaDB.gtidOf(map[string]string{"gtid_io_pos": "0-1-2", "gtid_replica_pos": "0-1-2", "using_gtid": "No"}); g.mode != "by file position" {
		t.Errorf("mode = %q, want by file position", g.mode)
	}
}

// **The absences that make MariaDB's dump not MySQL's.** Both are MySQL 8
// spellings MariaDB's client does not have, and a flag it does not have is an
// immediate "unknown option" rather than a lesser dump — so MySQL's flags
// reaching this engine would break every dump, and this is what would catch it.
func TestMariaDBsDumpLeavesOutMySQLsFlags(t *testing.T) {
	useEngine(t, MariaDB)
	args := strings.Join(dumpArgs(req(t, "mariadb.dump", map[string]any{
		"database": "app", "out": "x.sql",
	})), " ")
	for _, mysqlOnly := range []string{"--no-tablespaces", "--set-gtid-purged"} {
		if strings.Contains(args, mysqlOnly) {
			t.Errorf("dumpArgs passes %q, which MariaDB's client does not have: %q", mysqlOnly, args)
		}
	}
	for _, want := range []string{"--single-transaction", "--protocol=TCP", "--routines", "--events", "--triggers"} {
		if !strings.Contains(args, want) {
			t.Errorf("dumpArgs missing %q in %q", want, args)
		}
	}
	if !strings.HasPrefix(args, "--no-defaults") {
		t.Errorf("--no-defaults is not first, where the client requires it: %q", args)
	}
}

// The tls input speaks go-sql-driver's vocabulary everywhere in this plugin;
// MariaDB's child speaks the --ssl family. **Not --ssl-mode**, which is the
// MySQL 8 spelling — pinned here because it is the single likeliest thing to
// cross from MySQL's dialect, and it would fail on every MariaDB client
// rather than degrading.
func TestMariaDBTLSModesMapByMeaning(t *testing.T) {
	useEngine(t, MariaDB)
	for tls, want := range map[string][]string{
		"false":       {"--skip-ssl"},
		"true":        {"--ssl", "--ssl-verify-server-cert"},
		"skip-verify": {"--ssl"},
		"verify-ca":   {"--ssl", "--ssl-ca=/etc/mysql/ca.pem", "--skip-ssl-verify-server-cert"},
	} {
		values := map[string]any{"database": "app", "tls": tls}
		if tls == "verify-ca" {
			values["ca-file"] = "/etc/mysql/ca.pem"
		}
		args := dumpArgs(req(t, "mariadb.dump", values))
		joined := strings.Join(args, " ")
		for _, w := range want {
			if !slices.Contains(args, w) {
				t.Errorf("tls=%s: missing %q in %q", tls, w, joined)
			}
		}
		if strings.Contains(joined, "--ssl-mode") {
			t.Errorf("tls=%s: passes MySQL's --ssl-mode, which this client does not have: %q", tls, joined)
		}
	}
	// skip-verify encrypts without verifying, so the verify flag must be the
	// difference between it and "true" rather than something both carry.
	skip := dumpArgs(req(t, "mariadb.dump", map[string]any{"database": "app", "tls": "skip-verify"}))
	if slices.Contains(skip, "--ssl-verify-server-cert") {
		t.Errorf("tls=skip-verify still verifies the server: %v", skip)
	}
	preferred := strings.Join(dumpArgs(req(t, "mariadb.dump", map[string]any{
		"database": "app", "tls": "preferred",
	})), " ")
	if strings.Contains(preferred, "ssl") {
		t.Errorf("tls=preferred passes a flag where the client default is the meaning: %q", preferred)
	}
}

// A MySQL client answering to the mysqldump name refuses MariaDB's --ssl
// family on its first flag, and the way out is the other plugin.
func TestMariaDBsToolSkewNamesTheMySQLPlugin(t *testing.T) {
	useEngine(t, MariaDB)
	r := req(t, "mariadb.dump", map[string]any{"database": "app"})
	verr := classifyDump(errors.New("exit status 7"), "mysqldump: unknown variable 'ssl-verify-server-cert'", r)
	if verr.Code != "mariadb.dump.toolskew" || !strings.Contains(verr.Hint, "the mysql plugin drives mysqldump") {
		t.Errorf("tool skew = %s %q, want mariadb.dump.toolskew naming the mysql plugin", verr.Code, verr.Hint)
	}
}

// **The Galera fact MySQL's dump has no reason to state.** A node that has
// lost quorum still accepts connections and still answers queries — from its
// own side of a partition — so a dump taken there backs up that side, and
// the file says nothing about it afterwards. The receipt has to, while
// somebody is still looking at it.
func TestTheReceiptNamesALostQuorum(t *testing.T) {
	useEngine(t, MariaDB)
	split := source{version: "11.4.2-MariaDB", note: galeraNote("non-Primary", plugin.SurfaceCLI)}.describe()
	if !strings.Contains(split, "non-Primary") || !strings.Contains(split, "lost quorum") {
		t.Errorf("describe() = %q, want it to say the node is not in the primary component", split)
	}
	// Where the whole picture is, as a capability this plugin has: the line
	// once named `rta mariadb cluster`, a command rta does not have.
	if !strings.Contains(split, "`rta mariadb replication status` has the whole picture") {
		t.Errorf("describe() = %q, want it to name where the whole picture is", split)
	}
	if tui := galeraNote("non-Primary", plugin.SurfaceTUI); !strings.Contains(tui, "`mariadb.replication.status` has the whole picture") {
		t.Errorf("the note in the TUI = %q, want it to name the capability by its ID", tui)
	}
	if healthy := galeraNote("Primary", plugin.SurfaceCLI); strings.Contains(healthy, "lost quorum") {
		t.Errorf("the note = %q, want no warning for a node holding quorum", healthy)
	}
	// A standalone MariaDB is not a broken cluster — the same reading
	// galera.go makes of an absent wsrep provider.
	if standalone := galeraNote("", plugin.SurfaceCLI); standalone != "" {
		t.Errorf("the note = %q, want nothing about Galera on a standalone server", standalone)
	}
}
