package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The recipes behind live completion, proven against a server that records
// what was asked: names only come back, only GETs go out, the delimiter
// walks one segment, and every failure is silence rather than an error.

// recordingS3 serves canned XML and records every request line.
func recordingS3(t *testing.T, body string) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu    sync.Mutex
		asked []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

const bucketsXML = `<?xml version="1.0" encoding="UTF-8"?>
<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Owner><ID>me</ID></Owner>
  <Buckets>
    <Bucket><Name>media</Name><CreationDate>2024-03-01T00:00:00.000Z</CreationDate></Bucket>
    <Bucket><Name>backups</Name><CreationDate>2024-03-01T00:00:00.000Z</CreationDate></Bucket>
  </Buckets>
</ListAllMyBucketsResult>`

const keysXML = `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>backups</Name>
  <Prefix>db/</Prefix>
  <Delimiter>/</Delimiter>
  <KeyCount>2</KeyCount>
  <IsTruncated>false</IsTruncated>
  <Contents><Key>db/dump.sql</Key><Size>10</Size>
    <LastModified>2024-03-01T00:00:00.000Z</LastModified></Contents>
  <CommonPrefixes><Prefix>db/daily/</Prefix></CommonPrefixes>
</ListBucketResult>`

func TestSuggestBucketsListsNamesOnlyThroughGETs(t *testing.T) {
	srv, asked := recordingS3(t, bucketsXML)
	r := reqFor(t, "s3.bucket.list", endpointOf(t, srv), map[string]any{})

	got := suggestBuckets(context.Background(), r)
	if len(got) != 2 || got[0] != "backups" || got[1] != "media" {
		t.Fatalf("suggestBuckets = %v, want the two names, sorted", got)
	}
	for _, line := range asked() {
		if !strings.HasPrefix(line, "GET ") {
			t.Errorf("a completion sent %q — a listing must never be anything but a GET", line)
		}
	}
}

func TestSuggestKeysWalksOneSegmentAndKeepsTheSeparator(t *testing.T) {
	srv, asked := recordingS3(t, keysXML)
	r := reqFor(t, "s3.object.list", endpointOf(t, srv), map[string]any{
		"bucket": "backups", "prefix": "db/",
	})

	got := suggestKeys("prefix")(context.Background(), r)
	want := map[string]bool{"db/dump.sql": true, "db/daily/": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("suggestKeys = %v, want the key and the common prefix with its trailing slash — "+
			"the slash is what lets the next press fetch deeper", got)
	}
	segmentWalked := false
	for _, line := range asked() {
		if !strings.HasPrefix(line, "GET ") {
			t.Errorf("a completion sent %q, want GETs only", line)
		}
		if strings.Contains(line, "delimiter=%2F") && strings.Contains(line, "prefix=db%2F") {
			segmentWalked = true
		}
	}
	if !segmentWalked {
		t.Errorf("no request pinned delimiter=/ with the typed prefix; asked: %v — without the "+
			"delimiter this walks the whole bucket", asked())
	}
}

