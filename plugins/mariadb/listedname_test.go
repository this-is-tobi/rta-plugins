package main

import (
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A name somebody else chose — a database, a table, a column, an account, a
// host — is shown and not cleaned. The renderer strips an escape sequence from
// a cell on its way to a terminal, so a table called `esc` ESC `[31mred` came
// out as `escred`, another and ordinary name, and a newline in a name split a
// row in two. Shown quoted, the one place an odd name differs from an ordinary
// one is the quotation marks; and an ordinary name, spaces and accents
// included, is shown exactly as it always was.
const (
	oddName      = "esc\x1b[31mred\nline"
	oddShown     = `"esc\x1b[31mred\nline"`
	ordinaryName = "Café ordres"
)

// shownAs checks one cell against what a reader should see, and that nothing a
// terminal would act on is left in it.
func shownAs(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
	if strings.ContainsAny(got, "\x1b\n") {
		t.Errorf("%s = %q still holds a character a terminal acts on", what, got)
	}
}

func TestADatabaseListShowsAnOddNameQuotedAndAnOrdinaryOneAsItIs(t *testing.T) {
	db := fakeDB(t, []string{"SCHEMA_NAME", "n", "size"}, [][]driver.Value{
		{[]byte(oddName), int64(1), int64(4096)},
		{[]byte(ordinaryName), int64(2), int64(2048)},
	})
	tbl, err := databaseTable(t.Context(), db, req(t, "mariadb.database.list", map[string]any{}), 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) != 2 {
		t.Fatalf("rows = %v, want both databases", tbl.Rows)
	}
	shownAs(t, "an odd database", tbl.Rows[0][0], oddShown)
	shownAs(t, "an ordinary database", tbl.Rows[1][0], ordinaryName)
}

func TestATableListShowsAnOddNameQuotedAndAnOrdinaryOneAsItIs(t *testing.T) {
	db := fakeDB(t, []string{"TABLE_NAME", "TABLE_TYPE", "ENGINE", "TABLE_ROWS", "SIZE"}, [][]driver.Value{
		{[]byte(oddName), []byte("BASE TABLE"), []byte("InnoDB"), int64(12), int64(4096)},
		{[]byte(ordinaryName), []byte("VIEW"), []byte(""), int64(0), int64(0)},
	})
	v, err := tableTable(t.Context(), db, req(t, "mariadb.table.list", map[string]any{"schema": "shop"}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	shownAs(t, "an odd table", tbl.Rows[0][0], oddShown)
	shownAs(t, "an ordinary table", tbl.Rows[1][0], ordinaryName)
}

// A session's user, the host it came from and the database it selected are
// each somebody's choice: an account is named by whoever created it, and a
// host is whatever the client's address resolves to. A session that selected
// no database arrives as an empty cell, and stays one.
func TestASessionShowsItsUserHostAndDatabaseQuotedWhenTheyAreOdd(t *testing.T) {
	row := func(id int64, user, host, database string) []driver.Value {
		return []driver.Value{id, []byte(user), []byte(host), []byte(database),
			[]byte("Query"), int64(3), []byte("Sending data"), []byte("SELECT 1")}
	}
	db := fakeDB(t, processlistColumns, [][]driver.Value{
		row(1, oddName, oddName, oddName),
		row(2, ordinaryName, "db.internal:51000", ordinaryName),
		row(3, "app", "10.0.0.9:51000", ""),
	})
	v, err := activityView(t.Context(), db, req(t, "mariadb.activity", map[string]any{}), true, true)
	if err != nil {
		t.Fatal(err)
	}
	rows := v.(view.Table).Rows
	for i, col := range []string{"user", "host", "database"} {
		shownAs(t, "an odd "+col, rows[0][1+i], oddShown)
	}
	shownAs(t, "an ordinary user", rows[1][1], ordinaryName)
	shownAs(t, "an ordinary host", rows[1][2], "db.internal:51000")
	shownAs(t, "an ordinary database", rows[1][3], ordinaryName)
	shownAs(t, "no database", rows[2][3], "")
	if rows[0][4] != "Query" || rows[0][6] != "Sending data" || rows[0][7] != "SELECT 1" {
		t.Errorf("row = %v, want the command, state and statement as the server said them", rows[0])
	}
}

// A schema's shape is names all the way down: the database, each table, each
// column, and the type, where an ENUM spells out the members its author wrote.
func TestASchemaShowsEveryOddNameInItsShapeQuotedAndEveryOrdinaryOneAsItIs(t *testing.T) {
	columns := []string{"TABLE_NAME", "COLUMN_NAME", "COLUMN_TYPE", "IS_NULLABLE", "COLUMN_KEY", "EXTRA"}
	db := fakeDB(t, columns, [][]driver.Value{
		{[]byte(oddName), []byte(oddName), []byte("enum('a\x1bb','c')"), []byte("NO"), []byte("PRI"), []byte("")},
		{[]byte(oddName), []byte("plain"), []byte("varchar(10)"), []byte("YES"), []byte(""), []byte("")},
		{[]byte(ordinaryName), []byte(ordinaryName), []byte("int"), []byte("NO"), []byte(""), []byte("auto_increment")},
	})
	v, err := schemaTree(t.Context(), db, req(t, "mariadb.schema", map[string]any{"schema": oddName}))
	if err != nil {
		t.Fatal(err)
	}
	root := v.(view.Tree).Roots[0]
	shownAs(t, "an odd database", root.Label, oddShown)
	odd, ordinary := root.Children[0], root.Children[1]
	shownAs(t, "an odd table", odd.Label, oddShown)
	shownAs(t, "an odd column", odd.Children[0].Label, oddShown)
	shownAs(t, "a column's type holding an odd member", odd.Children[0].Detail,
		`"enum('a\x1bb','c')", primary key, not null`)
	shownAs(t, "an ordinary column's type", odd.Children[1].Detail, "varchar(10)")
	shownAs(t, "an ordinary table", ordinary.Label, ordinaryName)
	shownAs(t, "an ordinary column", ordinary.Children[0].Label, ordinaryName)
	shownAs(t, "an ordinary column's detail", ordinary.Children[0].Detail, "int, not null, auto_increment")
}

// The caveat a tree carries when the account's grants may hide tables names
// the database it is about, and the tree beside it is titled with it. The
// statement in its hint is SQL a reader runs, so it names the database as it
// is and not as a list shows it.
func TestAPartialSchemaNamesAnOddDatabaseQuotedButGrantsItAsItIs(t *testing.T) {
	columns := []string{"TABLE_NAME", "COLUMN_NAME", "COLUMN_TYPE", "IS_NULLABLE", "COLUMN_KEY", "EXTRA"}
	db := fakeDB(t, columns, [][]driver.Value{
		{[]byte("orders"), []byte("id"), []byte("bigint"), []byte("NO"), []byte("PRI"), []byte("")},
	})
	fakeRoutes = []struct {
		match  string
		result fakeResult
	}{grantRoute("GRANT USAGE ON *.* TO `me`@`%`", "GRANT SELECT ON `x`.`orders` TO `me`@`%`")}
	t.Cleanup(func() { fakeRoutes = nil })

	v, err := schemaTree(t.Context(), db, req(t, "mariadb.schema", map[string]any{"schema": oddName}))
	if err != nil {
		t.Fatal(err)
	}
	s := v.(view.Sections)
	shownAs(t, "the section's title", s.Items[0].Title, oddShown)
	if !strings.Contains(s.Warnings[0].Message, "tables in "+oddShown+" missing") {
		t.Errorf("message = %q, want the database shown quoted", s.Warnings[0].Message)
	}
	if !strings.Contains(s.Warnings[0].Hint, "GRANT SELECT ON `"+oddName+"`.*") {
		t.Errorf("hint = %q, want the statement to name the database as it is", s.Warnings[0].Hint)
	}

	ordinary := partialListing("mariadb.schema.partial", ordinaryName, "this shape")
	if !strings.Contains(ordinary.Message, "tables in "+ordinaryName+" missing") {
		t.Errorf("message = %q, want an ordinary database named as it always was", ordinary.Message)
	}
}

// What a query's own result names is the table's: a SELECT * answers with the
// column names its author chose. The cells under them are whatever was
// stored, which is the renderer's to neutralise and not a name to quote.
func TestAResultsHeadersShowAnOddColumnNameQuotedAndAnOrdinaryOneAsItIs(t *testing.T) {
	db := fakeDB(t, []string{oddName, ordinaryName, "count(*)", ""}, [][]driver.Value{
		{[]byte(oddName), []byte("x"), int64(1), nil},
	})
	v, err := queryView(t.Context(), db, req(t, "mariadb.query", map[string]any{"sql": "select 1"}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	want := []string{oddShown, ordinaryName, "count(*)", ""}
	for i, w := range want {
		shownAs(t, "header "+tbl.Columns[i].Name, tbl.Columns[i].Name, w)
	}
	if tbl.Rows[0][0] != oddName {
		t.Errorf("cell = %q, want the stored value untouched", tbl.Rows[0][0])
	}
}

// A replica's channel and source, the host a source lists each replica under,
// and the binary log files the positions are read in, are names some other
// server's operator set.
func TestReplicationShowsEveryOddNameQuotedAndEveryOrdinaryOneAsItIs(t *testing.T) {
	channel := func(name, host, file string, io string) map[string]string {
		return map[string]string{
			"channel_name": name, "source_host": host, "source_port": "3306",
			"replica_io_running": io, "replica_sql_running": "Yes", "seconds_behind_source": "0",
			"source_log_file": file, "read_source_log_pos": "157",
			"relay_source_log_file": "binlog.000007", "exec_source_log_pos": "100",
		}
	}
	server := func(channels ...map[string]string) state {
		return state{
			vars:     map[string]string{"log_bin": "ON"},
			channels: channels,
			binlog:   map[string]string{"file": "bin\x1blog.000007", "position": "157"},
			hosts: []map[string]string{
				{"server_id": "2", "host": oddName, "port": "3306"},
				{"server_id": "3", "host": ordinaryName, "port": "3306"},
				{"server_id": "4", "host": "", "port": "3306"},
			},
			unread: map[section]*view.Error{},
		}
	}

	t.Run("one source", func(t *testing.T) {
		v := replicationView(server(channel("", oddName, "bin\x1blog.000007", "Yes")))
		sources, _ := tableOf(v, "sources")
		row := sources.Rows[0]
		shownAs(t, "the default channel", row[0], "(default)")
		shownAs(t, "an odd source", row[1], `"esc\x1b[31mred\nline:3306"`)
		shownAs(t, "a position in an odd file", row[5], `"bin\x1blog.000007:157"`)
		shownAs(t, "a position in an ordinary file", row[6], "binlog.000007:100")
		summary, _ := tableOf(v, "summary")
		shownAs(t, "the summary's role", summary.Rows[0][0],
			`replica of "esc\x1b[31mred\nline:3306" and source of 3 replicas`)
		hosts, _ := tableOf(v, "replicas")
		shownAs(t, "an odd replica host", hosts.Rows[0][1], oddShown)
		shownAs(t, "an ordinary replica host", hosts.Rows[1][1], ordinaryName)
		shownAs(t, "a replica that gave no host", hosts.Rows[2][1], "-")
		shownAs(t, "the binary log", pairsOf(v, "server")["binary log"], `"bin\x1blog.000007:157"`)
	})

	t.Run("two sources", func(t *testing.T) {
		v := replicationView(server(
			channel(oddName, "db.internal", "binlog.000007", "No"),
			channel(ordinaryName, "db.internal", "binlog.000007", "Yes"),
		))
		sources, _ := tableOf(v, "sources")
		shownAs(t, "an odd channel", sources.Rows[0][0], oddShown)
		shownAs(t, "an ordinary channel", sources.Rows[1][0], ordinaryName)
		shownAs(t, "an ordinary source", sources.Rows[1][1], "db.internal:3306")
		summary, _ := tableOf(v, "summary")
		if want := oddShown + ": IO thread stopped"; !strings.Contains(summary.Rows[0][2], want) {
			t.Errorf("detail = %q, want the failing channel named %s", summary.Rows[0][2], want)
		}
	})

	t.Run("an ordinary binary log", func(t *testing.T) {
		st := server()
		st.binlog = map[string]string{"file": "binlog.000007", "position": "157"}
		shownAs(t, "the binary log", pairsOf(replicationView(st), "server")["binary log"], "binlog.000007:157")
	})
}

// A host reported with no port is the other way hostPort lists it: by the host
// alone, which is just as much a name somebody else set, and an address with a
// colon in it is no reason to quote one that reads as itself.
func TestAHostListedWithNoPortIsQuotedWhenOddAndAsItIsWhenOrdinary(t *testing.T) {
	shownAs(t, "an odd host", hostPort(oddName, ""), oddShown)
	shownAs(t, "an ordinary host", hostPort(ordinaryName, ""), ordinaryName)
	shownAs(t, "an address with a port", hostPort("2001:db8::1", "3306"), "[2001:db8::1]:3306")
	shownAs(t, "no host", hostPort("", "3306"), "-")
}
