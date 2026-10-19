package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The bucket of s3.overview and s3.bucket.list is a list: it filters the set of
// buckets the connection holds, so naming none reads all of them (within the
// profile fence when there is one) and naming some narrows. A profile fenced to
// several buckets could not be handed to either, because the input was one name.

// acctBucket is one bucket of an accountFake and what the server does when it
// is read.
type acctBucket struct {
	keys       []fakeKey
	lifecycle  string // as bucketFake's: "" none, "denied", or the XML
	listDenied bool   // every listing is refused with AccessDenied
	listFails  bool   // every listing is a 500
	stalls     bool   // every request to the bucket is held until the caller gives up
}

// accountFake is an S3 account of several buckets, listed the way S3 lists. It
// records every request, the listing queries of each bucket, and how many
// listings were in flight at once, which is what a test about how much of an
// account is read, and how fast, asks.
type accountFake struct {
	*httptest.Server
	buckets map[string]*acctBucket

	// accountDenied refuses the account's list of buckets, as a key scoped to
	// one bucket is; badKey refuses every request as a key the service does
	// not know; delay is how long each listing takes.
	accountDenied bool
	badKey        bool
	delay         time.Duration

	// release lets a stalled request go when the test ends.
	release chan struct{}

	mu       sync.Mutex
	asked    []string
	listed   []listedBucket
	inflight int
	peak     int
}

type listedBucket struct {
	bucket string
	query  url.Values
}

