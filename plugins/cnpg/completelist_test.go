package main

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The namespaces and the clusters of cnpg.list, cnpg.backup.list and cnpg.storage
// are lists, completed on a press of the key a name at a time. The host calls the
// Suggest the way press does: what the call resolves to, with the name being typed
// under the field's own name as a string, and holds the answer to the connection's
// fence. The capabilities that act on one cluster keep their single names and
// complete them as before.

var listings = []string{"cnpg.list", "cnpg.backup.list", "cnpg.storage"}

// clustersIn is the clusters of one namespace as `kubectl get clusters -o json`
// prints them.
func clustersIn(namespace string, names ...string) map[string]any {
	items := make([]any, len(names))
	for i, name := range names {
		items[i] = map[string]any{"metadata": map[string]any{"name": name, "namespace": namespace}}
	}
	return map[string]any{"items": items}
}

// press is a completion press on one input of a listing: what the call resolves to
// over the fence, the element being typed under the input's name, and the answer
// held to the fence.
func press(t *testing.T, id, input, element string, caller map[string]any, fence plugin.Fence) []string {
	t.Helper()
	f, req := pressRequest(t, id, input, element, caller, fence)
	return fence.Filter(f, f.Suggest(context.Background(), req))
}

// pressRequest is the Suggest of one input and the request a press answers it with, for
// a test that makes the call itself.
func pressRequest(t *testing.T, id, input, element string, caller map[string]any, fence plugin.Fence) (plugin.Field, plugin.Request) {
	t.Helper()
	c := capabilityOf(t, id)
	f, ok := inputOf(c, input)
	if !ok {
		t.Fatalf("%s has no %s", id, input)
	}
	resolved := plugin.Resolve(c, plugin.Inputs{Caller: maps.Clone(caller), Fence: fence})
	resolved[f.Name] = element
	return f, plugin.LiveRequest(resolved)
}

func TestTheNamespacesAndClustersOfTheListingsCompleteOnAPress(t *testing.T) {
	for _, id := range listings {
		for _, input := range []string{"namespace", "cluster"} {
			f, _ := inputOf(capabilityOf(t, id), input)
			if f.Type != plugin.StringSlice || f.Required || !f.Live || f.Suggest == nil {
				t.Errorf("%s: %s is %+v, want an optional list completed live", id, input, f)
			}
		}
	}
	if err := Plugin().Validate(); err != nil {
		t.Errorf("the declaration is refused: %v", err)
	}
}

func TestAPressOnTheNamespaceListNamesEveryNamespaceWhateverIsTyped(t *testing.T) {
	stub := perNamespace{dir: t.TempDir()}
	fakeKubectl(t, `echo "$*" >> `+stub.dir+`/calls
printf 'namespace/default\nnamespace/databases\nnamespace/staging\n'
`)
	presses := 0
	for _, id := range listings {
		for _, element := range []string{"", "da", "databases", "no-such-namespace"} {
			presses++
			got := press(t, id, "namespace", element, map[string]any{"context": "homelab"}, nil)
			if want := []string{"databases", "default", "staging"}; !slices.Equal(got, want) {
				t.Errorf("%s: a press on %q = %v, want every namespace %v", id, element, got, want)
			}
		}
	}
	calls := stub.calls()
	if len(calls) != presses {
		t.Fatalf("%d presses made %d calls, want one each: %q", presses, len(calls), calls)
	}
	for _, call := range calls {
		if !strings.HasPrefix(call, "get namespaces -o=name ") || !strings.Contains(call, "--context=homelab") {
			t.Errorf("a press made %q, want the list of namespaces in the context homelab", call)
		}
	}
}

