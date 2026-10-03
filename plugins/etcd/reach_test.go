package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A key that is not there is looked for again with the call the hint hands
// over, and that call has to reach the cluster this one read: through a
// profile's forward the endpoint was 127.0.0.1 and a port that closed with
// the call, so the profile is what names the cluster, and reached directly
// the endpoint and what the certificate was checked against stay. The
// hint without them was run against whatever endpoint the configuration
// there named.
func TestAMissingKeyHintReachesTheClusterItRead(t *testing.T) {
	addr, ca := memberBehindAForward(t)
	for _, tc := range []struct {
		name         string
		profile      string
		tunnel       plugin.Tunnel
		surface      plugin.Surface
		want, unwant []string
	}{
		{"a terminal, no profile", "", plugin.TunnelNone, plugin.SurfaceCLI,
			[]string{"rta etcd kv list /k", "--endpoint " + addr, "--ca-file " + ca, "--tls-server-name svc.example.internal"},
			[]string{"--profile"}},
		{"a terminal, through a forward", "lab", plugin.TunnelKube, plugin.SurfaceCLI,
			[]string{"rta etcd kv list /k", "--profile lab", "--ca-file " + ca},
			[]string{"--endpoint"}},
		{"an agent, through a forward", "lab", plugin.TunnelKube, plugin.SurfaceMCP,
			[]string{"etcd_kv_list", `"profile":"lab"`, `"prefix":"/k"`},
			[]string{"endpoint", "ca-file", "tls-server-name"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t, "etcd.kv.get", map[string]any{"endpoint": addr, "ca-file": ca,
				"tls-server-name": "svc.example.internal", "key": "/k"}).
				WithProfile(tc.profile, tc.tunnel).WithSurface(tc.surface)
			c, verr := connectWithin(context.Background(), r, 5*time.Second)
			if verr != nil {
				t.Fatalf("connect: %s: %s", verr.Code, verr.Message)
			}
			defer func() { _ = c.Close() }()
			_, err := kvGetView(context.Background(), c, r)
			var refusal *view.Error
			if !errors.As(err, &refusal) || refusal.Code != "etcd.key.notfound" {
				t.Fatalf("got %v, want etcd.key.notfound", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(refusal.Hint, w) {
					t.Errorf("hint %q does not hold %q", refusal.Hint, w)
				}
			}
			for _, w := range tc.unwant {
				if strings.Contains(refusal.Hint, w) {
					t.Errorf("hint %q holds %q", refusal.Hint, w)
				}
			}
		})
	}
}
