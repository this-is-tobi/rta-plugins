package main

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A profile fenced to several collections is the connection of a tile that names
// them, and the overview and the collection list took one name: a list read as
// text that is in no fence entry, and a call that names none under a multi-value
// fence is refused as blank. `collection` is now a list on those two. Naming
// none still reads every collection the key can see, naming one is the page it
// always was, and naming several reads exactly those, a request each and no
// listing of the instance.

// requestPaths is what the instance was asked, sorted: the collections of a list
// are read several at a time, so the order they arrive in is nobody's to promise
// and the set of them is.
func requestPaths(f *fakeQdrant) []string {
	out := make([]string, 0, len(f.bodies))
	for _, rec := range f.bodies {
		out = append(out, rec.path)
	}
	slices.Sort(out)
	return out
}

func overviewOf(t *testing.T, f *fakeQdrant, values map[string]any) view.View {
	t.Helper()
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", values))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTheCollectionOfTheTwoListingsIsAListAndTheOthersNameOne(t *testing.T) {
	for _, id := range []string{"qdrant.overview", "qdrant.collection.list"} {
		var field plugin.Field
		for _, in := range capabilityOf(t, id).Inputs {
			if in.Name == "collection" {
				field = in
			}
		}
		if field.Type != plugin.StringSlice || field.Required || field.Short != "c" || field.Config != "collection" {
			t.Errorf("%s: collection = %+v, want an optional list with the short c and the config key collection", id, field)
		}
	}
	for _, id := range []string{"qdrant.collection.show", "qdrant.points.count", "qdrant.points.scroll", "qdrant.dump", "qdrant.restore"} {
		var field plugin.Field
		for _, in := range capabilityOf(t, id).Inputs {
			if in.Name == "collection" {
				field = in
			}
		}
		if field.Type != plugin.String || !field.Required {
			t.Errorf("%s: collection = %+v, want the one required name it always was", id, field)
		}
	}
	// The grant's scope is the record a call names: it stays on the one capability
	// that has one, and the listings name no record.
	for id, want := range map[string]string{"qdrant.points.scroll": "collection", "qdrant.overview": "", "qdrant.collection.list": ""} {
		if got := capabilityOf(t, id).Scope; got != want {
			t.Errorf("%s: Scope = %q, want %q", id, got, want)
		}
	}
}

// The page of several is the page of the blank call restricted to them: counted
// by status, worst first, and a request for each and for nothing else — the
// instance's listing is not asked, which is what a fence on the names is for.
func TestSeveralCollectionsNamedAreTheOnesReadAndNoOther(t *testing.T) {
	f := newFakeQdrant(t, instanceRoutes())
	v := overviewOf(t, f, map[string]any{"collection": []string{"images", "docs"}, "detail": true})

	if got, want := requestPaths(f), []string{"/", "/cluster", "/collections/docs", "/collections/images"}; !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	var order []string
	for _, row := range section(t, v, "collections").(view.Table).Rows {
		order = append(order, row[0]+":"+row[4])
	}
	if got, want := strings.Join(order, " "), "images:red docs:green"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}
	table := section(t, v, "collections").(view.Table)
	if table.Total != 2 || len(table.Warnings) != 0 {
		t.Errorf("total %d, warnings %+v: two of two are the whole of it", table.Total, table.Warnings)
	}
	summary := section(t, v, "summary").(view.KeyValue)
	if got, want := pair(summary, "collections"), "2, 1 red, 1 green"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
	if got, want := pair(summary, "status"), "fail — images is red"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := pair(section(t, v, "status").(view.KeyValue), "collections"); got != "2" {
		t.Errorf("status block collections = %q, want the two read", got)
	}
}

