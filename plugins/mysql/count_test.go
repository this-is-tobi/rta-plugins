package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A count of one reads in the singular wherever this plugin prints one. These
// are the two that said "1 more tables" and "more than 1 rows" when a --limit
// left exactly one out.

func TestOneTableLeftOutOfTheSchemaIsCountedInTheSingular(t *testing.T) {
	db := fakeDB(t, []string{"TABLE_NAME", "COLUMN_NAME", "COLUMN_TYPE", "IS_NULLABLE", "COLUMN_KEY", "EXTRA"},
		[][]driver.Value{
			{[]byte("customers"), []byte("id"), []byte("bigint"), []byte("NO"), []byte("PRI"), []byte("")},
			{[]byte("orders"), []byte("id"), []byte("bigint"), []byte("NO"), []byte("PRI"), []byte("")},
		})
	v, err := schemaTree(context.Background(), db, req(t, "mysql.schema", map[string]any{"schema": "shop", "limit": 1}))
	if err != nil {
		t.Fatal(err)
	}
	// A fork that checks its grants may wrap the tree in a caveat about
	// tables it cannot see; the count is inside the tree either way.
	if s, wrapped := v.(view.Sections); wrapped && len(s.Items) > 0 {
		v = s.Items[0].View
	}
	tree, ok := v.(view.Tree)
	if !ok {
		t.Fatalf("schema returned %s, want a Tree", view.TypeOf(v))
	}
	children := tree.Roots[0].Children
	if len(children) != 2 {
		t.Fatalf("children = %+v, want one table and the marker for the other", children)
	}
	if got := children[1].Detail; !strings.HasPrefix(got, "1 more table;") {
		t.Errorf("marker = %q, want it to count one table in the singular", got)
	}
}

func TestALimitOfOneRowIsRefusedInTheSingular(t *testing.T) {
	db := fakeDB(t, []string{"n"}, [][]driver.Value{{int64(1)}, {int64(2)}})

	_, err := queryView(context.Background(), db, req(t, "mysql.query", map[string]any{"sql": "select n", "limit": 1}))
	var verr *view.Error
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want the row-bound refusal", err)
	}
	if !strings.HasSuffix(verr.Message, "more than 1 row") {
		t.Errorf("message = %q, want the bound counted in the singular", verr.Message)
	}
}
