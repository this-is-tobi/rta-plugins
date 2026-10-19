package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A profile that fences `collection` says which collection the connection is
// for, and a fence binds only the inputs a capability declares. Neither listing
// declared one, so `only: collection: [docs]` held every other capability and
// left the overview and the collection list naming each collection of the
// instance, with its point count and status: the inventory the fence was
// written to keep out.

func capabilityOf(t *testing.T, id string) plugin.Capability {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no capability %q", id)
	return plugin.Capability{}
}

// instanceRoutes is an instance of three collections, one of which is the one
// a connection is for.
func instanceRoutes() map[string]string {
	return map[string]string{
		"/":            fxRoot,
		"/cluster":     fxClusterDisabled,
		"/collections": envelope(`{"collections":[{"name":"docs"},{"name":"faq"},{"name":"images"}]}`),
		"/collections/docs": envelope(`{"status":"green","points_count":1204331,"indexed_vectors_count":1204331,` +
			`"segments_count":8,"config":{"params":{"vectors":{"size":768,"distance":"Cosine"}}}}`),
		"/collections/faq": envelope(`{"status":"green","points_count":4120,"indexed_vectors_count":4120,` +
			`"segments_count":2,"config":{"params":{"vectors":{"size":768,"distance":"Cosine"}}}}`),
		"/collections/images": envelope(`{"status":"red","points_count":12000000,"indexed_vectors_count":0,` +
			`"segments_count":30,"config":{"params":{"vectors":{"size":768,"distance":"Cosine"}}}}`),
	}
}

func TestAFencedCollectionFillsTheListingsAndAnotherIsRefused(t *testing.T) {
	for _, id := range []string{"qdrant.overview", "qdrant.collection.list"} {
		t.Run(id, func(t *testing.T) {
			c := capabilityOf(t, id)
			fence := plugin.Fence{"collection": {"docs"}}
			if breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: map[string]any{}}); breach != nil {
				t.Fatalf("a call naming nothing was refused under a one-entry fence: %+v", breach)
			}
			filled := plugin.ResolveRequest(c, plugin.Inputs{Fence: fence, Caller: map[string]any{}}, false, false)
			if got := filled.StringSlice("collection"); len(got) != 1 || got[0] != "docs" {
				t.Errorf("the fence filled collection = %q, want docs", got)
			}
			if plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: map[string]any{"collection": "images"}}) == nil {
				t.Error("a collection outside the fence was let through")
			}
		})
	}
}

func TestTheOverviewOfANamedCollectionIsAboutThatCollectionAndReadsNoOther(t *testing.T) {
	f := newFakeQdrant(t, instanceRoutes())
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{"collection": "docs", "detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	if got := pair(section(t, v, "status").(view.KeyValue), "collections"); got != "1" {
		t.Errorf("collections = %q, want the one shown", got)
	}
	rows := section(t, v, "collections").(view.Table).Rows
	if len(rows) != 1 || rows[0][0] != "docs" || rows[0][1] != "1204331" || rows[0][4] != "green" {
		t.Errorf("rows = %v, want docs alone", rows)
	}
	for _, rec := range f.bodies {
		if strings.Contains(rec.path, "faq") || strings.Contains(rec.path, "images") {
			t.Errorf("asked for %s: a collection the call is not about", rec.path)
		}
	}
}

func TestTheCollectionListOfANamedCollectionHoldsThatCollectionAlone(t *testing.T) {
	f := newFakeQdrant(t, instanceRoutes())
	table, verr := collectionTable(t.Context(), reqAt(t, f, "qdrant.collection.list", map[string]any{"collection": "docs"}))
	if verr != nil {
		t.Fatal(verr)
	}
	if len(table.Rows) != 1 || table.Rows[0][0] != "docs" || table.Total != 1 {
		t.Errorf("rows = %v (total %d), want docs alone", table.Rows, table.Total)
	}

	table, verr = collectionTable(t.Context(), reqAt(t, f, "qdrant.collection.list", map[string]any{}))
	if verr != nil {
		t.Fatal(verr)
	}
	if len(table.Rows) != 3 {
		t.Errorf("rows = %d, want every collection when none is named", len(table.Rows))
	}
}

// The page is about the collection it was given, so one that is not there is
// the finding and not a row that reads as a collection the key may not see.
func TestACollectionThatIsNotThereIsAnErrorNotAnUnreadableRow(t *testing.T) {
	f := newFakeQdrant(t, instanceRoutes())
	_, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{"collection": "absent"}))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "qdrant.notfound" {
		t.Fatalf("err = %#v, want qdrant.notfound", err)
	}
}
