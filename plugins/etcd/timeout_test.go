package main

import (
	"context"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A cluster that answers nothing is sent to the overview that shows whether
// the members can see each other, and the overview is a call that reaches the
// cluster that did not answer: through a profile's forward the endpoint was
// 127.0.0.1 and a port that closed with the call, so the profile names the
// cluster, and reached directly the endpoint and how it was reached stay.
func TestTheOverviewATimeoutOffersReachesTheClusterThatDidNotAnswer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		values  map[string]any
		tunnel  plugin.Tunnel
		profile string
		surface plugin.Surface
		want    string
		unwant  string
	}{
		{"a terminal, no profile", map[string]any{"endpoint": "etcd-0.internal:2379", "tls": true},
			plugin.TunnelNone, "", plugin.SurfaceCLI,
			"`rta etcd overview --endpoint etcd-0.internal:2379 --tls` shows whether the members can see each other", "--profile"},
		{"a terminal, through a forward", map[string]any{"endpoint": "127.0.0.1:54321", "ca-file": "/etc/etcd/ca.pem"},
			plugin.TunnelKube, "lab", plugin.SurfaceCLI,
			"`rta etcd overview --profile lab --ca-file /etc/etcd/ca.pem` shows whether the members can see each other", "--endpoint"},
		{"an agent, through a profile", map[string]any{"endpoint": "127.0.0.1:54321"},
			plugin.TunnelKube, "lab", plugin.SurfaceMCP,
			"`etcd_overview {\"profile\":\"lab\"}` shows whether the members can see each other", "endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t, "etcd.overview", tc.values).WithProfile(tc.profile, tc.tunnel).WithSurface(tc.surface)
			verr := classify(context.DeadlineExceeded, r)
			if verr.Code != "etcd.timeout" || !strings.Contains(verr.Hint, tc.want) || strings.Contains(verr.Hint, tc.unwant) {
				t.Errorf("%s %q, want etcd.timeout with %q and not %q", verr.Code, verr.Hint, tc.want, tc.unwant)
			}
		})
	}
}
