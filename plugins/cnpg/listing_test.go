package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The listing a cluster not found offers is a call, and it reaches the cluster
// the refused call read: through a profile, or in a context typed at a
// terminal, the listing bare read the current context, where the cluster was
// not found either. An agent is given the profile alone, the context being
// Local.
func TestTheListingAClusterNotFoundOffersReachesTheClusterItWasLookedFor(t *testing.T) {
	const notFound = `Error from server (NotFound): clusters.postgresql.cnpg.io "shop" not found`
	for _, tc := range []struct {
		name    string
		values  map[string]any
		profile string
		surface plugin.Surface
		want    string
	}{
		{"a terminal, nothing to say", map[string]any{}, "", plugin.SurfaceCLI,
			"`rta cnpg list --all-namespaces` shows what is there"},
		{"a terminal, a context", map[string]any{"context": "kind"}, "", plugin.SurfaceCLI,
			"`rta cnpg list --all-namespaces --context kind` shows what is there"},
		{"a terminal, a profile", map[string]any{"context": "kind"}, "prod", plugin.SurfaceCLI,
			"`rta cnpg list --all-namespaces --profile prod --context kind` shows what is there"},
		{"an agent, a profile", map[string]any{"context": "kind"}, "prod", plugin.SurfaceMCP,
			"`cnpg_list {\"all-namespaces\":true,\"profile\":\"prod\"}` shows what is there"},
		{"an agent, nothing to say", map[string]any{}, "", plugin.SurfaceMCP,
			"the `cnpg_list` tool with the \"all-namespaces\" argument shows what is there"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, verr := selectionOf(req(tc.values).WithProfile(tc.profile, plugin.TunnelNone).WithSurface(tc.surface))
			if verr != nil {
				t.Fatal(verr)
			}
			got := classify(context.Background(), &exec.ExitError{}, notFound, nil, s.caller())
			if !strings.Contains(got.Hint, tc.want) {
				t.Errorf("hint = %q, want %q in it", got.Hint, tc.want)
			}
		})
	}
}