func s3Error(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<Error><Code>%s</Code><Message>%s</Message></Error>`, code, message)
}

func newAccount(t *testing.T, buckets map[string][]fakeKey) *accountFake {
	t.Helper()
	a := &accountFake{buckets: map[string]*acctBucket{}, release: make(chan struct{})}
	for name, keys := range buckets {
		sorted := append([]fakeKey(nil), keys...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
		a.buckets[name] = &acctBucket{keys: sorted}
	}
	a.Server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.Close)
	t.Cleanup(func() { close(a.release) })
	return a
}

func (a *accountFake) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	a.mu.Lock()
	a.asked = append(a.asked, r.Method+" "+r.URL.RequestURI())
	a.mu.Unlock()

	if a.badKey {
		s3Error(w, http.StatusForbidden, "InvalidAccessKeyId", "The access key ID you provided does not exist")
		return
	}
	if r.URL.Path == "/" {
		if a.accountDenied {
			s3Error(w, http.StatusForbidden, "AccessDenied", "Access Denied.")
			return
		}
		_, _ = w.Write([]byte(`<ListAllMyBucketsResult><Buckets>`))
		for name := range a.buckets {
			var escaped strings.Builder
			_ = xml.EscapeText(&escaped, []byte(name))
			_, _ = fmt.Fprintf(w, `<Bucket><Name>%s</Name><CreationDate>2025-04-27T10:00:00.000Z</CreationDate></Bucket>`, escaped.String())
		}
		_, _ = w.Write([]byte(`</Buckets></ListAllMyBucketsResult>`))
		return
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	b, there := a.buckets[name]
	if !there {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	if b.stalls {
		select {
		case <-r.Context().Done():
		case <-a.release:
		}
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case q.Has("versioning"):
		_, _ = w.Write([]byte(`<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`))
	case q.Has("lifecycle"):
		switch b.lifecycle {
		case "":
			s3Error(w, http.StatusNotFound, "NoSuchLifecycleConfiguration", "The lifecycle configuration does not exist")
		case "denied":
			s3Error(w, http.StatusForbidden, "AccessDenied", "Access Denied.")
		default:
			_, _ = w.Write([]byte(b.lifecycle))
		}
	case q.Get("list-type") == "2":
		a.list(w, name, b, q)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *accountFake) list(w http.ResponseWriter, name string, b *acctBucket, q url.Values) {
	a.mu.Lock()
	a.listed = append(a.listed, listedBucket{name, q})
	a.inflight++
	a.peak = max(a.peak, a.inflight)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.inflight--
		a.mu.Unlock()
	}()
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	switch {
	case b.listDenied:
		s3Error(w, http.StatusForbidden, "AccessDenied", "Access Denied.")
		return
	case b.listFails:
		s3Error(w, http.StatusInternalServerError, "InternalError", "We encountered an internal error. Please try again.")
		return
	}

	limit := 1000
	if v, err := strconv.Atoi(q.Get("max-keys")); err == nil && v > 0 && v < limit {
		limit = v
	}
	after := q.Get("start-after")
	if tok := q.Get("continuation-token"); tok != "" {
		after = strings.TrimPrefix(tok, "after:")
	}
	var out strings.Builder
	n, truncated, token := 0, false, ""
	for _, k := range b.keys {
		if !strings.HasPrefix(k.Key, q.Get("prefix")) || k.Key <= after {
			continue
		}
		if n == limit {
			truncated = true
			break
		}
		_, _ = fmt.Fprintf(&out, `<Contents><Key>%s</Key><Size>%d</Size><ETag>"e"</ETag><LastModified>%s</LastModified></Contents>`,
			k.Key, k.Size, k.Modified.UTC().Format("2006-01-02T15:04:05.000Z"))
		token = "after:" + k.Key
		n++
	}
	_, _ = fmt.Fprintf(w, `<ListBucketResult><Name>%s</Name><KeyCount>%d</KeyCount><MaxKeys>%d</MaxKeys><IsTruncated>%t</IsTruncated>`+
		`<NextContinuationToken>%s</NextContinuationToken>%s</ListBucketResult>`, name, n, limit, truncated, token, out.String())
}

func (a *accountFake) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asked...)
}

// listingsOf is how many listings were made of the named bucket.
func (a *accountFake) listingsOf(bucket string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, l := range a.listed {
		if l.bucket == bucket {
			n++
		}
	}
	return n
}

func (a *accountFake) listings() []listedBucket {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]listedBucket(nil), a.listed...)
}

// requestsLike counts the requests whose line (METHOD /path?query) matches.
func (a *accountFake) requestsLike(pattern string) int {
	re := regexp.MustCompile(pattern)
	n := 0
	for _, line := range a.requests() {
		if re.MatchString(line) {
			n++
		}
	}
	return n
}

func (a *accountFake) peakListings() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.peak
}

// manyBuckets is n buckets named b00, b01, ... each holding three objects.
func manyBuckets(t *testing.T, n int) *accountFake {
	t.Helper()
	buckets := map[string][]fakeKey{}
	for i := range n {
		buckets[fmt.Sprintf("b%02d", i)] = numberedKeys(3)
	}
	return newAccount(t, buckets)
}

func overviewOn(t *testing.T, a *accountFake, values map[string]any) (view.View, error) {
	t.Helper()
	return runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, a.Server), values))
}

func tileOn(t *testing.T, a *accountFake, values map[string]any) view.KeyValue {
	t.Helper()
	v, err := overviewOn(t, a, values)
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("the compact overview is a %s, want the rows alone", view.TypeOf(v))
	}
	return kv
}

func marshalled(t *testing.T, v view.View) string {
	t.Helper()
	raw, err := view.Marshal(view.Envelope{View: v})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func names(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%02d", prefix, i)
	}
	return out
}

// Several buckets named: one request each, the first page of its keys, and the
// account's own listing and the bucket's versioning and lifecycle are left to
// the detail. Worst first: what could not be read, then what holds nothing,
// then the one written longest ago.
func TestSeveralBucketsAreReadWithOneRequestEachAndMergedIntoOnePage(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{
		"backups": numberedKeys(3),
		"logs":    nil,
		"media":   numberedKeys(2500),
		"photos":  numberedKeys(2),
	})
	a.buckets["photos"].listDenied = true
	kv := tileOn(t, a, map[string]any{"bucket": []string{"photos", "media", "logs", "backups"}})

	if got, want := keysOf(kv), "status,buckets,photos,logs,backups,media,address"; got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	for key, want := range map[string]string{
		"status":  "warn — photos could not be read",
		"buckets": "4 named",
		"photos":  "could not be read — access denied",
		"logs":    "0 objects",
		"address": endpointOf(t, a.Server),
	} {
		if got := rowOf(kv, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// k1000002 is written 2 hours into the year, k1000999 999 hours into it.
	if got, want := rowOf(kv, "backups"), "3 objects · newest "+format.Ago(time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)); got != want {
		t.Errorf("backups = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "media"), "1000+ objects · newest "+format.Ago(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(999*time.Hour))+" (first page)"; got != want {
		t.Errorf("media = %q, want %q: the newest of a full page is of that page and says so", got, want)
	}
	assertFitsATile(t, kv)

	// Four buckets, four requests: the first page of each, and nothing else.
	reqs := a.requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests %v, want one for each bucket", len(reqs), reqs)
	}
	for _, l := range a.listings() {
		if l.query.Get("max-keys") != "1000" || l.query.Get("start-after") != "" {
			t.Errorf("%s was listed with %v, want one request for the first thousand keys", l.bucket, l.query)
		}
	}
	if a.requestsLike(`^HEAD |versioning|lifecycle|^GET /$`) != 0 {
		t.Errorf("requests = %v: the compact form of a list reads neither the account, nor the bucket, nor its settings", reqs)
	}
}

// Worst first, and among the buckets that are well the one written longest ago:
// a backup bucket whose newest object is a week old is the row to find, whatever
// the alphabet says. The names are chosen so that no part of the order is the
// alphabetical one.
func TestBucketsThatAreWellAreOrderedByWhenTheNewestWasWrittenLongestAgoFirst(t *testing.T) {
	at := func(month time.Month) []fakeKey {
		return []fakeKey{{Key: "k", Size: 1, Modified: time.Date(2026, month, 1, 0, 0, 0, 0, time.UTC)}}
	}
	a := newAccount(t, map[string][]fakeKey{
		"alpha": at(time.September), "bravo": at(time.February), "charlie": at(time.June),
		"yankee": nil, "zulu": at(time.March),
	})
	a.buckets["zulu"].listDenied = true
	kv := tileOn(t, a, map[string]any{})

	if got, want := keysOf(kv), "status,buckets,zulu,yankee,bravo,charlie,alpha,address"; got != want {
		t.Errorf("rows = %s, want %s: unreadable, empty, then the oldest newest write first", got, want)
	}

	// The same page whichever way the list is written.
	again := tileOn(t, a, map[string]any{"bucket": []string{"zulu", "charlie", "yankee", "alpha", "bravo"}})
	if got, want := keysOf(again), keysOf(kv); got != want {
		t.Errorf("a list in another order is %s, want %s", got, want)
	}
}

// With no bucket named, every bucket the credentials see: the account's listing,
// and then one request a bucket.
func TestNoBucketNamedReadsEveryBucketTheCredentialsSee(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3), "media": numberedKeys(5)})
	kv := tileOn(t, a, map[string]any{})

	if got, want := keysOf(kv), "status,buckets,backups,media,address"; got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	if got := rowOf(kv, "buckets"); got != "2" {
		t.Errorf("buckets = %q, want the count the credentials see", got)
	}
	if got := rowOf(kv, "status"); got != "ok" {
		t.Errorf("status = %q", got)
	}
	if got := len(a.requests()); got != 3 {
		t.Errorf("%d requests %v, want the account's listing and one a bucket", got, a.requests())
	}
	assertFitsATile(t, kv)
}

func TestAnAccountWithNoBucketIsTheThreeRowsItWas(t *testing.T) {
	a := newAccount(t, nil)
	kv := tileOn(t, a, map[string]any{})
	if got, want := keysOf(kv), "status,buckets,address"; got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	if rowOf(kv, "buckets") != "0" || rowOf(kv, "status") != "ok" {
		t.Errorf("rows = %v", kv.Pairs)
	}
}

// A key that may not list the account has nothing to read the buckets of.
func TestAnAccountThatCannotBeListedIsStillTheErrorItWas(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3)})
	a.accountDenied = true
	_, err := overviewOn(t, a, map[string]any{})
	if verr, ok := err.(*view.Error); !ok || verr.Code != "s3.denied" {
		t.Fatalf("err = %v, want s3.denied", err)
	}
}

// A list of one is the page of that bucket, whichever way it is written.
func TestOneBucketInAListIsReadExactlyAsTheSingleBucketWas(t *testing.T) {
	for name, values := range map[string]map[string]any{
		"the compact page": {},
		"the detail":       {"detail": true},
		"under a prefix":   {"prefix": "k100"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBucketFake(t, "backups", numberedKeys(2500))
			run := func(bucket any) (string, []string) {
				v := map[string]any{"bucket": bucket}
				for k, val := range values {
					v[k] = val
				}
				before := len(f.requests())
				page, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, f.Server), v))
				if err != nil {
					t.Fatal(err)
				}
				return marshalled(t, page), f.requests()[before:]
			}
			single, singleReqs := run("backups")
			for label, bucket := range map[string]any{
				"a list of one":          []string{"backups"},
				"a list of one over MCP": []any{"backups"},
				"a name twice":           []string{"backups", "backups"},
				"a name and a blank":     []string{"", "backups"},
			} {
				got, reqs := run(bucket)
				if got != single {
					t.Errorf("%s: the page differs from the one the plain name gave:\n%s\n%s", label, got, single)
				}
				if len(reqs) != len(singleReqs) {
					t.Errorf("%s: %d requests, the plain name made %d", label, len(reqs), len(singleReqs))
				}
			}
		})
	}
}

// A name is read as the host compared it to the fence. Dropping an empty string
// is the whole of what is done to the list before that, apart from putting it in
// order: a name trimmed afterwards would be one the fence never held, and read.
func TestNamesAreUsedExactlyAsTheHostHeldThemToTheFence(t *testing.T) {
	r := reqFor(t, "s3.overview", "127.0.0.1:1", map[string]any{"bucket": []string{"media", "", " backups", "media", "  "}})
	if got, want := strings.Join(bucketNames(r), "|"), "  | backups|media"; got != want {
		t.Errorf("bucketNames = %q, want %q", got, want)
	}

	a := newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3), "media": numberedKeys(2)})
	kv := tileOn(t, a, map[string]any{"bucket": []string{" backups", "media"}})
	if got, want := rowOf(kv, plugin.ListedName(" backups")), "could not be read — not a valid bucket name"; got != want {
		t.Errorf("a padded name = %q, want %q: it is not the bucket it pads", got, want)
	}
	if got := rowOf(kv, "media"); !strings.HasPrefix(got, "2 objects") {
		t.Errorf("media = %q", got)
	}
	if got := a.listingsOf("backups"); got != 0 {
		t.Errorf("backups was listed %d times for a name with a space before it", got)
	}

	_, err := overviewOn(t, a, map[string]any{"bucket": []string{" backups"}})
	if verr, ok := err.(*view.Error); !ok || verr.Code != "s3.bucket.invalid" {
		t.Errorf("err = %v, want s3.bucket.invalid for a single padded name", err)
	}
	assertEmptyTable(t, listOn(t, a, map[string]any{"bucket": []string{" backups"}}), `no bucket named " backups"`)
}

func TestOneMissingBucketInAListIsTheErrorTheMissingBucketWas(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3)})
	_, plain := overviewOn(t, a, map[string]any{"bucket": "nosuch"})
	_, listed := overviewOn(t, a, map[string]any{"bucket": []string{"nosuch"}})
	pv, ok1 := plain.(*view.Error)
	lv, ok2 := listed.(*view.Error)
	if !ok1 || !ok2 || pv.Code != "s3.bucket.notfound" || pv.Code != lv.Code || pv.Message != lv.Message || pv.Hint != lv.Hint {
		t.Errorf("a list of one missing bucket is %v, the plain name gave %v", listed, plain)
	}
}

// A bucket named that is not there is a row saying so, and the others answer.
func TestABucketNamedThatDoesNotExistIsARowAndNotAnError(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3), "media": numberedKeys(5)})
	kv := tileOn(t, a, map[string]any{"bucket": []string{"backups", "nosuch", "media"}})

	if got, want := keysOf(kv), "status,buckets,nosuch,backups,media,address"; got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	if got, want := rowOf(kv, "nosuch"), "could not be read — not found"; got != want {
		t.Errorf("nosuch = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "status"), "warn — nosuch could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := rowOf(kv, "backups"); !strings.HasPrefix(got, "3 objects") {
		t.Errorf("backups = %q: a sibling that is not there never changes what the others answer", got)
	}

	// Every one of them missing is still a page of rows: it is the answer.
	kv = tileOn(t, a, map[string]any{"bucket": []string{"nosuch", "nowhere"}})
	if got, want := rowOf(kv, "status"), "warn — nosuch, nowhere could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// A bucket the key may not list is a row and a warning, and the others answer.
func TestABucketTheKeyMayNotListIsNamedAndTheOthersStillAnswer(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3), "media": numberedKeys(5), "vault": numberedKeys(1)})
	a.buckets["vault"].listDenied = true
	kv := tileOn(t, a, map[string]any{"bucket": []string{"backups", "media", "vault"}})

	if got, want := rowOf(kv, "status"), "warn — vault could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "vault"), "could not be read — access denied"; got != want {
		t.Errorf("vault = %q, want %q", got, want)
	}
	if rowOf(kv, "backups") == "" || rowOf(kv, "media") == "" {
		t.Errorf("rows = %v, want the buckets that could be read to stand", kv.Pairs)
	}
	assertFitsATile(t, kv)
}

// A service that fails one bucket is that bucket's row; one that fails them all
// the same way is the page's error, as it was for one bucket: a page of rows
// that all say the credentials are wrong is the error told badly.
func TestAServiceThatFailsEveryBucketIsTheErrorAndOneThatFailsOneIsARow(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3), "media": numberedKeys(5)})
	a.buckets["media"].listFails = true
	kv := tileOn(t, a, map[string]any{"bucket": []string{"backups", "media"}})
	if got := rowOf(kv, "media"); !strings.HasPrefix(got, "could not be read — InternalError") {
		t.Errorf("media = %q, want the service's failure in a row", got)
	}

	a.badKey = true
	_, err := overviewOn(t, a, map[string]any{"bucket": []string{"backups", "media"}})
	if verr, ok := err.(*view.Error); !ok || verr.Code != "s3.auth.failed" {
		t.Fatalf("err = %v, want the s3.auth.failed every bucket met", err)
	}

	// And an endpoint nothing listens on, for the detail as for the compact page.
	for _, detail := range []bool{false, true} {
		r := reqFor(t, "s3.overview", "127.0.0.1:1", map[string]any{"bucket": []string{"backups", "media"}, "detail": detail})
		_, err = runOverview(context.Background(), r)
		if verr, ok := err.(*view.Error); !ok || verr.Code != "s3.conn.refused" {
			t.Fatalf("detail %v: err = %v, want the s3.conn.refused every bucket met", detail, err)
		}
	}
}

// A cap on the list, so that a typo cannot start hundreds of requests a refresh.
//
// The refusal says what the cap is and that the call reaches more, and never
// how many it reaches nor that they were named: the list may be a profile's
// fence filled in by the host, and the host keeps a fence's contents from an
// agent, which a count of them would give back.
func TestMoreThanThirtyBucketsAreRefusedBeforeAnythingIsAsked(t *testing.T) {
	a := manyBuckets(t, 45)
	for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceMCP, plugin.SurfaceTUI} {
		r := reqFor(t, "s3.overview", endpointOf(t, a.Server), map[string]any{"bucket": names("b", 45)}).WithSurface(sf)
		_, err := runOverview(context.Background(), r)
		verr, ok := err.(*view.Error)
		if !ok || verr.Code != "s3.bucket.toomany" {
			t.Fatalf("%s: err = %v, want s3.bucket.toomany", sf, err)
		}
		if want := "an overview reads at most 30 buckets, and this call reaches more"; verr.Message != want {
			t.Errorf("%s: message = %q, want %q", sf, verr.Message, want)
		}
		for what, text := range map[string]string{"message": verr.Message, "hint": verr.Hint} {
			if strings.Contains(text, "45") || strings.Contains(text, "named") {
				t.Errorf("%s: the %s says how many the call reaches, or that they were named: %q", sf, what, text)
			}
		}
		if !strings.Contains(verr.Hint, "fewer") || !strings.Contains(verr.Hint, "fence") {
			t.Errorf("%s: the hint never says to name fewer or fence fewer: %q", sf, verr.Hint)
		}
	}
	if got := a.requests(); len(got) != 0 {
		t.Errorf("%d requests %v for a list that was refused", len(got), got)
	}

	// Thirty are read, and a name given twice is one.
	if _, err := overviewOn(t, a, map[string]any{"bucket": names("b", 30)}); err != nil {
		t.Fatalf("thirty buckets were refused: %v", err)
	}
	if got := len(a.requests()); got != 30 {
		t.Errorf("%d requests for thirty buckets, want 30", got)
	}
	if _, err := overviewOn(t, a, map[string]any{"bucket": append(names("b", 30), names("b", 30)...)}); err != nil {
		t.Errorf("thirty buckets named twice were refused: %v", err)
	}
}

// What a refresh costs, counted from the requests the service saw, at the most
// the cap lets it be: thirty buckets of more than a page of keys each.
func TestWhatARefreshCostsIsCountedFromTheRequestsTheServiceSaw(t *testing.T) {
	big := map[string][]fakeKey{}
	for _, name := range names("b", maxBuckets) {
		big[name] = numberedKeys(5000)
	}
	small, thirty := manyBuckets(t, 8), manyBuckets(t, 30)
	three := []string{"b00", "b01", "b02"}
	for name, c := range map[string]struct {
		account *accountFake
		values  map[string]any
		want    int
	}{
		"one bucket":                    {small, map[string]any{"bucket": "b00"}, 5},
		"one bucket in a list":          {small, map[string]any{"bucket": []string{"b00"}}, 5},
		"three named":                   {small, map[string]any{"bucket": three}, 3},
		"three named, detail":           {small, map[string]any{"bucket": three, "detail": true}, 1 + 3*4},
		"eight, none named":             {small, map[string]any{}, 1 + 8},
		"eight, none named, detail":     {small, map[string]any{"detail": true}, 1 + 8*4},
		"thirty big ones, none named":   {newAccount(t, big), map[string]any{}, 1 + 30},
		"thirty small ones, detail":     {thirty, map[string]any{"detail": true}, 1 + 30*4},
		"thirty big ones, detail":       {newAccount(t, big), map[string]any{"detail": true}, 1 + 30*(3+3)},
		"thirty big ones named":         {newAccount(t, big), map[string]any{"bucket": names("b", 30)}, 30},
		"thirty big ones named, detail": {newAccount(t, big), map[string]any{"bucket": names("b", 30), "detail": true}, 1 + 30*(3+3)},
	} {
		before := len(c.account.requests())
		if _, err := overviewOn(t, c.account, c.values); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// A bucket costs HEAD, versioning and lifecycle in the detail, and then the
		// listings of its first two thousand keys: two pages and one for a peek past
		// them when it has more, which is where three comes from.
		if got := len(c.account.requests()) - before; got != c.want {
			t.Errorf("%s: %d requests, want %d", name, got, c.want)
		}
	}
}

// Past thirty buckets in the account, the first thirty by name are read, and the
// verdict says how many were: ok for the thirty is a claim about thirty.
func TestAnAccountOfMoreThanThirtyBucketsReadsTheFirstThirtyAndSaysSo(t *testing.T) {
	a := manyBuckets(t, 41)
	kv := tileOn(t, a, map[string]any{})

	if got, want := rowOf(kv, "status"), "ok — 30 of 41 buckets read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := rowOf(kv, "buckets"); got != "41" {
		t.Errorf("buckets = %q, want the 41 the credentials see", got)
	}
	if got := len(a.requests()); got != 31 {
		t.Errorf("%d requests, want the account's listing and thirty buckets", got)
	}
	for i := range 41 {
		name, want := fmt.Sprintf("b%02d", i), 0
		if i < 30 {
			want = 1
		}
		if got := a.listingsOf(name); got != want {
			t.Errorf("%s was listed %d times, want %d: only the first thirty by name are read", name, got, want)
		}
	}
	assertFitsATile(t, kv)

	// A bucket it could not read, past the cut, is said as well.
	a = manyBuckets(t, 41)
	a.buckets["b03"].listDenied = true
	kv = tileOn(t, a, map[string]any{})
	if got, want := rowOf(kv, "status"), "warn — b03 could not be read; 30 of 41 buckets read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// Six reads at a time, however many buckets there are.
func TestNoMoreThanSixBucketsAreReadAtOnce(t *testing.T) {
	a := manyBuckets(t, 20)
	a.delay = 40 * time.Millisecond
	tileOn(t, a, map[string]any{"bucket": names("b", 20)})
	// The number is the documented one, not the constant, which a test of the
	// constant against itself would follow wherever it was moved.
	if peak := a.peakListings(); peak < 2 || peak > 6 {
		t.Errorf("%d listings at once, want several and no more than six", peak)
	}

	a = manyBuckets(t, 20)
	a.delay = 40 * time.Millisecond
	if _, err := overviewOn(t, a, map[string]any{"bucket": names("b", 20), "detail": true}); err != nil {
		t.Fatal(err)
	}
	if peak := a.peakListings(); peak < 2 || peak > 6 {
		t.Errorf("detail: %d listings at once, want several and no more than six", peak)
	}
}

// A tile holds eight rows: what does not fit is one row of the buckets that are
// well, and what is wrong is never the row that was cut.
func TestAThirtyBucketTileIsEightRowsWithTheWellOnesFolded(t *testing.T) {
	a := manyBuckets(t, 30)
	a.buckets["b17"].listDenied = true
	kv := tileOn(t, a, map[string]any{})

	if len(kv.Pairs) != 8 {
		t.Fatalf("%d rows, want eight: %v", len(kv.Pairs), kv.Pairs)
	}
	if got, want := keysOf(kv), "status,buckets,b17,b00,b01,b02,more,address"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}
	if got, want := rowOf(kv, "more"), "26 buckets, all ok"; got != want {
		t.Errorf("more = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "status"), "warn — b17 could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	assertFitsATile(t, kv)

	// A prefix takes a row, and the folded row grows by one.
	a = manyBuckets(t, 30)
	kv = tileOn(t, a, map[string]any{"prefix": "k100"})
	if len(kv.Pairs) != 8 {
		t.Fatalf("with a prefix: %d rows, want eight: %v", len(kv.Pairs), kv.Pairs)
	}
	if got, want := keysOf(kv), "status,buckets,prefix,b00,b01,b02,more,address"; got != want {
		t.Errorf("with a prefix: rows = %s, want %s", got, want)
	}
	if got, want := rowOf(kv, "more"), "27 buckets, all ok"; got != want {
		t.Errorf("with a prefix: more = %q, want %q", got, want)
	}

	// Five buckets fit as they are, six do not.
	if got := len(tileOn(t, manyBuckets(t, 5), map[string]any{}).Pairs); got != 8 {
		t.Errorf("five buckets are %d rows, want all of them and the three around them", got)
	}
	kv = tileOn(t, manyBuckets(t, 6), map[string]any{})
	if got, want := keysOf(kv), "status,buckets,b00,b01,b02,b03,more,address"; got != want {
		t.Errorf("six buckets: rows = %s, want %s", got, want)
	}
	if got, want := rowOf(kv, "more"), "2 buckets, all ok"; got != want {
		t.Errorf("six buckets: more = %q, want %q", got, want)
	}
}

// Past the eight rows, what could not be read is counted in the folded row.
func TestAFoldedRowSaysHowManyOfTheBucketsItHidesCouldNotBeRead(t *testing.T) {
	a := manyBuckets(t, 12)
	for _, name := range names("b", 10) {
		a.buckets[name].listDenied = true
	}
	kv := tileOn(t, a, map[string]any{})
	if got, want := rowOf(kv, "more"), "8 buckets, 6 could not be read"; got != want {
		t.Errorf("more = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "status"), "warn — 10 buckets could not be read, first b00"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	assertFitsATile(t, kv)
}

// A prefix holds every bucket's page to the keys under it.
func TestAPrefixHoldsEveryBucketOfAListToTheKeysUnderIt(t *testing.T) {
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	keys := []fakeKey{{Key: "app/a", Size: 1, Modified: at}, {Key: "app/b", Size: 1, Modified: at}, {Key: "other/x", Size: 1, Modified: at}}
	a := newAccount(t, map[string][]fakeKey{"one": keys, "two": keys})
	kv := tileOn(t, a, map[string]any{"bucket": []string{"one", "two"}, "prefix": "app/"})

	if got, want := keysOf(kv), "status,buckets,prefix,one,two,address"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}
	if got := rowOf(kv, "prefix"); got != "app/" {
		t.Errorf("prefix = %q", got)
	}
	if got := rowOf(kv, "one"); !strings.HasPrefix(got, "2 objects") {
		t.Errorf("one = %q, want the two under app/", got)
	}
	for _, l := range a.listings() {
		if l.query.Get("prefix") != "app/" {
			t.Errorf("%s was listed without the prefix: %v", l.bucket, l.query)
		}
	}
}

// A long name is cut so the row still fits a line of a tile, and a name that
// holds a newline or an escape is shown the way a list of names shows it.
func TestTheNameOfABucketFitsTheRowAndNeverBreaksIt(t *testing.T) {
	long := strings.Repeat("backup-", 8) + "end"
	a := newAccount(t, map[string][]fakeKey{long: numberedKeys(2500), "plain": numberedKeys(2), "odd\nname": numberedKeys(2)})
	kv := tileOn(t, a, map[string]any{})

	assertFitsATile(t, kv)
	var cut bool
	for _, p := range kv.Pairs {
		if strings.Contains(p.Key, "\n") || strings.Contains(p.Value, "\n") {
			t.Errorf("row %q = %q holds a newline", p.Key, p.Value)
		}
		cut = cut || (strings.HasPrefix(p.Key, "backup-backup") && strings.HasSuffix(p.Key, "…"))
	}
	if !cut {
		t.Errorf("rows = %v, want the long name cut with an ellipsis", kv.Pairs)
	}
	if rowOf(kv, `"odd\nname"`) == "" {
		t.Errorf("rows = %v, want the odd name quoted and its newline written out", kv.Pairs)
	}
}

// Status, buckets, prefix, more and address are valid bucket names, and a row
// that took the key of one of the page's own would be found by whoever looks the
// page up by key (an agent reading `status`, a tile colouring it) in place of the
// verdict. The bucket's row is `bucket <name>` then, and only then.
func TestABucketNamedLikeARowOfThePageNeverTakesThatRow(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{
		"status": numberedKeys(3), "address": numberedKeys(2), "buckets": numberedKeys(1), "logs": nil,
	})
	a.buckets["status"].listDenied = true
	kv := tileOn(t, a, map[string]any{})

	assertUniqueKeys(t, keysOf(kv))
	for key, want := range map[string]string{
		"status":         "warn — status could not be read",
		"buckets":        "4",
		"address":        endpointOf(t, a.Server),
		"bucket status":  "could not be read — access denied",
		"bucket address": "2 objects · newest " + format.Ago(time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)),
		"bucket buckets": "1 object · newest " + format.Ago(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		"logs":           "0 objects",
	} {
		if got := rowOf(kv, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got, want := keysOf(kv), "status,buckets,bucket status,logs,bucket buckets,bucket address,address"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}

	// The page's own prefix and folded rows are keys too.
	more := map[string][]fakeKey{"more": numberedKeys(2), "prefix": numberedKeys(2)}
	for _, name := range names("b", 9) {
		more[name] = numberedKeys(2)
	}
	b := newAccount(t, more)
	b.buckets["more"].listDenied = true
	kv = tileOn(t, b, map[string]any{"prefix": "k"})
	assertUniqueKeys(t, keysOf(kv))
	if got, want := rowOf(kv, "more"), "8 buckets, all ok"; got != want {
		t.Errorf("more = %q, want %q: the folded row is the page's", got, want)
	}
	if got, want := rowOf(kv, "prefix"), "k"; got != want {
		t.Errorf("prefix = %q, want %q: the filter is the page's", got, want)
	}
	if got, want := rowOf(kv, "bucket more"), "could not be read — access denied"; got != want {
		t.Errorf("bucket more = %q, want %q", got, want)
	}
	assertFitsATile(t, kv)

	// A name that collides with nothing is its own key, whatever its length.
	if got := rowOf(tileOn(t, newAccount(t, map[string][]fakeKey{"logs": nil}), map[string]any{}), "logs"); got != "0 objects" {
		t.Errorf("logs = %q: a name that is not a key of the page is its row's key", got)
	}
}

// A long name is cut to fit a tile, and two that are cut to the same thing would
// be one key: the second is spelled out, since a name too cut to tell from
// another is no name.
func TestTwoLongNamesThatAreCutAlikeAreNotOneKey(t *testing.T) {
	first, second := strings.Repeat("company-production-", 2)+"logs-eu", strings.Repeat("company-production-", 2)+"logs-us"
	a := newAccount(t, map[string][]fakeKey{first: numberedKeys(2500), second: numberedKeys(2500), "plain": nil})
	kv := tileOn(t, a, map[string]any{})

	assertUniqueKeys(t, keysOf(kv))
	var spelled, cut int
	for _, p := range kv.Pairs {
		switch {
		case p.Key == first || p.Key == second:
			spelled++
		case strings.HasPrefix(p.Key, "company-production-") && strings.HasSuffix(p.Key, "…"):
			cut++
		}
	}
	if spelled != 1 || cut != 1 {
		t.Errorf("rows = %v, want one of the two cut and the other in full", kv.Pairs)
	}
}

func assertUniqueKeys(t *testing.T, keys string) {
	t.Helper()
	seen := map[string]bool{}
	for _, key := range strings.Split(keys, ",") {
		if seen[key] {
			t.Errorf("the key %q is on the page twice: %s", key, keys)
		}
		seen[key] = true
	}
}

// The same in the detail: a section is titled by its bucket, and the one the
// page opens with is `status`.
func TestADetailBucketNamedStatusIsNotTheStatusSection(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"status": numberedKeys(3), "logs": numberedKeys(1)})
	a.buckets["status"].listDenied = true
	v, err := overviewOn(t, a, map[string]any{"detail": true})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	sections := map[string]map[string]string{}
	for _, s := range v.(view.Sections).Items {
		titles = append(titles, s.Title)
		sections[s.Title] = pairsOf(t, s.View)
	}
	if got, want := strings.Join(titles, ","), "status,bucket status,logs"; got != want {
		t.Fatalf("sections = %s, want %s", got, want)
	}
	if got, want := sections["status"]["status"], "warn — status objects could not be read"; got != want {
		t.Errorf("the status section says %q, want %q", got, want)
	}
	if got := sections["status"]["address"]; got != endpointOf(t, a.Server) {
		t.Errorf("the status section's address = %q", got)
	}
	if got, want := sections["bucket status"]["objects"], "could not be read — access denied"; got != want {
		t.Errorf("the bucket's section says %q, want %q", got, want)
	}

	// A bucket named status that is not there is a section that says so, and is
	// not the verdict either.
	v, err = overviewOn(t, newAccount(t, map[string][]fakeKey{"logs": numberedKeys(1)}),
		map[string]any{"bucket": []string{"status", "logs"}, "detail": true})
	if err != nil {
		t.Fatal(err)
	}
	titles, sections = nil, map[string]map[string]string{}
	for _, s := range v.(view.Sections).Items {
		titles = append(titles, s.Title)
		sections[s.Title] = pairsOf(t, s.View)
	}
	if got, want := strings.Join(titles, ","), "status,bucket status,logs"; got != want {
		t.Fatalf("sections = %s, want %s", got, want)
	}
	if got, want := sections["status"]["status"], "warn — status could not be read"; got != want {
		t.Errorf("the status section says %q, want %q", got, want)
	}
	if got, want := sections["bucket status"]["status"], "could not be read — not found"; got != want {
		t.Errorf("the bucket's section says %q, want %q", got, want)
	}
}

// The detail is the single-bucket detail of each: a section a bucket, in the
// order of the compact page, with the account asked once for the dates.
func TestTheDetailOfSeveralBucketsIsTheDetailOfEachOne(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3), "media": numberedKeys(2500), "logs": numberedKeys(1)})
	a.buckets["media"].lifecycle = thirtyDays
	a.buckets["logs"].lifecycle = "denied"
	v, err := overviewOn(t, a, map[string]any{"bucket": []string{"media", "backups", "logs"}, "detail": true})
	if err != nil {
		t.Fatal(err)
	}
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("the detail is a %s, want a page", view.TypeOf(v))
	}
	var titles []string
	sections := map[string]map[string]string{}
	for _, s := range page.Items {
		titles = append(titles, s.Title)
		sections[s.Title] = pairsOf(t, s.View)
	}
	if got, want := strings.Join(titles, ","), "status,logs,backups,media"; got != want {
		t.Fatalf("sections = %s, want %s", got, want)
	}
	if got, want := sections["status"]["status"], "warn — logs retention could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := sections["status"]["buckets"]; got != "3 named" {
		t.Errorf("buckets = %q", got)
	}
	if got := sections["status"]["address"]; got != endpointOf(t, a.Server) {
		t.Errorf("address = %q", got)
	}

	// Each section is what the detail of that bucket alone says of it.
	for _, name := range []string{"backups", "media", "logs"} {
		single, err := overviewOn(t, a, map[string]any{"bucket": name, "detail": true})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{}
		for _, s := range single.(view.Sections).Items {
			for k, val := range pairsOf(t, s.View) {
				if k != "address" && k != "bucket" {
					want[k] = val
				}
			}
		}
		got := sections[name]
		for k, val := range want {
			if got[k] != val {
				t.Errorf("%s: %s = %q, the detail of the bucket alone says %q", name, k, got[k], val)
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("%s: %s = %q is a row the detail of the bucket alone does not have", name, k, got[k])
			}
		}
	}
	if got := sections["media"]["retention"]; got != "1 rule, objects expire after 30d" {
		t.Errorf("media retention = %q", got)
	}
	if got := sections["logs"]["status"]; got != "warn — retention could not be read" {
		t.Errorf("logs status = %q", got)
	}
}

// The detail asks the account once, however many buckets: the dates of
// creation are in its listing and nowhere else.
func TestTheDetailAsksTheAccountOnceAndEachBucketForItsOwn(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3), "media": numberedKeys(5)})
	if _, err := overviewOn(t, a, map[string]any{"bucket": []string{"backups", "media"}, "detail": true}); err != nil {
		t.Fatal(err)
	}
	if got := a.requestsLike(`^GET /$`); got != 1 {
		t.Errorf("the account was listed %d times, want once: %v", got, a.requests())
	}
	for _, name := range []string{"backups", "media"} {
		if got := a.requestsLike(`^HEAD /` + name + `/`); got != 1 {
			t.Errorf("%s was looked up %d times, want once", name, got)
		}
		if got := a.requestsLike(`^GET /` + name + `/\?versioning`); got != 1 {
			t.Errorf("%s had its versioning read %d times, want once", name, got)
		}
		if got := a.requestsLike(`^GET /` + name + `/\?lifecycle`); got != 1 {
			t.Errorf("%s had its lifecycle read %d times, want once", name, got)
		}
		if a.listingsOf(name) == 0 {
			t.Errorf("%s was never listed", name)
		}
	}

	// A key that may not list the account still gets the buckets, without dates.
	a = newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3), "media": numberedKeys(5)})
	a.accountDenied = true
	v, err := overviewOn(t, a, map[string]any{"bucket": []string{"backups", "media"}, "detail": true})
	if err != nil {
		t.Fatalf("a key that may not list the account lost the detail: %v", err)
	}
	for _, s := range v.(view.Sections).Items {
		if _, dated := pairsOf(t, s.View)["created"]; dated {
			t.Errorf("section %s has a creation date the account never gave", s.Title)
		}
	}
}

func TestADetailBucketThatIsNotThereIsASectionSayingSo(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": numberedKeys(3)})
	v, err := overviewOn(t, a, map[string]any{"bucket": []string{"backups", "nosuch"}, "detail": true})
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]map[string]string{}
	var titles []string
	for _, s := range v.(view.Sections).Items {
		titles = append(titles, s.Title)
		sections[s.Title] = pairsOf(t, s.View)
	}
	if got, want := strings.Join(titles, ","), "status,nosuch,backups"; got != want {
		t.Fatalf("sections = %s, want %s", got, want)
	}
	if got, want := sections["nosuch"]["status"], "could not be read — not found"; got != want {
		t.Errorf("nosuch = %v, want %q", sections["nosuch"], want)
	}
	if got, want := sections["status"]["status"], "warn — nosuch could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if sections["backups"]["objects"] != "3 objects" {
		t.Errorf("backups = %v, want the bucket that is there to answer", sections["backups"])
	}
}

// The detail of an account past the cut says it read part of it, in the verdict
// and as a warning on the page.
func TestTheDetailOfAnAccountPastTheCutSaysHowManyWereRead(t *testing.T) {
	a := manyBuckets(t, 35)
	v, err := overviewOn(t, a, map[string]any{"detail": true})
	if err != nil {
		t.Fatal(err)
	}
	page := v.(view.Sections)
	if got, want := pairsOf(t, page.Items[0].View)["status"], "ok — 30 of 35 buckets read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if len(page.Items) != 31 {
		t.Errorf("%d sections, want the status and thirty buckets", len(page.Items))
	}
	if len(page.Warnings) != 1 || page.Warnings[0].Code != "s3.buckets.partial" || page.Warnings[0].Hint == "" {
		t.Errorf("warnings = %+v, want the page said to be partial", page.Warnings)
	}
}

// An endpoint that never answers is given up on at the overview's bound for a
// list as for a name, and a page of rows that all say they did not answer is
// never what it answers.
func TestAListOfBucketsOnAnEndpointThatNeverAnswersIsGivenUpOn(t *testing.T) {
	boundOverviewAt(t, 150*time.Millisecond)
	srv := silentEndpoint(t)
	for name, values := range map[string]map[string]any{
		"a list":            {"bucket": []string{"one", "two"}},
		"a list and detail": {"bucket": []string{"one", "two"}, "detail": true},
	} {
		started := time.Now()
		_, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, srv), values))
		verr, ok := err.(*view.Error)
		if !ok || verr.Code != "s3.timeout" {
			t.Fatalf("%s: err = %#v, want s3.timeout", name, err)
		}
		if took := time.Since(started); took > 2*time.Second {
			t.Errorf("%s: gave up after %s, want about the bound of 150ms", name, took)
		}
	}
}

// One bucket that does not answer in time is a row that says so, and the page is
// what the others answered: it is not the whole overview turned into a timeout,
// which is what an endpoint that answers none of them is.
func TestABucketThatDoesNotAnswerInTimeIsARowAndTheOthersStillAnswer(t *testing.T) {
	boundOverviewAt(t, 300*time.Millisecond)
	for name, values := range map[string]map[string]any{
		"named":              {"bucket": []string{"fine1", "fine2", "slow"}},
		"none named":         {},
		"named with detail":  {"bucket": []string{"fine1", "fine2", "slow"}, "detail": true},
		"none named, detail": {"detail": true},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAccount(t, map[string][]fakeKey{"fine1": numberedKeys(3), "fine2": numberedKeys(2), "slow": numberedKeys(1)})
			a.buckets["slow"].stalls = true
			started := time.Now()
			v, err := overviewOn(t, a, values)
			if err != nil {
				t.Fatalf("one bucket that did not answer turned the page into an error: %v", err)
			}
			if took := time.Since(started); took > 3*time.Second {
				t.Errorf("took %s, want about the bound of 300ms", took)
			}

			var status, slow, fine1 string
			switch page := v.(type) {
			case view.KeyValue:
				status, slow, fine1 = rowOf(page, "status"), rowOf(page, "slow"), rowOf(page, "fine1")
			case view.Sections:
				sections := map[string]map[string]string{}
				for _, s := range page.Items {
					sections[s.Title] = pairsOf(t, s.View)
				}
				status, slow, fine1 = sections["status"]["status"], sections["slow"]["status"], sections["fine1"]["objects"]
			default:
				t.Fatalf("the page is a %s", view.TypeOf(v))
			}
			if want := "warn — slow could not be read"; status != want {
				t.Errorf("status = %q, want %q", status, want)
			}
			if want := "could not be read — did not answer in time"; slow != want {
				t.Errorf("slow = %q, want %q", slow, want)
			}
			if !strings.HasPrefix(fine1, "3 objects") {
				t.Errorf("fine1 = %q: a bucket that answered stands beside the one that did not", fine1)
			}
		})
	}
}

// Only the caller's own cancel ends the call: a bucket that answered before it is
// not a page, and the one that had not is not a row.
func TestACallerThatGivesUpMidWayEndsTheCallAndGetsNoPage(t *testing.T) {
	boundOverviewAt(t, 10*time.Second)
	for _, detail := range []bool{false, true} {
		a := newAccount(t, map[string][]fakeKey{"fine": numberedKeys(3), "slow": numberedKeys(1)})
		a.buckets["slow"].stalls = true
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(150*time.Millisecond, cancel)
		r := reqFor(t, "s3.overview", endpointOf(t, a.Server), map[string]any{"bucket": []string{"fine", "slow"}, "detail": detail})
		started := time.Now()
		v, err := runOverview(ctx, r)
		timer.Stop()
		cancel()
		verr, ok := err.(*view.Error)
		if !ok || verr.Code != "s3.cancelled" {
			t.Fatalf("detail %v: got %v and err %v, want s3.cancelled", detail, v, err)
		}
		if took := time.Since(started); took > 3*time.Second {
			t.Errorf("detail %v: the call went on for %s after the caller gave up", detail, took)
		}
	}
}

// The detail's deadline is the overview's bound for each round of six buckets,
// from the start of the call: thirteen buckets that each take 100ms with a bound
// of 200ms need three rounds, and a flat bound would cut the last of them.
func TestTheDetailsDeadlineGrowsByTheBoundForEachRoundOfSixBuckets(t *testing.T) {
	boundOverviewAt(t, 200*time.Millisecond)
	a := manyBuckets(t, 13)
	a.delay = 100 * time.Millisecond
	started := time.Now()
	v, err := overviewOn(t, a, map[string]any{"detail": true})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took <= 200*time.Millisecond {
		t.Fatalf("took %s: the test never needed more than the flat bound", took)
	}
	page := v.(view.Sections)
	if got := pairsOf(t, page.Items[0].View)["status"]; got != "ok" {
		t.Errorf("status = %q: a bucket in the last round was cut by a bound of one round's", got)
	}
	if len(page.Items) != 14 {
		t.Errorf("%d sections, want the status and thirteen buckets", len(page.Items))
	}

	// And it is still one deadline for the page: thirteen buckets of which one
	// never answers are given up on after three rounds, not after thirteen.
	a = manyBuckets(t, 13)
	a.buckets["b05"].stalls = true
	started = time.Now()
	v, err = overviewOn(t, a, map[string]any{"detail": true})
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(started)
	if took < 500*time.Millisecond || took > 1500*time.Millisecond {
		t.Errorf("took %s, want about three bounds of 200ms", took)
	}
	if got, want := pairsOf(t, v.(view.Sections).Items[0].View)["status"], "warn — b05 could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// The host holds each element of the list to the fence: every element inside it
// passes, and one outside is refused, element by element. What a call that
// names none resolves to under a fence of several is the host's to say, and it
// is not the same before and after the rta release that fills a list input with
// the whole fenced list, so nothing here asserts it. What the plugin does with
// that list, once it has it, is the test that follows.
func TestAFenceOfThreeHoldsEachBucketOfTheList(t *testing.T) {
	fenced := []any{"alpha", "bravo", "charlie"}
	for _, id := range []string{"s3.overview", "s3.bucket.list"} {
		c := capabilityOf(t, id)
		fence := plugin.Fence{"bucket": fenced}
		inside := map[string]map[string]any{
			"all three":   {"bucket": []string{"alpha", "bravo", "charlie"}},
			"two":         {"bucket": []string{"alpha", "charlie"}},
			"over MCP":    {"bucket": []any{"bravo", "alpha"}},
			"a bare name": {"bucket": "bravo"},
		}
		outside := map[string]map[string]any{
			"only outside":          {"bucket": []string{"delta"}},
			"a padded fence entry":  {"bucket": []string{" alpha"}},
			"outside after all":     {"bucket": []string{"alpha", "bravo", "charlie", "delta"}},
			"outside before all":    {"bucket": []string{"delta", "alpha", "bravo", "charlie"}},
			"a bare name outside":   {"bucket": "delta"},
			"outside over MCP":      {"bucket": []any{"alpha", "delta"}},
			"outside among a blank": {"bucket": []string{"", "delta"}},
		}
		for _, entry := range fenced {
			inside["only "+entry.(string)] = map[string]any{"bucket": []string{entry.(string)}}
			outside["one outside with "+entry.(string)] = map[string]any{"bucket": []string{entry.(string), "delta"}}
		}
		for name, caller := range inside {
			in := plugin.Inputs{Fence: fence, Caller: caller}
			if breach := plugin.CheckFence(c, in); breach != nil {
				t.Errorf("%s %s: a list inside the fence was refused: %+v", id, name, breach)
				continue
			}
			if got := plugin.ResolveRequest(c, in, false, false).StringSlice("bucket"); len(got) == 0 {
				t.Errorf("%s %s: the handler reads no bucket from %v", id, name, caller)
			}
		}
		for name, caller := range outside {
			breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: caller})
			if breach == nil {
				t.Errorf("%s %s got past a fence on the bucket", id, name)
				continue
			}
			if breach.Input.Name != "bucket" {
				t.Errorf("%s %s: the breach is of %q, want bucket", id, name, breach.Input.Name)
			}
		}
		// A fence of one fills the input, in the shape it reads.
		one := plugin.Inputs{Fence: plugin.Fence{"bucket": {"alpha"}}, Caller: map[string]any{}}
		if breach := plugin.CheckFence(c, one); breach != nil {
			t.Fatalf("%s: a call naming nothing was refused under a one-entry fence: %+v", id, breach)
		}
		if got := plugin.ResolveRequest(c, one, false, false).StringSlice("bucket"); len(got) != 1 || got[0] != "alpha" {
			t.Errorf("%s: the fence filled bucket = %v, want alpha", id, got)
		}
	}
}

// The seam the list exists for: a connection fenced to three buckets of an
// account of four, and a call that names none, which the host resolves to the
// fence itself (the same names a caller naming all three sends, which the fence
// lets through). Each of the three is read, with the request of its own, and
// the fourth is neither read nor listed.
func TestTheWholeFencedListIsOnePageOfItsBucketsAndNoOtherOfTheAccount(t *testing.T) {
	fenced := []string{"alpha", "bravo", "charlie"}
	a := newAccount(t, map[string][]fakeKey{
		"alpha": numberedKeys(3), "bravo": numberedKeys(2), "charlie": nil, "delta": numberedKeys(4),
	})
	for _, id := range []string{"s3.overview", "s3.bucket.list"} {
		in := plugin.Inputs{Fence: plugin.Fence{"bucket": {"alpha", "bravo", "charlie"}}, Caller: map[string]any{"bucket": fenced}}
		if breach := plugin.CheckFence(capabilityOf(t, id), in); breach != nil {
			t.Fatalf("%s: the whole fenced list was refused by the fence it came from: %+v", id, breach)
		}
	}

	kv := tileOn(t, a, map[string]any{"bucket": fenced})
	if got, want := keysOf(kv), "status,buckets,charlie,bravo,alpha,address"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}
	if got := rowOf(kv, "buckets"); got != "3 named" {
		t.Errorf("buckets = %q", got)
	}
	if got := rowOf(kv, "status"); got != "ok" {
		t.Errorf("status = %q", got)
	}
	if got := rowOf(kv, "alpha"); !strings.HasPrefix(got, "3 objects") {
		t.Errorf("alpha = %q", got)
	}
	if got := len(a.requests()); got != 3 {
		t.Errorf("%d requests %v, want a page for each of the three", got, a.requests())
	}
	if a.requestsLike(`delta|^GET /$`) != 0 {
		t.Errorf("requests = %v: the bucket outside the fence, or the account, was asked", a.requests())
	}

	rows := listRows(t, listOn(t, a, map[string]any{"bucket": fenced}))
	if len(rows) != 3 || rows[0][0] != "alpha" || rows[1][0] != "bravo" || rows[2][0] != "charlie" {
		t.Errorf("rows = %v, want the three of the fence by name and not delta", rows)
	}
}

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

// The bucket of the two listings is a list the way the one bucket was a name:
// the short b and the config key a profile's fence binds to are the same, the
// words say it is a list, and only these two take one.
func TestTheBucketOfTheTwoListingsIsAListAndTheOthersAreStillOneBucket(t *testing.T) {
	for _, id := range []string{"s3.overview", "s3.bucket.list"} {
		c := capabilityOf(t, id)
		var field plugin.Field
		for _, f := range c.Inputs {
			if f.Name == "bucket" {
				field = f
			}
		}
		if field.Type != plugin.StringSlice || field.Short != "b" || field.Config != "bucket" || field.Required {
			t.Errorf("%s: bucket is %+v, want an optional list with the short b bound to the config key bucket", id, field)
		}
		if field.Local {
			t.Errorf("%s: bucket is Local, which a fence cannot fill and a list on a dashboard cannot name", id)
		}
		if !strings.Contains(field.Help, "buckets") {
			t.Errorf("%s: the help never says bucket is a list: %q", id, field.Help)
		}
		if c.Scope != "" || c.NeedsGrant {
			t.Errorf("%s names the grant scope %q: a listing that is read-only has none, and a selector must never stand in one", id, c.Scope)
		}
	}
	for _, text := range []string{capabilityOf(t, "s3.overview").Description, capabilityOf(t, "s3.overview").Agent} {
		if !strings.Contains(text, "buckets") || !strings.Contains(text, strconv.Itoa(maxBuckets)) {
			t.Errorf("the overview's words never say it reads a list of buckets, at most %d: %q", maxBuckets, text)
		}
	}
	for _, c := range Plugin().Capabilities {
		if c.ID == "s3.overview" || c.ID == "s3.bucket.list" {
			continue
		}
		for _, f := range c.Inputs {
			if f.Name == "bucket" && f.Type == plugin.StringSlice {
				t.Errorf("%s takes a list of buckets, which only the two listings do", c.ID)
			}
		}
	}
	if err := Plugin().Validate(); err != nil {
		t.Errorf("the declaration is refused: %v", err)
	}
	if got := capabilityOf(t, "s3.overview").Pace(); got != 2*time.Minute {
		t.Errorf("the overview paces at %s, want the two minutes it had", got)
	}
}

// A list of buckets is never read as the one bucket a key action works on: the
// required bucket of the others stays the single name, Live completion and all.
func TestTheBucketOfTheOtherCapabilitiesIsStillOneRequiredName(t *testing.T) {
	for _, id := range []string{"s3.policy.get", "s3.object.list", "s3.object.tree", "s3.object.show", "s3.bucket.download", "s3.bucket.upload"} {
		var field plugin.Field
		for _, f := range capabilityOf(t, id).Inputs {
			if f.Name == "bucket" {
				field = f
			}
		}
		if field.Type != plugin.String || !field.Required || !field.Live {
			t.Errorf("%s: bucket is %+v, want the required name it was", id, field)
		}
	}
}

// The prefix box of the overview completes under the bucket when there is one.
func TestThePrefixOfTheOverviewCompletesUnderTheOneBucketNamed(t *testing.T) {
	srv, asked := recordingS3(t, keysXML)
	complete := suggestKeys("prefix")
	one := reqFor(t, "s3.overview", endpointOf(t, srv), map[string]any{"bucket": []string{"backups"}, "prefix": "db/"})
	if got := complete(context.Background(), one); len(got) == 0 {
		t.Error("a list of one bucket completed nothing")
	}
	n := len(asked())
	two := reqFor(t, "s3.overview", endpointOf(t, srv), map[string]any{"bucket": []string{"backups", "media"}, "prefix": "db/"})
	if got := complete(context.Background(), two); got != nil || len(asked()) != n {
		t.Errorf("a list of two buckets completed %v, want nothing and no request", got)
	}
}

func listRows(t *testing.T, v view.View) [][]string {
	t.Helper()
	tbl, ok := v.(view.Table)
	if !ok {
		t.Fatalf("want a table, got %s", view.TypeOf(v))
	}
	return tbl.Rows
}

func listOn(t *testing.T, a *accountFake, values map[string]any) view.View {
	t.Helper()
	v, err := runBucketList(context.Background(), reqFor(t, "s3.bucket.list", endpointOf(t, a.Server), values))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A list narrows the listing to those buckets, and a name the credentials do not
// see is a row saying so.
func TestTheBucketListOfSeveralBucketsHoldsThoseAndSaysWhichAreNotThere(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": nil, "media": nil, "logs": nil})

	rows := listRows(t, listOn(t, a, map[string]any{"bucket": []string{"media", "backups"}}))
	if len(rows) != 2 || rows[0][0] != "backups" || rows[1][0] != "media" {
		t.Errorf("rows = %v, want the two named, by name", rows)
	}

	v := listOn(t, a, map[string]any{"bucket": []string{"media", "nosuch", "backups"}})
	rows = listRows(t, v)
	if len(rows) != 3 || rows[0][0] != "backups" || rows[1][0] != "media" || rows[2][0] != "nosuch" {
		t.Fatalf("rows = %v, want the two there and then the one that is not", rows)
	}
	if got, want := rows[2][len(rows[2])-1], "not found"; got != want {
		t.Errorf("the row of a bucket that is not there says %q, want %q", got, want)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(rows[0][len(rows[0])-1]) {
		t.Errorf("a bucket that is there has the date it was created in %q", rows[0])
	}

	// Naming none is every bucket, and a list longer than the overview reads is
	// still one request.
	if rows := listRows(t, listOn(t, a, map[string]any{})); len(rows) != 3 {
		t.Errorf("rows = %d, want every bucket when none is named", len(rows))
	}
	many := newAccount(t, map[string][]fakeKey{"backups": nil})
	listOn(t, many, map[string]any{"bucket": names("b", 45)})
	if got := len(many.requests()); got != 1 {
		t.Errorf("%d requests for 45 names, want the account's listing", got)
	}
}

// A list of one is the table the one name gave, in the empty case too.
func TestTheBucketListOfOneBucketInAListIsTheListOfThatName(t *testing.T) {
	a := newAccount(t, map[string][]fakeKey{"backups": nil, "media": nil})
	for _, name := range []string{"backups", "nosuch"} {
		plain := marshalled(t, listOn(t, a, map[string]any{"bucket": name}))
		for label, bucket := range map[string]any{"a list": []string{name}, "over MCP": []any{name}, "twice": []string{name, name}, "a blank": []string{name, ""}} {
			if got := marshalled(t, listOn(t, a, map[string]any{"bucket": bucket})); got != plain {
				t.Errorf("%s %s: %s, the plain name gave %s", name, label, got, plain)
			}
		}
	}
	assertEmptyTable(t, listOn(t, a, map[string]any{"bucket": []string{"nosuch"}}), `no bucket named "nosuch"`)
}
