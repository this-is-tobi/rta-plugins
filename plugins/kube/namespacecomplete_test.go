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

// A list of namespaces completes the element being typed on a press of the key.
//
// The host calls a Live Suggest only then, in the plugin's own process and with the
// credentials the run would get (plugin.LiveRequest), and hands it the element, the
// text after the last comma of the box, as a string under the field's own name. It
// leaves out of the answer the namespaces the box already lists and holds the rest
// to the fence of the connection. These tests call the Suggest the way the host does
// and apply the one narrowing a test of the plugin can, the fence's.

// listedNamespaceInputs is the namespace input of every capability that takes a
// list of them, by capability.
func listedNamespaceInputs(t *testing.T) map[string]plugin.Field {
	t.Helper()
	out := map[string]plugin.Field{}
	for _, c := range Plugin().Capabilities {
		if f := inputOf(c, "namespace"); f.Type == plugin.StringSlice {
			out[c.ID] = f
		}
	}
	return out
}

// aClusterOfNamespaces is a cluster that holds the nine namespaces of aHomelab and
// nothing in them, which is all a completion reads and none of the certificates
// aHomelab takes a moment to make.
func aClusterOfNamespaces(t *testing.T) *scriptedCluster {
	t.Helper()
	c := newScriptedCluster(t)
	c.currentContext("homelab", "")
	items := make([]string, len(everyNamespace))
	for i, ns := range everyNamespace {
		items[i] = jsonOf(t, obj{"metadata": obj{"name": ns}, "status": obj{"phase": "Active"}})
	}
	c.list(t, "namespaces", items...)
	return c
}

// press is a completion press on a box: the request a Live Suggest is answered
// with, carrying the element being typed under the field's name as a string, over
// what the form resolved the rest of the call to.
func press(f plugin.Field, element string, rest map[string]any) []string {
	return pressWith(context.Background(), f, element, rest)
}

func pressWith(ctx context.Context, f plugin.Field, element string, rest map[string]any) []string {
	values := maps.Clone(rest)
	if values == nil {
		values = map[string]any{}
	}
	values[f.Name] = element
	return f.Suggest(ctx, plugin.LiveRequest(values))
}

func TestEveryListOfNamespacesCompletesOnAPress(t *testing.T) {
	inputs := listedNamespaceInputs(t)
	var got []string
	for id, f := range inputs {
		got = append(got, id)
		if !f.Live || f.Suggest == nil {
			t.Errorf("%s: namespace is a list with Live %v and Suggest %v, want a live completion", id, f.Live, f.Suggest != nil)
		}
	}
	slices.Sort(got)
	want := []string{
		"kube.cert.list", "kube.deployment.list", "kube.event.list", "kube.metrics.pod", "kube.namespace.list",
		"kube.overview", "kube.pod.list", "kube.pvc.list", "kube.pvc.usage", "kube.quota.list",
		"kube.serviceaccount.list",
	}
	if !slices.Equal(got, want) {
		t.Errorf("the capabilities that take a list of namespaces are %v, want %v", got, want)
	}
	if err := Plugin().Validate(); err != nil {
		t.Errorf("the declaration is refused: %v", err)
	}
}

// What a press asks the cluster is every namespace the context sees, whatever the
// element: the box matches the names against what is typed, so a Suggest that cut
// them by the element would only hide a namespace the box would have offered.
//
// One cluster for every press: a stub is a script written to disk, and the first
// run of a new file is what a test of this size waits on.
func TestAPressOnANamespaceListAsksTheClusterOnceForEveryNamespace(t *testing.T) {
	c := aClusterOfNamespaces(t)
	presses := 0
	for id, f := range listedNamespaceInputs(t) {
		for _, element := range []string{"", "gi", "gitea", "no-such-namespace"} {
			presses++
			if got := press(f, element, nil); !slices.Equal(got, everyNamespace) {
				t.Errorf("%s: a press on %q = %v, want every namespace %v", id, element, got, everyNamespace)
			}
		}
	}
	calls := c.calls(t)
	if len(calls) != presses {
		t.Fatalf("%d presses made %d calls, want one each: %q", presses, len(calls), calls)
	}
	for _, call := range calls {
		if !strings.HasPrefix(call, "get namespaces ") {
			t.Errorf("a press made %q, want a `get namespaces`", call)
		}
	}
}