// The clusters are looked for in the namespaces the box beside them names, each
// with a call of its own, and in every namespace while it names none.
func TestAPressOnTheClusterListLooksInTheNamespacesNamed(t *testing.T) {
	answers := map[string]any{
		"all":       clustersIn("databases", "app-db"),
		"databases": clustersIn("databases", "app-db", "billing-db"),
		"staging":   clustersIn("staging", "app-db", "cache"),
		"own":       clustersIn("default", "scratch"),
	}
	for name, tc := range map[string]struct {
		caller map[string]any
		want   []string
		asked  []string
	}{
		"none named: every namespace, in one call": {
			caller: map[string]any{},
			want:   []string{"app-db\t--namespace=databases"},
			asked:  []string{"*"},
		},
		"one named: that namespace, as the bare names": {
			caller: map[string]any{"namespace": []string{"databases"}},
			want:   []string{"app-db", "billing-db"},
			asked:  []string{"databases"},
		},
		"a bare name is one": {
			caller: map[string]any{"namespace": "databases"},
			want:   []string{"app-db", "billing-db"},
			asked:  []string{"databases"},
		},
		"several named: a call each, a name once": {
			caller: map[string]any{"namespace": []string{"staging", "databases", "databases", " "}},
			want:   []string{"app-db\t--namespace=databases", "billing-db\t--namespace=databases", "cache\t--namespace=staging"},
			asked:  []string{"databases", "staging"},
		},
		"the switch beside the namespaces reads past nothing": {
			caller: map[string]any{"namespace": []string{"databases", "staging"}, "all-namespaces": true},
			want:   []string{"app-db\t--namespace=databases", "billing-db\t--namespace=databases", "cache\t--namespace=staging"},
			asked:  []string{"databases", "staging"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			stub := servesPerNamespace(t, answers, nil, "")
			for _, id := range listings {
				if got := press(t, id, "cluster", "app", tc.caller, nil); !slices.Equal(got, tc.want) {
					t.Errorf("%s: a press = %q, want %q", id, got, tc.want)
				}
			}
			var want []string
			for range listings {
				want = append(want, tc.asked...)
			}
			slices.Sort(want)
			if got := stub.namespaces(); !slices.Equal(got, want) {
				t.Errorf("namespaces asked = %q, want %q: one call each, and the cluster is never listed whole", got, want)
			}
		})
	}
}

// A namespace the credential may not read is left out and the others answer, and
// too many namespaces to read are none: a completion is silent where a listing
// would say why.
func TestAPressOnTheClusterListIsSilentWhereAListingWouldSayWhy(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{
		"databases": clustersIn("databases", "app-db"),
	}, map[string]string{"staging": forbiddenClusters("staging")}, "")
	got := press(t, "cnpg.list", "cluster", "", map[string]any{"namespace": []string{"databases", "staging"}}, nil)
	if !slices.Equal(got, []string{"app-db\t--namespace=databases"}) {
		t.Errorf("a press = %q, want the namespace that answers", got)
	}
	if got := press(t, "cnpg.list", "cluster", "", map[string]any{"namespace": []string{"staging"}}, nil); got != nil {
		t.Errorf("a refused namespace answered %q, want silence", got)
	}

	before := len(stub.calls())
	if got := press(t, "cnpg.list", "cluster", "", map[string]any{"namespace": namespacesNamed(maxNamespaces + 1)}, nil); got != nil {
		t.Errorf("%d namespaces answered %q, want none", maxNamespaces+1, got)
	}
	if got := press(t, "cnpg.list", "cluster", "", map[string]any{"namespace": []string{"--all-namespaces"}}, nil); got != nil {
		t.Errorf("a namespace kubectl takes for a flag answered %q", got)
	}
	if got := press(t, "cnpg.list", "cluster", "", map[string]any{"context": "--kubeconfig=/tmp/theirs"}, nil); got != nil {
		t.Errorf("a context kubectl takes for a flag answered %q", got)
	}
	if after := len(stub.calls()); after != before {
		t.Errorf("a refused press reached the cluster: %q", stub.calls()[before:])
	}
}

// Under a fence on the namespace the form has already filled the box beside the
// clusters with what the fence holds, so a connection bound by a Role in each of
// them is asked in each and never across them; and the fence on the clusters holds
// the answer.
func TestAPressUnderAFenceLooksOnlyInTheNamespacesTheFenceHoldsAndOffersItsClusters(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{
		"all":       clustersIn("everywhere", "leak"),
		"databases": clustersIn("databases", "app-db", "billing-db"),
		"staging":   clustersIn("staging", "app-db", "cache"),
	}, nil, "")
	fence := plugin.Fence{"namespace": {"databases", "staging"}, "cluster": {"app-db", "cache"}}
	got := press(t, "cnpg.list", "cluster", "", map[string]any{}, fence)
	if want := []string{"app-db\t--namespace=databases", "cache\t--namespace=staging"}; !slices.Equal(got, want) {
		t.Errorf("a fenced press = %q, want %q", got, want)
	}
	if got, want := stub.namespaces(), []string{"databases", "staging"}; !slices.Equal(got, want) {
		t.Errorf("namespaces asked = %q, want the two the fence holds and no sweep", got)
	}
}

