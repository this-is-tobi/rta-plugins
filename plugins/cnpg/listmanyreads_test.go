package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The backup listing and the volume read over several namespaces: the same
// rules as the listing of clusters (listmany_test.go), on the objects they read.

func forbiddenBackups(namespace string) string {
	return fmt.Sprintf(forbiddenIn, backupCRD, "backups", "postgresql.cnpg.io", namespace)
}

func forbiddenVolumes(namespace string) string {
	return fmt.Sprintf(forbiddenIn, "persistentvolumeclaims", "persistentvolumeclaims", "", namespace)
}

func TestBackupsOfSeveralNamespacesAreOneListWithTheNamespaceLeading(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{
		"alpha": backupItems(
			aBackup("old", "alpha", "app-db", "completed", 3*time.Hour),
			aBackup("new", "alpha", "cache", "completed", time.Hour),
			aBackup("other", "alpha", "queue", "completed", 5*time.Hour),
		),
		"beta": backupItems(aBackup("mid", "beta", "app-db", "completed", 2*time.Hour)),
	}, nil, "")
	v, err := runBackupList(context.Background(), req(map[string]any{
		"namespace": []string{"beta", "alpha"}, "cluster": []string{"app-db", "cache"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	if cols := columnNames(tbl); cols[0] != "Namespace" || cols[1] != "Backup" {
		t.Errorf("columns = %v, want the namespace leading over several", cols)
	}
	var names []string
	for _, row := range tbl.Rows {
		names = append(names, row[0]+"/"+row[1])
	}
	if want := []string{"alpha/new", "beta/mid", "alpha/old"}; !slices.Equal(names, want) {
		t.Errorf("rows = %v, want the named clusters' backups, newest first across namespaces", names)
	}
	if got := stub.namespaces(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("namespaces asked = %v, want one call each", got)
	}
	if tbl.Total != 3 {
		t.Errorf("total = %d, want 3", tbl.Total)
	}
}

func TestBackupsOfOneNamespaceInAListAreTheSinglePageItAlwaysWas(t *testing.T) {
	doc := backupItems(
		aBackup("a", "prod", "app-db", "completed", time.Hour),
		aBackup("b", "prod", "app-db", "failed", 2*time.Hour),
	)
	var pages []string
	for _, value := range []any{"prod", []string{"prod"}} {
		servesPerNamespace(t, map[string]any{"prod": doc}, nil, "")
		v, err := runBackupList(context.Background(), req(map[string]any{"namespace": value}))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := view.Marshal(view.Envelope{View: withoutTimes(v)})
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, string(raw))
	}
	if pages[0] != pages[1] {
		t.Errorf("a list of one answers\n%s\nwant what the bare name answers\n%s", pages[1], pages[0])
	}
	if strings.Contains(pages[0], `"Namespace"`) {
		t.Error("one namespace led with a Namespace column")
	}
}

// withoutTimes is v with the values of its time columns left out: two reads
// moments apart value an age a second apart whenever they straddle one, and
// nothing else about the two pages may differ.
func withoutTimes(v view.View) view.View {
	switch t := v.(type) {
	case view.Sections:
		items := make([]view.Section, len(t.Items))
		for i, s := range t.Items {
			s.View = withoutTimes(s.View)
			items[i] = s
		}
		t.Items = items
		return t
	case view.Table:
		values := make([][]string, len(t.Values))
		for i, row := range t.Values {
			kept := append([]string(nil), row...)
			for j, c := range t.Columns {
				if (c.Kind == view.KindDuration || c.Kind == view.KindTimestamp) && j < len(kept) {
					kept[j] = ""
				}
			}
			values[i] = kept
		}
		t.Values = values
		return t
	}
	return v
}

func TestTheBackupLimitCutsTheMergedListAndKeepsTheExactTotal(t *testing.T) {
	servesPerNamespace(t, map[string]any{
		"alpha": backupItems(
			aBackup("a1", "alpha", "app-db", "completed", time.Hour),
			aBackup("a2", "alpha", "app-db", "failed", 9*time.Hour),
		),
		"beta": backupItems(
			aBackup("b1", "beta", "app-db", "completed", 2*time.Hour),
			aBackup("b2", "beta", "app-db", "completed", 3*time.Hour),
		),
	}, nil, "")
	v, err := runBackupList(context.Background(), req(map[string]any{"namespace": []string{"alpha", "beta"}, "limit": 2}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	if len(tbl.Rows) != 2 || tbl.Total != 4 {
		t.Errorf("%d rows, total %d, want 2 of 4", len(tbl.Rows), tbl.Total)
	}
	if tbl.Rows[0][1] != "a2" {
		t.Errorf("first row is %v, want the failed backup ahead of newer ones", tbl.Rows[0])
	}
	if hasCode(tbl.Warnings, "cnpg.backup.list.partial") == nil {
		t.Errorf("warnings = %+v, want the partial one", tbl.Warnings)
	}
}

func TestANamespaceWhoseBackupsCannotBeReadIsNamedAndTheOthersAnswer(t *testing.T) {
	servesPerNamespace(t, map[string]any{
		"alpha": backupItems(aBackup("a1", "alpha", "app-db", "completed", time.Hour)),
	}, map[string]string{"beta": forbiddenBackups("beta")}, "")
	v, err := runBackupList(context.Background(), req(map[string]any{"namespace": []string{"alpha", "beta"}}))
	if err != nil {
		t.Fatalf("one namespace refused made the page an error: %v", err)
	}
	tbl, failed := tableOf(t, v)
	if len(tbl.Rows) != 1 || tbl.Rows[0][1] != "a1" {
		t.Errorf("rows = %v, want the readable namespace's backup", tbl.Rows)
	}
	if failed == nil || len(failed.Rows) != 1 {
		t.Fatalf("failed = %+v, want one row for the namespace that could not be read", failed)
	}
	row := failed.Rows[0]
	if row[0] != "beta" || row[2] != "fail" || !strings.Contains(row[3], "could not be read — not allowed to list backups.postgresql.cnpg.io in namespace beta") {
		t.Errorf("row = %v", row)
	}
	if w := hasCode(tbl.Warnings, "cnpg.namespace.unreadable"); w == nil || !strings.Contains(w.Message, "beta") {
		t.Errorf("warnings = %+v, want one naming beta", tbl.Warnings)
	}
}

func TestAnEmptyBackupListSaysItReadOnlyWhatItCould(t *testing.T) {
	servesPerNamespace(t, nil, map[string]string{"beta": forbiddenBackups("beta")}, "")
	v, err := runBackupList(context.Background(), req(map[string]any{"namespace": []string{"alpha", "beta"}}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, failed := tableOf(t, v)
	if !strings.Contains(tbl.Empty, "no CloudNativePG backups in namespace alpha") || strings.Contains(tbl.Empty, "beta") {
		t.Errorf("empty = %q, want it to name alpha alone, the namespace that answered", tbl.Empty)
	}
	if failed == nil {
		t.Error("the namespace that could not be read has no row")
	}
}

func TestVolumesOfSeveralNamespacesAreOneReadByLabelInEach(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{
		"alpha": volumeItems(aVolume("app-1", "alpha", "app-db", "app-db-1", pvcRoleData, "Bound")),
		"beta":  volumeItems(aVolume("cache-1", "beta", "cache", "cache-1", pvcRoleData, "Bound")),
	}, nil, "")
	v, err := runStorage(context.Background(), req(map[string]any{"namespace": []string{"beta", "alpha"}}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	if cols := columnNames(tbl); cols[0] != "Namespace" || cols[1] != "Cluster" {
		t.Errorf("columns = %v, want the namespace and the cluster leading", cols)
	}
	if len(tbl.Rows) != 2 || tbl.Rows[0][0] != "alpha" || tbl.Rows[1][0] != "beta" {
		t.Errorf("rows = %v, want them by namespace", tbl.Rows)
	}
	for _, call := range stub.calls() {
		if !strings.Contains(call, "-l cnpg.io/cluster ") || strings.Contains(call, "--all-namespaces") {
			t.Errorf("call %q, want the label's existence in one namespace", call)
		}
	}
	if got := stub.namespaces(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("namespaces asked = %v", got)
	}
}

func TestSeveralClustersAreOneSetSelectorAndOneIsStillItsLabelValue(t *testing.T) {
	for _, tc := range []struct {
		clusters any
		want     string
		column   bool
	}{
		{[]string{"cache", "app-db", "cache"}, "-l cnpg.io/cluster in (app-db,cache) ", true},
		{[]string{"app-db"}, "-l cnpg.io/cluster=app-db ", false},
		{"app-db", "-l cnpg.io/cluster=app-db ", false},
	} {
		stub := servesPerNamespace(t, map[string]any{
			"alpha": volumeItems(aVolume("app-1", "alpha", "app-db", "app-db-1", pvcRoleData, "Bound")),
		}, nil, "")
		v, err := runStorage(context.Background(), req(map[string]any{"namespace": "alpha", "cluster": tc.clusters}))
		if err != nil {
			t.Fatal(err)
		}
		if calls := stub.calls(); len(calls) != 1 || !strings.Contains(calls[0], tc.want) {
			t.Errorf("%v: calls = %v, want %q in the one call", tc.clusters, calls, tc.want)
		}
		tbl, _ := tableOf(t, v)
		if has := slices.Contains(columnNames(tbl), "Cluster"); has != tc.column {
			t.Errorf("%v: the Cluster column is there = %v, want %v", tc.clusters, has, tc.column)
		}
	}
}

func TestEveryNamespaceWithSeveralClustersIsOneReadByASetOfLabels(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{
		"all": volumeItems(
			aVolume("app-1", "alpha", "app-db", "app-db-1", pvcRoleData, "Bound"),
			aVolume("cache-1", "beta", "cache", "cache-1", pvcRoleData, "Bound"),
			aVolume("queue-1", "beta", "queue", "queue-1", pvcRoleData, "Bound"),
		),
	}, nil, "")
	// The stub does not select by label, as the API server does, so the rows are
	// not the test's to hold: the selector it was asked with is.
	if _, err := runStorage(context.Background(), req(map[string]any{
		"all-namespaces": true, "cluster": []string{"cache", "app-db"},
	})); err != nil {
		t.Fatal(err)
	}
	calls := stub.calls()
	if len(calls) != 1 || !strings.Contains(calls[0], "-l cnpg.io/cluster in (app-db,cache) ") || !strings.Contains(calls[0], "--all-namespaces") {
		t.Errorf("calls = %v, want one read of every namespace by the set", calls)
	}
}

func TestAnEmptyVolumeReadOfSeveralClustersNamesTheSetItLookedFor(t *testing.T) {
	servesPerNamespace(t, nil, nil, "")
	v, err := runStorage(context.Background(), req(map[string]any{
		"namespace": []string{"alpha", "beta"}, "cluster": []string{"b", "a"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if body := v.(view.Table).Empty; !strings.Contains(body, "no volumes labelled cnpg.io/cluster in (a,b) in namespaces alpha, beta") {
		t.Errorf("body = %q", body)
	}
}

func TestAnEmptyBackupListOfSeveralClustersNamesThemAndPointsAtTheirListing(t *testing.T) {
	servesPerNamespace(t, nil, nil, "")
	for sf, want := range map[plugin.Surface]string{
		plugin.SurfaceCLI: "no backups of a, b in namespace prod — `rta cnpg list --cluster a --cluster b --namespace prod` " +
			"shows where they are and whether anything is configured to take a backup",
		plugin.SurfaceMCP: "no backups of a, b in namespace prod — `cnpg_list {\"cluster\":[\"a\",\"b\"],\"namespace\":[\"prod\"]}` " +
			"shows where they are and whether anything is configured to take a backup",
	} {
		v, err := runBackupList(context.Background(), req(map[string]any{
			"namespace": "prod", "cluster": []string{"b", "a"},
		}).WithSurface(sf))
		if err != nil {
			t.Fatal(err)
		}
		tbl, _ := tableOf(t, v)
		if tbl.Empty != want {
			t.Errorf("%s: empty = %q, want %q", sf, tbl.Empty, want)
		}
	}
}

func TestANamespaceWhoseVolumesCannotBeReadIsAFindingAndTheOthersAnswer(t *testing.T) {
	servesPerNamespace(t, map[string]any{
		"alpha": volumeItems(aVolume("app-1", "alpha", "app-db", "app-db-1", pvcRoleData, "Bound")),
	}, map[string]string{"beta": forbiddenVolumes("beta")}, "")
	v, err := runStorage(context.Background(), req(map[string]any{"namespace": []string{"alpha", "beta"}}))
	if err != nil {
		t.Fatalf("one namespace refused made the page an error: %v", err)
	}
	sections, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("view is %T, want the volumes with a section of findings", v)
	}
	volumes := sections.Items[0].View.(view.Table)
	if len(volumes.Rows) != 1 || volumes.Rows[0][0] != "alpha" {
		t.Errorf("volumes = %v", volumes.Rows)
	}
	problems := sections.Items[1].View.(view.Table)
	if len(problems.Rows) != 1 || problems.Rows[0][0] != "beta" || problems.Rows[0][2] != "fail" ||
		!strings.Contains(problems.Rows[0][3], "could not be read — not allowed to list persistentvolumeclaims in namespace beta") {
		t.Errorf("findings = %v", problems.Rows)
	}
	if w := hasCode(volumes.Warnings, "cnpg.namespace.unreadable"); w == nil || !strings.Contains(w.Message, "beta") {
		t.Errorf("warnings = %+v, want one naming beta", volumes.Warnings)
	}
}