// The context the form resolved is the cluster asked, and the rest of the call is
// not read: the switch beside the box does not stop a completion, as it never
// stopped one of a single name.
func TestAPressReadsTheContextAndNothingElseOfTheCall(t *testing.T) {
	f := listedNamespaceInputs(t)["kube.pod.list"]
	c := aClusterOfNamespaces(t)
	got := press(f, "gi", map[string]any{
		"context": "staging", "all-namespaces": true, "unhealthy": true,
	})
	if !slices.Equal(got, everyNamespace) {
		t.Errorf("a press = %v, want every namespace despite all-namespaces", got)
	}
	calls := c.calls(t)
	if len(calls) != 1 || !strings.Contains(calls[0], "--context=staging") ||
		strings.Contains(calls[0], "--all-namespaces") || strings.Contains(calls[0], "--namespace") {
		t.Errorf("a press made %q, want the list of namespaces in the context staging and no scope", calls)
	}
}

// typedElements is what a person can type into the box of a list: the start of a name
// and a whole one, and then what no name has in it, a comma, a quote, a space, a leading
// dash, a path that walks up, a separator, a line break, a shell's metacharacters, and
// more characters than a name has.
var typedElements = []string{
	"gi", "gitea",
	"a,b", `"quoted"`, "two words", "-n", "--kubeconfig=/tmp/theirs", "../../etc/hosts",
	"ns/with/slashes", "line\nbreak", "'; touch /tmp/x #", strings.Repeat("x", 300),
}

// sent is everything kubectl was run with so far, one line for each run.
func (c *scriptedCluster) sent(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.dir, "calls"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(raw)
}

// What a press sends the cluster is the same whatever is typed: the element is the
// text the box matches the names against, and it reaches nothing. The answer does not
// show that, for a Suggest that asked kubectl for the namespaces by the element would
// answer them all as well; what kubectl was run with does, and it is held to the run
// of a press on nothing, which has no trace of any of the elements in it.
func TestAPressSendsTheClusterNothingOfTheElementTyped(t *testing.T) {
	c := aClusterOfNamespaces(t)
	for id, f := range listedNamespaceInputs(t) {
		start := len(c.sent(t))
		press(f, "", nil)
		baseline := c.sent(t)[start:]
		if !strings.HasPrefix(baseline, "get namespaces ") || strings.Count(baseline, "\n") != 1 {
			t.Fatalf("%s: a press on nothing ran kubectl with %q, want one `get namespaces`", id, baseline)
		}
		for _, element := range typedElements {
			if strings.Contains(baseline, element) {
				t.Fatalf("%q is already in a press on nothing (%q): the check could not tell it was sent", element, baseline)
			}
			start = len(c.sent(t))
			if got := press(f, element, nil); !slices.Equal(got, everyNamespace) {
				t.Errorf("%s: a press on %q = %v, want every namespace", id, element, got)
			}
			ran := c.sent(t)[start:]
			if strings.Contains(ran, element) {
				t.Errorf("%s: a press on %q ran kubectl with %q, which carries it", id, element, ran)
			}
			if ran != baseline {
				t.Errorf("%s: a press on %q ran kubectl with %q, want what a press on nothing runs, %q", id, element, ran, baseline)
			}
		}
	}
}