// typedElements is what a person can type into the box of a list: the start of a name
// and a whole one, and then what no name has in it, a comma, a quote, a space, a leading
// dash, a path that walks up, a separator, a line break, a shell's metacharacters, and
// more characters than a name has.
var typedElements = []string{
	"app", "app-db",
	"a,b", `"quoted"`, "two words", "-n", "--namespace=other", "../../etc/hosts",
	"name/with/slashes", "line\nbreak", "'; touch /tmp/x #", strings.Repeat("x", 300),
}

// record is everything the stub was run with so far, as it wrote it.
func (p perNamespace) record() string {
	raw, _ := os.ReadFile(filepath.Join(p.dir, "calls"))
	return string(raw)
}

// ranAfter is the runs of the stub after the first n bytes of its record, a line for
// each, sorted: the reads of a list run at once, so their order is not the test's to
// hold.
func ranAfter(record string, n int) string {
	lines := strings.Split(strings.TrimSpace(record[n:]), "\n")
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// What a press sends the cluster is the same whatever is typed: the element is the text
// the box matches the names against, and it reaches nothing. The answer does not show
// that, for a Suggest that asked kubectl for the clusters by the element would answer
// them all as well; what kubectl was run with does, and it is held to the runs of a
// press on nothing, which have no trace of any of the elements in them.
func holdsToThePressOnNothing(t *testing.T, stub perNamespace, id, input string, caller map[string]any, runs int) {
	t.Helper()
	start := len(stub.record())
	want := press(t, id, input, "", caller, nil)
	baseline := ranAfter(stub.record(), start)
	if got := strings.Count(baseline, "\n") + 1; got != runs || !strings.HasPrefix(baseline, "get ") {
		t.Fatalf("%s: a press on nothing in %s ran kubectl as %q, want %d runs of `get`", id, input, baseline, runs)
	}
	for _, element := range typedElements {
		// "-n" is in --namespace: a text the element is part of already cannot show it was
		// sent, and the comparison with the press on nothing below is what does.
		unseen := !strings.Contains(baseline, element)
		start = len(stub.record())
		if got := press(t, id, input, element, caller, nil); !slices.Equal(got, want) {
			t.Errorf("%s: a press on %q in %s = %q, want what a press on nothing answers, %q", id, element, input, got, want)
		}
		ran := ranAfter(stub.record(), start)
		if unseen && strings.Contains(ran, element) {
			t.Errorf("%s: a press on %q in %s ran kubectl as %q, which carries it", id, element, input, ran)
		}
		if ran != baseline {
			t.Errorf("%s: a press on %q in %s ran kubectl as %q, want what a press on nothing runs, %q", id, element, input, ran, baseline)
		}
	}
}

func TestAPressOnTheNamespaceListSendsTheClusterNothingOfTheElementTyped(t *testing.T) {
	stub := perNamespace{dir: t.TempDir()}
	fakeKubectl(t, `echo "$*" >> `+stub.dir+`/calls
printf 'namespace/default\nnamespace/databases\nnamespace/staging\n'
`)
	for _, id := range listings {
		holdsToThePressOnNothing(t, stub, id, "namespace", map[string]any{"context": "homelab"}, 1)
	}
}

func TestAPressOnTheClusterListSendsTheClusterNothingOfTheElementTyped(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{
		"all":       clustersIn("databases", "app-db"),
		"databases": clustersIn("databases", "app-db", "billing-db"),
		"staging":   clustersIn("staging", "app-db", "cache"),
		"own":       clustersIn("default", "scratch"),
	}, nil, "")
	for name, tc := range map[string]struct {
		caller map[string]any
		runs   int
	}{
		"in every namespace": {caller: map[string]any{"context": "homelab"}, runs: 1},
		"in one":             {caller: map[string]any{"namespace": []string{"databases"}}, runs: 1},
		"in two":             {caller: map[string]any{"namespace": []string{"databases", "staging"}}, runs: 2},
	} {
		for _, id := range listings {
			t.Run(name+"/"+id, func(t *testing.T) {
				holdsToThePressOnNothing(t, stub, id, "cluster", tc.caller, tc.runs)
			})
		}
	}
}

