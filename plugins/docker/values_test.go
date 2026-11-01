package main

import (
	"slices"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A size is shown as the CLI wrote it and valued as the bytes that text says,
// in decimal units as docker counts them, rounded to the byte; a size the CLI
// did not write as one has no value, rather than reading as zero to a program.
func TestASizeIsValuedAsTheBytesTheCLIWrote(t *testing.T) {
	images := imageTable([]imageRow{
		{Repository: "app", Tag: "v1", ID: "sha256:aaaa", Size: "4.35GB"},
		{Repository: "app", Tag: "v0", ID: "sha256:bbbb", Size: "N/A"},
	})
	disk := diskTable([]diskRow{{Type: "Images", TotalCount: "3", Active: "1", Size: "1.5kB", Reclaimable: "1kB (66%)"}})
	for _, tc := range []struct {
		name        string
		table       view.Table
		row         int
		text, value string
	}{
		{"images", images, 0, "4.35GB", "4350000000"},
		{"images", images, 1, "N/A", ""},
		{"disk", disk, 0, "1.5kB", "1500"},
	} {
		col := slices.IndexFunc(tc.table.Columns, func(c view.Column) bool { return c.Name == "size" })
		if got := tc.table.Rows[tc.row][col]; got != tc.text {
			t.Errorf("%s row %d shows %q, want %q", tc.name, tc.row, got, tc.text)
		}
		var value string
		if tc.row < len(tc.table.Values) && tc.table.Values[tc.row] != nil {
			value = tc.table.Values[tc.row][col]
		}
		if value != tc.value {
			t.Errorf("%s row %d is valued %q, want %q", tc.name, tc.row, value, tc.value)
		}
		if value != "" && !view.ValueOK(view.KindBytes, value) {
			t.Errorf("%s row %d's value %q is not one a size column takes", tc.name, tc.row, value)
		}
	}
}
