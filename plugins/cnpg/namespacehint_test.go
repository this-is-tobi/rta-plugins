package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A namespace beside every namespace is refused, and the caller who sent only
// the switch did not send the namespace: a profile or the config filled it in.
// "Pass one or the other" asked them to do what they had done, so the refusal
// says where the other half may have come from and how to lift it, spelled for
// the surface that reads it.
func TestAnAllNamespacesRefusalSaysWhereTheNamespaceMayHaveComeFrom(t *testing.T) {
	for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceMCP, plugin.SurfaceTUI} {
		_, verr := selectionOf(plugin.NewRequest(map[string]any{
			"namespace": "apps", "all-namespaces": true,
		}, false, false).WithSurface(sf))
		if verr == nil || verr.Code != "cnpg.namespace.ambiguous" {
			t.Fatalf("%s: a namespace beside every namespace was not refused as ambiguous: %v", sf, verr)
		}
		for _, want := range []string{"a profile or the config", sf.InputName("namespace") + " given empty lifts it", "`only:`", "the operator's"} {
			if !strings.Contains(verr.Hint, want) {
				t.Errorf("%s: the hint never says %q:\n%s", sf, want, verr.Hint)
			}
		}
	}
}

// The way out the hint gives is one the call takes: an empty namespace is no
// namespace, whatever filled the other one.
func TestAnEmptyNamespaceBesideEveryNamespaceIsAccepted(t *testing.T) {
	got, verr := selectionOf(plugin.NewRequest(map[string]any{
		"namespace": "", "all-namespaces": true,
	}, false, false))
	if verr != nil || !got.allNamespace || got.namespace != "" {
		t.Errorf("an empty namespace with every namespace was not accepted: %+v %v", got, verr)
	}
}
