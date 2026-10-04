//go:build livepg

// The schema description is SQL, and the whole point of writing a name as
// SQL's own escape is that, replayed into an empty database, it makes the same
// objects. The setup is listednames_live_test.go's.
package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

func TestTheSchemaDescriptionOfOddNamesReplaysAsTheSameObjects(t *testing.T) {
	ctx := context.Background()
	src := oddFixture(t)
	const replay = "rta_odd_replay"
	admin(t, "postgres", "drop database if exists "+replay)
	admin(t, "postgres", "create database "+replay)
	t.Cleanup(func() { admin(t, "postgres", "drop database if exists "+replay) })

	for _, schema := range []string{oddSchema, "public"} {
		body := runLive(t, "pg.schema.dump", map[string]any{"database": src, "schema": schema}).(view.Text).Body
		if strings.Contains(body, "\x1b") {
			t.Errorf("an escape sequence reaches the description of %q:\n%s", schema, body)
		}
		admin(t, replay, "create schema if not exists "+oddSchemaSQL)
		admin(t, replay, body)
	}

	names := func(database string) []string {
		conn, verr := connect(ctx, reqFor(t, "pg.status", liveValues(t, map[string]any{"database": database})))
		if verr != nil {
			t.Fatal(verr)
		}
		defer func() { _ = conn.Close(ctx) }()
		rows, err := conn.Query(ctx, `
			select 'column ' || n.nspname || ' . ' || c.relname || ' . ' || a.attname
			from pg_class c join pg_namespace n on n.oid = c.relnamespace
			  join pg_attribute a on a.attrelid = c.oid and a.attnum > 0 and not a.attisdropped
			where c.relkind in ('r', 'p') and n.nspname not like 'pg\_%' and n.nspname <> 'information_schema'
			union all
			select 'index ' || schemaname || ' . ' || tablename || ' . ' || indexname
			from pg_indexes
			where schemaname not like 'pg\_%' and schemaname <> 'information_schema'
			  and indexname !~ '_(pkey|key)$'
			union all
			select 'foreign key ' || n.nspname || ' . ' || c.relname
			from pg_constraint con join pg_class c on c.oid = con.conrelid
			  join pg_namespace n on n.oid = c.relnamespace
			where con.contype = 'f' and n.nspname not like 'pg\_%'
			order by 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	want, got := names(src), names(replay)
	if len(want) < 8 {
		t.Fatalf("the fixture made %q, want its tables, columns, indexes and foreign key", want)
	}
	if !slices.Equal(want, got) {
		t.Errorf("replayed into an empty database, the description made\n%q\nwant\n%q", got, want)
	}
}

// A column of a type a stranger named replays as the same column: the
// description writes the type's name in SQL's own escape, and a database that
// holds that type reads it back as the type of the column.
func TestTheSchemaDescriptionOfAnOddTypeReplaysAsTheSameColumns(t *testing.T) {
	ctx := context.Background()
	const (
		src, replay = "rta_odd_type_src", "rta_odd_type_replay"
		typeSQL     = `U&"typ\001b[31mred\000aline"`
	)
	drop := func() {
		admin(t, "postgres", "drop database if exists "+src)
		admin(t, "postgres", "drop database if exists "+replay)
	}
	drop()
	t.Cleanup(drop)
	for _, db := range []string{src, replay} {
		admin(t, "postgres", "create database "+db)
		admin(t, db, "create type "+typeSQL+" as enum ('a')")
	}
	admin(t, src, "create table kinds (id int primary key, kind "+typeSQL+", tags "+typeSQL+"[], price numeric(10, 2))")

	body := runLive(t, "pg.schema.dump", map[string]any{"database": src, "schema": "public"}).(view.Text).Body
	if strings.ContainsAny(body, "\x1b") {
		t.Errorf("an escape sequence reaches the description:\n%s", body)
	}
	admin(t, replay, body)

	columns := func(database string) []string {
		conn, verr := connect(ctx, reqFor(t, "pg.status", liveValues(t, map[string]any{"database": database})))
		if verr != nil {
			t.Fatal(verr)
		}
		defer func() { _ = conn.Close(ctx) }()
		rows, err := conn.Query(ctx, `
			select a.attname || ' ' || format_type(a.atttypid, a.atttypmod)
			from pg_attribute a join pg_class c on c.oid = a.attrelid
			where c.relname = 'kinds' and a.attnum > 0 and not a.attisdropped
			order by a.attnum`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	want, got := columns(src), columns(replay)
	if len(want) != 4 {
		t.Fatalf("the fixture made %q, want its four columns", want)
	}
	if !slices.Equal(want, got) {
		t.Errorf("replayed, the description made the columns\n%q\nwant\n%q", got, want)
	}
}
