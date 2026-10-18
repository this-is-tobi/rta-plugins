package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The compact form is what a dashboard tile shows and what an agent reads by
// default: the verdict first, then what the collections are, then the cluster,
// and the version last. A tile is the first eight lines of it, and the page that
// used to be the default opened with the address and a constant title.

const compactRows = "status,collections,cluster,version"

func compactOf(t *testing.T, f *fakeQdrant, values map[string]any) view.KeyValue {
	t.Helper()
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", values))
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("the compact overview is a %s, want the rows alone", view.TypeOf(v))
	}
	return kv
}

func keysOf(kv view.KeyValue) string {
	keys := make([]string, 0, len(kv.Pairs))
	for _, p := range kv.Pairs {
		keys = append(keys, p.Key)
	}
	return strings.Join(keys, ",")
}

// A tile a third of a screen wide still reads: no row is longer than a line of
// an 80-column terminal, and there are no more rows than a tile holds.
func assertFitsATile(t *testing.T, kv view.KeyValue) {
	t.Helper()
	if len(kv.Pairs) > 8 {
		t.Errorf("%d rows, a tile holds eight: %v", len(kv.Pairs), kv.Pairs)
	}
	for _, p := range kv.Pairs {
		if n := utf8.RuneCountInString(p.Key) + 2 + utf8.RuneCountInString(p.Value); n > 80 || strings.Contains(p.Value, "\n") {
			t.Errorf("row %q is %d columns or wraps: %q", p.Key, n, p.Value)
		}
	}
}

func requestsTo(f *fakeQdrant, prefix string) int {
	n := 0
	for _, rec := range f.bodies {
		if strings.HasPrefix(rec.path, prefix) {
			n++
		}
	}
	return n
}

