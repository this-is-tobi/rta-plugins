//go:build livepg

// Names a stranger chose, against a real server — the questions the scripted
// tests cannot answer: that PostgreSQL takes such a name at all, and that the
// listings and the description show it written out.
//
//	docker run --rm -d --name rta-pg-lab -e POSTGRES_PASSWORD=lab -p 5499:5432 postgres:18
//	RTA_TEST_PG_PORT=5499 RTA_TEST_PG_PASSWORD=lab \
//	  go test . -tags livepg -count=1 -v -run 'TestOddNames|TestTheSchemaDescriptionOfOdd'
//	docker rm -f rta-pg-lab
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// Every name here is spelled with Unicode escapes in SQL, because SQL has no
// other way to put an escape character in an identifier: each holds U+001B and
// a newline in the middle of a word.
const (
	oddRoleSQL     = `U&"rta_odd\001b[31mrole\000ax"`
	oddDatabaseSQL = `U&"rta_odd\001b[7mdb\000ax"`
	oddSchemaSQL   = `U&"sch\001b[2Jema\000ax"`
	oddTableSQL    = `U&"tab\001b[31mred\000aline"`
	oddColumnSQL   = `U&"col\001b[0m\000az"`
	oddRole        = "rta_odd\x1b[31mrole\nx"
	oddSchema      = "sch\x1b[2Jema\nx"
	oddTable       = "tab\x1b[31mred\nline"
)

// oddFixture makes a database holding a table, a column, an index and a
// foreign key named by a stranger, beside a plain table, and a role and a
// database of the same kind, and returns the database's name. Everything it
// made is dropped when the test ends.
func oddFixture(t *testing.T) string {
	t.Helper()
	const src = "rta_odd_src"
	drop := func() {
		admin(t, "postgres", "drop database if exists "+src)
		admin(t, "postgres", "drop database if exists "+oddDatabaseSQL)
		admin(t, "postgres", "drop role if exists "+oddRoleSQL)
	}
	drop()
	admin(t, "postgres", "create database "+src)
	admin(t, "postgres", "create database "+oddDatabaseSQL)
	admin(t, "postgres", "create role "+oddRoleSQL+" login password 'x'")
	t.Cleanup(drop)
	admin(t, src, "grant connect on database "+src+" to "+oddRoleSQL)
	admin(t, src, "create schema "+oddSchemaSQL)
	admin(t, src, "create table "+oddSchemaSQL+"."+oddTableSQL+" (id int primary key, "+oddColumnSQL+
		" text not null default 'secret', parent int references "+oddSchemaSQL+"."+oddTableSQL+" (id))")
	admin(t, src, `create index U&"idx\001b[1m\000ax" on `+oddSchemaSQL+"."+oddTableSQL+" ("+oddColumnSQL+")")
	admin(t, src, `create unique index on `+oddSchemaSQL+"."+oddTableSQL+" ("+oddColumnSQL+", id)")
	admin(t, src, `create table public."café table" (id int primary key, "my col é" text)`)
	return src
}

// runLive runs the capability the way a call does, against the live server.
func runLive(t *testing.T, id string, values map[string]any) view.View {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID != id {
			continue
		}
		v, err := c.Run(context.Background(), reqFor(t, id, liveValues(t, values)))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		return v
	}
	t.Fatalf("no capability %s", id)
	return nil
}

func TestOddNamesAgainstARealServer(t *testing.T) {
	ctx := context.Background()
	src := oddFixture(t)

	t.Run("the table list", func(t *testing.T) {
		table := runLive(t, "pg.table.list", map[string]any{"database": src}).(view.Table)
		var found, plain bool
		for _, row := range table.Rows {
			bare(t, "a row", strings.Join(row, " | "))
			found = found || (row[0] == `"sch\x1b[2Jema\nx"` && row[1] == `"tab\x1b[31mred\nline"`)
			plain = plain || (row[0] == "public" && row[1] == "café table")
		}
		if !found || !plain {
			t.Errorf("rows = %q, want the odd table written out and the plain one as it is", table.Rows)
		}
	})

	t.Run("the database list", func(t *testing.T) {
		table := runLive(t, "pg.database.list", map[string]any{"database": src}).(view.Table)
		var found bool
		for _, row := range table.Rows {
			bare(t, "a row", strings.Join(row, " | "))
			found = found || row[0] == `"rta_odd\x1b[7mdb\nx"`
		}
		if !found {
			t.Errorf("rows = %q, want the odd database written out", table.Rows)
		}
	})

	t.Run("the status and the activity of an odd role", func(t *testing.T) {
		as := map[string]any{"database": src, "user": oddRole, "password": "x"}
		conn, verr := connect(ctx, reqFor(t, "pg.status", liveValues(t, as)))
		if verr != nil {
			t.Fatal(verr)
		}
		defer func() { _ = conn.Close(ctx) }()

		kv := runLive(t, "pg.status", as).(view.KeyValue)
		for _, p := range kv.Pairs {
			bare(t, p.Key, p.Value)
			if p.Key == "connected as" && p.Value != `"rta_odd\x1b[31mrole\nx"` {
				t.Errorf("connected as %q, want the role written out", p.Value)
			}
		}

		table := runLive(t, "pg.activity", map[string]any{"database": src}).(view.Table)
		var found bool
		for _, row := range table.Rows {
			bare(t, "a row", strings.Join(row, " | "))
			found = found || row[1] == `"rta_odd\x1b[31mrole\nx"`
		}
		if !found {
			t.Errorf("rows = %q, want the odd role written out", table.Rows)
		}
	})

	t.Run("the dump of an odd table", func(t *testing.T) {
		dump := runLive(t, "pg.table.dump", map[string]any{"database": src, "table": oddSchema + "." + oddTable}).(view.Table)
		if want := `"col\x1b[0m\nz"`; dump.Columns[1].Name != want {
			t.Errorf("columns = %+v, want the odd one written out as %s", dump.Columns, want)
		}
	})
}
