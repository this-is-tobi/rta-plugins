package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A table's sizes and lags carry values beside their text, which is what a
// program reading the answer as JSON gets as a number instead of a string to
// parse; its grades rta values from their text. Each value is one its column's
// kind takes: one that is not is dropped on the way out, as if it were never
// there.
func TestTheReplicationTablesCarryTheirValues(t *testing.T) {
	f := primaryFacts([]standbyRow{hiddenStandby(), driftingStandby()}, []slotRow{lostSlot(), extendedSlot()})
	for name, tbl := range map[string]view.Table{
		"standbys": standbysTable(f), "slots": slotsTable(f), "attention": attentionTable(f),
	} {
		checkValues(t, name, tbl)
	}

	drifting := standbysTable(primaryFacts([]standbyRow{driftingStandby()}, nil))
	if got := valueAt(t, drifting, 0, "Behind"); got == "" {
		t.Errorf("Behind of a standby measured behind has no value: %v", drifting.Values)
	}
	// Hidden is not zero to a program either: the dash has no value.
	hidden := standbysTable(primaryFacts([]standbyRow{hiddenStandby()}, nil))
	if got := valueAt(t, hidden, 0, "Behind"); got != "" {
		t.Errorf("Behind of a hidden standby = %q, want no value", got)
	}
}

func TestAListTypesItsCellsByTheirColumnsKinds(t *testing.T) {
	tbl := view.Table{
		Columns: []view.Column{
			{Name: "Table"}, {Name: "Rows", Kind: view.KindNumber}, {Name: "Size", Kind: view.KindBytes},
			{Name: "State", Kind: view.KindStatus},
		},
		Rows: [][]string{
			{"orders", "42", "16384", "idle in transaction (aborted)"},
			{"users", "", "", "Lock"},
		},
	}
	typed(&tbl)
	checkValues(t, "list", tbl)
	for _, tc := range []struct {
		row              int
		column, text, va string
	}{
		{0, "Table", "orders", ""},
		{0, "Rows", "42", ""}, // rta values a count from its text
		{0, "Size", "16.0 KiB", "16384"},
		{0, "State", "idle in transaction (aborted)", "idle in transaction aborted"},
		{1, "Rows", "", ""},
		{1, "Size", "", ""},
		{1, "State", "Lock", "lock"},
	} {
		i := columnIndex(t, tbl, tc.column)
		if got := tbl.Rows[tc.row][i]; got != tc.text {
			t.Errorf("row %d %s shows %q, want %q", tc.row, tc.column, got, tc.text)
		}
		if got := valueAt(t, tbl, tc.row, tc.column); got != tc.va {
			t.Errorf("row %d %s is valued %q, want %q", tc.row, tc.column, got, tc.va)
		}
	}
}

func checkValues(t *testing.T, name string, tbl view.Table) {
	t.Helper()
	if tbl.Values != nil && len(tbl.Values) != len(tbl.Rows) {
		t.Fatalf("%s: values for %d rows, and %d rows", name, len(tbl.Values), len(tbl.Rows))
	}
	for r, values := range tbl.Values {
		for c, v := range values {
			if v != "" && !view.ValueOK(tbl.Columns[c].Kind, v) {
				t.Errorf("%s: row %d %s is valued %q, which a %q column does not take",
					name, r, tbl.Columns[c].Name, v, tbl.Columns[c].Kind)
			}
		}
	}
}

func valueAt(t *testing.T, tbl view.Table, row int, column string) string {
	t.Helper()
	if row >= len(tbl.Values) || tbl.Values[row] == nil {
		return ""
	}
	return tbl.Values[row][columnIndex(t, tbl, column)]
}

func columnIndex(t *testing.T, tbl view.Table, column string) int {
	t.Helper()
	for i, c := range tbl.Columns {
		if c.Name == column {
			return i
		}
	}
	t.Fatalf("no column %q", column)
	return -1
}
