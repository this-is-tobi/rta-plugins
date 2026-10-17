package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// emptyOf runs one capability against a cluster that holds nothing of the kind
// and returns the sentence its table carries for a person, wherever the table
// sits in the view.
func emptyOf(t *testing.T, run plugin.Handler, values map[string]any) string {
	t.Helper()
	return emptyOn(t, plugin.SurfaceCLI, run, values)
}

func emptyOn(t *testing.T, sf plugin.Surface, run plugin.Handler, values map[string]any) string {
	t.Helper()
	v, err := run(context.Background(), plugin.NewRequest(values, false, false).WithSurface(sf))
	if err != nil {
		t.Fatal(err)
	}
	switch v := v.(type) {
	case view.Table:
		return v.Empty
	case view.Sections:
		for _, s := range v.Items {
			if tbl, ok := s.View.(view.Table); ok {
				return tbl.Empty
			}
		}
	}
	t.Fatalf("got %T, want a table", v)
	return ""
}

// A listing with nothing in it is a table with headings over no rows, which on
// a screen reads like a listing that failed. Each says what it looked at, so
// an empty answer is a result and not a doubt: the scope is named because the
// likeliest reason for an empty list is a namespace nobody meant.
func TestAnEmptyListingSaysWhatItLookedAtAndFound(t *testing.T) {
	withFixtureKubectl(t, `{"items":[]}`)
	cases := []struct {
		name   string
		run    plugin.Handler
		values map[string]any
		want   []string
	}{
		{"pods", runPodList, map[string]any{"namespace": "demo"}, []string{"no pods", "namespace demo"}},
		{"pods everywhere", runPodList, map[string]any{"all-namespaces": true}, []string{"no pods", "any namespace"}},
		{"pods in the context's namespace", runPodList, map[string]any{}, []string{"no pods", "the current namespace"}},
		{"deployments", runDeploymentList, map[string]any{"namespace": "demo"}, []string{"no deployments", "namespace demo"}},
		{"warning events", runEventList, map[string]any{"namespace": "demo"}, []string{"no warning events", "namespace demo", "--normal"}},
		{"events of every type", runEventList, map[string]any{"namespace": "demo", "normal": true}, []string{"no events", "namespace demo"}},
		{"volume claims", runPVCList, map[string]any{"namespace": "demo"}, []string{"no persistent volume claims", "namespace demo"}},
		{"certificates", runCertList, map[string]any{"namespace": "demo"}, []string{"no TLS Secrets", "namespace demo"}},
		{"quotas", runQuotaList, map[string]any{"namespace": "demo"}, []string{"no resource quotas", "namespace demo"}},
		{"provisioned accounts", runServiceAccountList, map[string]any{"namespace": "demo"}, []string{"no ServiceAccounts", "namespace demo", "`rta kube serviceaccount provision`"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := emptyOf(t, c.run, c.values)
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("empty = %q, want %q in it", got, want)
				}
			}
		})
	}
}

// What to run next is spelled for the surface that reads it.
func TestTheEmptyProvisionedListNamesTheNextCallForItsSurface(t *testing.T) {
	withFixtureKubectl(t, `{"items":[]}`)
	got := emptyOn(t, plugin.SurfaceTUI, runServiceAccountList, map[string]any{"namespace": "demo"})
	if !strings.Contains(got, "`kube.serviceaccount.provision`") {
		t.Errorf("empty = %q, want the capability as the TUI names it", got)
	}
}

// --unhealthy is a question, and "every pod is serving" is its answer when the
// cluster has pods and all of them are: a bare header under it reads as the
// filter having failed. When there were no pods at all, saying every one is
// serving would be true of nothing, so it says there are none.
func TestAnUnhealthyListThatFindsNothingSaysSo(t *testing.T) {
	withKubectlAnswering(t, filepath.Join("testdata", "pods-serving.json"))
	got := emptyOf(t, runPodList, map[string]any{"namespace": "demo", "unhealthy": true})
	for _, want := range []string{"every pod", "is serving", "namespace demo", "2 checked"} {
		if !strings.Contains(got, want) {
			t.Errorf("empty = %q, want %q in it", got, want)
		}
	}

	withFixtureKubectl(t, `{"items":[]}`)
	got = emptyOf(t, runPodList, map[string]any{"namespace": "demo", "unhealthy": true})
	if strings.Contains(got, "serving") || !strings.Contains(got, "no pods") {
		t.Errorf("empty = %q, want that there are no pods, and no claim about their health", got)
	}
}

// The sentence is for a person. A table that has rows carries none, and it is
// never in the data a script or an agent reads.
func TestAListingWithRowsCarriesNoEmptySentence(t *testing.T) {
	withKubectlAnswering(t, filepath.Join("testdata", "pods-edge.json"))
	if got := emptyOf(t, runPodList, map[string]any{"namespace": "demo"}); got != "" {
		t.Errorf("a table with rows says %q", got)
	}
}
