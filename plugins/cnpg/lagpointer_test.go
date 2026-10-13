package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func overviewPairs(t *testing.T, c cluster, lag string) map[string]string {
	t.Helper()
	sections, ok := statusView(c, pointers{lag: lag}).(view.Sections)
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
// surface that asked, with the profile when the call came through one, and
// what it takes to reach the primary it reads them from. It only points: this
// read stays one GET of one resource.
func TestAClusterWithReplicasPointsAtWhereLagIsRead(t *testing.T) {
	const lists = " lists each standby's sent, write, flush and replay position (LSN) and how far behind it is"
	for _, tc := range []struct {
		name    string
		profile string
		surface plugin.Surface
		want    string
	}{
		{"a terminal", "", plugin.SurfaceCLI, "`rta pg replication`" + lists},
		{"a terminal, through a profile", "prod", plugin.SurfaceCLI, "`rta pg replication --profile prod`" + lists},
		{"an agent", "", plugin.SurfaceMCP, "the `pg_replication` tool" + lists},
		{"an agent, through a profile", "prod", plugin.SurfaceMCP, "`pg_replication {\"profile\":\"prod\"}`" + lists},
		{"a form", "", plugin.SurfaceTUI, "`pg.replication`" + lists},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(map[string]any{"cluster": "app-db"}).WithProfile(tc.profile, plugin.TunnelNone).WithSurface(tc.surface)
			got := overviewPairs(t, healthy(), lagPointer(r))["Lag and positions"]
			if !strings.Contains(got, tc.want) || !strings.HasPrefix(got, "not in the Cluster resource — ") {
				t.Errorf("row = %q, want it to hold %q", got, tc.want)
			}
			// Where it connects, said the same on every surface: the Service that
			// follows the primary, reached through a profile's forward, as a role
			// that is shown a standby's positions.
			for _, part := range []string{"read from the primary", "the cluster's read-write service (app-db-rw)",
				"a profile's kube: forward", "pg_monitor"} {
				if !strings.Contains(got, part) {
					t.Errorf("row = %q, want it to say %q", got, part)
				}
			}
		})
	}

	// A call that named no cluster says what to look for and not a name it was
	// never given.
	unnamed := overviewPairs(t, healthy(), lagPointer(req(nil)))["Lag and positions"]
	if !strings.Contains(unnamed, "the cluster's read-write service,") || strings.Contains(unnamed, "-rw") {
		t.Errorf("row = %q, want the service described and no name invented", unnamed)
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

// What the capability says of itself — the README is generated from it, and an
// agent decides from it — has the same answer as the row: no live position is
// read here, because that needs the instances and this plugin reads the
// resource and no pod, and the pg plugin's replication page reached through the
// cluster's read-write service has every standby's.
func TestTheStatusDescriptionSaysWhereTheLivePositionsAreAndWhyNotHere(t *testing.T) {
	c := capabilityOf(t, "cnpg.status")
	// The README and a person at a terminal read the long form, which says what
	// the page lists and as whom; an agent reads the short one on every
	// connection, held to 800 bytes with the summary, which says where and how
	// to reach the primary.
	for _, want := range []string{"replication page", "sent, write, flush and replay LSN", "read-write service",
		"<cluster>-rw", "kube:", "pg_monitor", "reads no pod"} {
		if !strings.Contains(c.Description, want) {
			t.Errorf("the description does not say %q: %s", want, c.Description)
		}
	}
	for _, want := range []string{"LSN", "replication capability", "kube: forward", "<cluster>-rw", "no pods, exec or logs"} {
		if !strings.Contains(c.Agent, want) {
			t.Errorf("the agent text does not say %q: %s", want, c.Agent)
		}
	}
}
