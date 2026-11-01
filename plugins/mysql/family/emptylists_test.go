package family

import (
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// An empty listing is still its table (docs: writing a plugin): a program reads
// zero rows under the columns it knows, and a person is told what the nothing
// is, where a bare grid of headings reads like a listing that failed.
func TestAnEmptyListingIsItsTable(t *testing.T) {
	r := func(id string, values map[string]any) plugin.Request {
		return req(t, id, values).WithSurface(plugin.SurfaceMCP)
	}
	for _, tc := range []struct {
		name     string
		run      func() (view.Table, error)
		sentence string
	}{
		{"databases", func() (view.Table, error) {
			db := fakeDB(t, []string{"SCHEMA_NAME", "n", "size"}, nil)
			return databaseTable(t.Context(), db, r("mysql.database.list", map[string]any{}), 50, true)
		}, "no databases"},
		{"tables", func() (view.Table, error) {
			db := fakeDB(t, []string{"TABLE_NAME", "TABLE_TYPE", "ENGINE", "TABLE_ROWS", "SIZE"}, nil)
			v, err := tableTable(t.Context(), db, r("mysql.table.list", map[string]any{"schema": "shop"}))
			if err != nil {
				return view.Table{}, err
			}
			return v.(view.Table), nil
		}, "no tables in shop"},
		{"sessions", func() (view.Table, error) {
			db := fakeDB(t, processlistColumns, [][]driver.Value{})
			v, err := activityView(t.Context(), db, r("mysql.activity", map[string]any{}), true, true)
			if err != nil {
				return view.Table{}, err
			}
			return v.(view.Table), nil
		}, "no active sessions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tbl, err := tc.run()
			if err != nil {
				t.Fatal(err)
			}
			if len(tbl.Rows) != 0 || tbl.Total != 0 || len(tbl.Columns) == 0 {
				t.Errorf("table = %d columns, %d rows, total %d, want its columns and no rows",
					len(tbl.Columns), len(tbl.Rows), tbl.Total)
			}
			if tbl.Empty != tc.sentence {
				t.Errorf("empty sentence = %q, want %q", tbl.Empty, tc.sentence)
			}
			raw, err := view.Marshal(view.Envelope{View: tbl})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"rows":[]`) || !strings.Contains(string(raw), `"type":"table"`) {
				t.Errorf("an agent reads %s, want a table with rows []", raw)
			}
		})
	}
}
