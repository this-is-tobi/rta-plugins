package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// An empty listing is still its table (docs: writing a plugin): a program reads
// zero rows under the columns it knows, and a person is told what the nothing
// is, where a bare grid of headings reads like a listing that failed.
func TestAnEmptyListingIsItsTable(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections":                    envelope(`{"collections":[]}`),
		"/collections/docs/points/scroll": `{"result":{"points":[],"next_page_offset":null}}`,
	})
	for id, tc := range map[string]struct {
		values   map[string]any
		sentence string
	}{
		"qdrant.collection.list": {map[string]any{}, "no collections"},
		"qdrant.points.scroll":   {map[string]any{"collection": "docs"}, "no points in docs"},
	} {
		t.Run(id, func(t *testing.T) {
			var v view.View
			var err error
			for _, c := range Plugin().Capabilities {
				if c.ID == id {
					v, err = c.Run(t.Context(), reqAt(t, f, id, tc.values))
				}
			}
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
