package family

import (
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A DATE is written as the day, and a DATETIME or a TIMESTAMP with the
// fraction of a second it holds. The driver hands a DATE back as a time at
// midnight, which came out as 2026-10-31 00:00:00, a time of day the column
// does not hold, and the seconds alone cut a DATETIME(6) short. Neither
// carries a zone, and each is valued with none.
func TestATimeIsWrittenAsItsColumnsTypeHoldsIt(t *testing.T) {
	at := time.Date(2026, 10, 31, 14, 2, 5, 250e6, time.UTC)
	for _, c := range []struct {
		typeName    string
		v           any
		text, value string
	}{
		{"DATE", time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), "2026-10-31", "2026-10-31"},
		{"DATETIME", at, "2026-10-31 14:02:05.25", "2026-10-31T14:02:05.25"},
		{"TIMESTAMP", at, "2026-10-31 14:02:05.25", "2026-10-31T14:02:05.25"},
		{"DATETIME", time.Date(2026, 10, 31, 14, 2, 5, 0, time.UTC), "2026-10-31 14:02:05", "2026-10-31T14:02:05"},
	} {
		got := cellOf(c.v, c.typeName)
		tbl := view.Table{Columns: []view.Column{{Name: "at", Kind: view.KindTimestamp}}, Rows: [][]string{{got}}}
		if got != c.text || tbl.Value(0, 0) != c.value {
			t.Errorf("%s: written %q valued %q, want %q valued %q", c.typeName, got, tbl.Value(0, 0), c.text, c.value)
		}
	}
}
