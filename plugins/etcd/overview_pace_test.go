package main

import (
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A tile of etcd.overview is a Status, a MemberList and a connection to every
// other member it can reach, on every refresh. Without a declared pace the host
// ran it at its default and a configuration could shorten that to the timer's own
// tick, so `refresh: 5s` was accepted and a cluster was dialled for a glance at
// the speed of a clock. A declared pace is a floor a configuration cannot go below.
func TestTheOverviewTileCannotBeRefreshedFasterThanAMinute(t *testing.T) {
	var found bool
	for _, c := range Plugin().Capabilities {
		if c.ID != "etcd.overview" {
			continue
		}
		found = true
		if got := c.Pace(); got != time.Minute {
			t.Errorf("etcd.overview is re-run every %s, want a minute", got)
		}
		if pace, cut := plugin.ClampPace(c, 5*time.Second); pace != time.Minute || !cut {
			t.Errorf("a configured 5s pace came to %s (cut %v), want the minute it cannot go below", pace, cut)
		}
	}
	if !found {
		t.Fatal("no etcd.overview")
	}
}
