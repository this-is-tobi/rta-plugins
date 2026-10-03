package main

import (
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A listing cut off at its bound says so. The three listings asked the server
// for exactly `limit` rows and showed whatever came back, so a server with a
// hundred databases listed with a limit of ten read like a server with ten:
// the answer an agent would act on, on the screen somebody opened to find out
// what is there. The queries ask one row past the bound, which tells a full
// page from a page that ends on the boundary; the fake driver answers every
// query with its canned rows, so three rows against a bound of two is the
// page that was cut.
func TestAListingThatWasCutOffSaysSo(t *testing.T) {
	r := func(id string, values map[string]any) plugin.Request {
		return req(t, id, values).WithSurface(plugin.SurfaceMCP)
	}
	three := func(row []driver.Value) [][]driver.Value { return [][]driver.Value{row, row, row} }
	for _, tc := range []struct {
		name string
		run  func(limit int) (view.Table, error)
		noun string
	}{
		{"databases", func(limit int) (view.Table, error) {
			db := fakeDB(t, []string{"SCHEMA_NAME", "n", "size"},
				three([]driver.Value{[]byte("shop"), int64(4), int64(4096)}))
			return databaseTable(t.Context(), db, r("mysql.database.list", map[string]any{}), limit, true)
		}, "2 databases"},
		{"tables", func(limit int) (view.Table, error) {
			db := fakeDB(t, []string{"TABLE_NAME", "TABLE_TYPE", "ENGINE", "TABLE_ROWS", "SIZE"},
				three([]driver.Value{[]byte("orders"), []byte("BASE TABLE"), []byte("InnoDB"), int64(12), int64(4096)}))
			v, err := tableTable(t.Context(), db, r("mysql.table.list", map[string]any{"schema": "shop", "limit": limit}))
			if err != nil {
				return view.Table{}, err
			}
			return v.(view.Table), nil
		}, "2 tables"},
		{"sessions", func(limit int) (view.Table, error) {
			db := fakeDB(t, processlistColumns, three(processlistRow("SELECT 1")))
			v, err := activityView(t.Context(), db, r("mysql.activity", map[string]any{"limit": limit}), true, true)
			if err != nil {
				return view.Table{}, err
			}
			return v.(view.Table), nil
		}, "2 sessions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cut, err := tc.run(2)
			if err != nil {
				t.Fatal(err)
			}
			var said *view.Error
			for i := range cut.Warnings {
				if cut.Warnings[i].Code == "mysql.list.partial" {
					said = &cut.Warnings[i]
				}
			}
			if len(cut.Rows) != 2 || cut.Total != 2 {
				t.Errorf("rows = %d, total = %d, want the bound of 2 with the extra row dropped", len(cut.Rows), cut.Total)
			}
			if said == nil {
				t.Fatalf("a listing cut at 2 said nothing: %+v", cut.Warnings)
			}
			if !strings.Contains(said.Message, "stopped at "+tc.noun) || !strings.Contains(said.Hint, `the "limit" argument`) {
				t.Errorf("warning = %+v, want it to say it stopped at %s and how to go on", said, tc.noun)
			}

			whole, err := tc.run(3)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range whole.Warnings {
				if w.Code == "mysql.list.partial" {
					t.Errorf("a listing that ends on its bound reported more: %+v", w)
				}
			}
		})
	}
}

// Where the cut is the point it says nothing: the overview's ten largest
// databases are ten on purpose, and a warning there would name a `limit` the
// overview does not take.
func TestAListingCutOnPurposeIsQuiet(t *testing.T) {
	db := fakeDB(t, []string{"SCHEMA_NAME", "n", "size"}, [][]driver.Value{
		{[]byte("a"), int64(1), int64(1)}, {[]byte("b"), int64(1), int64(1)}, {[]byte("c"), int64(1), int64(1)},
	})
	cut, err := databaseTable(t.Context(), db, req(t, "mysql.overview", map[string]any{}), 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.Rows) != 2 || len(cut.Warnings) != 0 {
		t.Errorf("%d rows and warnings %+v, want two rows and none", len(cut.Rows), cut.Warnings)
	}
}
