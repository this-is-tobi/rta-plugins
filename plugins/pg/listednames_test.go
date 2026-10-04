package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A name a stranger can choose — a role, a database, a schema, a table, a
// column — is shown as it is when it reads as itself and written out when it
// does not. Created with `create table "orders<ESC>[2K"`, a table came out of a
// listing as `orders`, the name of another table, and one with a newline split
// its row in two; nothing told the reader either name was odd.
//
// Every test here drives a view the way a call does, through a querier that
// answers from a script, and puts the same two names through it: one holding an
// escape sequence and a newline, which must come out quoted with both written
// out, and one that is only spaces and an accent, which must come out as it
// went in.
const (
	oddName   = "esc\x1b[31mred\nline"
	oddShown  = `"esc\x1b[31mred\nline"`
	plainName = "café table"
)

// answer is what a script says to a query that contains match.
type answer struct {
	match string
	cols  []string
	rows  [][]any
}

// script is a querier that answers from a list, in order, and refuses what it
// was not told about — which a view that treats a failed read as "leave that
// line out" reads as exactly that.
type script []answer

func (s script) find(sql string) (answer, bool) {
	for _, a := range s {
		if strings.Contains(sql, a.match) {
			return a, true
		}
	}
	return answer{}, false
}

func (s script) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	a, ok := s.find(sql)
	if !ok {
		return nil, errors.New("not scripted: " + sql)
	}
	return &valueRows{cols: a.cols, rows: a.rows}, nil
}

func (s script) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	a, ok := s.find(sql)
	if !ok || len(a.rows) == 0 {
		return valueRow{err: errors.New("not scripted: " + sql)}
	}
	return valueRow{vals: a.rows[0]}
}

type valueRow struct {
	vals []any
	err  error
}

func (r valueRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return assignAll(dest, r.vals)
}

// valueRows is a result set of the values it was given, which is what a view
// that reads names from the catalogue scans, and what one that renders any
// result reads as values.
type valueRows struct {
	cols []string
	rows [][]any
	at   int
}

func (r *valueRows) Close()                        {}
func (r *valueRows) Err() error                    { return nil }
func (r *valueRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *valueRows) FieldDescriptions() []pgconn.FieldDescription {
	out := make([]pgconn.FieldDescription, 0, len(r.cols))
	for _, c := range r.cols {
		out = append(out, pgconn.FieldDescription{Name: c})
	}
	return out
}
func (r *valueRows) Next() bool {
	if r.at >= len(r.rows) {
		return false
	}
	r.at++
	return true
}
func (r *valueRows) Scan(dest ...any) error { return assignAll(dest, r.rows[r.at-1]) }
func (r *valueRows) Values() ([]any, error) { return r.rows[r.at-1], nil }
func (r *valueRows) RawValues() [][]byte    { return nil }
func (r *valueRows) Conn() *pgx.Conn        { return nil }
func assignAll(dest, vals []any) error {
	if len(dest) != len(vals) {
		return errors.New("scanned the wrong number of columns")
	}
	for i, v := range vals {
		d := reflect.ValueOf(dest[i]).Elem()
		if v == nil {
			d.SetZero()
			continue
		}
		val := reflect.ValueOf(v)
		if d.Kind() == reflect.Pointer {
			p := reflect.New(d.Type().Elem())
			p.Elem().Set(val.Convert(d.Type().Elem()))
			d.Set(p)
			continue
		}
		d.Set(val.Convert(d.Type()))
	}
	return nil
}

// bare fails the test when text still holds a character that would reach a
// terminal or split a line.
func bare(t *testing.T, what, text string) {
	t.Helper()
	if strings.ContainsAny(text, "\x1b\n") {
		t.Errorf("%s still holds the raw escape or newline: %q", what, text)
	}
}

func rowsOf(t *testing.T, v view.View, err error) [][]string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	table, ok := v.(view.Table)
	if !ok {
		t.Fatalf("view is %T, want a table", v)
	}
	return table.Rows
}

