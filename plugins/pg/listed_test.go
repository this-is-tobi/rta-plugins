package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A listing cut off at its bound says so. pg.table.list and pg.activity asked
// the server for exactly `limit` rows and showed whatever came back, so a
// database with a hundred tables listed with a limit of fifty read like a
// database with fifty, and a server with eighty sessions like one with fifty:
// the answer an agent would act on, on the screen somebody opened to find out
// what is there. The queries ask one row past the bound, which tells a full
// page from a page that ends on the boundary.
func TestAListingThatWasCutOffSaysSo(t *testing.T) {
	cut, err := listed(&fakeRows{cols: []string{"id"}, remaining: 4}, 3, "table", true, plugin.SurfaceMCP)
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.Rows) != 3 {
		t.Errorf("rows = %d, want the bound of 3 with the extra row dropped", len(cut.Rows))
	}
	if len(cut.Warnings) != 1 || cut.Warnings[0].Code != "pg.list.partial" {
		t.Fatalf("warnings = %+v, want the one that says it stopped", cut.Warnings)
	}
	if w := cut.Warnings[0]; !strings.Contains(w.Message, "stopped at 3 tables") ||
		!strings.Contains(w.Hint, `the "limit" argument`) {
		t.Errorf("warning = %+v, want it to say it stopped at 3 tables and how to go on", w)
	}

	whole, err := listed(&fakeRows{cols: []string{"id"}, remaining: 3}, 3, "table", true, plugin.SurfaceMCP)
	if err != nil {
		t.Fatal(err)
	}
	if len(whole.Rows) != 3 || len(whole.Warnings) != 0 {
		t.Errorf("a listing that ends on its bound reported more: %d rows, %+v", len(whole.Rows), whole.Warnings)
	}
}

// Where the cut is the point it says nothing: the overview's five largest
// tables are five on purpose, and a warning there would name a `limit` the
// overview does not take.
func TestAListingCutOnPurposeIsQuiet(t *testing.T) {
	cut, err := listed(&fakeRows{cols: []string{"id"}, remaining: 6}, 5, "table", false, plugin.SurfaceMCP)
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.Rows) != 5 || len(cut.Warnings) != 0 {
		t.Errorf("%d rows and warnings %+v, want five rows and none", len(cut.Rows), cut.Warnings)
	}
}