// A list of one is the collection named, byte for byte, however the list was
// spelled: a flag given once, an array of one, a bare string from a client that
// did not read the schema, a name given twice, an empty one beside it.
func TestAListOfOneIsTheSingleCollectionItWas(t *testing.T) {
	// One server, so that the address the page prints is the same in every run.
	f := newFakeQdrant(t, instanceRoutes())
	for _, detail := range []bool{false, true} {
		f.bodies = nil
		want := overviewOf(t, f, map[string]any{"collection": "docs", "detail": detail})
		wantPaths := requestPaths(f)

		for name, value := range map[string]any{
			"a list":             []string{"docs"},
			"an array":           []any{"docs"},
			"twice and an empty": []string{"docs", "", "docs"},
		} {
			f.bodies = nil
			got := overviewOf(t, f, map[string]any{"collection": value, "detail": detail})
			if !reflect.DeepEqual(got, want) {
				t.Errorf("detail=%v, %s: the page differs from the single name's:\n got %#v\nwant %#v", detail, name, got, want)
			}
			if !reflect.DeepEqual(requestPaths(f), wantPaths) {
				t.Errorf("detail=%v, %s: requests = %v, want %v", detail, name, requestPaths(f), wantPaths)
			}
		}
	}
}

// The host holds each element of the list to the profile's fence. A call whose
// every name is in it passes, and one name outside it is refused whole.
func TestEveryNameOfTheListIsHeldToTheFence(t *testing.T) {
	fence := plugin.Fence{"collection": {"docs", "faq", "images"}}
	for _, id := range []string{"qdrant.overview", "qdrant.collection.list"} {
		c := capabilityOf(t, id)
		for name, tc := range map[string]struct {
			named   any
			refused bool
		}{
			"two of three":            {[]string{"docs", "faq"}, false},
			"all three":               {[]any{"docs", "faq", "images"}, false},
			"one of three, as a name": {"docs", false},
			"a fourth beside two":     {[]string{"docs", "ghost"}, true},
			"a fourth alone":          {[]string{"ghost"}, true},
			"a fourth, as a name":     {"ghost", true},
		} {
			breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: map[string]any{"collection": tc.named}})
			if (breach != nil) != tc.refused {
				t.Errorf("%s, %s: breach = %+v, want refused = %v", id, name, breach, tc.refused)
			}
		}
	}
}

func TestACollectionThatIsNotThereIsARowAndAWarningWhenSeveralAreNamed(t *testing.T) {
	f := newFakeQdrant(t, instanceRoutes())
	v := overviewOf(t, f, map[string]any{"collection": []string{"docs", "ghost"}, "detail": true})

	table := section(t, v, "collections").(view.Table)
	if len(table.Rows) != 2 || table.Rows[0][0] != "ghost" || table.Rows[0][4] != "not found" || table.Rows[1][0] != "docs" {
		t.Fatalf("rows = %v, want ghost, not found, ahead of docs", table.Rows)
	}
	summary := section(t, v, "summary").(view.KeyValue)
	if got, want := pair(summary, "collections"), "2, 1 not found, 1 green"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
	if got, want := pair(summary, "status"), "warn — ghost is not found"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}

	// Every one missing is still a page, with a verdict that says so.
	kv := compactOf(t, newFakeQdrant(t, instanceRoutes()), map[string]any{"collection": []string{"ghost", "phantom"}})
	if got, want := pair(kv, "status"), "warn — 2 collections not found, first ghost"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}

	// One name that is not there is the finding and an error, as it always was,
	// and so is the same name given twice.
	for _, named := range []any{"ghost", []string{"ghost"}, []string{"ghost", "ghost"}} {
		_, err := runOverview(t.Context(), reqAt(t, newFakeQdrant(t, instanceRoutes()), "qdrant.overview",
			map[string]any{"collection": named}))
		if verr, ok := err.(*view.Error); !ok || verr.Code != "qdrant.notfound" {
			t.Errorf("%v: err = %#v, want qdrant.notfound", named, err)
		}
	}
}

