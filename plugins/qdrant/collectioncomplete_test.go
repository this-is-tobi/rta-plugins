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

// The collection of qdrant.overview and qdrant.collection.list is a list, completed
// on a press of the key a name at a time. The host calls the Suggest the way press
// does: what the call resolves to, credentials included, with the name being typed
// under the field's own name as a string, and holds the answer to the connection's
// fence. The collections that act on one keep their required name and complete it.

// anInstance serves the collection list of an instance that holds three
// collections, one of them named with a comma, and records each request with the
// api-key it carried, and whole (onTheWire).
type anInstance struct {
	*httptest.Server
	mu    sync.Mutex
	asked []string
	sent  []string
}

func newInstance(t *testing.T) *anInstance {
	t.Helper()
	i := &anInstance{}
	i.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i.mu.Lock()
		i.asked = append(i.asked, r.Method+" "+r.URL.EscapedPath()+" key="+r.Header.Get("api-key"))
		i.sent = append(i.sent, onTheWire(r))
		i.mu.Unlock()
		if r.URL.Path != "/collections" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"collections":[{"name":"docs"},{"name":"audit"},{"name":"a,b"}]},"status":"ok","time":0.001}`))
	}))
	t.Cleanup(i.Close)
	return i
}

func (i *anInstance) requests() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return slices.Clone(i.asked)
}

// wire is every request the instance got, whole.
func (i *anInstance) wire() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return slices.Clone(i.sent)
}

func (i *anInstance) address() string { return strings.TrimPrefix(i.URL, "http://") }

func collectionInput(t *testing.T, id string) (plugin.Capability, plugin.Field) {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID != id {
			continue
		}
		for _, f := range c.Inputs {
			if f.Name == "collection" {
				return c, f
			}
		}
	}
	t.Fatalf("no capability %s with a collection", id)
	return plugin.Capability{}, plugin.Field{}
}

func press(t *testing.T, id, address, element string, fence plugin.Fence) []string {
	t.Helper()
	f, req := pressRequest(t, id, address, element, fence)
	return fence.Filter(f, f.Suggest(context.Background(), req))
}

// pressRequest is the Suggest of the collection box and the request a press answers it
// with, for a test that makes the call itself.
func pressRequest(t *testing.T, id, address, element string, fence plugin.Fence) (plugin.Field, plugin.Request) {
	t.Helper()
	c, f := collectionInput(t, id)
	resolved := plugin.Resolve(c, plugin.Inputs{Caller: map[string]any{"address": address, "api-key": "s3cret-key"}, Fence: fence})
	resolved[f.Name] = element
	return f, plugin.LiveRequest(resolved)
}

// onTheWire is a request as the instance got it: the line, the headers and the body.
func onTheWire(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\nHost: %s\n", r.Method, r.URL.RequestURI(), r.Proto, r.Host)
	for _, name := range slices.Sorted(maps.Keys(r.Header)) {
		fmt.Fprintf(&b, "%s: %s\n", name, strings.Join(r.Header[name], ","))
	}
	return b.String() + "\n" + string(body)
}

func TestTheCollectionOfTheTwoListingsCompletesOnAPress(t *testing.T) {
	for _, id := range []string{"qdrant.overview", "qdrant.collection.list"} {
		_, f := collectionInput(t, id)
		if f.Type != plugin.StringSlice || f.Short != "c" || f.Config != "collection" || f.Required || !f.Live || f.Suggest == nil {
			t.Errorf("%s: collection is %+v, want an optional list completed live", id, f)
		}
	}
	for _, id := range []string{"qdrant.collection.show", "qdrant.points.count", "qdrant.points.scroll", "qdrant.dump", "qdrant.restore"} {
		if _, f := collectionInput(t, id); f.Type != plugin.String || !f.Required || !f.Live || f.Suggest == nil {
			t.Errorf("%s: collection is %+v, want the required name it was, completed live", id, f)
		}
	}
	if err := Plugin().Validate(); err != nil {
		t.Errorf("the declaration is refused: %v", err)
	}
}

// What a press asks the instance is its list of collections, once, whatever is
// typed: the box matches the names against the text, and the host quotes the one
// with a comma when it is accepted. The api-key goes with the request, the
// credential the run would get, and is no part of the answer.
func TestAPressOnTheCollectionListNamesEveryCollectionWhateverIsTyped(t *testing.T) {
	inst := newInstance(t)
	presses := 0
	for _, id := range []string{"qdrant.overview", "qdrant.collection.list"} {
		for _, element := range []string{"", "au", "audit", "no-such-collection"} {
			presses++
			got := press(t, id, inst.address(), element, nil)
			if want := []string{"a,b", "audit", "docs"}; !slices.Equal(got, want) {
				t.Errorf("%s: a press on %q = %v, want every collection %v", id, element, got, want)
			}
		}
	}
	asked := inst.requests()
	if len(asked) != presses {
		t.Fatalf("%d presses made %d requests, want one each: %v", presses, len(asked), asked)
	}
	for _, line := range asked {
		if line != "GET /collections key=s3cret-key" {
			t.Errorf("a press sent %q, want the collection list with the key the run would send", line)
		}
	}
}

func TestAPressUnderAFenceOffersOnlyTheCollectionsTheFenceAllows(t *testing.T) {
	inst := newInstance(t)
	fence := plugin.Fence{"collection": {"docs", "a,b"}}
	for _, id := range []string{"qdrant.overview", "qdrant.collection.list"} {
		if got := press(t, id, inst.address(), "", fence); !slices.Equal(got, []string{"a,b", "docs"}) {
			t.Errorf("%s: a fenced press = %v, want only the two the fence names", id, got)
		}
	}
}

// typedElements is what a person can type into the box of a list: the start of a name
// and a whole one, and then what no name has in it, a quote, a space, a leading dash, a
// path that walks up, a separator, a line break, a shell's metacharacters, and more
// characters than a name has. And a comma, which a name may have, and the host writes in
// double quotes when it accepts one.
var typedElements = []string{
	"au", "audit",
	"a,b", `"quoted"`, "two words", "-n", "--collection=other", "../../etc/hosts",
	"collection/with/slashes", "line\nbreak", "'; touch /tmp/x #", strings.Repeat("x", 300),
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

// What a press sends the instance is the same whatever is typed: the element is the text
// the box matches the names against, and it reaches nothing. The answer does not show
// that, for a Suggest that listed the collections by the element would answer all three
// as well; the requests the instance got do, line, headers and body, and they are held to
// those of a press on nothing, which have no trace of any of the elements in them.
func TestAPressSendsTheInstanceNothingOfTheElementTyped(t *testing.T) {
	inst := newInstance(t)
	for _, id := range []string{"qdrant.overview", "qdrant.collection.list"} {
		before := len(inst.wire())
		press(t, id, inst.address(), "", nil)
		baseline := inst.wire()[before:]
		if len(baseline) != 1 || !strings.HasPrefix(baseline[0], "GET /collections HTTP/1.1\n") {
			t.Fatalf("%s: a press on nothing sent %q, want the one list of collections", id, baseline)
		}
		for _, element := range typedElements {
			if carries(baseline[0], element) {
				t.Fatalf("%q is already in a press on nothing (%q): the check could not tell it was sent", element, baseline[0])
			}
			before = len(inst.wire())
			if got := press(t, id, inst.address(), element, nil); !slices.Equal(got, []string{"a,b", "audit", "docs"}) {
				t.Errorf("%s: a press on %q = %v, want every collection", id, element, got)
			}
			got := inst.wire()[before:]
			if carries(strings.Join(got, "\n"), element) {
				t.Errorf("%s: a press on %q sent %q, which carries it", id, element, got)
			}
			if !slices.Equal(got, baseline) {
				t.Errorf("%s: a press on %q sent %q, want what a press on nothing sends, %q", id, element, got, baseline)
			}
		}
	}
}

// silentInstance takes a request and holds it until the test ends or the client lets go.
// It counts the requests that came in, and a value reaches gone as the client drops the
// connection of one.
type silentInstance struct {
	*httptest.Server
	arrived atomic.Int32
	gone    chan struct{}
}

func anInstanceThatNeverAnswers(t *testing.T) *silentInstance {
	t.Helper()
	s := &silentInstance{gone: make(chan struct{}, 8)}
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

func (s *silentInstance) address() string { return strings.TrimPrefix(s.URL, "http://") }

// answerBound is how long a press that was given up on may take: an instance that would
// not answer in all the test's time, and the milliseconds the press needs.
const answerBound = 5 * time.Second

// answeredWithin is a press made under ctx, failing the test when it has not answered
// by answerBound. The call cannot be given up on from here, and a Suggest that ignores
// its context would otherwise hold the test for as long as it holds the host.
func answeredWithin(t *testing.T, ctx context.Context, address string) []string {
	t.Helper()
	f, req := pressRequest(t, "qdrant.collection.list", address, "au", nil)
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
// context reaches the request, the connection is dropped and the instance sees it go,
// and the goroutines that held it end. The instance here would never answer, so an
// answer inside the bound is the context's doing, and not the plugin's own thirty seconds.
func TestAPressNobodyWaitsForAnyMoreReturnsAtOnceAndLeavesNothingRunning(t *testing.T) {
	for name, tc := range map[string]struct {
		context func(s *silentInstance) (context.Context, context.CancelFunc)
		reached bool
		never   bool
	}{
		"its time runs out while the instance is answering": {
			context: func(*silentInstance) (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 300*time.Millisecond)
			},
		},
		"it is cancelled while the instance is answering": {
			context: func(s *silentInstance) (context.Context, context.CancelFunc) {
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
		"it is cancelled before the instance is asked": {
			context: func(*silentInstance) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			never: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			instance := anInstanceThatNeverAnswers(t)
			before := runtime.NumGoroutine()
			ctx, cancel := tc.context(instance)
			defer cancel()
			started := time.Now()
			if got := answeredWithin(t, ctx, instance.address()); got != nil {
				t.Errorf("a press that was given up on answered %v, want nothing", got)
			}
			if took := time.Since(started); took > 3*time.Second {
				t.Errorf("a press that was given up on took %s, want about the moment it was given", took)
			}
			arrived := int(instance.arrived.Load())
			switch {
			case tc.never && arrived != 0:
				t.Errorf("a press whose context was over asked the instance %d times, want none", arrived)
			case tc.reached && arrived == 0:
				t.Fatal("the instance never got the request, which the test needed it to")
			}
			for range arrived {
				select {
				case <-instance.gone:
				case <-time.After(3 * time.Second):
					t.Error("the instance still holds a request after the press was given up on")
				}
			}
			goroutinesBackTo(t, before)
		})
	}
}

// A caller that gives the press no end of its own does not hold it for ever: the
// request is held to the bound of every request this plugin makes (requestTimeout,
// thirty seconds, shortened here the way the overview shortens it).
func TestAPressIsHeldToTheRequestBoundWhenTheCallerGivesItNone(t *testing.T) {
	instance := anInstanceThatNeverAnswers(t)
	before := runtime.NumGoroutine()
	started := time.Now()
	if got := answeredWithin(t, boundedTo(context.Background(), 200*time.Millisecond), instance.address()); got != nil {
		t.Errorf("a press on an instance that never answers = %v, want nothing", got)
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("a press took %s, want about the 200ms the request was given", took)
	}
	goroutinesBackTo(t, before)
}

// A press that cannot be answered says nothing: an instance that is down, one that
// refuses the key, and an address a flag could be mistaken for.
func TestAPressThatCannotBeAnsweredIsSilent(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(refusing.Close)
	for name, address := range map[string]string{
		"refused":         strings.TrimPrefix(refusing.URL, "http://"),
		"nothing there":   "127.0.0.1:1",
		"a flag-like one": "--help",
	} {
		if got := press(t, "qdrant.overview", address, "au", nil); got != nil {
			t.Errorf("%s: a press answered %v, want silence", name, got)
		}
	}
}

// A live Suggest never answers the keystroke channel, which a TUI form re-evaluates
// as a sibling changes and a shell builds without credentials: the instance is asked
// on a press and on nothing else, and under a fence that channel offers the
// allowlist.
func TestTheKeystrokeChannelNeverAsksTheInstanceForACollectionList(t *testing.T) {
	inst := newInstance(t)
	fence := plugin.Fence{"collection": {"docs", "audit"}}
	for _, id := range []string{"qdrant.overview", "qdrant.collection.list"} {
		c, f := collectionInput(t, id)
		req := plugin.CompletionRequest(c, map[string]any{"address": inst.address(), "api-key": "s3cret-key", "collection": "au"})
		if got := f.Candidates(context.Background(), req); got != nil {
			t.Errorf("%s: Candidates = %v, want nothing for a live input", id, got)
		}
		if got := fence.Narrow(f, f.Candidates(context.Background(), req)); !slices.Equal(got, []string{"docs", "audit"}) {
			t.Errorf("%s: under a fence the channel offers %v, want the allowlist", id, got)
		}
	}
	if asked := inst.requests(); len(asked) != 0 {
		t.Errorf("the keystroke channel sent %v", asked)
	}
}
