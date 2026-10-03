package main

import (
	"context"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The address row is what a page kept or pasted into a ticket says about the
// server, and through a forward the address is 127.0.0.1 and a port that closed
// with the call. It names the server the way its reader reaches it again.
func TestTheOverviewNamesTheProfileNotTheEndOfItsForward(t *testing.T) {
	srv := newFakeServer(t, map[string]string{"INFO all": bulk(sampleInfo)})
	for name, tc := range map[string]struct {
		profile string
		tunnel  plugin.Tunnel
		want    string
	}{
		"direct":    {"", plugin.TunnelNone, srv.addr()},
		"a profile": {"prod", plugin.TunnelNone, srv.addr() + " (profile prod)"},
		"a forward": {"prod", plugin.TunnelKube, "profile prod (through its kube: forward)"},
	} {
		t.Run(name, func(t *testing.T) {
			r := req(t, "redis.overview", map[string]any{"address": srv.addr()})
			if tc.profile != "" {
				r = r.WithProfile(tc.profile, tc.tunnel)
			}
			var v view.View
			var err error
			for _, c := range Plugin().Capabilities {
				if c.ID == "redis.overview" {
					v, err = c.Run(context.Background(), r)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			server := sectionOf(t, v.(view.Sections), "server").(view.KeyValue)
			if got := pairValue(server, "address"); got != tc.want {
				t.Errorf("address row = %q, want %q", got, tc.want)
			}
		})
	}
}
