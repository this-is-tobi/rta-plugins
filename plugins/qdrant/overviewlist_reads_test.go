package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// How a list of collections is read: several at a time, each held to the
// overview's bound, the whole to one deadline, and a collection that goes wrong
// in any way a request can is a row of its own while the others answer.

// servingWith is answering for a request that has to go wrong in a way a status
// cannot say: the paths in odd are answered by their handler, every other by
// routes, and every request is recorded.
func servingWith(t *testing.T, routes map[string]string, odd map[string]http.HandlerFunc) *fakeQdrant {
	t.Helper()
	f := &fakeQdrant{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record(recorded{path: r.URL.EscapedPath()})
		if h, ok := odd[r.URL.Path]; ok {
			h(w, r)
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.Close)
	return f
}

// hang is an instance that took the connection and says nothing, until the
// caller gives up on it.
func hang(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

// dropped is an instance that closes the connection without an answer.
func dropped(w http.ResponseWriter, _ *http.Request) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func serverError(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"status":{"error":"upstream trouble"}}`))
}

// readsOf is how the two listings are asked for a list of collections, each
// returning the rows it came to and the warnings that went with them.
type readsOf func(t *testing.T, f *fakeQdrant, names []string) (rows [][]string, warnings []view.Error)

var bothListings = map[string]readsOf{
	"qdrant.overview": func(t *testing.T, f *fakeQdrant, names []string) ([][]string, []view.Error) {
		t.Helper()
		table := section(t, overviewOf(t, f, map[string]any{"collection": names, "detail": true}), "collections").(view.Table)
		return table.Rows, table.Warnings
	},
	"qdrant.collection.list": func(t *testing.T, f *fakeQdrant, names []string) ([][]string, []view.Error) {
		t.Helper()
		table, verr := collectionTable(t.Context(), reqAt(t, f, "qdrant.collection.list", map[string]any{"collection": names}))
		if verr != nil {
			t.Fatal(verr)
		}
		return table.Rows, table.Warnings
	},
}

// gate holds each request for a collection until six are in flight at once, or
// two seconds have passed, and then for a moment longer. What it reports is the
// most the plugin ever had in flight: an implementation that allowed fewer than
// six never gets them there, and one that allowed more has them all there at once.
type gate struct {
	mu        sync.Mutex
	now, peak int
	six       chan struct{}
	once      sync.Once
}

func (g *gate) hold(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		g.now++
		g.peak = max(g.peak, g.now)
		if g.now >= 6 {
			g.once.Do(func() { close(g.six) })
		}
		g.mu.Unlock()
		select {
		case <-g.six:
		case <-time.After(2 * time.Second):
		}
		time.Sleep(50 * time.Millisecond)
		g.mu.Lock()
		g.now--
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// Twenty names are read six at a time, and never more: the cap says how many a
// refresh may cost, and this says how many of them may be in flight against an
// instance somebody's search depends on. The same on both listings.
func TestNamedCollectionsAreReadSixAtATimeAndNoMore(t *testing.T) {
	for id, read := range bothListings {
		routes := manyRoutes(20, nil)
		g := &gate{six: make(chan struct{})}
		odd := map[string]http.HandlerFunc{}
		for i := range 20 {
			path := fmt.Sprintf("/collections/c%03d", i)
			odd[path] = g.hold(routes[path])
		}
		f := servingWith(t, routes, odd)

		rows, warnings := read(t, f, namesFrom(0, 20))
		if len(rows) != 20 || len(warnings) != 0 {
			t.Errorf("%s: %d rows and warnings %+v, want the twenty and nothing else", id, len(rows), warnings)
		}
		if g.peak != 6 {
			t.Errorf("%s: %d requests were in flight at once, want exactly 6", id, g.peak)
		}
	}
}

// A request that is never answered is given up on at the overview's bound, on the
// listing that is not the overview too, and only that collection is lost: its row
// says it did not answer in time and the others answer.
func TestACollectionThatNeverAnswersIsARowAndTheOthersAnswer(t *testing.T) {
	boundOverviewAt(t, 150*time.Millisecond)
	for id, read := range bothListings {
		f := servingWith(t, manyRoutes(3, nil), map[string]http.HandlerFunc{"/collections/c001": hang})

		started := time.Now()
		rows, warnings := read(t, f, namesFrom(0, 3))
		if took := time.Since(started); took > 3*time.Second {
			t.Errorf("%s: took %s, want about the bound of 150ms", id, took)
		}
		states := map[string]string{}
		for _, row := range rows {
			states[row[0]] = row[4]
		}
		if len(rows) != 3 || states["c000"] != "green" || states["c001"] != "unreadable" || states["c002"] != "green" {
			t.Errorf("%s: rows = %v, want c001 unreadable and the other two green", id, rows)
		}
		if len(warnings) != 1 || !strings.HasPrefix(warnings[0].Message, "c001 could not be read — ") ||
			!strings.Contains(warnings[0].Message, "did not answer in time") {
			t.Errorf("%s: warnings = %+v, want one that names c001 and says it did not answer in time", id, warnings)
		}
	}
}

// One deadline for the reads, and not a bound for each in turn: thirty-six
// collections that answer nothing are six rounds of the per-request bound if
// each is given its own, and about two of them if the reads share one.
func TestTheReadsOfAListShareOneDeadline(t *testing.T) {
	const bound = 400 * time.Millisecond
	boundOverviewAt(t, bound)
	routes := manyRoutes(36, nil)
	odd := map[string]http.HandlerFunc{}
	for i := range 36 {
		odd[fmt.Sprintf("/collections/c%03d", i)] = hang
	}
	for id, read := range bothListings {
		f := servingWith(t, routes, odd)

		started := time.Now()
		rows, warnings := read(t, f, namesFrom(0, 36))
		took := time.Since(started)
		if limit := 4 * bound; took > limit {
			t.Errorf("%s: took %s, over %s: every read was given its own bound in turn", id, took, limit)
		}
		if len(rows) != 36 || len(warnings) != 36 {
			t.Errorf("%s: %d rows and %d warnings, want a row and a warning for each of the thirty-six", id, len(rows), len(warnings))
		}
	}
}

// A named collection can fail in more ways than a refusal and a missing name. A
// server error, a connection that is dropped and a request that never answers are
// each that collection's row, with the reason, and the others answer.
func TestACollectionThatFailsInAnyWayIsItsOwnRow(t *testing.T) {
	boundOverviewAt(t, 150*time.Millisecond)
	for _, tc := range []struct {
		name    string
		fails   http.HandlerFunc
		why     string
		message string
	}{
		{"a server error", serverError, "request failed", "returned 500"},
		{"a dropped connection", dropped, "connection failed", "could not reach"},
		{"a request that never answers", hang, "timed out", "did not answer in time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			odd := map[string]http.HandlerFunc{"/collections/c001": tc.fails}

			f := servingWith(t, manyRoutes(3, nil), odd)
			kv := compactOf(t, f, map[string]any{"collection": namesFrom(0, 3)})
			if got, want := pair(kv, "status"), "warn — c001 could not be read"; got != want {
				t.Errorf("status = %q, want %q", got, want)
			}
			if got, want := pair(kv, "collections"), "3, 1 unreadable ("+tc.why+"), 2 green"; got != want {
				t.Errorf("collections = %q, want %q", got, want)
			}
			assertFitsATile(t, kv)

			f = servingWith(t, manyRoutes(3, nil), odd)
			table := section(t, overviewOf(t, f, map[string]any{"collection": namesFrom(0, 3), "detail": true}), "collections").(view.Table)
			states := map[string]string{}
			for _, row := range table.Rows {
				states[row[0]] = row[4]
			}
			if states["c000"] != "green" || states["c001"] != "unreadable" || states["c002"] != "green" {
				t.Errorf("rows = %v, want c001 unreadable and the others read", table.Rows)
			}
			if len(table.Warnings) != 1 || !strings.HasPrefix(table.Warnings[0].Message, "c001 could not be read — ") ||
				!strings.Contains(table.Warnings[0].Message, tc.message) {
				t.Errorf("warnings = %+v, want one that names c001 and says %q", table.Warnings, tc.message)
			}
		})
	}
}

// The compact form is what a tile and an agent read by default, so it is where a
// collection that could not be read is named: the verdict says which, up to two
// and then how many more, and the collections row says why in a few words. The
// detail's table names it too, and was the only place that did.
func TestTheCompactFormNamesTheCollectionsItCouldNotRead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refused map[string]int
		want    string
		row     string
	}{
		{"one", map[string]int{"/collections/c001": http.StatusForbidden},
			"warn — c001 could not be read", "5, 1 unreadable (denied), 4 green"},
		{"two", map[string]int{"/collections/c001": http.StatusForbidden, "/collections/c003": http.StatusForbidden},
			"warn — c001, c003 could not be read", "5, 2 unreadable (denied), 3 green"},
		{"three", map[string]int{"/collections/c001": http.StatusForbidden, "/collections/c002": http.StatusForbidden,
			"/collections/c004": http.StatusForbidden},
			"warn — c001, c002 +1 could not be read", "5, 3 unreadable (denied), 2 green"},
		{"for different reasons", map[string]int{"/collections/c001": http.StatusForbidden, "/collections/c002": http.StatusUnauthorized},
			"warn — c001, c002 could not be read", "5, 2 unreadable (denied, key rejected), 3 green"},
		{"for more reasons than a row has room for", map[string]int{"/collections/c001": http.StatusForbidden,
			"/collections/c002": http.StatusUnauthorized, "/collections/c003": http.StatusTooManyRequests},
			"warn — c001, c002 +1 could not be read", "5, 3 unreadable (denied, key rejected +1), 2 green"},
	} {
		// Named, and read from the instance's own listing: the same words.
		for how, values := range map[string]map[string]any{
			"named":  {"collection": namesFrom(0, 5)},
			"listed": {},
		} {
			kv := compactOf(t, answering(t, manyRoutes(5, nil), tc.refused), values)
			if got := pair(kv, "status"); got != tc.want {
				t.Errorf("%s, %s: status = %q, want %q", tc.name, how, got, tc.want)
			}
			if got := pair(kv, "collections"); got != tc.row {
				t.Errorf("%s, %s: collections = %q, want %q", tc.name, how, got, tc.row)
			}
			assertFitsATile(t, kv)
		}
	}

	// A collection that is red is still the worse fact, and the unreadable one
	// does not take its place.
	routes := manyRoutes(3, map[string]string{"c002": "red"})
	kv := compactOf(t, answering(t, routes, map[string]int{"/collections/c001": http.StatusForbidden}),
		map[string]any{"collection": namesFrom(0, 3)})
	if got, want := pair(kv, "status"), "fail — c002 is red"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// The name is what the host checked against the fence, byte for byte: an empty
// string is no name and is dropped, a name given twice is read once, and nothing
// is trimmed, so a fence entry written with a space around it is read as written
// and not as another collection that happens to be outside the fence.
func TestANameIsUsedExactlyAsTheHostCheckedItAndNeverTrimmed(t *testing.T) {
	f := newFakeQdrant(t, instanceRoutes())
	compactOf(t, f, map[string]any{"collection": []string{"faq", "", "docs ", "docs", "faq"}})
	want := []string{"/", "/cluster", "/collections/docs", "/collections/docs%20", "/collections/faq"}
	if got := requestPaths(f); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

// The seam the list exists for. A profile that fences several collections is the
// connection of a call that names those, and what reaches the handler is that
// list: the one the call wrote, and, once the host fills a call that names none
// with the fence's own, the fence itself. Given the whole fence as the list, both
// listings read exactly those collections and never ask the instance for its own
// listing, which is where the others are.
func TestTheWholeFenceHandedAsTheListIsTheFencedPage(t *testing.T) {
	fence := plugin.Fence{"collection": {"docs", "faq", "images"}}
	for id, spelt := range map[string][]any{
		"qdrant.overview":        {fence["collection"], []any{"docs", "faq", "images"}},
		"qdrant.collection.list": {fence["collection"], []any{"docs", "faq", "images"}},
	} {
		c := capabilityOf(t, id)
		for _, list := range spelt {
			f := newFakeQdrant(t, instanceRoutes())
			in := plugin.Inputs{Fence: fence, Caller: map[string]any{"collection": list, "address": f.endpoint()}}
			if breach := plugin.CheckFence(c, in); breach != nil {
				t.Fatalf("%s: the whole fence was refused by the fence: %+v", id, breach)
			}
			r := plugin.NewRequest(plugin.Resolve(c, in), false, false)

			var rows [][]string
			if id == "qdrant.overview" {
				v, err := runOverview(t.Context(), r.With(map[string]any{"detail": true}))
				if err != nil {
					t.Fatal(err)
				}
				rows = section(t, v, "collections").(view.Table).Rows
			} else {
				table, verr := collectionTable(t.Context(), r)
				if verr != nil {
					t.Fatal(verr)
				}
				rows = table.Rows
			}
			names := map[string]bool{}
			for _, row := range rows {
				names[row[0]] = true
			}
			if len(rows) != 3 || !names["docs"] || !names["faq"] || !names["images"] {
				t.Errorf("%s: rows = %v, want the three collections of the fence", id, rows)
			}
			for _, path := range requestPaths(f) {
				if path == "/collections" {
					t.Errorf("%s: the instance's listing was asked for", id)
				}
			}
		}
	}
}
