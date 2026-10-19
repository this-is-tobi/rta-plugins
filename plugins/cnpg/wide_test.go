package main

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The house rule for a read that has a container axis: an optional filter that
// is left out means everything within the connection, the axis (the namespace)
// gets one explicit read-only wide switch with a leading scope column, and a
// call that changes something never gets a wide form.

func capabilityOf(t *testing.T, id string) plugin.Capability {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no capability %s", id)
	return plugin.Capability{}
}

func inputOf(c plugin.Capability, name string) (plugin.Field, bool) {
	for _, f := range c.Inputs {
		if f.Name == name {
			return f, true
		}
	}
	return plugin.Field{}, false
}

// The wide switch is one declaration, so the name, the letter and the help of
// it are one thing, and it is on the reads that have a namespace to widen and on
// nothing that writes.
func TestTheWideSwitchIsOneDeclarationOnTheReadsAndOnNoWrite(t *testing.T) {
	want := allNamespacesField()
	for _, id := range []string{"cnpg.list", "cnpg.backup.list", "cnpg.storage"} {
		got, ok := inputOf(capabilityOf(t, id), "all-namespaces")
		if !ok {
			t.Errorf("%s has no wide form", id)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s declares %+v, want the shared %+v", id, got, want)
		}
	}
	if want.Config != "" {
		t.Error("the wide switch is configurable: a default of every namespace widens every read made through a file nobody is watching")
	}
	for _, c := range Plugin().Capabilities {
		if _, wide := inputOf(c, "all-namespaces"); wide && c.Safety != plugin.Read {
			t.Errorf("%s writes and has a wide form: a grant for it would cover every namespace", c.ID)
		}
	}
	// One cluster's read has no wide form either: it is about one record.
	if _, wide := inputOf(capabilityOf(t, "cnpg.status"), "all-namespaces"); wide {
		t.Error("cnpg.status reads one cluster and has a wide form")
	}
}

// A namespace given beside the wide switch is refused by every capability that
// has the switch, for the same reason the list was: the scope a grant is checked
// against is the namespace's value alone.
func TestEveryWideReadRefusesANamespaceBesideIt(t *testing.T) {
	fakeKubectl(t, `echo '{"items":[]}'`)
	both := map[string]any{"namespace": "prod", "all-namespaces": true}
	for name, run := range map[string]func(context.Context, plugin.Request) (view.View, error){
		"cnpg.list": runList, "cnpg.backup.list": runBackupList, "cnpg.storage": runStorage,
	} {
		_, err := run(context.Background(), req(both))
		if got := asViewError(t, err).Code; got != "cnpg.namespace.ambiguous" {
			t.Errorf("%s: code = %q, want cnpg.namespace.ambiguous", name, got)
		}
	}
}

func aBackup(name, namespace, cluster, phase string, old time.Duration) map[string]any {
	status := map[string]any{"phase": phase}
	if phase == "failed" {
		status["error"] = "while taking " + name + ": exit status 2"
	}
	return map[string]any{
		"metadata": map[string]any{
			"name": name, "namespace": namespace,
			"creationTimestamp": time.Now().Add(-old).UTC().Format(time.RFC3339),
		},
		"spec":   map[string]any{"cluster": map[string]any{"name": cluster}},
		"status": status,
	}
}

func backupItems(items ...map[string]any) map[string]any {
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = it
	}
	return map[string]any{"items": out}
}

// tableOf is the listing's table, whether or not a failure gave it a section
// beside it.
func tableOf(t *testing.T, v view.View) (view.Table, *view.Table) {
	t.Helper()
	switch got := v.(type) {
	case view.Table:
		return got, nil
	case view.Sections:
		main := got.Items[0].View.(view.Table)
		var failed *view.Table
		for _, s := range got.Items {
			if s.Title == "What failed" {
				f := s.View.(view.Table)
				failed = &f
			}
		}
		return main, failed
	}
	t.Fatalf("view is %T", v)
	return view.Table{}, nil
}

func columnNames(tbl view.Table) []string {
	var out []string
	for _, c := range tbl.Columns {
		out = append(out, c.Name)
	}
	return out
}