// One collection the key may not read does not end the page: it is a row that
// names it with the reason, the verdict says one could not be read, and the
// others answer.
func TestACollectionTheKeyMayNotReadDoesNotEndThePageOfSeveral(t *testing.T) {
	f := answering(t, instanceRoutes(), map[string]int{"/collections/faq": http.StatusForbidden})
	v := overviewOf(t, f, map[string]any{"collection": []string{"docs", "faq", "images"}, "detail": true})

	var order []string
	table := section(t, v, "collections").(view.Table)
	for _, row := range table.Rows {
		order = append(order, row[0]+":"+row[4])
	}
	if got, want := strings.Join(order, " "), "images:red faq:unreadable docs:green"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}
	if len(table.Warnings) != 1 || table.Warnings[0].Code != "qdrant.collection.unreadable" ||
		!strings.HasPrefix(table.Warnings[0].Message, "faq could not be read — ") ||
		!strings.Contains(table.Warnings[0].Message, "Global access is required") {
		t.Errorf("warnings = %+v, want one that names faq and says why", table.Warnings)
	}

	// The compact form, which is what a tile and an agent read, names it: the
	// verdict says which, and the row says why.
	f = answering(t, instanceRoutes(), map[string]int{"/collections/faq": http.StatusForbidden})
	kv := compactOf(t, f, map[string]any{"collection": []string{"docs", "faq"}})
	if got, want := pair(kv, "collections"), "2, 1 unreadable (denied), 1 green"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
	if got, want := pair(kv, "status"), "warn — faq could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}

	// Named alone, the one the key may not read is still the error it was.
	_, err := runOverview(t.Context(), reqAt(t, answering(t, instanceRoutes(), map[string]int{"/collections/faq": http.StatusForbidden}),
		"qdrant.overview", map[string]any{"collection": []string{"faq"}}))
	if verr, ok := err.(*view.Error); !ok || verr.Code != "qdrant.denied" {
		t.Errorf("err = %#v, want qdrant.denied", err)
	}
}

// A key scoped to its collections is refused the listing and reads the ones it
// is allowed. Naming them never asks for the listing, so nothing is lost.
func TestSeveralNamedCollectionsAreReadWhenTheKeyMayNotListTheInstance(t *testing.T) {
	f := answering(t, instanceRoutes(), map[string]int{"/collections": http.StatusForbidden})
	kv := compactOf(t, f, map[string]any{"collection": []string{"docs", "faq"}})
	if got, want := pair(kv, "collections"), "2, all green"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
	if got := pair(kv, "status"); got != "ok" {
		t.Errorf("status = %q, want ok", got)
	}
	for _, path := range requestPaths(f) {
		if path == "/collections" {
			t.Error("the instance's listing was asked for")
		}
	}
}

func namesFrom(first, n int) []string {
	names := make([]string, 0, n)
	for i := range n {
		names = append(names, fmt.Sprintf("c%03d", first+i))
	}
	return names
}

// A list is read whole or refused: the cap is how many a refresh may cost, and a
// list over it is a typo or a connection that should be two tiles. It is refused
// before the instance is asked anything, on both listings. The list may be the
// profile's fence filled in by the host, and the host withholds a fence's
// contents from an agent, so the refusal says what the cap is and that this call
// reaches more, and never how many it reaches nor that anybody named them.
func TestAListLongerThanTheCapIsRefusedBeforeAnythingIsAsked(t *testing.T) {
	asked := map[string]func(*fakeQdrant, plugin.Surface) error{
		"qdrant.overview": func(f *fakeQdrant, sf plugin.Surface) error {
			_, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview",
				map[string]any{"collection": namesFrom(0, 101)}).WithSurface(sf))
			return err
		},
		"qdrant.collection.list": func(f *fakeQdrant, sf plugin.Surface) error {
			_, verr := collectionTable(t.Context(), reqAt(t, f, "qdrant.collection.list",
				map[string]any{"collection": namesFrom(0, 101)}).WithSurface(sf))
			if verr == nil {
				return nil
			}
			return verr
		},
	}
	for id, ask := range asked {
		for _, tc := range []struct {
			sf           plugin.Surface
			wantHint     string
			forbiddenHas string
		}{
			{plugin.SurfaceCLI, "name fewer with --collection", ""},
			{plugin.SurfaceMCP, "ask the operator to fence fewer", "--collection"},
		} {
			f := newFakeQdrant(t, manyRoutes(150, nil))
			err := ask(f, tc.sf)
			verr, ok := err.(*view.Error)
			if !ok || verr.Code != "qdrant.collection.toomany" {
				t.Fatalf("%s, %s: err = %#v, want qdrant.collection.toomany", id, tc.sf, err)
			}
			if !strings.Contains(verr.Message, "at most 100 collections") || !strings.Contains(verr.Message, "this call reaches more") {
				t.Errorf("%s, %s: message = %q, want the cap and that this call reaches more", id, tc.sf, verr.Message)
			}
			for _, leak := range []string{"101", "named"} {
				if strings.Contains(verr.Message, leak) || strings.Contains(verr.Hint, leak) {
					t.Errorf("%s, %s: %q in %q / %q: the list may be the fence, and its size and who wrote it are withheld",
						id, tc.sf, leak, verr.Message, verr.Hint)
				}
			}
			if !strings.Contains(verr.Hint, tc.wantHint) || (tc.forbiddenHas != "" && strings.Contains(verr.Hint, tc.forbiddenHas)) {
				t.Errorf("%s, %s: hint = %q, want %q and not %q", id, tc.sf, verr.Hint, tc.wantHint, tc.forbiddenHas)
			}
			if len(f.bodies) != 0 {
				t.Errorf("%s, %s: %d requests were made before the list was refused: %v", id, tc.sf, len(f.bodies), requestPaths(f))
			}
		}
	}
}

