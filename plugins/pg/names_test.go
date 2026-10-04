package main

import (
	"fmt"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// quotedName is %q for a name that reads as itself, so the messages that
// quoted it before read as they did, and ListedName's spelling otherwise —
// which writes out what %q leaves, since strconv counts a Hangul filler
// printable.
func TestAQuotedNameIsWhatAMessageAlwaysSaidUnlessItIsOdd(t *testing.T) {
	for _, name := range []string{"orders", "café table", `a"b`, "日本語"} {
		if got, want := quotedName(name), fmt.Sprintf("%q", name); got != want {
			t.Errorf("quotedName(%q) = %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{
		oddName:           oddShown,
		"hangul\u3164end": `"hangul\u3164end"`,
		"":                `""`,
		"\"leading":       `"\"leading"`,
	} {
		if got := quotedName(name); got != want {
			t.Errorf("quotedName(%q) = %q, want %q", name, got, want)
		}
	}
}

// A NULL name is an empty cell, absent, and stays so; the cells of any other
// column are not touched.
func TestAnEmptyCellStaysEmptyWhenNamesAreListed(t *testing.T) {
	table := view.Table{Rows: [][]string{{"", oddName, "line\none"}, {plainName, "", "x"}}}
	listNames(&table, 0, 1, 7)
	want := [][]string{{"", oddShown, "line\none"}, {plainName, "", "x"}}
	for i := range want {
		for j := range want[i] {
			if table.Rows[i][j] != want[i][j] {
				t.Errorf("row %d = %q, want %q", i, table.Rows[i], want[i])
				break
			}
		}
	}
}
