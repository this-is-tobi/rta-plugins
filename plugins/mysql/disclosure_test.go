package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The read/write split in this plugin is enforced in two places, and both are
// the kind of thing that keeps working right up until somebody simplifies it.
// Neither was covered until probing found that removing the enforcement broke
// nothing — which is the only way to learn a test suite has a hole in it.

// fakeDB opens the fake driver as a *sql.DB, for the handlers that take one
// rather than a *sql.Rows.
func fakeDB(t *testing.T, columns []string, rows [][]driver.Value) *sql.DB {
	t.Helper()
	fakeAnswer.columns, fakeAnswer.rows, fakeAnswer.err = columns, rows, nil
	fakeRoutes = nil
	db, err := sql.Open("mysql-fake", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// processlistColumns and one row of it, in the order activityView scans.
var processlistColumns = []string{"ID", "USER", "HOST", "DB", "COMMAND", "TIME", "STATE", "INFO"}

func processlistRow(statement string) []driver.Value {
	return []driver.Value{
		int64(42), []byte("app"), []byte("10.0.0.9:51000"), []byte("shop"),
		[]byte("Query"), int64(12), []byte("Sending data"), []byte(statement),
	}
}

// **The read tier must not be able to produce the statement column.**
//
// mysql.overview --detail and mysql.activity share one function, and
// withStatements is the entire difference between them. If that flag stops
// being honoured, a read-tier call starts returning the text of every
// statement running on the server — which is where the literals are, and the
// reason mysql.activity is a write in the first place.
func TestTheReadTierNeverReturnsStatementText(t *testing.T) {
	secret := "SELECT * FROM patients WHERE mrn = 'A-12345'"
	db := fakeDB(t, processlistColumns, [][]driver.Value{processlistRow(secret)})
	r := req(t, "mysql.overview", map[string]any{})

	v, err := activityView(context.Background(), db, r, false)
	if err != nil {
		t.Fatal(err)
	}
	tbl, ok := v.(view.Table)
	if !ok {
		t.Fatalf("want Table, got %s", view.TypeOf(v))
	}

	for _, c := range tbl.Columns {
		if strings.EqualFold(c.Name, "statement") {
			t.Fatalf("the read tier produced a %q column", c.Name)
		}
	}
	// Belt and braces: the literal must not appear in any cell, whatever the
	// column ended up being called.
	for _, row := range tbl.Rows {
		for _, cell := range row {
			if strings.Contains(cell, "A-12345") {
				t.Fatalf("a statement literal reached the read tier: %q", cell)
			}
		}
	}
}

// The other half of the same claim: mysql.activity, which is a write, does
// return it. A test that only checked the read tier would pass just as well
// against a version that never returns statements at all.
func TestTheWriteTierDoesReturnStatementText(t *testing.T) {
	secret := "SELECT * FROM patients WHERE mrn = 'A-12345'"
	db := fakeDB(t, processlistColumns, [][]driver.Value{processlistRow(secret)})
	r := req(t, "mysql.activity", map[string]any{})

	v, err := activityView(context.Background(), db, r, true)
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)

	found := false
	for _, c := range tbl.Columns {
		if c.Name == "Statement" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mysql.activity has no Statement column: %+v", tbl.Columns)
	}
	if !strings.Contains(strings.Join(tbl.Rows[0], " "), "A-12345") {
		t.Errorf("mysql.activity did not return the statement text: %v", tbl.Rows[0])
	}

	// The statement column is bounded at its call site, not only inside the
	// helper. A statement written across twelve lines in application source
	// arrives with all of them, and would turn one table row into a page.
	long := "SELECT " + strings.Repeat("col_name, ", 60) + "1 FROM t"
	db = fakeDB(t, processlistColumns, [][]driver.Value{processlistRow(long)})
	v, err = activityView(context.Background(), db, r, true)
	if err != nil {
		t.Fatal(err)
	}
	last := v.(view.Table).Rows[0]
	if got := last[len(last)-1]; len([]rune(got)) > statementWidth+1 {
		t.Errorf("statement column is %d runes — the bound is not applied where it is used", len([]rune(got)))
	}
}

// **The transaction must be opened READ ONLY.**
//
// That is the entire enforcement for mysql.query: rta does not inspect the SQL
// and should not — a blocklist against a language with this many ways to spell
// a write is a promise nobody can keep. The server refusing writes is the real
// mechanism, and it only exists if the option is actually set.
func TestQueryRunsInsideAReadOnlyTransaction(t *testing.T) {
	lastTxOptions = driver.TxOptions{}
	db := fakeDB(t, []string{"n"}, [][]driver.Value{{int64(1)}})

	tx, err := db.BeginTx(context.Background(),
		&sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if !lastTxOptions.ReadOnly {
		t.Fatal("the fake driver does not record ReadOnly — the assertion below would be vacuous")
	}

	// Now the real path, which is what is actually being pinned.
	lastTxOptions = driver.TxOptions{}
	fakeAnswer.columns, fakeAnswer.rows = []string{"n"}, [][]driver.Value{{int64(1)}}
	if _, err := queryView(context.Background(), db, req(t, "mysql.query", map[string]any{"sql": "select 1"})); err != nil {
		t.Fatal(err)
	}
	if !lastTxOptions.ReadOnly {
		t.Error("mysql.query opened a read-write transaction — the server will no longer refuse writes")
	}
}

// route is one entry of fakeRoutes, spelled out so a test can build them.
func route(match string, columns []string, rows [][]driver.Value) struct {
	match  string
	result fakeResult
} {
	return struct {
		match  string
		result fakeResult
	}{match: match, result: fakeResult{columns: columns, rows: rows}}
}

// **INFORMATION_SCHEMA.PROCESSLIST is silently narrowed to this connection's
// own thread when the account lacks PROCESS**, which an application
// credential handed to rta generally does.
//
// So `mysql activity` on a server with fifty stuck sessions returned one
// row — this connection's own — with a Total that looked authoritative and
// no error anywhere. An SRE reading it concludes nothing else is running,
// on the screen they opened precisely because something is stuck.
//
// Threads_connected is a global status variable any account may read and it
// counts every connection the server holds, so the gap between it and what
// the listing shows is measured rather than inferred from SHOW GRANTS —
// which roles make unreliable.
func TestActivitySaysWhenSessionsAreHiddenFromThisAccount(t *testing.T) {
	db := fakeDB(t, processlistColumns, [][]driver.Value{processlistRow("SELECT 1")})
	fakeRoutes = []struct {
		match  string
		result fakeResult
	}{
		route("count(*)", []string{"n"}, [][]driver.Value{{int64(1)}}),
		route("Threads_connected", []string{"Variable_name", "Value"},
			[][]driver.Value{{[]byte("Threads_connected"), []byte("12")}}),
	}
	t.Cleanup(func() { fakeRoutes = nil })

	v, err := activityView(context.Background(), db, req(t, "mysql.activity", map[string]any{}), true)
	if err != nil {
		t.Fatal(err)
	}
	tbl, ok := v.(view.Table)
	if !ok {
		t.Fatalf("want Table, got %s", view.TypeOf(v))
	}
	if len(tbl.Warnings) == 0 {
		t.Fatalf("eleven sessions this account cannot see left no trace: %+v", tbl.Rows)
	}
	w := tbl.Warnings[0]
	if w.Code != "mysql.activity.partial" {
		t.Errorf("code = %q, want mysql.activity.partial", w.Code)
	}
	if !strings.Contains(w.Hint, "PROCESS") {
		t.Errorf("hint = %q, want it to name the privilege that would fix it", w.Hint)
	}
}

// And an account that can see the whole server says nothing extra, or the
// caveat stops being worth reading.
func TestActivityIsQuietWhenNothingIsHidden(t *testing.T) {
	db := fakeDB(t, processlistColumns, [][]driver.Value{processlistRow("SELECT 1")})
	fakeRoutes = []struct {
		match  string
		result fakeResult
	}{
		route("count(*)", []string{"n"}, [][]driver.Value{{int64(12)}}),
		route("Threads_connected", []string{"Variable_name", "Value"},
			[][]driver.Value{{[]byte("Threads_connected"), []byte("12")}}),
	}
	t.Cleanup(func() { fakeRoutes = nil })

	v, err := activityView(context.Background(), db, req(t, "mysql.activity", map[string]any{}), true)
	if err != nil {
		t.Fatal(err)
	}
	if w := v.(view.Table).Warnings; len(w) != 0 {
		t.Errorf("an account seeing every session warned anyway: %+v", w)
	}
}

// grantRoute answers SHOW GRANTS with the lines given.
func grantRoute(lines ...string) struct {
	match  string
	result fakeResult
} {
	rows := make([][]driver.Value, 0, len(lines))
	for _, l := range lines {
		rows = append(rows, []driver.Value{[]byte(l)})
	}
	return route("SHOW GRANTS", []string{"Grants for me@%"}, rows)
}

// **INFORMATION_SCHEMA lists only the tables this account holds some
// privilege on**, and MySQL offers no way to count what it filtered out —
// SHOW TABLES is filtered identically. A reporting account with SELECT on
// six tables of twenty got exactly those six back, with a Total, no error
// and no hint: "this database has 6 tables", to an agent sent to inventory
// the schema.
//
// The gap cannot be measured, so the question is asked from the other side:
// does this account hold a privilege wide enough that nothing can be hidden?
func TestTableListSaysWhenPerTableGrantsMayHideTables(t *testing.T) {
	db := fakeDB(t, []string{"TABLE_NAME", "TABLE_TYPE", "ENGINE", "TABLE_ROWS", "SIZE"},
		[][]driver.Value{{[]byte("orders"), []byte("BASE TABLE"), []byte("InnoDB"), int64(12), int64(4096)}})
	fakeRoutes = []struct {
		match  string
		result fakeResult
	}{
		grantRoute("GRANT USAGE ON *.* TO `me`@`%`", "GRANT SELECT ON `shop`.`orders` TO `me`@`%`"),
	}
	t.Cleanup(func() { fakeRoutes = nil })

	v, err := tableTable(context.Background(), db, req(t, "mysql.table.list", map[string]any{"schema": "shop"}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	if len(tbl.Warnings) == 0 {
		t.Fatalf("a per-table grant left the listing claiming to be complete: %+v", tbl.Rows)
	}
	if tbl.Warnings[0].Code != "mysql.table.partial" {
		t.Errorf("code = %q, want mysql.table.partial", tbl.Warnings[0].Code)
	}
	// The row that did answer is not lost to the caveat.
	if len(tbl.Rows) != 1 {
		t.Errorf("rows = %v, want the table it could see", tbl.Rows)
	}
}

// A schema-wide grant means nothing can be hidden, so the listing says
// nothing extra — otherwise every admin sees a caveat and learns to ignore it.
func TestTableListIsQuietUnderASchemaWideGrant(t *testing.T) {
	db := fakeDB(t, []string{"TABLE_NAME", "TABLE_TYPE", "ENGINE", "TABLE_ROWS", "SIZE"},
		[][]driver.Value{{[]byte("orders"), []byte("BASE TABLE"), []byte("InnoDB"), int64(12), int64(4096)}})
	for _, grant := range []string{
		"GRANT ALL PRIVILEGES ON *.* TO `root`@`localhost`",
		"GRANT SELECT ON `shop`.* TO `me`@`%`",
	} {
		fakeRoutes = []struct {
			match  string
			result fakeResult
		}{grantRoute(grant)}
		v, err := tableTable(context.Background(), db, req(t, "mysql.table.list", map[string]any{"schema": "shop"}))
		if err != nil {
			t.Fatal(err)
		}
		if w := v.(view.Table).Warnings; len(w) != 0 {
			t.Errorf("%q warned anyway: %+v", grant, w)
		}
	}
	fakeRoutes = nil
}

// **`GRANT USAGE ON *.*` is the baseline line every MySQL account has, and
// it means no privileges at all.**
//
// Matching a wide scope without excluding it read every account on every
// server as fully privileged, so the caveat above would never have fired
// once — a guard that passes its own tests and protects nothing. Found by
// writing the fixture the way a real SHOW GRANTS answers.
func TestTheUsageBaselineIsNotAWideGrant(t *testing.T) {
	db := fakeDB(t, nil, nil)
	for _, tc := range []struct {
		what    string
		grants  []string
		visible bool
	}{
		{"usage alone is no privilege", []string{"GRANT USAGE ON *.* TO `me`@`%`"}, false},
		{"usage beside a per-table grant", []string{
			"GRANT USAGE ON *.* TO `me`@`%`",
			"GRANT SELECT ON `shop`.`orders` TO `me`@`%`",
		}, false},
		{"usage beside a schema-wide grant", []string{
			"GRANT USAGE ON *.* TO `me`@`%`",
			"GRANT SELECT ON `shop`.* TO `me`@`%`",
		}, true},
		{"a real global grant", []string{"GRANT ALL PRIVILEGES ON *.* TO `root`@`localhost`"}, true},
	} {
		t.Run(tc.what, func(t *testing.T) {
			fakeRoutes = []struct {
				match  string
				result fakeResult
			}{grantRoute(tc.grants...)}
			t.Cleanup(func() { fakeRoutes = nil })
			if got := schemaFullyVisible(context.Background(), db, "shop"); got != tc.visible {
				t.Errorf("schemaFullyVisible = %v, want %v for %v", got, tc.visible, tc.grants)
			}
		})
	}
}

// And a schema whose shape may be missing tables says so, wrapped the way
// plugins/kube's quotaView wraps its table — a Tree has nowhere to carry a
// caveat of its own.
func TestSchemaSaysWhenItsShapeMayBeIncomplete(t *testing.T) {
	db := fakeDB(t, []string{"TABLE_NAME", "COLUMN_NAME", "COLUMN_TYPE", "IS_NULLABLE", "COLUMN_KEY", "EXTRA"},
		[][]driver.Value{{[]byte("orders"), []byte("id"), []byte("bigint"), []byte("NO"), []byte("PRI"), []byte("")}})
	fakeRoutes = []struct {
		match  string
		result fakeResult
	}{grantRoute("GRANT USAGE ON *.* TO `me`@`%`", "GRANT SELECT ON `shop`.`orders` TO `me`@`%`")}
	t.Cleanup(func() { fakeRoutes = nil })

	v, err := schemaTree(context.Background(), db, req(t, "mysql.schema", map[string]any{"schema": "shop"}))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("schema returned %s, want Sections carrying the caveat", view.TypeOf(v))
	}
	if len(s.Warnings) == 0 {
		t.Fatal("a shape that may be missing tables claimed to be the whole schema")
	}
	if _, ok := s.Items[0].View.(view.Tree); !ok {
		t.Errorf("the tree did not survive the wrapper: %+v", s.Items)
	}
}
