package main

import (
	"context"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

const (
	readyNode = `{"metadata":{"name":"n1"},"spec":{},"status":{"allocatable":{"pods":"110"},` +
		`"conditions":[{"type":"Ready","status":"True"}]}}`
	runningPod = `{"metadata":{"name":"web","namespace":"apps"},"spec":{"containers":[{}]},"status":{"phase":"Running",` +
		`"conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"ready":true,"state":{"running":{}}}]}}`
)

func overviewCapability(t *testing.T) plugin.Capability {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == "kube.overview" {
			return c
		}
	}
	t.Fatal("no kube.overview")
	return plugin.Capability{}
}

// listed is the `get <resource>` lines the cluster was asked.
func (c *scriptedCluster) listed(t *testing.T, resource string) []string {
	t.Helper()
	var out []string
	for _, line := range c.calls(t) {
		if strings.HasPrefix(line, "get "+resource+" ") {
			out = append(out, line)
		}
	}
	return out
}

func keyValues(t *testing.T, v view.View) map[string]string {
	t.Helper()
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("got %T, want key/value rows", v)
	}
	out := map[string]string{}
	for _, p := range kv.Pairs {
		out[p.Key] = p.Value
	}
	return out
}

// What the overview reads of a namespace is the pods, the quotas and the TLS
// Secrets in it, and a profile that fences the namespace to one has said which
// one. The overview declared no namespace, so the fence had nothing to hold it
// to: `kube pod list` was refused under the fence and `kube overview` answered
// with every pod name, quota name and certificate of the cluster.
func TestTheOverviewUnderANamespaceFenceReadsThatNamespaceAndNoOther(t *testing.T) {
	c := newScriptedCluster(t)
	overview := overviewCapability(t)
	in := plugin.Inputs{Fence: plugin.Fence{"namespace": {"apps"}}, Caller: map[string]any{}}
	if breach := plugin.CheckFence(overview, in); breach != nil {
		t.Fatalf("a call that names nothing was refused under a one-entry fence: %+v", breach)
	}
	req := plugin.ResolveRequest(overview, in, false, false)
	if got := req.String("namespace"); got != "apps" {
		t.Fatalf("the fence filled namespace = %q, want the one it allows", got)
	}
	if _, err := runOverview(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"pods", "resourcequotas", "secrets"} {
		lines := c.listed(t, kind)
		if len(lines) != 1 {
			t.Fatalf("%s was listed %d times, want once:\n%s", kind, len(lines), strings.Join(c.calls(t), "\n"))
		}
		if !strings.Contains(lines[0], "--namespace=apps") || strings.Contains(lines[0], "--all-namespaces") {
			t.Errorf("%q: the fenced namespace is not the one read", lines[0])
		}
	}
	for _, kind := range []string{"namespaces", "nodes"} {
		for _, line := range c.listed(t, kind) {
			if strings.Contains(line, "--namespace") || strings.Contains(line, "--all-namespaces") {
				t.Errorf("%q: %s are cluster-scoped and read as such", line, kind)
			}
		}
	}

	for name, caller := range map[string]map[string]any{
		"every namespace":      {"all-namespaces": true},
		"another namespace":    {"namespace": "payments"},
		"both, as a widening":  {"namespace": "apps", "all-namespaces": true},
		"a namespace and none": {"namespace": ""},
	} {
		wide := plugin.Inputs{Fence: in.Fence, Caller: caller}
		if plugin.CheckFence(overview, wide) == nil {
			t.Errorf("%s got past a fence on the namespace", name)
		}
	}
}

func TestTheOverviewReadsEveryNamespaceUnlessOneIsNamed(t *testing.T) {
	for name, tc := range map[string]struct {
		values map[string]any
		want   string
		reject string
	}{
		"none named":      {map[string]any{}, "--all-namespaces", "--namespace"},
		"every one named": {map[string]any{"all-namespaces": true}, "--all-namespaces", "--namespace"},
		"one named":       {map[string]any{"namespace": "prod"}, "--namespace=prod", "--all-namespaces"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newScriptedCluster(t)
			if _, err := runOverview(context.Background(), saReq(plugin.SurfaceCLI, false, tc.values)); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"pods", "resourcequotas", "secrets"} {
				line := strings.Join(c.listed(t, kind), "\n")
				if !strings.Contains(line, tc.want) || strings.Contains(line, tc.reject) {
					t.Errorf("%q, want %s and not %s", line, tc.want, tc.reject)
				}
			}
		})
	}
}

func TestTheOverviewRefusesANamespaceBesideEveryNamespaceAsItsSiblingsDo(t *testing.T) {
	newScriptedCluster(t)
	_, err := runOverview(context.Background(), saReq(plugin.SurfaceCLI, false,
		map[string]any{"namespace": "prod", "all-namespaces": true}))
	if verr, ok := err.(*view.Error); !ok || verr.Code != "kube.namespace.ambiguous" {
		t.Errorf("error = %v, want kube.namespace.ambiguous", err)
	}
}

// A page narrowed to one namespace says so where it said the kubeconfig's
// default, and does not report a capacity it did not measure: the pods it read
// are those of one namespace, against the slots of every node.
func TestANarrowedOverviewSaysWhichNamespaceItReadAndDoesNotCountSlots(t *testing.T) {
	c := newScriptedCluster(t)
	c.list(t, "nodes", readyNode)
	c.list(t, "pods", runningPod)

	narrow, err := runOverview(context.Background(), saReq(plugin.SurfaceCLI, false, map[string]any{"namespace": "apps"}))
	if err != nil {
		t.Fatal(err)
	}
	rows := keyValues(t, narrow)
	if !strings.HasPrefix(rows["namespace"], "apps") || !strings.Contains(rows["namespace"], "only") {
		t.Errorf("namespace = %q, want the one that was read, and that it is the only one", rows["namespace"])
	}
	if rows["pods"] != "1, 0 not ready" {
		t.Errorf("pods = %q, want the namespace's one", rows["pods"])
	}
	if !strings.Contains(rows["pod slots"], "not counted") {
		t.Errorf("pod slots = %q, want it said the slots were not counted", rows["pod slots"])
	}

	wide, err := runOverview(context.Background(), saReq(plugin.SurfaceCLI, false, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := keyValues(t, wide)["pod slots"]; got != "1 of 110 used (0%)" {
		t.Errorf("pod slots of the whole cluster = %q, want 1 of 110 used (0%%)", got)
	}
}

func TestTheDeploymentsOfADetailedOverviewAreThoseOfTheNamespaceRead(t *testing.T) {
	c := newScriptedCluster(t)
	if _, err := runOverview(context.Background(), saReq(plugin.SurfaceCLI, false,
		map[string]any{"namespace": "apps", "detail": true})); err != nil {
		t.Fatal(err)
	}
	lines := c.listed(t, "deployments")
	if len(lines) != 1 || !strings.Contains(lines[0], "--namespace=apps") || strings.Contains(lines[0], "--all-namespaces") {
		t.Errorf("deployments listed as %q, want the namespace read and no other", lines)
	}
}