// A million-object bucket answers with a screenful, and the listing stops
// there rather than draining the rest for nothing.
func TestAListingIsCapped(t *testing.T) {
	var keys strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&keys, "<Contents><Key>k-%03d</Key><Size>1</Size>"+
			"<LastModified>2024-03-01T00:00:00.000Z</LastModified></Contents>", i)
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>big</Name><KeyCount>200</KeyCount><IsTruncated>false</IsTruncated>` +
		keys.String() + `</ListBucketResult>`
	srv, _ := recordingS3(t, body)
	r := reqFor(t, "s3.object.list", endpointOf(t, srv), map[string]any{"bucket": "big"})

	got := suggestKeys("prefix")(context.Background(), r)
	if len(got) != completionCap {
		t.Errorf("listing returned %d entries, want it capped at %d", len(got), completionCap)
	}
}

// Every failure is silence: a completion that cannot answer must slow nobody
// down, and the run that follows classifies the same failure with a code and
// a hint.
func TestSuggestionsAreSilentOnEveryFailure(t *testing.T) {
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(denied.Close)

	cases := []struct {
		name     string
		endpoint string
	}{
		{"denied", endpointOf(t, denied)},
		{"nothing listening", "127.0.0.1:1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := reqFor(t, "s3.object.list", tc.endpoint, map[string]any{"bucket": "b"})
			if got := suggestBuckets(context.Background(), r); got != nil {
				t.Errorf("suggestBuckets = %v, want silence", got)
			}
			if got := suggestKeys("prefix")(context.Background(), r); got != nil {
				t.Errorf("suggestKeys = %v, want silence", got)
			}
		})
	}
}

// The wiring is part of the contract: every input whose values exist
// server-side declares Live with a Suggest, so a refactor cannot silently
// put a service listing back on the keystroke channel — or drop the
// completion — without failing here.
func TestServerSideInputsDeclareLive(t *testing.T) {
	wantLive := map[string]bool{"bucket": true, "key": true, "prefix": true, "dest-bucket": true}
	seen := map[string]bool{}
	for _, c := range Plugin().Capabilities {
		for _, f := range c.Inputs {
			if wantLive[f.Name] {
				seen[f.Name] = true
				if !f.Live || f.Suggest == nil {
					t.Errorf("%s: input %q is not Live with a Suggest", c.ID, f.Name)
				}
			} else if f.Live {
				t.Errorf("%s: input %q declares Live, and nothing here lists it server-side", c.ID, f.Name)
			}
		}
	}
	for name := range wantLive {
		if !seen[name] {
			t.Errorf("no capability declares input %q — the wiring this test pins moved", name)
		}
	}
}

// The bucket of s3.overview and s3.bucket.list is a list, completed on a press of
// the key a name at a time. The host calls the Suggest the way press does: what the
// call resolves to, with the name being typed under the field's own name as a
// string, and holds the answer to the connection's fence.
//
// The capabilities that act on one bucket keep their required name and complete it
// as before (TestTheBucketOfTheOtherCapabilitiesIsStillOneRequiredName).

func bucketInput(t *testing.T, id string) plugin.Field {
	t.Helper()
	for _, f := range capabilityOf(t, id).Inputs {
		if f.Name == "bucket" {
			return f
		}
	}
	t.Fatalf("%s has no bucket", id)
	return plugin.Field{}
}

func press(t *testing.T, id, endpoint, element string, fence plugin.Fence) []string {
	t.Helper()
	f, req := pressRequest(t, id, endpoint, element, fence)
	return fence.Filter(f, f.Suggest(context.Background(), req))
}

// pressRequest is the Suggest of the bucket box and the request a press answers it
// with, for a test that makes the call itself.
func pressRequest(t *testing.T, id, endpoint, element string, fence plugin.Fence) (plugin.Field, plugin.Request) {
	t.Helper()
	f := bucketInput(t, id)
	resolved := plugin.Resolve(capabilityOf(t, id), plugin.Inputs{Caller: map[string]any{
		"address": endpoint, "access-key": "test", "secret-key": "test",
	}, Fence: fence})
	resolved[f.Name] = element
	return f, plugin.LiveRequest(resolved)
}

func TestTheBucketOfTheTwoListingsCompletesOnAPress(t *testing.T) {
	for _, id := range []string{"s3.overview", "s3.bucket.list"} {
		f := bucketInput(t, id)
		if f.Type != plugin.StringSlice || f.Short != "b" || f.Config != "bucket" || f.Required || !f.Live || f.Suggest == nil {
			t.Errorf("%s: bucket is %+v, want an optional list completed live", id, f)
		}
	}
	if err := Plugin().Validate(); err != nil {
		t.Errorf("the declaration is refused: %v", err)
	}
}

func TestAPressOnTheBucketListNamesEveryBucketWhateverIsTyped(t *testing.T) {
	srv, asked := recordingS3(t, bucketsXML)
	presses := 0
	for _, id := range []string{"s3.overview", "s3.bucket.list"} {
		for _, element := range []string{"", "me", "backups", "no-such-bucket"} {
			presses++
			got := press(t, id, endpointOf(t, srv), element, nil)
			if len(got) != 2 || got[0] != "backups" || got[1] != "media" {
				t.Errorf("%s: a press on %q = %v, want both buckets, sorted: the box matches the typed text", id, element, got)
			}
		}
	}
	lines := asked()
	if len(lines) != presses {
		t.Fatalf("%d presses sent %d requests, want one each: %v", presses, len(lines), lines)
	}
	for _, line := range lines {
		if line != "GET /" {
			t.Errorf("a press sent %q, want the account's listing and nothing else", line)
		}
	}
}

func TestAPressUnderAFenceOffersOnlyTheBucketsTheFenceAllows(t *testing.T) {
	srv, _ := recordingS3(t, bucketsXML)
	fence := plugin.Fence{"bucket": {"media"}}
	for _, id := range []string{"s3.overview", "s3.bucket.list"} {
		if got := press(t, id, endpointOf(t, srv), "", fence); len(got) != 1 || got[0] != "media" {
			t.Errorf("%s: a fenced press = %v, want only media", id, got)
		}
	}
}

// typedElements is what a person can type into the box of a list: the start of a name
// and a whole one, and then what no name has in it, a comma, a quote, a space, a leading
// dash, a path that walks up, a separator, a line break, a shell's metacharacters, and
// more characters than a name has.
var typedElements = []string{
	"gi", "media",
	"a,b", `"quoted"`, "two words", "-n", "--bucket=other", "../../etc/hosts",
	"bucket/with/slashes", "line\nbreak", "'; touch /tmp/x #", strings.Repeat("x", 300),
}

// onTheWire is a request as the service got it: the line, the headers and the body. The
// date and the signature are left out, which are not the same twice; the credential and
// the headers that were signed stay.
func onTheWire(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\nHost: %s\n", r.Method, r.URL.RequestURI(), r.Proto, r.Host)
	for _, name := range slices.Sorted(maps.Keys(r.Header)) {
		value := strings.Join(r.Header[name], ",")
		switch name {
		case "X-Amz-Date":
			continue
		case "Authorization":
			value, _, _ = strings.Cut(value, ", Signature=")
		}
		fmt.Fprintf(&b, "%s: %s\n", name, value)
	}
	return b.String() + "\n" + string(body)
}

// servingOnTheWire serves canned XML like recordingS3 and records every request whole
// (onTheWire), not the line alone.
func servingOnTheWire(t *testing.T, body string) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu   sync.Mutex
		sent []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent = append(sent, onTheWire(r))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(sent)
	}
}

// carries is whether text has the element in it as it was typed, or as a query or a
// path would spell it.
func carries(text, element string) bool {
	for _, spelling := range []string{element, url.QueryEscape(element), url.PathEscape(element)} {
		if strings.Contains(text, spelling) {
			return true
		}
	}
	return false
}

// What a press sends the service is the same whatever is typed: the element is the text
// the box matches the names against, and it reaches nothing. The answer does not show
// that, for a Suggest that listed the account by the element would answer every bucket
// as well; the requests the service got do, line, headers and body, and they are held to
// those of a press on nothing, which have no trace of any of the elements in them.
func TestAPressSendsTheServiceNothingOfTheElementTyped(t *testing.T) {
	srv, sent := servingOnTheWire(t, bucketsXML)
	for _, id := range []string{"s3.overview", "s3.bucket.list"} {
		before := len(sent())
		press(t, id, endpointOf(t, srv), "", nil)
		baseline := sent()[before:]
		if len(baseline) != 1 || !strings.HasPrefix(baseline[0], "GET / HTTP/1.1\n") {
			t.Fatalf("%s: a press on nothing sent %q, want the one listing of the account", id, baseline)
		}
		for _, element := range typedElements {
			if carries(baseline[0], element) {
				t.Fatalf("%q is already in a press on nothing (%q): the check could not tell it was sent", element, baseline[0])
			}
			before = len(sent())
			if got := press(t, id, endpointOf(t, srv), element, nil); !slices.Equal(got, []string{"backups", "media"}) {
				t.Errorf("%s: a press on %q = %v, want both buckets", id, element, got)
			}
			got := sent()[before:]
			if carries(strings.Join(got, "\n"), element) {
				t.Errorf("%s: a press on %q sent %q, which carries it", id, element, got)
			}
			if !slices.Equal(got, baseline) {
				t.Errorf("%s: a press on %q sent %q, want what a press on nothing sends, %q", id, element, got, baseline)
			}
		}
	}
}

// silentService takes a request and holds it until the test ends or the client lets go.
// It counts the requests that came in, and a value reaches gone as the client drops the
// connection of one.
type silentService struct {
	*httptest.Server
	arrived atomic.Int32
	gone    chan struct{}
}

func aServiceThatNeverAnswers(t *testing.T) *silentService {
	t.Helper()
	s := &silentService{gone: make(chan struct{}, 8)}
	release := make(chan struct{})
	s.Server = httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		s.arrived.Add(1)
		select {
		case <-r.Context().Done():
			s.gone <- struct{}{}
		case <-release:
		}
	}))
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(release) })
	return s
}

// answerBound is how long a press that was given up on may take: a service that would
// not answer in all the test's time, and the milliseconds the press needs.
const answerBound = 5 * time.Second

// answeredWithin is a press made under ctx, failing the test when it has not answered
// by answerBound. The call cannot be given up on from here, and a Suggest that ignores
// its context would otherwise hold the test for as long as it holds the host.
func answeredWithin(t *testing.T, ctx context.Context, id, endpoint string) []string {
	t.Helper()
	f, req := pressRequest(t, id, endpoint, "gi", nil)
	answer := make(chan []string, 1)
	go func() { answer <- f.Suggest(ctx, req) }()
	select {
	case got := <-answer:
		return got
	case <-time.After(answerBound):
		t.Fatalf("a press had not answered %s after its context was over", answerBound)
		return nil
	}
}

// goroutinesBackTo waits for the goroutines of a press to end, and fails the test when
// more are running than were before it.
func goroutinesBackTo(t *testing.T, before int) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); runtime.NumGoroutine() > before; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			stacks := make([]byte, 1<<16)
			t.Errorf("%d goroutines running after a press, %d before it:\n%s",
				runtime.NumGoroutine(), before, stacks[:runtime.Stack(stacks, true)])
			return
		}
	}
}

// A press nobody waits for any more (the person left the box, the few seconds the host
// gives it ran out) comes back at once with nothing, and leaves nothing running: the
// context reaches the request, the connection is dropped and the service sees it go,
// and the goroutines that held it end. The service here would never answer, so an
// answer inside the bound is the context's doing, and not the library's own minute.
func TestAPressNobodyWaitsForAnyMoreReturnsAtOnceAndLeavesNothingRunning(t *testing.T) {
	for name, tc := range map[string]struct {
		context func(s *silentService) (context.Context, context.CancelFunc)
		reached bool
		never   bool
	}{
		"its time runs out while the service is answering": {
			context: func(*silentService) (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 300*time.Millisecond)
			},
		},
		"it is cancelled while the service is answering": {
			context: func(s *silentService) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					for deadline := time.Now().Add(answerBound); s.arrived.Load() == 0 && time.Now().Before(deadline); {
						time.Sleep(5 * time.Millisecond)
					}
					cancel()
				}()
				return ctx, cancel
			},
			reached: true,
		},
		"it is cancelled before the service is asked": {
			context: func(*silentService) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			never: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := aServiceThatNeverAnswers(t)
			before := runtime.NumGoroutine()
			ctx, cancel := tc.context(service)
			defer cancel()
			started := time.Now()
			if got := answeredWithin(t, ctx, "s3.bucket.list", endpointOf(t, service.Server)); got != nil {
				t.Errorf("a press that was given up on answered %v, want nothing", got)
			}
			if took := time.Since(started); took > 3*time.Second {
				t.Errorf("a press that was given up on took %s, want about the moment it was given", took)
			}
			arrived := int(service.arrived.Load())
			switch {
			case tc.never && arrived != 0:
				t.Errorf("a press whose context was over asked the service %d times, want none", arrived)
			case tc.reached && arrived == 0:
				t.Fatal("the service never got the request, which the test needed it to")
			}
			for range arrived {
				select {
				case <-service.gone:
				case <-time.After(3 * time.Second):
					t.Error("the service still holds a request after the press was given up on")
				}
			}
			goroutinesBackTo(t, before)
		})
	}
}

// A live Suggest never answers the keystroke channel, which is the one a TUI form
// re-evaluates as a sibling changes and a shell builds without credentials: the
// account is listed on a press and on nothing else, and under a fence that channel
// offers the allowlist.
func TestTheKeystrokeChannelNeverListsTheAccountForABucketList(t *testing.T) {
	srv, asked := recordingS3(t, bucketsXML)
	fence := plugin.Fence{"bucket": {"media", "archive"}}
	for _, id := range []string{"s3.overview", "s3.bucket.list"} {
		f := bucketInput(t, id)
		req := plugin.CompletionRequest(capabilityOf(t, id), map[string]any{
			"address": endpointOf(t, srv), "access-key": "test", "secret-key": "test", "bucket": "me",
		})
		if got := f.Candidates(context.Background(), req); got != nil {
			t.Errorf("%s: Candidates = %v, want nothing for a live input", id, got)
		}
		if got := fence.Narrow(f, f.Candidates(context.Background(), req)); len(got) != 2 || got[0] != "media" || got[1] != "archive" {
			t.Errorf("%s: under a fence the channel offers %v, want the allowlist", id, got)
		}
	}
	if lines := asked(); len(lines) != 0 {
		t.Errorf("the keystroke channel sent %v", lines)
	}
}
