package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A kubeconfig that exists and names no current context made every call fail
// as kube.failed with kubectl's own sentence and no hint, when the two calls
// that fix it are this plugin's own: the listing of the contexts there are,
// and the switch that makes one current. An agent is told whose the fix is
// instead, since the listing needs a grant of its own for it
// (TestAnAgentIsNeitherToldTheContextsNorSentToTheListThatNeedsAGrant).
func TestAKubeconfigWithNoCurrentContextNamesTheCallsThatFixIt(t *testing.T) {
	for _, tc := range []struct {
		surface plugin.Surface
		want    string
	}{
		{plugin.SurfaceCLI, "`rta kube context list` shows the contexts this machine has, and `rta kube context set` makes one current"},
		{plugin.SurfaceTUI, "`kube.context.list` shows the contexts this machine has, and `kube.context.set` makes one current"},
		{plugin.SurfaceMCP, "ask the operator which context to use"},
	} {
		verr := classify(context.Background(), errors.New("exit status 1"), "error: current-context is not set\n",
			[]string{"get", "nodes"}, tc.surface)
		if verr.Code != "kube.context.none" {
			t.Errorf("%s: code = %q, want kube.context.none", tc.surface, verr.Code)
		}
		if !strings.Contains(verr.Hint, tc.want) {
			t.Errorf("%s: hint = %q, want %q in it", tc.surface, verr.Hint, tc.want)
		}
	}
}

// The switch to a context leaves the name for its reader to type, bare as a
// usage line spells it, rather than quoted as a name somebody gave.
func TestTheSwitchToAContextLeavesItsNameForTheReader(t *testing.T) {
	for sf, want := range map[plugin.Surface]string{
		plugin.SurfaceCLI: "`rta kube context set <name>` picks one",
		plugin.SurfaceTUI: "`kube.context.set name=<name>` picks one",
	} {
		if got := pickContext(sf); got != want {
			t.Errorf("%s: %q, want %q", sf, got, want)
		}
	}
}

// With no kubeconfig at all, a current kubectl does not say so: it tries
// localhost:8080 and answers with a client-go log line about a connection
// refused. The overview put that line in its cluster row as "did not answer",
// with a hint to check the VPN, for a machine that has no cluster to be on a VPN
// for. `config view` has already answered, and lists no context: nothing is asked
// of any cluster.
func TestAnOverviewOfAMachineWithNoKubeconfigContextSaysNothingWasContacted(t *testing.T) {
	c := newScriptedCluster(t)
	c.write(t, "config.json", `{"kind":"Config","apiVersion":"v1","preferences":{},"clusters":null,"users":null,"contexts":null,"current-context":""}`)
	c.write(t, "get.fail", `E1018 23:21:40.718598   43556 memcache.go:381] "Couldn't get current server API group list" `+
		`err="Get \"http://localhost:8080/api?timeout=15s\": dial tcp [::1]:8080: connect: connection refused"`)

	for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceMCP} {
		v, err := runOverview(context.Background(), saReq(sf, false, map[string]any{}))
		if err != nil {
			t.Fatal(err)
		}
		rows := keyValues(t, v)
		if want := "was never contacted — no kubeconfig context"; rows["cluster"] != want {
			t.Errorf("%s: cluster = %q, want %q", sf, rows["cluster"], want)
		}
		for _, bad := range []string{"VPN", "localhost", "memcache"} {
			if strings.Contains(rows["what to check"], bad) || strings.Contains(rows["cluster"], bad) {
				t.Errorf("%s: the page mentions %q: %v", sf, bad, rows)
			}
		}
		if sf == plugin.SurfaceMCP && !strings.Contains(rows["what to check"], "ask the operator") {
			t.Errorf("an agent is not told it is the operator's to fix: %q", rows["what to check"])
		}
		if sf == plugin.SurfaceCLI && !strings.Contains(rows["what to check"], "kubectl config get-contexts") {
			t.Errorf("a person is not told how to see the contexts: %q", rows["what to check"])
		}
	}
	if len(c.listed(t, "namespaces"))+len(c.listed(t, "pods"))+len(c.listed(t, "nodes")) != 0 {
		t.Errorf("a cluster was asked although the kubeconfig names none:\n%s", strings.Join(c.calls(t), "\n"))
	}
}
