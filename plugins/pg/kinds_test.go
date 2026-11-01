package main

import (
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/this-is-tobi/rta/pkg/view"
)

// typedRows is a result set of the columns and rows given, for what
// rowsToTable makes of the types and values pgx hands it.
type typedRows struct {
	fields []pgconn.FieldDescription
	rows   [][]any
	at     int
}

func (r *typedRows) Close()                                       {}
func (r *typedRows) Err() error                                   { return nil }
func (r *typedRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *typedRows) FieldDescriptions() []pgconn.FieldDescription { return r.fields }
func (r *typedRows) Next() bool                                   { r.at++; return r.at <= len(r.rows) }
func (r *typedRows) Scan(...any) error                            { return nil }
func (r *typedRows) Values() ([]any, error)                       { return r.rows[r.at-1], nil }
func (r *typedRows) RawValues() [][]byte                          { return nil }
func (r *typedRows) Conn() *pgx.Conn                              { return nil }

// A result's columns say what they hold, from the type each was declared
// with, as mysql.query's do: every column was text, so a count sorted as a
// word and nothing carried a value. The numbers are numbers and the three
// times are times, each written as its type holds it; money, interval and
// text stay text. A timestamp and a date have no zone, and are valued with
// none rather than with the UTC pgx hands them back in.
func TestAResultColumnHasTheKindItsTypeHolds(t *testing.T) {
	col := func(name string, oid uint32) pgconn.FieldDescription {
		return pgconn.FieldDescription{Name: name, DataTypeOID: oid}
	}
	rows := &typedRows{
		fields: []pgconn.FieldDescription{
			col("id", pgtype.Int8OID), col("total", pgtype.NumericOID), col("ratio", pgtype.Float8OID),
			col("seen", pgtype.TimestamptzOID), col("at", pgtype.TimestampOID), col("day", pgtype.DateOID),
			col("name", pgtype.TextOID), col("waited", pgtype.IntervalOID),
		},
		rows: [][]any{{
			int64(10),
			pgtype.Numeric{Int: big.NewInt(1234567890123456789), Exp: -2, Valid: true},
			1.5,
			time.Date(2026, 10, 31, 14, 2, 5, 25e7, time.FixedZone("CET", 3600)),
			time.Date(2026, 10, 31, 14, 2, 5, 123456e3, time.UTC),
			time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC),
			"web",
			pgtype.Interval{Days: 1, Microseconds: 7384e6, Valid: true},
		}},
	}
	tbl, err := rowsToTable(rows, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct {
		kind        view.ColumnKind
		text, value string
	}{
		{view.KindNumber, "10", "10"},
		{view.KindNumber, "12345678901234567.89", "12345678901234567.89"},
		{view.KindNumber, "1.5", "1.5"},
		{view.KindTimestamp, "2026-10-31T14:02:05.25+01:00", "2026-10-31T13:02:05.25Z"},
		{view.KindTimestamp, "2026-10-31 14:02:05.123456", "2026-10-31T14:02:05.123456"},
		{view.KindTimestamp, "2026-10-31", "2026-10-31"},
		{"", "web", ""},
		{"", "1 day 02:03:04", ""},
	} {
		c := tbl.Columns[i]
		if c.Kind != want.kind || tbl.Rows[0][i] != want.text || tbl.Value(0, i) != want.value {
			t.Errorf("%s: kind %q, text %q, value %q; want %q, %q, %q",
				c.Name, c.Kind, tbl.Rows[0][i], tbl.Value(0, i), want.kind, want.text, want.value)
		}
	}
}
