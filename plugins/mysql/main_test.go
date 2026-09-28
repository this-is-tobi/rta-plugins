package main

import (
	stdnet "net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/view"
)

// req builds a resolved request the way the host would — defaults applied,
// caller values on top — so these test the values a handler actually sees
// rather than a hand-made map.
func req(t *testing.T, capID string, values map[string]any) plugin.Request {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == capID {
			return plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false)
		}
	}
	t.Fatalf("no capability %q", capID)
	return plugin.Request{}
}

// sdktest is the definition of "a correct plugin", and mysql gets no
// exemption from it — the spelling rule among the others, which holds what
// every capability declares to the SDK's speller.
//
// Every read here is NoPreview, so the suite runs none of them. What it
// drives is the four writes, under --dry-run, and two of them take an input
// no default supplies: without one the suite fails rather than report a
// pass for a capability it never ran.
func TestConformance(t *testing.T) {
	sdktest.Check(t, Plugin(), sdktest.WithInputs(conformanceInputs))
}

// conformanceInputs points every write at a port nothing listens on, so none
// of them reaches a server. mysql.query and mysql.activity have no dry run
// of their own — they change nothing, and read under --dry-run as they do
// without — so left at the default host they would read whatever server the
// machine running the tests has. A dump's or a restore's dry run that
// stopped being dry fails as a refused connection.
//
// The dump's file and the restore's fixture are inside dir, the directory
// the suite watches: a dry run that wrote the one or touched the other is
// caught where it happened. A real SQL fixture, because the restore looks
// at the file before its dry run says anything, and refuses one that is not
// there or is empty. Which way either dry run answers depends on whether
// the client tools are on $PATH — a description of the call, or a refusal
// naming the missing tool — and both leave dir as it was.
func conformanceInputs(dir string) map[string]map[string]any {
	conn := func(m map[string]any) map[string]any {
		m["host"], m["port"] = "127.0.0.1", 1
		m["user"], m["database"] = "conformance", "conformance"
		return m
	}
	fixture := filepath.Join(dir, "conformance.sql")
	_ = os.WriteFile(fixture, []byte("select 1;\n"), 0o600)
	return map[string]map[string]any{
		"mysql.query":    conn(map[string]any{"sql": "select 1"}),
		"mysql.activity": conn(map[string]any{}),
		"mysql.dump":     conn(map[string]any{"out": filepath.Join(dir, "mysql.sql")}),
		"mysql.restore":  conn(map[string]any{"file": fixture}),
	}
}

// A password containing '@' or '/' once had to survive a DSN, where hand
// assembly ran it into the next component and the failure was an
// authentication error naming nothing. The driver is handed its Config now,
// with no string between, and this holds it to that: the password, and the
// address beside it, arrive exactly as given.
func TestAwkwardPasswordsReachTheDriverAsGiven(t *testing.T) {
	for _, pw := range []string{"p@ss/word", "with'quote'", `back\slash`, "sp ace", "a:b@c/d?e"} {
		cfg, verr := driverConfig(req(t, "mysql.status", map[string]any{"password": pw, "host": "db.internal"}))
		if verr != nil {
			t.Fatalf("password %q: %v", pw, verr)
		}
		if cfg.Passwd != pw {
			t.Errorf("password %q reached the driver as %q", pw, cfg.Passwd)
		}
		if cfg.Addr != "db.internal:3306" {
			t.Errorf("password %q corrupted the address: %q", pw, cfg.Addr)
		}
	}
}

// The port belongs to the address, not to a separate field the driver would
// ignore. Getting this wrong reaches the default port against the right host,
// which looks like the server being down.
func TestTheAddressCarriesHostAndPort(t *testing.T) {
	cfg, verr := driverConfig(req(t, "mysql.status", map[string]any{"host": "10.0.0.5", "port": 3307}))
	if verr != nil {
		t.Fatal(verr)
	}
	if cfg.Addr != "10.0.0.5:3307" {
		t.Errorf("addr = %q, want 10.0.0.5:3307", cfg.Addr)
	}
	if cfg.Net != "tcp" {
		t.Errorf("net = %q, want tcp", cfg.Net)
	}
}

// Every capability here reaches off the box, so cap must have forced
// NoPreview on all of them. That is what keeps the automatic dashboard from
// deciding, on its own, that somebody else's production database is worth
// polling every few seconds.
func TestEveryCapabilityIsNoPreview(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if !c.NoPreview {
			t.Errorf("%s: NoPreview = false, want true — every capability here reaches off the box", c.ID)
		}
	}
}