func TestAHealthyInstanceIsOkFirstAndItsVersionLast(t *testing.T) {
	kv := compactOf(t, newFakeQdrant(t, manyRoutes(3, nil)), map[string]any{})
	if got := keysOf(kv); got != compactRows {
		t.Fatalf("rows = %s, want %s", got, compactRows)
	}
	for key, want := range map[string]string{
		"status": "ok", "collections": "3, all green", "cluster": "standalone", "version": "1.19.1",
	} {
		if got := pair(kv, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	assertFitsATile(t, kv)
}

func TestTheCollectionsAreCountedByStatusWorstFirstAndTheWorstIsTheVerdict(t *testing.T) {
	kv := compactOf(t, newFakeQdrant(t, manyRoutes(4, map[string]string{"c001": "yellow", "c003": "red"})), map[string]any{})
	if got, want := pair(kv, "status"), "fail — c003 is red"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := pair(kv, "collections"), "4, 1 red, 1 yellow, 2 green"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
	assertFitsATile(t, kv)
}

func TestSeveralCollectionsOfOneStatusAreCountedAndTheFirstIsNamed(t *testing.T) {
	kv := compactOf(t, newFakeQdrant(t, manyRoutes(4, map[string]string{"c000": "red", "c002": "red"})), map[string]any{})
	if got, want := pair(kv, "status"), "fail — 2 collections red, first c000"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	kv = compactOf(t, newFakeQdrant(t, manyRoutes(4, map[string]string{"c001": "yellow", "c002": "grey"})), map[string]any{})
	if got, want := pair(kv, "status"), "warn — c001 is yellow"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := pair(kv, "collections"), "4, 1 yellow, 1 grey, 2 green"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
}

// Past the hundred it reads, the row says how many it read of how many there are
// and the verdict is about those: ok for a hundred green ones is a claim about a
// hundred, which the row beside it makes.
func TestAnInstanceBeyondTheBoundSaysHowManyWereRead(t *testing.T) {
	f := newFakeQdrant(t, manyRoutes(150, map[string]string{"c120": "red"}))
	kv := compactOf(t, f, map[string]any{})
	if got, want := pair(kv, "collections"), "100 of 150 read, all green"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
	if got := pair(kv, "status"); got != "ok" {
		t.Errorf("status = %q, want ok for the hundred that were read", got)
	}
	if got := requestsTo(f, "/collections/"); got != overviewCollections {
		t.Errorf("read %d collections, want %d", got, overviewCollections)
	}
	assertFitsATile(t, kv)
}

func TestAnInstanceWithNoCollectionIsOkAndSaysNone(t *testing.T) {
	routes := map[string]string{"/": fxRoot, "/cluster": fxClusterDisabled, "/collections": envelope(`{"collections":[]}`)}
	kv := compactOf(t, newFakeQdrant(t, routes), map[string]any{})
	if got, want := pair(kv, "collections"), "none"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
	if got := pair(kv, "status"); got != "ok" {
		t.Errorf("status = %q", got)
	}
}

func TestACollectionTheCredentialCannotReadIsCountedAndWarns(t *testing.T) {
	routes := manyRoutes(3, nil)
	delete(routes, "/collections/c001")
	kv := compactOf(t, newFakeQdrant(t, routes), map[string]any{})
	if got, want := pair(kv, "collections"), "3, 1 unreadable, 2 green"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
	if got, want := pair(kv, "status"), "warn — 1 collection could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

func TestACollectionFenceShowsThatCollectionAlone(t *testing.T) {
	f := newFakeQdrant(t, instanceRoutes())
	kv := compactOf(t, f, map[string]any{"collection": "docs"})
	if got, want := pair(kv, "collections"), "1, green"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
	for _, rec := range f.bodies {
		if strings.Contains(rec.path, "faq") || strings.Contains(rec.path, "images") {
			t.Errorf("asked for %s: a collection the call is not about", rec.path)
		}
	}
}

// answering is a server that answers the routes, and a status of its own for the
// paths named: a credential a Qdrant refuses part of the API to.
func answering(t *testing.T, routes map[string]string, refused map[string]int) *fakeQdrant {
	t.Helper()
	f := &fakeQdrant{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.bodies = append(f.bodies, recorded{path: r.URL.EscapedPath()})
		if code, ok := refused[r.URL.Path]; ok {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"status":{"error":"Forbidden: Global access is required"}}`))
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

// A credential that may not list the collections still reads the version and the
// cluster: the row says it could not be read, the verdict a warning, and the page
// is not an error.
func TestAListingTheCredentialMayNotReadIsARowAndNotAnError(t *testing.T) {
	f := answering(t, manyRoutes(2, nil), map[string]int{"/collections": http.StatusForbidden})
	kv := compactOf(t, f, map[string]any{})
	if got := keysOf(kv); got != compactRows {
		t.Errorf("rows = %s, want %s", got, compactRows)
	}
	if got, want := pair(kv, "collections"), "could not be read — this credential may not list the collections"; got != want {
		t.Errorf("collections = %q, want %q", got, want)
	}
	if got, want := pair(kv, "status"), "warn — collections could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := pair(kv, "version"); got != "1.19.1" {
		t.Errorf("version = %q, want the rows that could be read to stand", got)
	}
	assertFitsATile(t, kv)
}

// The cluster's own state needs global read access, which a key scoped to
// collections does not have: the row says so in the words the theme turns amber.
func TestAClusterStateTheCredentialMayNotReadIsARowAndAWarning(t *testing.T) {
	f := answering(t, manyRoutes(2, nil), map[string]int{"/cluster": http.StatusForbidden})
	kv := compactOf(t, f, map[string]any{})
	if got, want := pair(kv, "cluster"), "could not be read — this credential reads collections only"; got != want {
		t.Errorf("cluster = %q, want %q", got, want)
	}
	if got, want := pair(kv, "status"), "warn — cluster state could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := pair(kv, "collections"); got != "2, all green" {
		t.Errorf("collections = %q, want the rows that could be read to stand", got)
	}
}

func TestAServerThatDoesNotReportItsClusterIsNotWarnedAbout(t *testing.T) {
	routes := manyRoutes(1, nil)
	delete(routes, "/cluster")
	kv := compactOf(t, newFakeQdrant(t, routes), map[string]any{})
	if got := pair(kv, "cluster"); got != "not reported by this server" {
		t.Errorf("cluster = %q", got)
	}
	if got := pair(kv, "status"); got != "ok" {
		t.Errorf("status = %q, want a server too old for the endpoint not to be a fault", got)
	}
}

// A peer that does not answer the one asked is the finding, named; and the
// replicas of every collection, one request each, are the detail's.
func TestAPeerThatDoesNotAnswerIsNamedAndNoReplicaIsAsked(t *testing.T) {
	f := newFakeQdrant(t, peerRoutes(fxTelemetryDown))
	kv := compactOf(t, f, map[string]any{})
	if got, want := pair(kv, "status"), "fail — peer dbwp-r12plug-qd3 is not answering"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := pair(kv, "cluster"), "3 peers, this one is follower, dbwp-r12plug-qd3 not answering"; got != want {
		t.Errorf("cluster = %q, want %q", got, want)
	}
	if got := requestsTo(f, "/collections/docs3/cluster"); got != 0 {
		t.Errorf("%d replica requests, want none: the compact form shows no replica", got)
	}
	assertFitsATile(t, compactOf(t, f, map[string]any{}))
}

func TestAHealthyClusterNamesItsPeersAndItsRoleAndAMixOfVersions(t *testing.T) {
	f := newFakeQdrant(t, peerRoutes(fxTelemetryUp))
	if got, want := pair(compactOf(t, f, map[string]any{}), "cluster"), "3 peers, this one is follower"; got != want {
		t.Errorf("cluster = %q, want %q", got, want)
	}
	skewed := strings.Replace(fxTelemetryUp, `"version":"1.19.1"`, `"version":"1.18.0"`, 1)
	f = newFakeQdrant(t, peerRoutes(skewed))
	kv := compactOf(t, f, map[string]any{})
	if got, want := pair(kv, "cluster"), "3 peers, this one is follower, on 2 versions"; got != want {
		t.Errorf("cluster = %q, want %q", got, want)
	}
	if got := pair(kv, "status"); got != "ok" {
		t.Errorf("status = %q, want a rolling upgrade not to be a fault", got)
	}
}

func TestAConsensusThreadThatStoppedIsAFailure(t *testing.T) {
	stopped := strings.Replace(fxClusterAtFollower, `"consensus_thread_status":"working"`,
		`"consensus_thread_status":"stopped_with_err","err":"disk full"`, 1)
	routes := peerRoutes(fxTelemetryUp)
	routes["/cluster"] = stopped
	kv := compactOf(t, newFakeQdrant(t, routes), map[string]any{})
	if got := pair(kv, "status"); !strings.HasPrefix(got, "fail — this peer: the consensus thread stopped") {
		t.Errorf("status = %q", got)
	}
}

// The detail is the page the default used to be: the compact rows first, then
// the status block, the peers and the table of collections with a replicas
// column, which is where the per-collection requests are made.
func TestTheDetailIsTheTableOfCollectionsWithItsReplicas(t *testing.T) {
	f := newFakeQdrant(t, peerRoutes(fxTelemetryDown))
	v, err := runOverview(t.Context(), reqAt(t, f, "qdrant.overview", map[string]any{"detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, it := range v.(view.Sections).Items {
		titles = append(titles, it.Title)
	}
	if got, want := strings.Join(titles, ","), "summary,status,peers,collections"; got != want {
		t.Errorf("sections = %s, want %s", got, want)
	}
	status := section(t, v, "status").(view.KeyValue)
	for _, key := range []string{"address", "title", "version", "cluster", "collections"} {
		if pair(status, key) == "" {
			t.Errorf("the detail status lost its %q row: %v", key, status.Pairs)
		}
	}
	coll := section(t, v, "collections").(view.Table)
	if last := coll.Columns[len(coll.Columns)-1]; last.Name != "Replicas" {
		t.Errorf("columns = %v, want the replicas column", coll.Columns)
	}
	if got := requestsTo(f, "/collections/docs3/cluster"); got != 1 {
		t.Errorf("%d replica requests, want one for the collection", got)
	}
}

func TestTheOverviewDeclaresADetailedPageAndSaysSoToAgents(t *testing.T) {
	c := capabilityOf(t, "qdrant.overview")
	if !c.Detailed {
		t.Error("qdrant.overview is not Detailed")
	}
	for name, text := range map[string]string{"description": c.Description, "agent": c.Agent} {
		if !strings.Contains(text, "`detail`") || !strings.Contains(text, "status") {
			t.Errorf("the %s text does not say what the default answer is and what `detail` adds: %q", name, text)
		}
	}
}

// An instance that is not there is the error it always was.
func TestAnInstanceNothingListensForIsTheErrorItAlwaysWas(t *testing.T) {
	f := newFakeQdrant(t, nil)
	r := reqAt(t, f, "qdrant.overview", map[string]any{})
	f.Close()
	_, err := runOverview(t.Context(), r)
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "qdrant.conn.refused" {
		t.Errorf("err = %#v, want qdrant.conn.refused", err)
	}
}
