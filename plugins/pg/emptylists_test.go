package main

import (
	"context"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// An empty listing is still its table (docs: writing a plugin): a program reads
// zero rows under the columns it knows, and a person is told what the nothing
// is, where a bare grid of headings reads like a listing that failed.
func TestAnEmptyListingIsItsTable(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		run      func() (view.View, error)
		sentence string
	}{
		{"databases", func() (view.View, error) {
			q := script{{match: "pg_database", cols: []string{"datname", "size", "owner"}}}
			return databaseListView(ctx, q, reqFor(t, "pg.database.list", nil))
		}, "no databases"},
		{"tables", func() (view.View, error) {
			q := script{{match: "pg_stat_user_tables", cols: []string{"schemaname", "relname", "n", "size"}}}
			return tableListView(ctx, q, reqFor(t, "pg.table.list", nil), true)
		}, "no tables"},
		{"tables of a schema", func() (view.View, error) {
			q := script{
				{match: "pg_stat_user_tables", cols: []string{"schemaname", "relname", "n", "size"}},
				{match: "pg_namespace", cols: []string{"nspname"}, rows: [][]any{{"app"}}},
			}
			return tableListView(ctx, q, reqFor(t, "pg.table.list", map[string]any{"schema": "app"}), true)
		}, "no tables in app"},
		{"sessions", func() (view.View, error) {
			q := script{{match: "pg_stat_activity", cols: []string{"pid", "usename", "application_name", "state", "seconds", "query"}}}
			return activityView(ctx, q, reqFor(t, "pg.activity", nil), true, true)
		}, "no other sessions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := tc.run()
			if err != nil {
				t.Fatal(err)
			}
			tbl, ok := v.(view.Table)
			if !ok {
				t.Fatalf("an empty answer is a %T, want the table", v)
			}
			if len(tbl.Rows) != 0 || tbl.Total != 0 || len(tbl.Columns) == 0 {
				t.Errorf("table = %d columns, %d rows, total %d, want its columns and no rows",
					len(tbl.Columns), len(tbl.Rows), tbl.Total)
			}
			if tbl.Empty != tc.sentence {
				t.Errorf("empty sentence = %q, want %q", tbl.Empty, tc.sentence)
			}
			raw, err := view.Marshal(view.Envelope{View: v})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"rows":[]`) || !strings.Contains(string(raw), `"type":"table"`) {
				t.Errorf("an agent reads %s, want a table with rows []", raw)
			}
		})
	}
}