// The safety split is the whole argument of this plugin, so it is pinned
// rather than left to a code review: the read tier describes the database and
// returns nothing anybody stored in it, and the two capabilities that hand
// back stored values are writes.
//
// The table fails in both directions. A new capability that is not accounted
// for fails, and an entry naming a capability that no longer exists fails —
// which is what stops this from rotting into a list of things that used to be
// true.
func TestSafetyClassesMatchWhatEachCapabilityDiscloses(t *testing.T) {
	want := map[string]plugin.Safety{
		// Read: everything here is something somebody declared, not something
		// somebody entered.
		"mysql.status":        plugin.Read,
		"mysql.overview":      plugin.Read,
		"mysql.database.list": plugin.Read,
		"mysql.table.list":    plugin.Read,
		"mysql.schema":        plugin.Read,
		// Write: both return values stored by somebody. query returns rows;
		// activity returns the statement text of everything running, and a
		// WHERE clause is a place a value hides.
		"mysql.query":    plugin.Write,
		"mysql.activity": plugin.Write,
		// The dump/restore pair refuses MCP outright instead of taking a
		// grant (a whole database has no blast radius a grant could name);
		// the restore is Destructive besides — mysqldump's own DROP TABLE
		// statements run first, and the class buys the --yes gate.
		"mysql.dump":    plugin.Write,
		"mysql.restore": plugin.Destructive,
	}
	seen := map[string]bool{}
	for _, c := range Plugin().Capabilities {
		seen[c.ID] = true
		expect, ok := want[c.ID]
		if !ok {
			t.Errorf("%s: not accounted for in this test's table", c.ID)
			continue
		}
		if c.Safety != expect {
			t.Errorf("%s: Safety = %s, want %s", c.ID, c.Safety, expect)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("%s: declared in this test's table but not in Plugin()", id)
		}
	}
}

// Every capability must be reachable and describable. A capability with no
// Run ships as dead weight in the MCP schema; one with no Description is one
// `rta explain` cannot answer for.
func TestEveryCapabilityIsRunnableAndDescribed(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.Run == nil {
			t.Errorf("%s: no Run", c.ID)
		}
		if strings.TrimSpace(c.Description) == "" {
			t.Errorf("%s: no Description — `rta explain` has nothing to print", c.ID)
		}
		if !strings.HasPrefix(c.ID, Plugin().Name+".") {
			t.Errorf("%s: capability IDs must be namespaced by %q", c.ID, Plugin().Name)
		}
	}
}

// The fork is only stated in the version string, and somebody debugging needs
// to know which one answered — the two have diverged enough that the answer
// changes what to try next.
func TestFlavourIsReadFromTheVersionString(t *testing.T) {
	cases := map[string]string{
		"8.4.0":                            "MySQL",
		"8.0.36-0ubuntu0.22.04.1":          "MySQL",
		"11.4.2-MariaDB":                   "MariaDB",
		"10.11.8-MariaDB-1:10.11.8+maria~": "MariaDB",
		"8.0.35-27":                        "MySQL",
		"8.0.35-27.1.Percona":              "Percona",
	}
	for version, want := range cases {
		if got := flavourOf(version); got != want {
			t.Errorf("flavourOf(%q) = %q, want %q", version, got, want)
		}
	}
}

// NULL and the string "NULL" are indistinguishable once printed, so a NULL
// renders as an empty cell. Anything else makes somebody wonder whether a
// column literally holds that word.
func TestNullRendersAsAnEmptyCell(t *testing.T) {
	if got := cell(nil); got != "" {
		t.Errorf("cell(nil) = %q, want an empty cell", got)
	}
	if got := cell([]byte("NULL")); got != "NULL" {
		t.Errorf("a literal NULL string must survive: got %q", got)
	}
}

// The driver hands back []byte for most string-ish columns. A value that
// reached the table as "[52 50]" means this was skipped.
func TestBytesColumnsBecomeText(t *testing.T) {
	if got := cell([]byte("hello")); got != "hello" {
		t.Errorf("cell([]byte) = %q, want hello", got)
	}
}

// INFORMATION_SCHEMA size expressions come back untyped, and the type varies
// with the server and the driver's settings. A size column nobody can read at
// a glance is a column that gets piped into another tool instead of read.
func TestSizesRenderAsBytesWhateverTypeTheyArriveAs(t *testing.T) {
	for _, v := range []any{int64(1048576), uint64(1048576), []byte("1048576"), float64(1048576)} {
		if got := bytesCell(v); got != "1.0 MiB" {
			t.Errorf("bytesCell(%T) = %q, want 1.0 MiB", v, got)
		}
	}
	if got := bytesCell(nil); got != "-" {
		t.Errorf("bytesCell(nil) = %q, want -", got)
	}
}

// A statement written across twelve lines in application source arrives with
// all of them, and would turn one table row into a page.
func TestRunningStatementsAreCollapsedAndBounded(t *testing.T) {
	got := truncateStatement("SELECT *\n  FROM orders\n WHERE id = 1")
	if got != "SELECT * FROM orders WHERE id = 1" {
		t.Errorf("newlines survived: %q", got)
	}
	long := truncateStatement(strings.Repeat("x", statementWidth+50))
	if len([]rune(long)) != statementWidth+1 {
		t.Errorf("length = %d, want %d plus the ellipsis", len([]rune(long)), statementWidth)
	}
	if !strings.HasSuffix(long, "…") {
		t.Errorf("truncation is not marked: %q", long)
	}
}

