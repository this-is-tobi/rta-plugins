package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// An empty listing is still its table (docs: writing a plugin): a program
// reads zero rows, and a person is told what the nothing is. These two used to
// answer with text, so the same tool was a table with keys and a text without,
// and an agent branching on the type met both.
func assertEmptyTable(t *testing.T, v view.View, sentence string) {
	t.Helper()
	tbl, ok := v.(view.Table)
	if !ok {
		t.Fatalf("an empty answer is a %T, want the table", v)
	}
	if len(tbl.Rows) != 0 || tbl.Total != 0 || len(tbl.Columns) == 0 {
		t.Errorf("table = %d columns, %d rows, total %d, want its columns and no rows", len(tbl.Columns), len(tbl.Rows), tbl.Total)
	}
	if tbl.Empty != sentence {
		t.Errorf("empty sentence = %q, want %q", tbl.Empty, sentence)
	}
	raw, err := view.Marshal(view.Envelope{View: v})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"rows":[]`) || !strings.Contains(string(raw), `"type":"table"`) {
		t.Errorf("an agent reads %s, want a table with rows []", raw)
	}
}

func TestAnEmptyKeyListIsItsTable(t *testing.T) {
	srv := newFakeServer(t, map[string]string{"SCAN 0 MATCH nothing:* COUNT 200": array(bulk("0"), array())})
	v, err := run(t, "redis.key.list", srv, map[string]any{"pattern": "nothing:*"})
	if err != nil {
		t.Fatal(err)
	}
	assertEmptyTable(t, v, `no keys match "nothing:*"`)
}

func TestAnEmptySlowLogIsItsTable(t *testing.T) {
	srv := newFakeServer(t, map[string]string{"SLOWLOG GET 25": array()})
	v, err := run(t, "redis.slowlog", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEmptyTable(t, v, "the slow log is empty")
}
