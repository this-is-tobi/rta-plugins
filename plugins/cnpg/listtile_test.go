package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A dashboard tile is the table cnpg.list returns, drawn at the width the tile
// has. The columns that are given up first are the last and the plain ones, so
// what these tests hold is the order of the columns and what each one says: the
// name first, since a row that has lost its name says nothing, and then the
// verdict.

func clusterTable(t *testing.T, clusters ...cluster) view.Table {
	t.Helper()
	serves(t, clusterList{Items: clusters})
	v, err := runList(context.Background(), req(map[string]any{"all-namespaces": true}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, ok := v.(view.Table)
	if !ok {
		t.Fatalf("view is %T, want a table", v)
	}
	return tbl
}

func cellOf(t *testing.T, tbl view.Table, row int, column string) string {
	t.Helper()
	i := slices.Index(columnNames(tbl), column)
	if i < 0 {
		t.Fatalf("no %q column in %v", column, columnNames(tbl))
	}
	return tbl.Rows[row][i]
}

func TestTheListLeadsWithTheNameAndTheVerdictAndHasNoAge(t *testing.T) {
	tbl := clusterTable(t, healthy())
	want := []string{"Cluster", "Status", "Backup", "Ready", "Primary", "Phase"}
	if got := columnNames(tbl); !slices.Equal(got, want) {
		t.Fatalf("columns = %v, want %v", got, want)
	}
	if tbl.Columns[1].Kind != view.KindStatus {
		t.Errorf("Status is %q, want it graded", tbl.Columns[1].Kind)
	}
	// The first column is the one a narrow tile keeps, and it holds the whole
	// name: the namespace beside the cluster, so that two clusters of one name
	// in two namespaces are two rows that can be told apart.
	if got := cellOf(t, tbl, 0, "Cluster"); got != "databases/app-db" {
		t.Errorf("Cluster = %q, want namespace/name", got)
	}
	// A column a narrow tile gives up before the verdict: the primary and the
	// phase say what the verdict already does, and the phase is prose that must
	// not be the one that takes the slack and costs the Status its place.
	for _, name := range []string{"Primary", "Phase"} {
		if tbl.Columns[slices.Index(columnNames(tbl), name)].Priority == 0 {
			t.Errorf("%s has no priority: a narrow tile would keep it over the Status", name)
		}
	}
	if tbl.Columns[0].Priority != 0 || tbl.Columns[1].Priority != 0 {
		t.Error("the name and the Status are given up before another column")
	}
}

// The Status is the worst of the findings the status page lists under "Needs
// attention", computed by the same function: a list that said ok for a cluster
// whose status page said warn was two answers to one question.
func TestTheStatusIsTheWorstFindingTheStatusPageWouldList(t *testing.T) {
	failing := time.Now().Add(-10 * time.Minute)
	for _, tc := range []struct {
		name   string
		mutate func(*cluster)
		want   string
	}{
		{"nothing wrong", func(*cluster) {}, "ok"},
		{"a primary that is unhealthy", func(c *cluster) { c.Status.PrimaryFailingSince = &failing }, "fail"},
		{"no primary", func(c *cluster) { c.Status.CurrentPrimary, c.Status.TargetPrimary = "", "" }, "fail"},
		{"fewer instances than asked", func(c *cluster) { c.Status.ReadyInstances = 2 }, "warn"},
		{"WAL archiving failing", func(c *cluster) {
			c.Status.Conditions = append(c.Status.Conditions, condition{Type: "ContinuousArchiving", Status: "False"})
		}, "warn"},
		{"no backup configured and none ever taken", func(c *cluster) { c.Status.LastSuccessfulBackup = "" }, "warn"},
		{"an expired certificate", func(c *cluster) {
			c.Status.Certificates.Expirations = map[string]string{
				"app-db-server": time.Now().Add(-time.Hour).UTC().Format("2006-01-02 15:04:05 -0700 MST"),
			}
		}, "fail"},
		{"a warning and a failure", func(c *cluster) {
			c.Status.ReadyInstances = 2
			c.Status.PrimaryFailingSince = &failing
		}, "fail"},
		{"a note that is not a finding", func(c *cluster) {
			c.Spec.Plugins = []clusterPlugin{{Name: barmanCloudPlugin, IsWALArchiver: true}}
			c.Status.LastSuccessfulBackup = ""
		}, "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := healthy()
			tc.mutate(&c)
			tbl := clusterTable(t, c)
			if got := cellOf(t, tbl, 0, "Status"); got != tc.want {
				t.Errorf("Status = %q, want %q (the findings: %v)", got, tc.want, problemsIn(t, c))
			}
		})
	}
}

func TestTheBackupColumnIsTheAgeOfTheLastSuccessOrWhyThereIsNone(t *testing.T) {
	archiving := condition{Type: "ContinuousArchiving", Status: "False"}
	for _, tc := range []struct {
		name   string
		mutate func(*cluster)
		want   string
	}{
		{"a recent success", func(*cluster) {}, "4h ago"},
		{"a success days ago", func(c *cluster) {
			c.Status.LastSuccessfulBackup = time.Now().Add(-4 * 24 * time.Hour).Format(time.RFC3339)
		}, "4d ago"},
		{"nothing configured, nothing taken", func(c *cluster) { c.Status.LastSuccessfulBackup = "" }, "none"},
		{"configured and never succeeded", func(c *cluster) {
			c.Status.LastSuccessfulBackup = ""
			c.Spec.Backup = &clusterBackup{BarmanObjectStore: &barmanStore{}}
		}, "never"},
		{"archiving failing", func(c *cluster) {
			c.Spec.Backup = &clusterBackup{BarmanObjectStore: &barmanStore{}}
			c.Status.Conditions = append(c.Status.Conditions, archiving)
		}, "failing"},
		{"archiving failing over a recent success", func(c *cluster) {
			c.Status.Conditions = append(c.Status.Conditions, archiving)
		}, "failing"},
		{"archiving not yet known", func(c *cluster) {
			c.Status.Conditions = append(c.Status.Conditions, condition{Type: "ContinuousArchiving", Status: "Unknown"})
		}, "4h ago"},
		{"the last attempt failed after the last success", func(c *cluster) {
			c.Status.LastFailedBackup = time.Now().Add(-time.Hour).Format(time.RFC3339)
		}, "failing"},
		{"an old failure before the last success", func(c *cluster) {
			c.Status.LastFailedBackup = time.Now().Add(-9 * time.Hour).Format(time.RFC3339)
		}, "4h ago"},
		{"backups taken by a plugin, which the resource does not record", func(c *cluster) {
			c.Spec.Plugins = []clusterPlugin{{Name: barmanCloudPlugin, IsWALArchiver: true}}
		}, "not recorded"},
		{"a plugin and no success in the resource", func(c *cluster) {
			c.Spec.Plugins = []clusterPlugin{{Name: barmanCloudPlugin, IsWALArchiver: true}}
			c.Status.LastSuccessfulBackup = ""
		}, "not recorded"},
		{"a plugin whose archiving fails", func(c *cluster) {
			c.Spec.Plugins = []clusterPlugin{{Name: barmanCloudPlugin, IsWALArchiver: true}}
			c.Status.Conditions = append(c.Status.Conditions, archiving)
		}, "failing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := healthy()
			tc.mutate(&c)
			if got := cellOf(t, clusterTable(t, c), 0, "Backup"); got != tc.want {
				t.Errorf("Backup = %q, want %q", got, tc.want)
			}
		})
	}
}

