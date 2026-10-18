package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/view"
)

// An instance that takes the connection and never answers held the overview for
// thirty-one seconds: the wait of a request that is allowed to be a count over a
// large collection with no index, which is not what the overview asks for.

func boundOverviewAt(t *testing.T, d time.Duration) {
	t.Helper()
	old := overviewTimeout
	overviewTimeout = d
	t.Cleanup(func() { overviewTimeout = old })
}

func TestAnInstanceThatNeverAnswersIsGivenUpOnAtTheOverviewsBound(t *testing.T) {
	boundOverviewAt(t, 150*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	started := time.Now()
	_, err := runOverview(t.Context(), req(t, "qdrant.overview", map[string]any{
		"address": strings.TrimPrefix(srv.URL, "http://")}))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "qdrant.timeout" {
		t.Fatalf("err = %#v, want qdrant.timeout", err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("gave up after %s, want about the bound of 150ms", took)
	}
	// The hint was a count or a scroll over a large collection, which the overview
	// is not, and said nothing of the cause a silent instance usually has.
	if !strings.Contains(verr.Hint, "firewall that drops") {
		t.Errorf("hint = %q, want the cause the other plugins give", verr.Hint)
	}
}

// The bound is a request's, and not the page's: an overview reads up to a
// hundred collections one request at a time, and over a slow link their sum is
// no fault of the instance. The eight requests of five collections, 40ms each,
// take longer between them than the bound of 200ms, and none of them does.
func TestTheBoundIsOfEachRequestAndNotOfTheWholePage(t *testing.T) {
	boundOverviewAt(t, 200*time.Millisecond)
	routes := manyRoutes(5, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	started := time.Now()
	v, err := runOverview(t.Context(), req(t, "qdrant.overview", map[string]any{
		"address": strings.TrimPrefix(srv.URL, "http://")}))
	if err != nil {
		t.Fatalf("a slow instance that answers every request was given up on: %v", err)
	}
	if took := time.Since(started); took <= overviewTimeout {
		t.Fatalf("took only %s: the page was never longer than its bound, so nothing here was tested", took)
	}
	if rows := section(t, v, "collections").(view.Table).Rows; len(rows) != 5 {
		t.Errorf("rows = %d, want 5", len(rows))
	}
}

// Only the overview's: a count or a scroll over a large collection is allowed
// the time it takes.
func TestTheOverviewsBoundIsNotTheBoundOfAnyOtherCall(t *testing.T) {
	boundOverviewAt(t, 20*time.Millisecond)
	routes := manyRoutes(1, nil)
	routes["/cluster"] = fxClusterDisabled
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(routes[r.URL.Path]))
	}))
	t.Cleanup(srv.Close)
	if _, verr := collectionTable(t.Context(), req(t, "qdrant.collection.list", map[string]any{
		"address": strings.TrimPrefix(srv.URL, "http://")})); verr != nil {
		t.Errorf("qdrant.collection.list was cut short by the overview's bound: %v", verr)
	}
}

// A 5xx is the instance failing, not a setting wrong; the hint was the page of
// every input. A 503 and a peer that does not answer already say what they are.
func TestAServerErrorIsToldToBeRetried(t *testing.T) {
	r := req(t, "qdrant.overview", map[string]any{"address": "127.0.0.1:1"})
	for _, code := range []int{500, 502, 504} {
		verr := classifyStatus(code, []byte(`{"status":{"error":"upstream trouble"}}`), r)
		if verr.Code != "qdrant.request.failed" || !strings.Contains(verr.Hint, "the service is overloaded or restarting; retry") {
			t.Errorf("%d: %s — %s", code, verr.Code, verr.Hint)
		}
	}
	if verr := classifyStatus(503, nil, r); verr.Code != "qdrant.unavailable" || strings.Contains(verr.Hint, "overloaded") {
		t.Errorf("503: %s — %s, want its own", verr.Code, verr.Hint)
	}
	for _, code := range []int{400, 409, 422} {
		if verr := classifyStatus(code, nil, r); strings.Contains(verr.Hint, "retry") {
			t.Errorf("%d: %q advises retrying a refusal", code, verr.Hint)
		}
	}
}