// Guessing here would silently describe `mysql` or `information_schema`,
// which is a confidently wrong answer to a question nobody asked.
func TestSchemaRefusesRatherThanGuessing(t *testing.T) {
	_, verr := schemaOf(req(t, "mysql.schema", map[string]any{}))
	if verr == nil {
		t.Fatal("no database named anywhere, and it did not refuse")
	}
	if !strings.Contains(verr.Hint, "config") {
		t.Errorf("refusal does not say how to fix it: %q", verr.Hint)
	}

	// The connection's own database is the fallback, so the common case — one
	// database in the config — needs no argument.
	got, verr := schemaOf(req(t, "mysql.schema", map[string]any{"database": "app"}))
	if verr != nil {
		t.Fatalf("connection database was not used as the fallback: %v", verr)
	}
	if got != "app" {
		t.Errorf("schema = %q, want app", got)
	}

	// An explicit argument wins over the connection's own.
	got, _ = schemaOf(req(t, "mysql.schema", map[string]any{"database": "app", "schema": "other"}))
	if got != "other" {
		t.Errorf("schema = %q, want the explicit argument to win", got)
	}
}

// The key marker comes first because it is what somebody scanning a schema is
// looking for, and the constraint is stated rather than its opposite because
// nullable is SQL's default and "not null" is the news.
func TestColumnDetailStatesTypeKeyAndConstraint(t *testing.T) {
	got := columnDetail(column{name: "id", dataType: "bigint unsigned", key: "PRI", extra: "auto_increment"})
	for _, want := range []string{"bigint unsigned", "primary key", "not null", "auto_increment"} {
		if !strings.Contains(got, want) {
			t.Errorf("columnDetail = %q, missing %q", got, want)
		}
	}
	if !strings.HasPrefix(got, "bigint unsigned") {
		t.Errorf("type must come first: %q", got)
	}

	nullable := columnDetail(column{name: "note", dataType: "text", nullable: true})
	if strings.Contains(nullable, "not null") {
		t.Errorf("a nullable column claimed not null: %q", nullable)
	}
}

// A view is the distinction worth keeping in the Type column; "BASE TABLE" is
// the standard's word and nobody else's.
func TestClassifyReturnsAlreadyClassifiedErrorsUnchanged(t *testing.T) {
	original := view.Errorf("mysql.something.specific", "a precise message").WithHint("a precise hint")
	got := classify(original, req(t, "mysql.status", map[string]any{}))
	if got.Code != "mysql.something.specific" {
		t.Errorf("a classified error was re-wrapped as %q — the specific answer is now buried", got.Code)
	}
}

// Every branch here is a sentence somebody has stared at without knowing what
// to do next, so each must produce a distinct code and a hint that names the
// next step. Switching on the error number rather than the message is what
// keeps this working across versions and locales.
func TestConnectionFailuresAreClassifiedByNumber(t *testing.T) {
	cases := []struct {
		number uint16
		code   string
	}{
		{1045, "mysql.auth.failed"},
		{1044, "mysql.database.denied"},
		{1049, "mysql.database.notfound"},
		{1146, "mysql.table.notfound"},
		{1142, "mysql.denied"},
		{1130, "mysql.host.denied"},
		{1290, "mysql.readonly"},
	}
	r := req(t, "mysql.status", map[string]any{"host": "db.internal", "database": "app"})
	for _, c := range cases {
		got := classify(&mysql.MySQLError{Number: c.number, Message: "server text"}, r)
		if got.Code != c.code {
			t.Errorf("error %d classified as %q, want %q", c.number, got.Code, c.code)
		}
		if strings.TrimSpace(got.Hint) == "" {
			t.Errorf("error %d has no hint — the code alone does not say what to do next", c.number)
		}
	}

	// An unrecognised number must still carry its own number and the server's
	// message, rather than collapsing into a generic failure that names nothing.
	other := classify(&mysql.MySQLError{Number: 1064, Message: "You have an error in your SQL syntax"}, r)
	if !strings.Contains(other.Message, "1064") || !strings.Contains(other.Message, "syntax") {
		t.Errorf("unrecognised error lost its detail: %q", other.Message)
	}
}

// A name DNS cannot resolve is that, and not a port nothing listens on. The
// driver's dial wraps the lookup's failure in a *net.OpError, and the check
// for a refused dial, read first, answered "nothing is listening on
// nonexistent.invalid:3306" and sent somebody to the server and its port when
// the name was the problem.
func TestANameDNSCannotResolveIsNotReadAsNothingListening(t *testing.T) {
	err := &stdnet.OpError{Op: "dial", Net: "tcp",
		Err: &stdnet.DNSError{Err: "no such host", Name: "db.internal", IsNotFound: true}}
	verr := classify(err, req(t, "mysql.overview", map[string]any{"host": "db.internal"}))
	if verr.Code != "mysql.host.unknown" {
		t.Errorf("code = %s, want mysql.host.unknown: %s", verr.Code, verr.Message)
	}
}