// A hundred is the whole of what a list may be: all of them are read, none of it
// partial, on both listings. A name given twice is one name, so a list longer than
// the cap that repeats itself is not over it.
func TestAHundredNamesAreAllReadOnBothListings(t *testing.T) {
	f := newFakeQdrant(t, manyRoutes(150, nil))
	v := overviewOf(t, f, map[string]any{"collection": namesFrom(20, 100), "detail": true})
	table := section(t, v, "collections").(view.Table)
	if len(table.Rows) != 100 || table.Total != 100 || len(table.Warnings) != 0 {
		t.Errorf("rows %d, total %d, warnings %+v: a hundred named is a hundred read, none of it partial",
			len(table.Rows), table.Total, table.Warnings)
	}
	if got := collectionReads(f); got != 100 {
		t.Errorf("read %d collections, want 100", got)
	}

	f = newFakeQdrant(t, manyRoutes(150, nil))
	repeated := append(namesFrom(0, 100), namesFrom(0, 50)...)
	list, verr := collectionTable(t.Context(), reqAt(t, f, "qdrant.collection.list", map[string]any{"collection": repeated}))
	if verr != nil {
		t.Fatalf("a hundred names given as a hundred and fifty was refused: %v", verr)
	}
	if len(list.Rows) != 100 || list.Total != 100 || len(list.Warnings) != 0 {
		t.Errorf("rows %d, total %d, warnings %+v, want the hundred", len(list.Rows), list.Total, list.Warnings)
	}
	if got := collectionReads(f); got != 100 {
		t.Errorf("read %d collections, want 100", got)
	}
}

// What a refresh costs on a standalone instance of three collections, counted
// at the server: all of them are the three that every page makes beside the
// listing and one per collection; naming some skips the listing.
func TestWhatARefreshAsksForOneSeveralAndAllCollections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		named any
		want  []string
	}{
		{"all", nil, []string{"/", "/cluster", "/collections", "/collections/docs", "/collections/faq", "/collections/images"}},
		{"one", []string{"faq"}, []string{"/", "/cluster", "/collections/faq"}},
		{"two", []string{"images", "docs"}, []string{"/", "/cluster", "/collections/docs", "/collections/images"}},
		{"three", []string{"docs", "faq", "images"}, []string{"/", "/cluster", "/collections/docs", "/collections/faq", "/collections/images"}},
	} {
		f := newFakeQdrant(t, instanceRoutes())
		values := map[string]any{}
		if tc.named != nil {
			values["collection"] = tc.named
		}
		compactOf(t, f, values)
		if got := requestPaths(f); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: requests = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// On a distributed instance the detail asks each collection for its replicas
// too, and a name that is not there is a row, not a failed request for them.
func TestSeveralCollectionsOfAClusterAreReadWithTheirReplicas(t *testing.T) {
	routes := distributedRoutes()
	routes["/collections/docs4"] = fxInfo
	routes["/collections/docs4/cluster"] = fxShardsActive
	f := newFakeQdrant(t, routes)
	v := overviewOf(t, f, map[string]any{"collection": []string{"docs4", "ghost", "docs3"}, "detail": true})

	table := section(t, v, "collections").(view.Table)
	if len(table.Rows) != 3 || table.Columns[len(table.Columns)-1].Name != "Replicas" {
		t.Fatalf("columns %v, rows %v, want three rows and the replicas column", table.Columns, table.Rows)
	}
	cells := map[string]string{}
	for _, row := range table.Rows {
		cells[row[0]] = row[len(row)-1]
	}
	if !strings.HasPrefix(cells["docs3"], "fail — 7 of 9 replicas active") || !strings.HasPrefix(cells["docs4"], "ok") || cells["ghost"] != "-" {
		t.Errorf("replicas = %v, want docs3 failing, docs4 ok and ghost without one", cells)
	}
	for _, path := range requestPaths(f) {
		if path == "/collections" {
			t.Error("the instance's listing was asked for")
		}
	}
}

func TestTheCollectionListOfSeveralNamedCollectionsHoldsThoseInNameOrder(t *testing.T) {
	f := newFakeQdrant(t, instanceRoutes())
	table, verr := collectionTable(t.Context(), reqAt(t, f, "qdrant.collection.list",
		map[string]any{"collection": []string{"images", "docs", "ghost"}}))
	if verr != nil {
		t.Fatal(verr)
	}
	var rows []string
	for _, row := range table.Rows {
		rows = append(rows, row[0]+":"+row[4])
	}
	if got, want := strings.Join(rows, " "), "docs:green ghost:not found images:red"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}
	if table.Total != 3 || len(table.Warnings) != 0 {
		t.Errorf("total %d, warnings %+v, want the three named and nothing partial", table.Total, table.Warnings)
	}
	for _, path := range requestPaths(f) {
		if path == "/collections" {
			t.Error("the instance's listing was asked for")
		}
	}
}