// The context beside the box is the one thing of the request a press reads, and it
// reaches kubectl as the value of one flag, in an argument of its own, or not at all:
// a name kubectl would read as a flag, or as two arguments, is never sent. Which
// contexts a person may name is not decided here. The plugin reads the shape of a name
// and nothing of a fence on the context, and a press holds the fence only to the
// answer, so a context the kubeconfig holds is asked whatever the profile allows.
func TestAPressReachesKubectlWithTheContextBesideItAsOneArgumentOrNotAtAll(t *testing.T) {
	f := listedNamespaceInputs(t)["kube.pod.list"]
	c := aClusterOfNamespaces(t)
	for _, name := range []string{
		"staging", "arn:aws:eks:eu-west-1:123456789012:cluster/prod", "a/b", "ops@cluster.example", strings.Repeat("x", 253),
	} {
		start := len(c.sent(t))
		press(f, "gi", map[string]any{"context": name})
		want := "get namespaces -o json --request-timeout=15s --context=" + name + "\n"
		if ran := c.sent(t)[start:]; ran != want {
			t.Errorf("a press in the context %q ran kubectl with %q, want %q", name, ran, want)
		}
	}
	for _, name := range []string{
		"a,b", `"quoted"`, "two words", "-n", "--kubeconfig=/tmp/theirs", "../../etc/hosts",
		"line\nbreak", "'; touch /tmp/x #", strings.Repeat("x", 300),
	} {
		start := len(c.sent(t))
		if got := press(f, "gi", map[string]any{"context": name}); got != nil {
			t.Errorf("a press in the context %q answered %v, want silence", name, got)
		}
		if ran := c.sent(t)[start:]; ran != "" {
			t.Errorf("a press in the context %q ran kubectl with %q, want it never run", name, ran)
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
	script := "#!/bin/sh\necho $$ >> " + shellQuote(pidFile) + "\n" +
		"n=0\nwhile [ -d " + shellQuote(dir) + " ] && [ ! -f " + shellQuote(release) + " ] && [ $n -lt 600 ]; do sleep 0.1; n=$((n+1)); done\n"
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(path).Run(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{release, pidFile} {
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	orig := kubectlBin
	kubectlBin = path
	t.Cleanup(func() {
		kubectlBin = orig
		_ = os.WriteFile(release, nil, 0o600)
	})
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
func answeredWithin(t *testing.T, ctx context.Context, f plugin.Field) []string {
	t.Helper()
	answer := make(chan []string, 1)
	go func() { answer <- pressWith(ctx, f, "gi", nil) }()
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
// context reaches kubectl, which is killed, and the goroutines that read it end. The
// kubectl here would answer in a minute, so an answer inside the bound is the
// context's doing and not the plugin's own twenty seconds.
func TestAPressNobodyWaitsForAnyMoreReturnsAtOnceAndLeavesNothingRunning(t *testing.T) {
	f := listedNamespaceInputs(t)["kube.overview"]
	pids := hangingKubectl(t)
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
		t.Run(name, func(t *testing.T) {
			ran := len(pids())
			before := runtime.NumGoroutine()
			ctx, cancel := tc.context(ran)
			defer cancel()
			started := time.Now()
			if got := answeredWithin(t, ctx, f); got != nil {
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

// The answer is names. Nothing of the request, which on a press carries the
// credentials the run would get, reaches it.
func TestAPressAnswersWithNamesAndSurvivesAnUnreadableCluster(t *testing.T) {
	f := listedNamespaceInputs(t)["kube.overview"]
	c := aClusterOfNamespaces(t)
	for _, name := range press(f, "", map[string]any{"token": "s3cret-token", "password": "s3cret-pass"}) {
		if verr := checkName("namespace", name); verr != nil {
			t.Errorf("%q is in the answer and is no namespace name", name)
		}
	}

	c.write(t, "get.fail", `Error from server (Forbidden): namespaces is forbidden: User "system:serviceaccount:ops:rta" `+
		`cannot list resource "namespaces" in API group "" at the cluster scope`)
	if got := press(f, "gi", nil); got != nil {
		t.Errorf("a cluster that refuses the list answered %v, want silence", got)
	}
	if got := press(f, "gi", map[string]any{"context": "--kubeconfig=/tmp/theirs"}); got != nil {
		t.Errorf("a context kubectl would take for a flag answered %v", got)
	}
}

// The keystroke channel never calls it. Candidates is the one gate both human
// surfaces ask, and a field marked Live answers nothing there, so editing a sibling
// box or typing in this one reads nothing of the cluster; under a fence the same
// channel offers the allowlist, which is the whole of what it can know.
func TestTheKeystrokeChannelNeverAsksTheClusterForNamespaces(t *testing.T) {
	fence := plugin.Fence{"namespace": toAny(homelabNamespaces)}
	c := aClusterOfNamespaces(t)
	for id, f := range listedNamespaceInputs(t) {
		req := plugin.CompletionRequest(capabilityNamed(t, id), map[string]any{"namespace": []string{"gitea", "gi"}})
		if got := f.Candidates(context.Background(), req); got != nil {
			t.Errorf("%s: Candidates = %v, want nothing for a live input", id, got)
		}
		if got := fence.Narrow(f, f.Candidates(context.Background(), req)); !slices.Equal(got, homelabNamespaces) {
			t.Errorf("%s: under a fence the channel offers %v, want the allowlist %v", id, got, homelabNamespaces)
		}
	}
	if calls := c.calls(t); len(calls) != 0 {
		t.Errorf("the keystroke channel reached the cluster: %q", calls)
	}
}

// The Suggest lists the cluster whole, for it cannot see a fence, and the host holds
// the answer to it: the seven namespaces of the connection are offered and the two
// beside them are not.
func TestAPressUnderAFenceOffersOnlyTheNamespacesTheFenceAllows(t *testing.T) {
	fence := plugin.Fence{"namespace": toAny(homelabNamespaces)}
	aClusterOfNamespaces(t)
	for id, f := range listedNamespaceInputs(t) {
		answer := press(f, "", nil)
		for _, outside := range []string{"kube-system", "payments"} {
			if !slices.Contains(answer, outside) {
				t.Fatalf("%s: the answer %v lacks %s, which only the fence should take out", id, answer, outside)
			}
		}
		if got := fence.Filter(f, answer); !slices.Equal(got, homelabNamespaces) {
			t.Errorf("%s: the fenced answer = %v, want the seven namespaces of the connection", id, got)
		}
	}
}