// The context and the namespaces beside the clusters are what a press reads of the
// request, and each reaches kubectl as the value of one flag, in an argument of its
// own, or not at all: a name kubectl would read as a flag, or as two arguments, is
// never sent. Which of them a person may name is not decided here. The plugin reads the
// shape of a name and nothing of a fence on the context, and a press holds the fence
// only to the answer, so a context the kubeconfig holds is asked whatever the profile
// allows.
func TestAPressReachesKubectlWithTheContextAndNamespacesBesideItAsOneArgumentEachOrNotAtAll(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{"databases": clustersIn("databases", "app-db")}, nil, "")
	for _, name := range []string{
		"staging", "arn:aws:eks:eu-west-1:123456789012:cluster/prod", "a/b", "ops@cluster.example", strings.Repeat("x", 253),
	} {
		start := len(stub.record())
		press(t, "cnpg.list", "cluster", "app", map[string]any{"context": name, "namespace": []string{"databases"}}, nil)
		want := "get clusters.postgresql.cnpg.io -o json --request-timeout=15s --context=" + name + " --namespace=databases"
		if ran := ranAfter(stub.record(), start); ran != want {
			t.Errorf("a press in the context %q ran kubectl as %q, want %q", name, ran, want)
		}
	}
	for _, name := range []string{
		"a,b", `"quoted"`, "two words", "-n", "--kubeconfig=/tmp/theirs", "../../etc/hosts",
		"line\nbreak", "'; touch /tmp/x #", strings.Repeat("x", 300),
	} {
		for what, caller := range map[string]map[string]any{
			"context":              {"context": name},
			"first of namespaces":  {"namespace": []string{name, "databases"}},
			"second of namespaces": {"namespace": []string{"databases", name}},
		} {
			start := len(stub.record())
			if got := press(t, "cnpg.list", "cluster", "app", caller, nil); got != nil {
				t.Errorf("a press with %s %q answered %q, want silence", what, name, got)
			}
			if ran := stub.record()[start:]; ran != "" {
				t.Errorf("a press with %s %q ran kubectl as %q, want it never run", what, name, ran)
			}
		}
	}
}

