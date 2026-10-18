package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The overview read every collection of the instance one request at a time: 1500
// collections made 1503 requests, seven seconds and 195 KB a run, and a tile
// re-ran that at the host's pace. It reads at most a hundred, and says how many
// there are.

// manyRoutes is an instance of n collections, c000 upward, every one green but
// those named in status.
func manyRoutes(n int, status map[string]string) map[string]string {
	names := make([]string, 0, n)
	routes := map[string]string{"/": fxRoot, "/cluster": fxClusterDisabled}
	for i := range n {
		name := fmt.Sprintf("c%03d", i)
		names = append(names, `{"name":"`+name+`"}`)
		state := "green"
		if s, ok := status[name]; ok {
			state = s
		}
		routes["/collections/"+name] = envelope(`{"status":"` + state + `","points_count":10,` +
			`"indexed_vectors_count":10,"segments_count":1,"config":{"params":{"vectors":{"size":4,"distance":"Cosine"}}}}`)
	}
	routes["/collections"] = envelope(`{"collections":[` + strings.Join(names, ",") + `]}`)
	return routes
}

func collectionReads(f *fakeQdrant) int {
	n := 0
	for _, rec := range f.bodies {
		if strings.HasPrefix(rec.path, "/collections/") {
			n++
		}
	}
	return n
}

func TestTheOverviewReadsAtMostAHundredCollectionsAndSaysHowManyThereAre(t *testing.T) {
	f := newFakeQdrant(t, manyRoutes(150, nil))
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{"detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	if got := collectionReads(f); got != overviewCollections {
		t.Errorf("read %d collections one by one, want %d", got, overviewCollections)
	}
	table := section(t, v, "collections").(view.Table)
	if len(table.Rows) != overviewCollections || table.Total != 150 {
		t.Errorf("table = %d rows, total %d, want 100 of 150", len(table.Rows), table.Total)
	}
	got := pair(section(t, v, "status").(view.KeyValue), "collections")
	if !strings.HasPrefix(got, "100 of 150 — ") || !strings.Contains(got, "collection list") {
		t.Errorf("collections = %q, want how many of how many and where the rest is", got)
	}
	if len(table.Warnings) != 1 || table.Warnings[0].Code != "qdrant.collections.partial" {
		t.Errorf("warnings = %+v, want the one that says the list continues", table.Warnings)
	}
}

func TestAnInstanceOfExactlyAHundredIsNotPartial(t *testing.T) {
	f := newFakeQdrant(t, manyRoutes(overviewCollections, nil))
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{"detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	table := section(t, v, "collections").(view.Table)
	if table.Total != overviewCollections || len(table.Warnings) != 0 {
		t.Errorf("total %d, warnings %+v: a hundred of a hundred is the whole of it", table.Total, table.Warnings)
	}
	if got := pair(section(t, v, "status").(view.KeyValue), "collections"); got != "100" {
		t.Errorf("collections = %q, want 100", got)
	}
}

// The status of a collection is only known by reading it, so what can be put
// first is the worst of the ones read.
func TestTheCollectionsReadAreListedWorstFirst(t *testing.T) {
	f := newFakeQdrant(t, manyRoutes(5, map[string]string{"c001": "yellow", "c003": "red"}))
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{"detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, row := range section(t, v, "collections").(view.Table).Rows {
		order = append(order, row[0]+":"+row[4])
	}
	if got, want := strings.Join(order, " "), "c003:red c001:yellow c000:green c002:green c004:green"; got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
}

// The list is the report that reads every one, and keeps its order.
func TestTheCollectionListStillReadsEveryCollectionInNameOrder(t *testing.T) {
	f := newFakeQdrant(t, manyRoutes(150, map[string]string{"c120": "red"}))
	table, verr := collectionTable(t.Context(), reqAt(t, f, "qdrant.collection.list", map[string]any{}))
	if verr != nil {
		t.Fatal(verr)
	}
	if len(table.Rows) != 150 || table.Total != 150 || len(table.Warnings) != 0 {
		t.Errorf("rows %d, total %d, warnings %+v, want all 150", len(table.Rows), table.Total, table.Warnings)
	}
	if table.Rows[0][0] != "c000" || table.Rows[120][4] != "red" {
		t.Errorf("rows are not in name order: first %v, 121st %v", table.Rows[0], table.Rows[120])
	}
}

func TestTheOverviewTileCannotBeRefreshedFasterThanAMinute(t *testing.T) {
	c := capabilityOf(t, "qdrant.overview")
	if got := c.Pace(); got != time.Minute {
		t.Errorf("qdrant.overview is re-run every %s, want a minute", got)
	}
	if pace, cut := plugin.ClampPace(c, 5*time.Second); pace != time.Minute || !cut {
		t.Errorf("a configured 5s pace came to %s (cut %v), want the minute it cannot go below", pace, cut)
	}
}
