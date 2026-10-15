package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The two listings of what is on a server answer to one bound, in every SQL
// plugin: 50 by default and 1000 at most. A model that leaves the limit out pays
// in tokens for all of what comes back, so the default is a screen, and the
// ceiling is what one answer holds before the bridge cuts it.
func TestTheListingsOfWhatIsOnAServerShareOneBound(t *testing.T) {
	for id, help := range map[string]string{
		"pg.table.list":    "how many tables to show",
		"pg.database.list": "how many databases to show",
	} {
		var limit *plugin.Field
		for _, c := range Plugin().Capabilities {
			if c.ID != id {
				continue
			}
			for i := range c.Inputs {
				if c.Inputs[i].Name == "limit" {
					limit = &c.Inputs[i]
				}
			}
		}
		if limit == nil {
			t.Fatalf("%s declares no limit", id)
		}
		if limit.Default != 50 || limit.Min != 1 || limit.Max != 1000 {
			t.Errorf("%s: limit is default %v, from %v to %v, want default 50, from 1 to 1000", id, limit.Default, limit.Min, limit.Max)
		}
		if limit.Help != help {
			t.Errorf("%s: limit help = %q, want %q", id, limit.Help, help)
		}
	}
}

// askedWith is a script that remembers the arguments its statement was sent.
type askedWith struct {
	script
	args []any
}

func (a *askedWith) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	a.args = args
	return a.script.Query(ctx, sql, args...)
}

// The database listing was the one listing here with no bound: it read every
// database up to an internal ceiling and refused past that with a message about
// rows. It asks the server for one past its limit, which tells a listing that
// ends on its bound from one that was cut, and says so when it was.
func TestTheDatabaseListIsBoundedAndSaysWhenItStopped(t *testing.T) {
	q := &askedWith{script: script{{match: "pg_database", cols: []string{"datname", "size", "owner"},
		rows: [][]any{
			{"a", "9 MB", "o"}, {"b", "8 MB", "o"}, {"c", "7 MB", "o"}, {"d", "6 MB", "o"},
		}}}}
	v, err := databaseListView(context.Background(), q, reqFor(t, "pg.database.list", map[string]any{"limit": 3}))
	if err != nil {
		t.Fatal(err)
	}
	if len(q.args) != 1 || q.args[0] != 4 {
		t.Errorf("the statement was sent %v, want the limit and one more, 4, as its only argument", q.args)
	}
	table, ok := v.(view.Table)
	if !ok {
		t.Fatalf("view is %T, want a table", v)
	}
	if len(table.Rows) != 3 {
		t.Errorf("rows = %d, want the 3 the limit allows", len(table.Rows))
	}
	if len(table.Warnings) != 1 || table.Warnings[0].Code != "pg.list.partial" ||
		table.Warnings[0].Message != "stopped at 3 databases; there are more" {
		t.Errorf("warnings = %+v, want pg.list.partial saying it stopped at 3 databases", table.Warnings)
	}
}