func TestATableListShowsAnOddNameWrittenOut(t *testing.T) {
	q := script{{match: "pg_stat_user_tables",
		cols: []string{"schemaname", "relname", "n_live_tup", "size"},
		rows: [][]any{
			{oddName, "orders", int64(10), "8 kB"},
			{"public", oddName, int64(3), "16 kB"},
			{"my schema é", plainName, int64(7), "24 kB"},
		}}}
	v, err := tableListView(context.Background(), q, reqFor(t, "pg.table.list", nil), true)
	rows := rowsOf(t, v, err)
	want := [][]string{
		{oddShown, "orders", "10", "8 kB"},
		{"public", oddShown, "3", "16 kB"},
		{"my schema é", plainName, "7", "24 kB"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
}

func TestADatabaseListShowsAnOddDatabaseAndOwnerWrittenOut(t *testing.T) {
	q := script{{match: "pg_database", cols: []string{"datname", "size", "owner"},
		rows: [][]any{
			{"db" + oddName, "8 kB", oddName},
			{plainName, "9 MB", "app role é"},
		}}}
	v, err := databaseListView(context.Background(), q, reqFor(t, "pg.database.list", nil))
	rows := rowsOf(t, v, err)
	want := [][]string{
		{`"dbesc\x1b[31mred\nline"`, "8 kB", oddShown},
		{plainName, "9 MB", "app role é"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
}

// A session's role is a name a stranger chose. Its application name is not
// shown through ListedName, because the server has already written it as
// printable ASCII — and a session that gave none stays an empty cell, not a
// pair of quotes.
func TestTheActivityListShowsAnOddRoleWrittenOutAndAnEmptyApplicationAsEmpty(t *testing.T) {
	q := script{{match: "pg_stat_activity",
		cols: []string{"pid", "usename", "application_name", "state", "seconds", "waiting"},
		rows: [][]any{
			{int32(10), oddName, `psql\x1b`, "active", int32(3), "Lock"},
			{int32(11), "app role é", "", "idle", int32(9), ""},
		}}}
	v, err := activityView(context.Background(), q, reqFor(t, "pg.activity", nil), false, true)
	rows := rowsOf(t, v, err)
	want := [][]string{
		{"10", oddShown, `psql\x1b`, "active", "3", "Lock"},
		{"11", "app role é", "", "idle", "9", ""},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
}

func TestTheStatusShowsAnOddDatabaseAndRoleWrittenOut(t *testing.T) {
	status := func(db, user string) view.KeyValue {
		q := script{{match: "version()", rows: [][]any{{"PostgreSQL 17", db, user, "8 MB"}}}}
		v, err := statusView(context.Background(), q, reqFor(t, "pg.status", nil))
		if err != nil {
			t.Fatal(err)
		}
		return v.(view.KeyValue)
	}
	pairs := map[string]string{}
	for _, p := range status(oddName, oddName).Pairs {
		pairs[p.Key] = p.Value
	}
	if pairs["database"] != oddShown || pairs["connected as"] != oddShown {
		t.Errorf("pairs = %q, want both names written out", pairs)
	}
	pairs = map[string]string{}
	for _, p := range status("app é", "app role é").Pairs {
		pairs[p.Key] = p.Value
	}
	if pairs["database"] != "app é" || pairs["connected as"] != "app role é" {
		t.Errorf("pairs = %q, want both names as they are", pairs)
	}
}

func TestTheOverviewGlanceShowsAnOddDatabaseWrittenOut(t *testing.T) {
	glance := func(db string) string {
		q := script{{match: "pg_size_pretty(pg_database_size(current_database()))", rows: [][]any{{db, "8 MB"}}}}
		v, err := compactOverview(context.Background(), q, reqFor(t, "pg.overview", nil))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range v.(view.KeyValue).Pairs {
			if p.Key == "database" {
				return p.Value
			}
		}
		t.Fatal("no database line")
		return ""
	}
	if got, want := glance(oddName), oddShown+" · 8 MB"; got != want {
		t.Errorf("database = %q, want %q", got, want)
	}
	if got, want := glance(plainName), plainName+" · 8 MB"; got != want {
		t.Errorf("database = %q, want %q", got, want)
	}
}

// The schemas a refusal lists are the ones the server has, so each is shown as
// a list shows a name; the one asked for is what the caller typed, quoted as
// it always was.
func TestTheSchemasARefusalListsAreWrittenOut(t *testing.T) {
	q := script{{match: "pg_namespace", cols: []string{"nspname"},
		rows: [][]any{{"public"}, {oddName}, {"my schema é"}}}}
	verr := unknownSchema(context.Background(), q, reqFor(t, "pg.schema.dump", nil), "absent")
	if verr == nil || verr.Code != "pg.schema.missing" {
		t.Fatalf("got %v, want pg.schema.missing", verr)
	}
	if want := "this database has: public, " + oddShown + ", my schema é"; verr.Hint != want {
		t.Errorf("hint = %q, want %q", verr.Hint, want)
	}
	bare(t, "the refusal", verr.Message+verr.Hint)
}

func TestTwoTablesOfOneNameAreListedWrittenOut(t *testing.T) {
	q := script{{match: "from pg_class c join pg_namespace n", cols: []string{"oid", "nspname", "relname"},
		rows: [][]any{{uint32(1), oddName, "orders"}, {uint32(2), "public", "orders"}, {uint32(3), "my schema é", "orders"}}}}
	_, verr := resolveRelation(context.Background(), q, reqFor(t, "pg.table.dump", map[string]any{"table": "orders"}))
	if verr == nil || verr.Code != "pg.table.ambiguous" {
		t.Fatalf("got %v, want pg.table.ambiguous", verr)
	}
	if want := "say which: " + oddShown + ".orders, public.orders, my schema é.orders"; verr.Hint != want {
		t.Errorf("hint = %q, want %q", verr.Hint, want)
	}
}

// What a refusal about a table says of it, and of its columns, is written the
// way a list shows them. The column asked for is what the caller typed.
func TestADumpRefusalNamesTheTableAndItsColumnsWrittenOut(t *testing.T) {
	rel := relation{oid: 42, schema: oddName, name: plainName}
	columns := script{
		{match: "pg_index"},
		{match: "pg_attribute", cols: []string{"attname"}, rows: [][]any{{oddName}, {"my col é"}}},
	}
	_, verr := dumpRows(context.Background(), columns,
		reqFor(t, "pg.table.dump", map[string]any{"table": "x", "columns": "absent"}), rel)
	if verr == nil || verr.Code != "pg.column.missing" {
		t.Fatalf("got %v, want pg.column.missing", verr)
	}
	if want := oddShown + "." + plainName + ` has no column "absent"`; verr.Message != want {
		t.Errorf("message = %q, want %q", verr.Message, want)
	}
	if want := "it has: " + oddShown + ", my col é"; verr.Hint != want {
		t.Errorf("hint = %q, want %q", verr.Hint, want)
	}

	_, verr = dumpRows(context.Background(), script{{match: "pg_attribute"}, {match: "pg_index"}},
		reqFor(t, "pg.table.dump", map[string]any{"table": "x"}), rel)
	if verr == nil || verr.Code != "pg.table.nocolumns" ||
		verr.Message != oddShown+"."+plainName+" has no readable columns" {
		t.Errorf("got %v, want pg.table.nocolumns naming the table written out", verr)
	}

	over := script{
		{match: "pg_index"},
		{match: "pg_attribute", cols: []string{"attname"}, rows: [][]any{{"id"}}},
		{match: "limit $1", cols: []string{"id"}, rows: [][]any{{1}, {2}}},
	}
	_, verr = dumpRows(context.Background(), over,
		reqFor(t, "pg.table.dump", map[string]any{"table": "x", "limit": 1}), rel)
	if verr == nil || verr.Code != "pg.dump.toomany" ||
		verr.Message != oddShown+"."+plainName+" has more than 1 row" {
		t.Errorf("got %v, want pg.dump.toomany naming the table written out", verr)
	}
}

// A result's headings are the names of columns somebody else created; what is
// under them is data, and is the body it always was — a value with a newline
// in it is an ordinary value.
func TestAResultsHeadingsAreWrittenOutAndItsValuesAreNot(t *testing.T) {
	rows := &valueRows{cols: []string{oddName, "id", plainName},
		rows: [][]any{{"line one\nline two", int64(1), "café"}}}
	table, err := rowsToTable(rows, 10)
	if err != nil {
		t.Fatal(err)
	}
	var headings []string
	for _, c := range table.Columns {
		headings = append(headings, c.Name)
	}
	if want := []string{oddShown, "id", plainName}; !reflect.DeepEqual(headings, want) {
		t.Errorf("headings = %q, want %q", headings, want)
	}
	if want := []string{"line one\nline two", "1", "café"}; !reflect.DeepEqual(table.Rows[0], want) {
		t.Errorf("row = %q, want the values as they are", table.Rows[0])
	}
}

func TestASlotsDatabaseIsWrittenOut(t *testing.T) {
	f := replicationFacts{slots: []slotRow{
		{name: "slot_a", kind: "logical", database: oddName},
		{name: "slot_b", kind: "logical", database: plainName},
		{name: "slot_c", kind: "physical"},
	}}
	table := slotsTable(f)
	var got []string
	for _, row := range table.Rows {
		got = append(got, row[2])
	}
	if want := []string{oddShown, plainName, "-"}; !reflect.DeepEqual(got, want) {
		t.Errorf("databases = %q, want %q", got, want)
	}
}

// The refusals that name the table a dump read — a result too big to return and
// an empty one that row-level security may have emptied — write it as a list
// does, and a table that reads as itself is named as it was.
func TestTheSizeAndPolicyRefusalsOfADumpNameTheTableWrittenOut(t *testing.T) {
	dump := func(rel relation, rows [][]any, extra ...answer) *view.Error {
		q := append(script{
			{match: "pg_index"},
			{match: "pg_attribute", cols: []string{"attname"}, rows: [][]any{{"body"}}},
			{match: "limit $1", cols: []string{"body"}, rows: rows},
		}, extra...)
		_, verr := dumpRows(context.Background(), q, reqFor(t, "pg.table.dump", map[string]any{"table": "x"}), rel)
		if verr == nil {
			t.Fatal("the dump was not refused")
		}
		return verr
	}
	huge := [][]any{{strings.Repeat("x", maxBytes+1)}}
	policy := answer{match: "relrowsecurity", rows: [][]any{{true, false}}}

	for rel, name := range map[relation]string{
		{oid: 1, schema: oddName, name: plainName}:      oddShown + "." + plainName,
		{oid: 2, schema: "my schema é", name: oddName}:  "my schema é." + oddShown,
		{oid: 3, schema: "my schema é", name: "orders"}: "my schema é.orders",
	} {
		toolarge := dump(rel, huge)
		if toolarge.Code != "pg.dump.toolarge" || !strings.HasPrefix(toolarge.Message, "the rows of "+name+" are over") {
			t.Errorf("got %s %q, want pg.dump.toolarge naming %s", toolarge.Code, toolarge.Message, name)
		}
		bare(t, "the refusal", toolarge.Message+toolarge.Hint)

		rls := dump(rel, nil, policy)
		if rls.Code != "pg.dump.rls" || !strings.HasPrefix(rls.Message, name+" returned no rows") {
			t.Errorf("got %s %q, want pg.dump.rls naming %s", rls.Code, rls.Message, name)
		}
		bare(t, "the refusal", rls.Message+rls.Hint)
	}
}

// The schema a description is too big to return is named the way a message
// always named it when it reads as itself, and written out when it does not —
// including by a character %q leaves raw, because strconv counts a Hangul
// filler printable and a reader sees nothing of it.
func TestTheSchemaADescriptionIsTooBigToReturnIsNamedWrittenOut(t *testing.T) {
	columns := make([][]any, 50000)
	for i := range columns {
		columns[i] = []any{"big", fmt.Sprintf("column_%06d", i), "text", false, false}
	}
	describe := func(schema string) *view.Error {
		q := script{
			{match: "select nspname from pg_namespace", cols: []string{"nspname"}, rows: [][]any{{schema}}},
			{match: "select c.relname, c.relkind = 'p'", cols: []string{"relname", "partitioned"},
				rows: [][]any{{"big", false}}},
			{match: "format_type(a.atttypid", rows: columns},
			{match: "pg_get_constraintdef"},
			{match: "pg_get_indexdef"},
			{match: "from pg_proc p", rows: [][]any{{int64(0), int64(0), int64(0), int64(0)}}},
		}
		_, verr := schemaDDL(context.Background(), q,
			reqFor(t, "pg.schema.dump", map[string]any{"schema": schema}))
		if verr == nil {
			t.Fatal("the description was not refused")
		}
		return verr.(*view.Error)
	}
	for schema, want := range map[string]string{
		oddName:           "the description of schema " + oddShown + " is ",
		"hangul\u3164end": `the description of schema "hangul\u3164end" is `,
		"my schema é":     `the description of schema "my schema é" is `,
	} {
		verr := describe(schema)
		if verr.Code != "pg.schema.toolarge" || !strings.HasPrefix(verr.Message, want) {
			t.Errorf("got %s %q, want pg.schema.toolarge opening %q", verr.Code, verr.Message, want)
		}
		bare(t, "the refusal", verr.Message+verr.Hint)
	}
}
