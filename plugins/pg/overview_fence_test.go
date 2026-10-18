package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A profile fences a schema by naming it: only: {schema: [public]}. pg.table.list
// declares the input and holds to it, and pg.overview with `detail` lists the
// largest tables through the same helper without declaring it, so the fence had
// nothing to bind there: tables of every other schema, with their sizes, came
// out beside a fence written to keep them out.

// withArgs is a script that remembers what each statement was sent.
type withArgs struct {
	script
	sent *[]sentQuery
}

type sentQuery struct {
	sql  string
	args []any
}

func (q withArgs) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	*q.sent = append(*q.sent, sentQuery{sql, args})
	return q.script.Query(ctx, sql, args...)
}

func (q withArgs) argsOf(t *testing.T, fragment string) []any {
	t.Helper()
	for _, s := range *q.sent {
		if strings.Contains(s.sql, fragment) {
			return s.args
		}
	}
	t.Fatalf("no statement containing %q was sent", fragment)
	return nil
}

func TestASchemaFenceFillsTheOverviewAndRefusesEveryOtherSchema(t *testing.T) {
	c := findCapability(t, "pg.overview")
	fence := plugin.Fence{"schema": {"public"}}
	if breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: map[string]any{}}); breach != nil {
		t.Fatalf("a call naming nothing was refused under a one-entry fence: %+v", breach)
	}
	filled := plugin.ResolveRequest(c, plugin.Inputs{Fence: fence, Caller: map[string]any{}}, false, false)
	if got := filled.String("schema"); got != "public" {
		t.Errorf("the fence filled schema = %q, want public", got)
	}
	if plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: map[string]any{"schema": "hr"}}) == nil {
		t.Error("a schema outside the fence was let through")
	}
}

func TestTheDetailedOverviewOfAFencedProfileListsTheTablesOfThatSchemaOnly(t *testing.T) {
	q := withArgs{
		script: script{
			statusAnswer(),
			serverAnswer("170006", false),
			primaryAnswer("0/3000148", "000000010000000000000003"),
			{match: "s.sync_state"}, {match: "r.slot_type"},
			{match: "pg_stat_user_tables", cols: []string{"schemaname", "relname", "n_live_tup", "size"},
				rows: [][]any{{"public", "orders", int64(10), "8 kB"}}},
			{match: "pg_stat_activity", cols: []string{"pid", "usename", "application_name", "state", "seconds", "waiting"}},
		},
		sent: new([]sentQuery),
	}
	req := plugin.ResolveRequest(findCapability(t, "pg.overview"), plugin.Inputs{
		Fence: plugin.Fence{"schema": {"public"}}, Caller: map[string]any{"detail": true}}, false, false)
	v, err := detailedOverview(context.Background(), q, req)
	if err != nil {
		t.Fatal(err)
	}
	if args := q.argsOf(t, "pg_stat_user_tables"); len(args) == 0 || args[0] != "public" {
		t.Errorf("the largest tables were asked for with %v, want the schema first", args)
	}
	if rows := section(t, v, "largest tables").(view.Table).Rows; len(rows) != 1 || rows[0][0] != "public" {
		t.Errorf("largest tables = %v", rows)
	}
}

func section(t *testing.T, v view.View, title string) view.View {
	t.Helper()
	for _, s := range v.(view.Sections).Items {
		if s.Title == title {
			return s.View
		}
	}
	t.Fatalf("no section %q", title)
	return nil
}
