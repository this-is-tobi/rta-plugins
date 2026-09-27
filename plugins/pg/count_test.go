package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A count of one reads in the singular wherever this plugin prints one. The
// row bound is printed in two refusals, the table dump's and the query's, and
// --limit 1 made both of them say "more than 1 rows". The dump's is the one a
// test can reach without a server, through a querier that answers the two
// catalogue lookups and then the rows.
func TestADumpPastALimitOfOneRowSaysSoInTheSingular(t *testing.T) {
	rel := relation{oid: 42, schema: "public", name: "orders"}
	_, verr := dumpRows(context.Background(), catalogueQuerier{rows: 2},
		reqFor(t, "pg.table.dump", map[string]any{"table": "public.orders", "limit": 1}), rel)
	if verr == nil {
		t.Fatal("two rows past a limit of one were not refused")
	}
	if verr.Code != "pg.dump.toomany" {
		t.Fatalf("code = %q, want pg.dump.toomany: %s", verr.Code, verr.Message)
	}
	if !strings.HasSuffix(verr.Message, "has more than 1 row") {
		t.Errorf("message = %q, want the bound counted in the singular", verr.Message)
	}
}

// catalogueQuerier answers columnsOf with one column, primaryKeyOf with no
// key, and the dump itself with `rows` rows.
type catalogueQuerier struct{ rows int }

func (q catalogueQuerier) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	switch {
	case strings.Contains(sql, "pg_index"):
		return &namedRows{fakeRows: &fakeRows{}}, nil
	case strings.Contains(sql, "pg_attribute"):
		return &namedRows{fakeRows: &fakeRows{remaining: 1}, names: []string{"id"}}, nil
	}
	return &fakeRows{cols: []string{"id"}, remaining: q.rows}, nil
}

func (catalogueQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

// namedRows is fakeRows for a catalogue lookup, which scans a name into a
// string rather than reading values.
type namedRows struct {
	*fakeRows
	names []string
}

func (r *namedRows) Scan(dest ...any) error {
	*dest[0].(*string) = r.names[r.n-1]
	return nil
}