// The phase is the operator's own sentence, and the one every healthy cluster
// carries is said as the word it comes to. What else it says is the news and is
// left as the operator wrote it.
func TestThePhaseIsOneWordWhenHealthyAndTheOperatorsOwnWordsOtherwise(t *testing.T) {
	waiting := healthy()
	waiting.Metadata.Name = "ledger-db"
	waiting.Status.Phase = "Waiting for the instances to become active"
	bare := healthy()
	bare.Metadata.Name = "bare-db"
	bare.Status.Phase = ""
	tbl := clusterTable(t, healthy(), waiting, bare)
	for i, want := range []string{"healthy", "Waiting for the instances to become active", "—"} {
		if got := cellOf(t, tbl, i, "Phase"); got != want {
			t.Errorf("row %d Phase = %q, want %q", i, got, want)
		}
	}
}

// The three that were wrong on the tile of the audit: a cluster whose archiving
// fails and one with no backup at all read ok beside a healthy one, and the name
// of the cluster was the first thing a narrow tile lost.
func TestAFleetReadsAsItIsAndEveryRowCarriesItsName(t *testing.T) {
	shop := healthy()
	shop.Metadata.Name, shop.Metadata.Namespace = "shop-db", "payments"
	shop.Spec.Backup = &clusterBackup{BarmanObjectStore: &barmanStore{}}
	shop.Status.Conditions = append(shop.Status.Conditions, condition{Type: "ContinuousArchiving", Status: "False"})
	scratch := healthy()
	scratch.Metadata.Name, scratch.Metadata.Namespace = "scratch-db", "auth"
	scratch.Spec.Instances, scratch.Status.ReadyInstances = 1, 1
	scratch.Status.LastSuccessfulBackup = ""
	ledger := healthy()
	ledger.Metadata.Name, ledger.Metadata.Namespace = "ledger-db", "payments"
	ledger.Status.ReadyInstances = 2
	auth := healthy()
	auth.Metadata.Name, auth.Metadata.Namespace = "auth-db", "auth"
	failing := time.Now().Add(-time.Minute)
	auth.Status.PrimaryFailingSince = &failing

	tbl := clusterTable(t, shop, scratch, ledger, auth)
	for i, want := range [][]string{
		{"payments/shop-db", "warn", "failing"},
		{"auth/scratch-db", "warn", "none"},
		{"payments/ledger-db", "warn", "4h ago"},
		{"auth/auth-db", "fail", "4h ago"},
	} {
		got := []string{cellOf(t, tbl, i, "Cluster"), cellOf(t, tbl, i, "Status"), cellOf(t, tbl, i, "Backup")}
		if !slices.Equal(got, want) {
			t.Errorf("row %d = %v, want %v", i, got, want)
		}
	}
	if tbl.Total != 4 {
		t.Errorf("total = %d, want 4", tbl.Total)
	}
}

// One cluster of one name in two namespaces is two rows, and the empty answer
// is still a sentence and no rows.
func TestTheSameNameInTwoNamespacesIsTwoRowsAndNoClustersIsASentence(t *testing.T) {
	a, b := healthy(), healthy()
	b.Metadata.Namespace = "staging"
	tbl := clusterTable(t, a, b)
	if x, y := cellOf(t, tbl, 0, "Cluster"), cellOf(t, tbl, 1, "Cluster"); x == y || !strings.HasSuffix(x, "/app-db") {
		t.Errorf("rows = %q and %q, want two that can be told apart", x, y)
	}
	empty := clusterTable(t)
	if len(empty.Rows) != 0 || empty.Empty == "" {
		t.Errorf("an empty answer is %d rows and %q, want no rows and a sentence", len(empty.Rows), empty.Empty)
	}
}
