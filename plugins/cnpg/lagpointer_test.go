package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func overviewPairs(t *testing.T, c cluster, lag string) map[string]string {
	t.Helper()
	sections, ok := statusView(c, lag).(view.Sections)
	if !ok {
		t.Fatalf("status is not sections")
	}
	pairs := map[string]string{}
	for _, p := range sections.Items[0].View.(view.KeyValue).Pairs {
		pairs[p.Key] = p.Value
	}
	return pairs
}

// The Cluster resource holds no replication positions and no lag, which is
// what someone opens a status page of replicas to ask. The page says so and
// says where they are read: the pg plugin's replication page, spelled for the
// surface that asked, with the profile when the call came through one. It
// only points: this read stays one GET of one resource.
func TestAClusterWithReplicasPointsAtWhereLagIsRead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		surface plugin.Surface
		want    string
	}{
		{"a terminal", "", plugin.SurfaceCLI, "`rta pg replication` reads each standby's position"},
		{"a terminal, through a profile", "prod", plugin.SurfaceCLI, "`rta pg replication --profile prod` reads each standby's position"},
		{"an agent", "", plugin.SurfaceMCP, "the `pg_replication` tool reads each standby's position"},
		{"an agent, through a profile", "prod", plugin.SurfaceMCP, "`pg_replication {\"profile\":\"prod\"}` reads each standby's position"},
		{"a form", "", plugin.SurfaceTUI, "`pg.replication` reads each standby's position"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(nil).WithProfile(tc.profile, plugin.TunnelNone).WithSurface(tc.surface)
			got := overviewPairs(t, healthy(), lagPointer(r))["Lag and positions"]
			if !strings.Contains(got, tc.want) || !strings.HasPrefix(got, "not in the Cluster resource — ") {
				t.Errorf("row = %q, want it to hold %q", got, tc.want)
			}
		})
	}

	single := healthy()
	single.Spec.Instances = 1
	if _, ok := overviewPairs(t, single, lagPointer(req(nil)))["Lag and positions"]; ok {
		t.Error("a single instance has no replica, and the page pointed at lag anyway")
	}
	if _, ok := overviewPairs(t, healthy(), "")["Lag and positions"]; ok {
		t.Error("a page given nothing to point at invented a row")
	}
}