// hangingKubectl is a kubectl that takes the call and never answers, and notes the
// process id of every run, so a test can see that none is left. It stops when the test
// does (the directory it was made in goes), and after a minute of its own.
//
// It waits in a loop of short sleeps, not in one long one, which makes it the harder
// stub: killing the script leaves a sleep holding the pipe Output reads until that
// sleep ends, as a kubectl whose credential plugin keeps its stdout open would.
//
// Run once before it is handed over: the first run of a new file is the slow one on
// this machine, and a press that is given up on after a moment would otherwise be
// given up on before kubectl had started.
func hangingKubectl(t *testing.T) (pids func() []int) {
	t.Helper()
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	pidFile := filepath.Join(dir, "pids")
	fakeKubectl(t, `echo $$ >> '`+pidFile+`'
n=0
while [ -d '`+dir+`' ] && [ ! -f '`+release+`' ] && [ $n -lt 600 ]; do sleep 0.1; n=$((n+1)); done
`)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(kubectlBin).Run(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{release, pidFile} {
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	return func() []int {
		raw, _ := os.ReadFile(pidFile)
		var out []int
		for _, field := range strings.Fields(string(raw)) {
			if pid, err := strconv.Atoi(field); err == nil {
				out = append(out, pid)
			}
		}
		return out
	}
}

// answerBound is how long a press that was given up on may take: a tenth of the
// minute its kubectl would take, and well over the milliseconds it needs.
const answerBound = 5 * time.Second

// answeredWithin is a press made under ctx, failing the test when it has not answered
// by answerBound. The call cannot be given up on from here, and a Suggest that ignores
// its context would otherwise hold the test for as long as it holds the host.
func answeredWithin(t *testing.T, ctx context.Context, input string, caller map[string]any) []string {
	t.Helper()
	f, req := pressRequest(t, "cnpg.list", input, "app", caller, nil)
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

// processesGone fails the test for each process still there after a moment: a press
// that was given up on may not leave a kubectl running against the cluster.
func processesGone(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		gone := false
		for deadline := time.Now().Add(3 * time.Second); !gone && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			gone = errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
		}
		if !gone {
			t.Errorf("process %d is still running after a press that was given up on", pid)
		}
	}
}

// A press nobody waits for any more (the person left the box, the few seconds the host
// gives it ran out) comes back at once with nothing, and leaves nothing running: the
// context reaches every kubectl it started, which is killed, and the goroutines that
// read them end. The kubectl here would answer in a minute, so an answer inside the
// bound is the context's doing and not the plugin's own timeout. The clusters of
// several namespaces are the case with a call in each at once.
func TestAPressNobodyWaitsForAnyMoreReturnsAtOnceAndLeavesNothingRunning(t *testing.T) {
	pids := hangingKubectl(t)
	for what, target := range map[string]struct {
		input  string
		caller map[string]any
	}{
		"the namespaces":                 {"namespace", map[string]any{"context": "homelab"}},
		"the clusters of one namespace":  {"cluster", map[string]any{"namespace": []string{"databases"}}},
		"the clusters of three of them":  {"cluster", map[string]any{"namespace": []string{"databases", "staging", "apps"}}},
		"the clusters of every one":      {"cluster", map[string]any{}},
		"the clusters of one named bare": {"cluster", map[string]any{"namespace": "databases"}},
	} {
		for name, tc := range map[string]struct {
			context func(ran int) (context.Context, context.CancelFunc)
			never   bool
		}{
			"its time runs out while kubectl is answering": {
				context: func(int) (context.Context, context.CancelFunc) {
					return context.WithTimeout(context.Background(), 300*time.Millisecond)
				},
			},
			"it is cancelled while kubectl is answering": {
				context: func(ran int) (context.Context, context.CancelFunc) {
					ctx, cancel := context.WithCancel(context.Background())
					go func() {
						for deadline := time.Now().Add(answerBound); len(pids()) <= ran && time.Now().Before(deadline); {
							time.Sleep(5 * time.Millisecond)
						}
						cancel()
					}()
					return ctx, cancel
				},
			},
			"it is cancelled before kubectl is run": {
				context: func(int) (context.Context, context.CancelFunc) {
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					return ctx, cancel
				},
				never: true,
			},
		} {
			t.Run(what+"/"+name, func(t *testing.T) {
				ran := len(pids())
				before := runtime.NumGoroutine()
				ctx, cancel := tc.context(ran)
				defer cancel()
				started := time.Now()
				if got := answeredWithin(t, ctx, target.input, target.caller); got != nil {
					t.Errorf("a press that was given up on answered %v, want nothing", got)
				}
				if took := time.Since(started); took > 3*time.Second {
					t.Errorf("a press that was given up on took %s, want about the moment it was given", took)
				}
				now := pids()
				if tc.never && len(now) != ran {
					t.Errorf("a press whose context was over ran kubectl %d times, want none", len(now)-ran)
				}
				processesGone(t, now[ran:])
				goroutinesBackTo(t, before)
			})
		}
	}
}

// A live Suggest never answers the keystroke channel, which a TUI form re-evaluates
// as a sibling changes and a shell builds without credentials: the cluster is asked
// on a press and on nothing else, and under a fence that channel offers the
// allowlist.
func TestTheKeystrokeChannelNeverAsksTheClusterForTheListings(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{"databases": clustersIn("databases", "app-db")}, nil, "")
	fence := plugin.Fence{"namespace": {"databases"}, "cluster": {"app-db", "cache"}}
	for _, id := range listings {
		c := capabilityOf(t, id)
		req := plugin.CompletionRequest(c, map[string]any{"namespace": []string{"databases"}, "cluster": []string{"app", "ca"}})
		for input, allowed := range map[string][]string{"namespace": {"databases"}, "cluster": {"app-db", "cache"}} {
			f, _ := inputOf(c, input)
			if got := f.Candidates(context.Background(), req); got != nil {
				t.Errorf("%s: %s Candidates = %v, want nothing for a live input", id, input, got)
			}
			if got := fence.Narrow(f, f.Candidates(context.Background(), req)); !slices.Equal(got, allowed) {
				t.Errorf("%s: under a fence the channel offers %v for %s, want %v", id, got, input, allowed)
			}
		}
	}
	if calls := stub.calls(); len(calls) != 0 {
		t.Errorf("the keystroke channel reached the cluster: %q", calls)
	}
}