// What a capability says of itself is what it does with the list, in sentences
// and not in the presence of a word: what a bare name reads, what none reads, how
// many a list may be, and what becomes of a collection that cannot be read.
func TestTheTextsSayWhatABareNameReadsAndWhatNamingNoneReads(t *testing.T) {
	const (
		bare = "a single name reads that collection alone"
		none = "none reads every collection the key can see, inside the profile's fence when it has one"
	)
	for _, id := range []string{"qdrant.overview", "qdrant.collection.list"} {
		c := capabilityOf(t, id)
		for _, want := range []string{bare, none, "at most 100"} {
			if !strings.Contains(c.Description, want) {
				t.Errorf("%s: the description does not say %q:\n%s", id, want, c.Description)
			}
		}
		for _, want := range []string{none, "some read exactly those", "at most 100"} {
			if !strings.Contains(c.AgentText(), want) {
				t.Errorf("%s: what an agent reads does not say %q:\n%s", id, want, c.AgentText())
			}
		}
		for _, in := range c.Inputs {
			if in.Name == "collection" && (!strings.Contains(in.Help, "inside the profile's fence when it has one") ||
				strings.Contains(in.Help, "when none is named")) {
				t.Errorf("%s: the help of collection = %q, want what none reads, inside the fence", id, in.Help)
			}
		}
	}
	if agent := capabilityOf(t, "qdrant.overview").Agent; !strings.Contains(agent, "one it cannot read is named in the verdict") ||
		strings.Contains(agent, "warning row") {
		t.Errorf("the overview's agent text = %q, want what becomes of a collection it cannot read", agent)
	}
	// The config keeps one name: a list goes in a tile's with:, the call or a
	// profile's only: fence, because the other capabilities that share the key
	// take text.
	if list := capabilityOf(t, "qdrant.collection.list").Description; !strings.Contains(list, "in the config or a profile's `set:` it stays one name") {
		t.Errorf("the collection list does not say that the config keeps one name:\n%s", list)
	}
}

// A comma splits a value of a list given on the command line, and Qdrant allows
// one in a collection's name. The help of the input says so, where the flag's
// usage and the README's table both print it.
func TestTheHelpOfTheListSaysHowANameWithACommaIsWritten(t *testing.T) {
	for _, id := range []string{"qdrant.overview", "qdrant.collection.list"} {
		for _, in := range capabilityOf(t, id).Inputs {
			if in.Name == "collection" && !strings.Contains(in.Help, "quote a name with a comma") {
				t.Errorf("%s: the help of collection = %q, want how a name with a comma is written", id, in.Help)
			}
		}
	}
}
