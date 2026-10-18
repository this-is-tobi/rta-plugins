package main

import (
	"testing"
	"time"
)

// How often a tile of a capability may be re-run is the capability's to say and
// the host's to wait out: a run's cost is the one thing the host cannot see.
// The three here are the ones whose runs are dear: every Secret's private key on
// the wire, a kubelet call on every node, and five cluster-wide lists.
func TestTheCapabilitiesWithDearRunsDeclareHowOftenATileMayRunThem(t *testing.T) {
	want := map[string]time.Duration{
		"kube.overview":  time.Minute,
		"kube.cert.list": time.Hour,
		"kube.pvc.usage": 5 * time.Minute,
	}
	for _, c := range Plugin().Capabilities {
		if d, ok := want[c.ID]; ok {
			if c.Refresh != d {
				t.Errorf("%s declares Refresh %v, want %v", c.ID, c.Refresh, d)
			}
			delete(want, c.ID)
		}
	}
	for id := range want {
		t.Errorf("no %s", id)
	}
}
