package main

import (
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A bucket's creation is a time a program reads as one, RFC 3339 in UTC,
// beside the date a person reads; a bucket named and not found carries none,
// and its row keeps every row after it on its own values.
func TestABucketsCreationIsValuedAsATime(t *testing.T) {
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("CET", 3600))
	tbl := bucketTable([]minio.BucketInfo{{Name: "a", CreationDate: created}, {Name: "b"}})
	tbl.Add(notFoundRow(tbl.Columns, "c")...)
	if len(tbl.Values) != len(tbl.Rows) {
		t.Fatalf("values for %d rows, and %d rows", len(tbl.Values), len(tbl.Rows))
	}
	last := len(tbl.Columns) - 1
	if tbl.Columns[last].Kind != view.KindTimestamp {
		t.Fatalf("Created is %q, want a timestamp", tbl.Columns[last].Kind)
	}
	for i, want := range []struct{ text, value string }{
		{"2026-03-04", "2026-03-04T04:06:07Z"},
		{"0001-01-01", ""},
		{"not found", ""},
	} {
		if got := tbl.Rows[i][last]; got != want.text {
			t.Errorf("row %d shows %q, want %q", i, got, want.text)
		}
		var value string
		if tbl.Values[i] != nil {
			value = tbl.Values[i][last]
		}
		if value != want.value {
			t.Errorf("row %d is valued %q, want %q", i, value, want.value)
		}
		if value != "" && !view.ValueOK(view.KindTimestamp, value) {
			t.Errorf("row %d's value %q is not one a timestamp column takes", i, value)
		}
	}
}
