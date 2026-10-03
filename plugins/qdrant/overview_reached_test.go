package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The endpoint row is what a page kept or pasted into a ticket says about the
// server, and through a forward the address is 127.0.0.1 and a port that closed
// with the call. It names the server the way its reader reaches it again.
func TestTheOverviewNamesTheProfileNotTheEndOfItsForward(t *testing.T) {
	f := newFakeQdrant(t, peerRoutes(fxTelemetryDown))
	for name, tc := range map[string]struct {
		profile string
		tunnel  plugin.Tunnel
		want    string
	}{
		"direct":    {"", plugin.TunnelNone, f.endpoint()},
		"a profile": {"prod", plugin.TunnelNone, f.endpoint() + " (profile prod)"},
		"a forward": {"prod", plugin.TunnelKube, "profile prod (through its kube: forward)"},
	} {
		t.Run(name, func(t *testing.T) {
			r := reqAt(t, f, "qdrant.overview", map[string]any{})
			if tc.profile != "" {
				r = r.WithProfile(tc.profile, tc.tunnel)
			}
			v, err := runOverview(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			if got := pair(section(t, v, "status").(view.KeyValue), "endpoint"); got != tc.want {
				t.Errorf("endpoint row = %q, want %q", got, tc.want)
			}
		})
	}
}
