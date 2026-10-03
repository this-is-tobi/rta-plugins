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
	for name, tc := range map[string]struct {
		r    plugin.Request
		want string
	}{
		"direct": {req(t, "s3.overview", map[string]any{"endpoint": "s3.internal:9000"}), "s3.internal:9000"},
		"a profile": {req(t, "s3.overview", map[string]any{"endpoint": "s3.internal:9000"}).
			WithProfile("prod", plugin.TunnelNone), "s3.internal:9000 (profile prod)"},
		"a forward": {req(t, "s3.overview", map[string]any{"endpoint": "127.0.0.1:41233"}).
			WithProfile("prod", plugin.TunnelKube), "profile prod (through its kube: forward)"},
	} {
		t.Run(name, func(t *testing.T) {
			kv, ok := compactOverview(tc.r, nil).(view.KeyValue)
			if !ok || len(kv.Pairs) == 0 || kv.Pairs[0].Key != "endpoint" || kv.Pairs[0].Value != tc.want {
				t.Errorf("pairs = %+v, want the endpoint row to read %q", kv.Pairs, tc.want)
			}
		})
	}
}