// The wide read asks for every namespace and leads with the one it came from;
// the ordinary read has no such column, because in one namespace it would say
// the same thing on every row.
func TestTheBackupListLeadsWithTheNamespaceOnlyWhenItReadsEveryOne(t *testing.T) {
	doc := backupItems(
		aBackup("shop-1", "prod", "shop", "completed", time.Hour),
		aBackup("shop-1", "staging", "shop", "completed", 2*time.Hour),
	)
	argv := servesWithArgs(t, doc)
	v, err := runBackupList(context.Background(), req(map[string]any{"all-namespaces": true}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	if cols := columnNames(tbl); cols[0] != "Namespace" || cols[1] != "Backup" {
		t.Errorf("columns = %v, want Namespace first", cols)
	}
	if tbl.Rows[0][0] != "prod" || tbl.Rows[1][0] != "staging" {
		t.Errorf("rows = %v, want the namespace each backup came from", tbl.Rows)
	}
	if !strings.Contains(argv(), "--all-namespaces") {
		t.Errorf("kubectl was asked %q, want every namespace", argv())
	}

	servesWithArgs(t, doc)
	v, err = runBackupList(context.Background(), req(map[string]any{"namespace": "prod"}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ = tableOf(t, v)
	if cols := columnNames(tbl); slices.Contains(cols, "Namespace") {
		t.Errorf("columns = %v, the namespace is the question that was asked", cols)
	}
}

// A listing that is cut says so, and says how many there are.
//
// The total is the exact count — it is what a reader needs to know how much of
// the whole they hold — and the warning is the part that cannot be missed: a
// shorter list reads as the whole of a smaller thing.
func TestACutBackupListSaysItIsPartialAndKeepsTheExactTotal(t *testing.T) {
	var items []map[string]any
	for i := 0; i < 5; i++ {
		items = append(items, aBackup(fmt.Sprintf("shop-%d", i), "prod", "shop", "completed", time.Duration(i+1)*time.Hour))
	}
	for _, tc := range []struct {
		sf   plugin.Surface
		hint string
	}{
		{plugin.SurfaceCLI, "raise --limit, or narrow the list with --cluster or --namespace"},
		{plugin.SurfaceMCP, `raise the "limit" argument, or narrow the list with the "cluster" argument or the "namespace" argument`},
	} {
		serves(t, backupItems(items...))
		v, err := runBackupList(context.Background(), req(map[string]any{"limit": 3}).WithSurface(tc.sf))
		if err != nil {
			t.Fatal(err)
		}
		tbl, _ := tableOf(t, v)
		if len(tbl.Rows) != 3 || tbl.Total != 5 {
			t.Errorf("%s: %d rows, total %d, want 3 of 5", tc.sf, len(tbl.Rows), tbl.Total)
		}
		if len(tbl.Warnings) != 1 {
			t.Fatalf("%s: warnings = %v, want one saying the list is partial", tc.sf, tbl.Warnings)
		}
		w := tbl.Warnings[0]
		if w.Code != "cnpg.backup.list.partial" || w.Message != "showing 3 of 5 backups, the failed ones first and then the newest" {
			t.Errorf("%s: warning = %q %q", tc.sf, w.Code, w.Message)
		}
		if w.Hint != tc.hint {
			t.Errorf("%s: hint = %q, want %q", tc.sf, w.Hint, tc.hint)
		}
	}

	// And an uncut list carries none, with the total it always had.
	serves(t, backupItems(items...))
	v, err := runBackupList(context.Background(), req(map[string]any{"limit": 5}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	if len(tbl.Warnings) != 0 || tbl.Total != 5 || len(tbl.Rows) != 5 {
		t.Errorf("a list that fits: %d rows, total %d, warnings %v", len(tbl.Rows), tbl.Total, tbl.Warnings)
	}
	if tbl.Rows[0][0] != "shop-0" {
		t.Errorf("first row is %q, want the newest", tbl.Rows[0][0])
	}
}

// **The cut must not drop the rows the listing is opened to find.** Newest
// first is right for a list that fits and wrong for one that is cut: a failure
// from last week sits below a hundred successes from this one.
func TestACutBackupListKeepsTheFailedOnesAndPutsThemFirst(t *testing.T) {
	items := []map[string]any{aBackup("old-failure", "prod", "shop", "failed", 200*time.Hour)}
	for i := 0; i < 8; i++ {
		items = append(items, aBackup(fmt.Sprintf("ok-%d", i), "prod", "shop", "completed", time.Duration(i+1)*time.Hour))
	}
	items = append(items, aBackup("older-failure", "prod", "shop", "failed", 300*time.Hour))
	serves(t, backupItems(items...))

	v, err := runBackupList(context.Background(), req(map[string]any{"limit": 4}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, failed := tableOf(t, v)
	var names []string
	for _, row := range tbl.Rows {
		names = append(names, row[0])
	}
	want := []string{"old-failure", "older-failure", "ok-0", "ok-1"}
	if !slices.Equal(names, want) {
		t.Errorf("rows = %v, want the failed ones first, each group newest first: %v", names, want)
	}
	if failed == nil || len(failed.Rows) != 2 {
		t.Errorf("the failures have no section of their own: %v", failed)
	}
	if tbl.Total != 10 {
		t.Errorf("total = %d, want every backup there is", tbl.Total)
	}
}

// More failures than the limit allows is still a cut: the newest of them, and
// the warning.
func TestAListOfOnlyFailuresIsCutToTheNewestOfThem(t *testing.T) {
	var items []map[string]any
	for i := 0; i < 4; i++ {
		items = append(items, aBackup(fmt.Sprintf("bad-%d", i), "prod", "shop", "failed", time.Duration(i+1)*time.Hour))
	}
	serves(t, backupItems(items...))
	v, err := runBackupList(context.Background(), req(map[string]any{"limit": 2}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	if len(tbl.Rows) != 2 || tbl.Rows[0][0] != "bad-0" || tbl.Rows[1][0] != "bad-1" || len(tbl.Warnings) != 1 {
		t.Errorf("rows = %v warnings = %v", tbl.Rows, tbl.Warnings)
	}
}

// The limit is bounded by default and from the config, and the default is what
// a call that says nothing gets — through the host's own resolution, which is
// the path a real call takes.
func TestTheBackupLimitHasADefaultAndAConfigKey(t *testing.T) {
	c := capabilityOf(t, "cnpg.backup.list")
	if got := plugin.Resolve(c, plugin.Inputs{})["limit"]; got != backupListLimit {
		t.Errorf("default limit = %v, want %d", got, backupListLimit)
	}
	if got := plugin.Resolve(c, plugin.Inputs{Config: map[string]any{"limit": 7}})["limit"]; got != 7 {
		t.Errorf("a configured limit resolved to %v, want 7", got)
	}

	var items []map[string]any
	for i := 0; i < backupListLimit+20; i++ {
		items = append(items, aBackup(fmt.Sprintf("b-%03d", i), "prod", "shop", "completed", time.Duration(i+1)*time.Minute))
	}
	serves(t, backupItems(items...))
	v, err := runBackupList(context.Background(), req(nil))
	if err != nil {
		t.Fatal(err)
	}
	if tbl, _ := tableOf(t, v); len(tbl.Rows) != backupListLimit || tbl.Total != backupListLimit+20 {
		t.Errorf("%d rows, total %d, want the default limit of %d rows of %d", len(tbl.Rows), tbl.Total, backupListLimit, backupListLimit+20)
	}
}

// The failures section names the namespace in the wide read too: two backups
// can share a name in two namespaces, and an error under a name that may mean
// either is not one anybody can act on.
func TestTheWideFailureSectionNamesTheNamespace(t *testing.T) {
	serves(t, backupItems(
		aBackup("shop-1", "prod", "shop", "failed", time.Hour),
		aBackup("shop-1", "staging", "shop", "failed", 2*time.Hour),
	))
	v, err := runBackupList(context.Background(), req(map[string]any{"all-namespaces": true}))
	if err != nil {
		t.Fatal(err)
	}
	_, failed := tableOf(t, v)
	if failed == nil || failed.Columns[0].Name != "Namespace" || failed.Rows[0][0] != "prod" || failed.Rows[1][0] != "staging" {
		t.Errorf("failures = %+v", failed)
	}
}

// What an empty wide listing offers: there is no one place to ask whether a
// cluster is configured, so it names the listing of clusters.
func TestAnEmptyWideBackupListOffersTheListingOfClusters(t *testing.T) {
	serves(t, backupItems())
	v, err := runBackupList(context.Background(),
		req(map[string]any{"all-namespaces": true, "cluster": "shop"}).WithSurface(plugin.SurfaceCLI))
	if err != nil {
		t.Fatal(err)
	}
	body := v.(view.Table).Empty
	if !strings.Contains(body, "no backups of shop in every namespace") ||
		!strings.Contains(body, "`rta cnpg list --all-namespaces` shows where a cluster of that name is") {
		t.Errorf("body = %q", body)
	}
}

func aVolume(name, namespace, cluster, instance, role, phase string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"name": name, "namespace": namespace,
			"labels": map[string]any{
				pvcSelector: cluster, pvcInstanceLabel: instance, pvcRoleLabel: role,
			},
		},
		"spec": map[string]any{
			"storageClassName": "fast",
			"resources":        map[string]any{"requests": map[string]any{"storage": "10Gi"}},
		},
		"status": map[string]any{"phase": phase, "capacity": map[string]any{"storage": "10Gi"}},
	}
}

func volumeItems(items ...map[string]any) map[string]any { return backupItems(items...) }

// The volumes of a cluster, and of every cluster: the selector is the label
// with a value for the one and the label alone for the other, so it is still
// CloudNativePG's own volumes and never the rest of a namespace.
func TestStorageWithNoClusterReadsEveryClustersVolumes(t *testing.T) {
	doc := volumeItems(
		aVolume("shop-1", "prod", "shop", "shop-1", pvcRoleData, "Bound"),
		aVolume("blog-1", "staging", "blog", "blog-1", pvcRoleData, "Bound"),
	)

	argv := servesWithArgs(t, doc)
	v, err := runStorage(context.Background(), req(map[string]any{"cluster": "shop", "namespace": "prod"}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	if slices.Contains(columnNames(tbl), "Cluster") {
		t.Errorf("columns = %v: a named cluster says the same thing on every row", columnNames(tbl))
	}
	if !strings.Contains(argv(), "-l cnpg.io/cluster=shop") {
		t.Errorf("argv = %q, want the one cluster's label value", argv())
	}

	argv = servesWithArgs(t, doc)
	v, err = runStorage(context.Background(), req(map[string]any{"all-namespaces": true}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ = tableOf(t, v)
	if cols := columnNames(tbl); cols[0] != "Namespace" || cols[1] != "Cluster" || cols[2] != "Volume" {
		t.Errorf("columns = %v, want the scope leading: namespace, then cluster", cols)
	}
	if tbl.Rows[0][0] != "prod" || tbl.Rows[0][1] != "shop" || tbl.Rows[1][0] != "staging" || tbl.Rows[1][1] != "blog" {
		t.Errorf("rows = %v, want them by namespace and cluster", tbl.Rows)
	}
	if a := argv(); !strings.Contains(a, "-l cnpg.io/cluster ") || strings.Contains(a, "cnpg.io/cluster=") || !strings.Contains(a, "--all-namespaces") {
		t.Errorf("argv = %q, want the label's existence over every namespace", a)
	}

	servesWithArgs(t, doc)
	v, err = runStorage(context.Background(), req(map[string]any{"namespace": "prod"}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ = tableOf(t, v)
	if cols := columnNames(tbl); cols[0] != "Cluster" || slices.Contains(cols, "Namespace") {
		t.Errorf("columns = %v, want the cluster and no namespace in one namespace", cols)
	}
}

func TestStorageProblemsNameTheNamespaceInTheWideRead(t *testing.T) {
	serves(t, volumeItems(
		aVolume("shop-1", "prod", "shop", "shop-1", pvcRoleData, "Pending"),
		aVolume("shop-1", "staging", "shop", "shop-1", pvcRoleData, "Pending"),
	))
	v, err := runStorage(context.Background(), req(map[string]any{"all-namespaces": true}))
	if err != nil {
		t.Fatal(err)
	}
	sections := v.(view.Sections)
	problems := sections.Items[1].View.(view.Table)
	if problems.Columns[0].Name != "Namespace" || problems.Rows[0][0] != "prod" || problems.Rows[1][0] != "staging" {
		t.Errorf("problems = %+v", problems)
	}
}

// An empty call to the volume read states what it looked for.
func TestAnEmptyVolumeReadSaysWhatItLookedFor(t *testing.T) {
	serves(t, volumeItems())
	for name, tc := range map[string]struct {
		values map[string]any
		want   string
	}{
		"named":   {map[string]any{"cluster": "shop", "namespace": "prod"}, "no volumes labelled cnpg.io/cluster=shop in namespace prod"},
		"unnamed": {map[string]any{"all-namespaces": true}, "no volumes labelled cnpg.io/cluster in every namespace"},
	} {
		v, err := runStorage(context.Background(), req(tc.values))
		if err != nil {
			t.Fatal(err)
		}
		if body := v.(view.Table).Empty; !strings.Contains(body, tc.want) {
			t.Errorf("%s: body = %q, want %q", name, body, tc.want)
		}
	}
}

// An empty answer says which part of the cluster it read.
//
// A call that names no namespace reads the context's own, and "no clusters in
// the current context" told somebody whose clusters live in another namespace
// that there were none — with no word about the switch that reads the rest.
func TestAnEmptyClusterListSaysWhichNamespaceItReadAndHowToReadTheRest(t *testing.T) {
	serves(t, clusterList{})
	for _, tc := range []struct {
		name   string
		values map[string]any
		sf     plugin.Surface
		want   []string
		not    string
	}{
		{"no namespace", map[string]any{}, plugin.SurfaceCLI,
			[]string{"no CloudNativePG clusters in the context's own namespace", "`rta cnpg list --all-namespaces` reads every namespace"}, ""},
		{"a context and no namespace", map[string]any{"context": "kind"}, plugin.SurfaceCLI,
			[]string{"in context kind, the context's own namespace", "`rta cnpg list --all-namespaces --context kind` reads every namespace"}, ""},
		{"a namespace", map[string]any{"namespace": "prod"}, plugin.SurfaceCLI,
			[]string{"no CloudNativePG clusters in namespace prod", "reads every namespace"}, ""},
		{"an agent", map[string]any{}, plugin.SurfaceMCP,
			[]string{"the `cnpg_list` tool with the \"all-namespaces\" argument reads every namespace"}, "rta cnpg"},
		{"every namespace", map[string]any{"all-namespaces": true}, plugin.SurfaceCLI,
			[]string{"no CloudNativePG clusters in every namespace"}, "reads every namespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := runList(context.Background(), req(tc.values).WithSurface(tc.sf))
			if err != nil {
				t.Fatal(err)
			}
			body := v.(view.Table).Empty
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("body = %q, want %q in it", body, want)
				}
			}
			if tc.not != "" && strings.Contains(body, tc.not) {
				t.Errorf("body = %q, must not carry %q", body, tc.not)
			}
		})
	}
}

// A refused read of every namespace says what that read needs.
//
// A ServiceAccount minted for one namespace holds a Role, and a Role cannot
// list across the cluster however much it reads inside its own namespace. The
// refusal said only that the cluster scope was refused, which reads as a
// verdict on the resource.
func TestARefusedReadOfEveryNamespaceSaysItNeedsAClusterWideRight(t *testing.T) {
	const stderr = `Error from server (Forbidden): clusters.postgresql.cnpg.io is forbidden: User "system:serviceaccount:ci:reader" ` +
		`cannot list resource "clusters" in API group "postgresql.cnpg.io" at the cluster scope`
	wide := []string{"get", clusterCRD, "-o", "json", "--all-namespaces"}
	for _, tc := range []struct {
		sf   plugin.Surface
		want []string
	}{
		{plugin.SurfaceCLI, []string{"needs the right to list cluster-wide", "ClusterRoleBinding",
			"namespaced Role", "Name one namespace with --namespace instead of using --all-namespaces"}},
		{plugin.SurfaceMCP, []string{"needs the right to list cluster-wide",
			`Name one namespace with the "namespace" argument instead of using the "all-namespaces" argument`}},
	} {
		s, verr := selectionOf(req(nil).WithSurface(tc.sf))
		if verr != nil {
			t.Fatal(verr)
		}
		got := classifyWith(t, s, stderr, wide...)
		if got.Message != "not allowed to list clusters.postgresql.cnpg.io in every namespace" {
			t.Errorf("%s: message = %q", tc.sf, got.Message)
		}
		for _, want := range tc.want {
			if !strings.Contains(got.Hint, want) {
				t.Errorf("%s: hint = %q, want %q in it", tc.sf, got.Hint, want)
			}
		}
	}

	// The same refusal of a read of one namespace does not say it.
	one := classifyAs(t, stderr, "get", clusterCRD, "-o", "json", "--namespace=prod")
	if strings.Contains(one.Hint, "cluster-wide") {
		t.Errorf("a read of one namespace was told it needs a cluster-wide right: %q", one.Hint)
	}
}

// What the declaration says an empty cluster does is what the host does with
// one: a value the caller gave wins over the profile and the config, an empty
// string included, which is how one call lifts a pinned cluster.
func TestAnEmptyClusterLiftsAPinAndTheHelpSaysSo(t *testing.T) {
	pinned := plugin.Inputs{
		Profile:     map[string]any{"cluster": "shop-prod"},
		ProfileName: "staging",
		Config:      map[string]any{"cluster": "from-config"},
	}
	for _, id := range []string{"cnpg.status", "cnpg.storage", "cnpg.backup.list", "cnpg.backup.request"} {
		c := capabilityOf(t, id)
		field, _ := inputOf(c, "cluster")

		// What a call that says nothing gets: the profile where one may fill
		// the input, the config where it may not (the scope is never a
		// profile's to fill).
		want := "shop-prod"
		if c.Scope == "cluster" {
			want = "from-config"
		}
		if got := plugin.Resolve(c, pinned)["cluster"]; got != want {
			t.Errorf("%s: a call that names no cluster resolves to %v, want the pin %q", id, got, want)
		}

		// The reads that take a list lift it with an empty one, which reads as no
		// cluster at all.
		lift := pinned
		lifted, says := any(""), "empty lifts one a profile or the config pins"
		if field.Type == plugin.StringSlice {
			lifted, says = []string{}, "empty lifts the ones a profile or the config pins"
		}
		lift.Caller = map[string]any{"cluster": lifted}
		if got := plugin.Resolve(c, lift)["cluster"]; !reflect.DeepEqual(got, lifted) {
			t.Errorf("%s: an empty cluster resolved to %v, want it to lift the pin", id, got)
		}
		if !strings.Contains(field.Help, says) {
			t.Errorf("%s: help = %q does not say an empty value lifts a pin", id, field.Help)
		}
	}
}

// An empty cluster where a cluster is required is refused as missing, which is
// what lifting the pin of a call that cannot go on without one comes to.
func TestAnEmptyClusterWhereOneIsRequiredIsRefusedAsMissing(t *testing.T) {
	for _, id := range []string{"cnpg.status", "cnpg.backup.request"} {
		c := capabilityOf(t, id)
		field, _ := inputOf(c, "cluster")
		if !field.Required {
			t.Errorf("%s: cluster is not required", id)
		}
		values := plugin.Resolve(c, plugin.Inputs{Caller: map[string]any{"cluster": ""}})
		var missing []string
		for _, f := range plugin.Missing(c, values, plugin.SurfaceCLI) {
			missing = append(missing, f.Name)
		}
		if !slices.Contains(missing, "cluster") {
			t.Errorf("%s: missing = %v, want cluster", id, missing)
		}
	}
	for _, id := range []string{"cnpg.storage", "cnpg.backup.list"} {
		if field, _ := inputOf(capabilityOf(t, id), "cluster"); field.Required {
			t.Errorf("%s: cluster is required, so a read of every cluster cannot be asked for", id)
		}
	}
}

// where() says which part of the cluster was read, since a message built on it
// is the whole of what an empty answer tells somebody.
func TestWhereNamesTheNamespaceAndNeverJustTheContext(t *testing.T) {
	for _, tc := range []struct {
		s    selection
		want string
	}{
		{selection{}, "the context's own namespace"},
		{selection{namespace: "prod"}, "namespace prod"},
		{selection{allNamespace: true}, "every namespace"},
		{selection{context: "kind"}, "context kind, the context's own namespace"},
		{selection{context: "kind", namespace: "prod"}, "context kind, namespace prod"},
		{selection{context: "kind", allNamespace: true}, "context kind, every namespace"},
	} {
		if got := tc.s.where(); got != tc.want {
			t.Errorf("%+v says %q, want %q", tc.s, got, tc.want)
		}
	}
}
