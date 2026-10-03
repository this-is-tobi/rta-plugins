package main

import (
	"context"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Over MCP the cursor is a bare `page.next` with no input named beside it, so
// a scroll that stopped says so in words and names the argument that continues
// it, which etcd.kv.list does for the same reason. The input is `offset`
// because the id is the first point of the next page, not the last of this one.
func TestAStoppedScrollNamesTheArgumentThatContinuesIt(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections/docs/points/scroll": `{"result":{"points":[
			{"id":1,"payload":{}},{"id":2,"payload":{}}
		],"next_page_offset":3}}`,
	})
	v, err := runPointsScroll(context.Background(),
		reqAt(t, f, "qdrant.points.scroll", map[string]any{"collection": "docs"}).WithSurface(plugin.SurfaceMCP))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	if len(tbl.Warnings) != 1 || tbl.Warnings[0].Code != "qdrant.points.scroll.partial" {
		t.Fatalf("warnings = %+v, want the one that says it stopped", tbl.Warnings)
	}
	if want := `the "offset" argument set to "3"`; !strings.Contains(tbl.Warnings[0].Hint, want) {
		t.Errorf("hint = %q, want it to contain %s", tbl.Warnings[0].Hint, want)
	}
}

// The last page carries neither a cursor nor a warning.
func TestAScrollThatReachedTheEndSaysNothingAboutMore(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections/docs/points/scroll": `{"result":{"points":[{"id":1,"payload":{}}],"next_page_offset":null}}`,
	})
	v, err := runPointsScroll(context.Background(),
		reqAt(t, f, "qdrant.points.scroll", map[string]any{"collection": "docs"}).WithSurface(plugin.SurfaceMCP))
	if err != nil {
		t.Fatal(err)
	}
	if tbl := v.(view.Table); tbl.Page != nil || len(tbl.Warnings) != 0 {
		t.Errorf("the last page reported more: page = %+v, warnings = %+v", tbl.Page, tbl.Warnings)
	}
}
