package main

import (
	"context"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// An empty listing is still its table (docs: writing a plugin): a program reads
// zero rows under the columns it knows, and a person is told what the nothing
// is. These three used to answer with a text, so cnpg_list was a table with a
// cluster and a text without, and an agent branching on the type met both.
func TestAnEmptyListingIsItsTable(t *testing.T) {
	for name, run := range map[string]func() (view.View, error){
		"cnpg.list": func() (view.View, error) {
			serves(t, clusterList{})
			return runList(context.Background(), req(map[string]any{"namespace": "prod"}))
		},
		"cnpg.backup.list": func() (view.View, error) {
			serves(t, backupItems())
			return runBackupList(context.Background(), req(map[string]any{"namespace": "prod"}))
		},
		"cnpg.storage": func() (view.View, error) {
			serves(t, volumeItems())
			return runStorage(context.Background(), req(map[string]any{"namespace": "prod"}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := run()
			if err != nil {
				t.Fatal(err)
			}
			tbl, ok := v.(view.Table)
			if !ok {
				t.Fatalf("an empty answer is a %T, want the table", v)
			}
			if len(tbl.Rows) != 0 || tbl.Total != 0 || len(tbl.Columns) < 3 {
				t.Errorf("table = %d columns, %d rows, total %d, want its columns and no rows",
					len(tbl.Columns), len(tbl.Rows), tbl.Total)
			}
			if !strings.Contains(tbl.Empty, "namespace prod") {
				t.Errorf("empty sentence = %q, want it to say where it looked", tbl.Empty)
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
