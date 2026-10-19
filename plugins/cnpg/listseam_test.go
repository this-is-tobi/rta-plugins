package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The three listings as the host calls them: with the lists a fence holds, with
// a name as it was written, and with a call that is cut short.

var theListings = map[string]func(context.Context, plugin.Request) (view.View, error){
	"cnpg.list": runList, "cnpg.backup.list": runBackupList, "cnpg.storage": runStorage,
}

// fenceOf is a profile's `only:` entries, input by input.
func fenceOf(entries map[string][]string) plugin.Fence {
	fence := plugin.Fence{}
	for input, names := range entries {
		for _, name := range names {
			fence[input] = append(fence[input], name)
		}
	}
	return fence
}

// A name is used as the host held it to the fence: the host compares the string
// it was given, so a name trimmed here would be one nothing compared, and
// padding is a name that is in no fence entry.
func TestANameIsUsedExactlyAsTheHostCheckedItAgainstTheFence(t *testing.T) {
	stub := servesPerNamespace(t, nil, nil, "")
	for _, values := range []map[string]any{
		{"namespace": []string{"alpha", " beta"}},
		{"namespace": []string{"alpha "}},
		{"cluster": []string{"app-db", " cache"}},
		{"namespace": []string{" "}},
	} {
		for name, run := range theListings {
			_, err := run(context.Background(), req(values))
			if got := asViewError(t, err).Code; got != "cnpg.name.invalid" {
				t.Errorf("%s %v: code = %q, want cnpg.name.invalid", name, values, got)
			}
		}
	}
	if calls := stub.calls(); len(calls) != 0 {
		t.Errorf("a padded name still asked the cluster: %v", calls)
	}
}

// What a host with the list-fence change resolves a blank list input to under a
// multi-value fence is the whole fence. The handler gets it as any caller's
// list, and a host without the change refuses the blank before it gets here, so
// this holds on both: the lists below are the fence, called by name.
func TestTheWholeFenceAHostFillsInIsReadLikeAnyListTheCallerNamed(t *testing.T) {
	fenceNamespaces := []string{"alpha", "beta", "gamma"}
	fenceClusters := []string{"app-db", "cache"}
	fence := fenceOf(map[string][]string{"namespace": fenceNamespaces, "cluster": fenceClusters})
	call := map[string]any{"namespace": fenceNamespaces, "cluster": fenceClusters}

	clusters := map[string]any{
		"alpha": backupItems(aCluster("app-db", "alpha"), aCluster("cache", "alpha"), aCluster("queue", "alpha")),
		"beta":  backupItems(aCluster("app-db", "beta"), aCluster("search", "beta")),
		"gamma": backupItems(aCluster("cache", "gamma")),
	}
	backups := map[string]any{
		"alpha": backupItems(
			aBackup("a-app", "alpha", "app-db", "completed", time.Hour),
			aBackup("a-cache", "alpha", "cache", "completed", 2*time.Hour),
			aBackup("a-queue", "alpha", "queue", "completed", 3*time.Hour),
		),
		"beta":  backupItems(aBackup("b-app", "beta", "app-db", "completed", 4*time.Hour), aBackup("b-search", "beta", "search", "completed", 5*time.Hour)),
		"gamma": backupItems(aBackup("g-cache", "gamma", "cache", "completed", 6*time.Hour)),
	}
	volumes := map[string]any{
		"alpha": volumeItems(aVolume("app-1", "alpha", "app-db", "app-db-1", pvcRoleData, "Bound")),
		"beta":  volumeItems(aVolume("cache-1", "beta", "cache", "cache-1", pvcRoleData, "Bound")),
		"gamma": volumeItems(aVolume("cache-2", "gamma", "cache", "cache-2", pvcRoleData, "Bound")),
	}

	for _, tc := range []struct {
		id      string
		answers map[string]any
		// seen is the names of what the page holds, in page order; nil where the stub cannot select as the
		// API server does (a label selector) and the calls are what is held.
		seen func(*testing.T, view.View) []string
		want []string
		// selector is what every call must carry.
		selector string
	}{
		{"cnpg.list", clusters, func(_ *testing.T, v view.View) []string { return firstCells(v.(view.Table)) },
			[]string{"alpha/app-db", "alpha/cache", "beta/app-db", "gamma/cache"}, ""},
		{"cnpg.backup.list", backups, func(t *testing.T, v view.View) []string {
			tbl, _ := tableOf(t, v)
			var out []string
			for _, row := range tbl.Rows {
				out = append(out, row[1])
			}
			return out
		}, []string{"a-app", "a-cache", "b-app", "g-cache"}, ""},
		{"cnpg.storage", volumes, nil, nil, "-l cnpg.io/cluster in (app-db,cache) "},
	} {
		t.Run(tc.id, func(t *testing.T) {
			c := capabilityOf(t, tc.id)
			if breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: call}); breach != nil {
				t.Fatalf("every element is in the fence and the call was refused: %+v", breach)
			}
			stub := servesPerNamespace(t, tc.answers, nil, "")
			v, err := theListings[tc.id](context.Background(), req(call))
			if err != nil {
				t.Fatal(err)
			}
			if got := stub.namespaces(); !slices.Equal(got, fenceNamespaces) {
				t.Errorf("namespaces asked = %v, want one call for each the fence holds", got)
			}
			for _, line := range stub.calls() {
				if strings.Contains(line, "--all-namespaces") || strings.Contains(line, "--field-selector") {
					t.Errorf("call %q reads across namespaces or asks the server for one name, want the namespace's own list", line)
				}
				if tc.selector != "" && !strings.Contains(line, tc.selector) {
					t.Errorf("call %q, want %q", line, tc.selector)
				}
			}
			if tc.seen != nil {
				if got := tc.seen(t, v); !slices.Equal(got, tc.want) {
					t.Errorf("page = %v, want %v: the clusters of the fence in the namespaces of the fence, and no other", got, tc.want)
				}
			}
		})
	}
}

// A fence longer than the cap is held element by element like any other, and
// the listing, which reads at most thirty, refuses what it is handed.
func TestAFenceOverTheCapPassesTheHostAndIsRefusedByTheListing(t *testing.T) {
	over := namespacesNamed(maxNamespaces + 1)
	stub := servesPerNamespace(t, nil, nil, "")
	for input, code := range map[string]string{"namespace": "cnpg.namespaces.toomany", "cluster": "cnpg.clusters.toomany"} {
		call := map[string]any{input: over}
		for id, run := range theListings {
			if breach := plugin.CheckFence(capabilityOf(t, id), plugin.Inputs{Fence: fenceOf(map[string][]string{input: over}), Caller: call}); breach != nil {
				t.Errorf("%s %s: every element is in the fence and the call was refused: %+v", id, input, breach)
			}
			_, err := run(context.Background(), req(call).WithSurface(plugin.SurfaceMCP))
			verr := asViewError(t, err)
			if verr.Code != code {
				t.Errorf("%s %s: code = %q, want %s", id, input, verr.Code, code)
			}
			if strings.Contains(verr.Message+verr.Hint, strconv.Itoa(len(over))) {
				t.Errorf("%s %s: %q says the size of the list", id, input, verr.Message+" "+verr.Hint)
			}
		}
	}
	if calls := stub.calls(); len(calls) != 0 {
		t.Errorf("a refused list still asked the cluster: %v", calls)
	}
}

// The read itself fills the listing the page is made of: a closure that returned
// the value before the read had filled it would give an empty page and no error.
func TestEveryListingHoldsWhatItReadWhetherItReadOneNamespaceOrSeveral(t *testing.T) {
	for _, namespaces := range [][]string{{"alpha"}, {"alpha", "beta"}} {
		for _, tc := range []struct {
			id      string
			answers func(ns string) any
			rows    func(*testing.T, view.View) int
		}{
			{"cnpg.list", func(ns string) any { return backupItems(aCluster("app-db", ns), aCluster("cache", ns)) },
				func(_ *testing.T, v view.View) int { return len(v.(view.Table).Rows) }},
			{"cnpg.backup.list", func(ns string) any {
				return backupItems(aBackup("one", ns, "app-db", "completed", time.Hour), aBackup("two", ns, "app-db", "completed", 2*time.Hour))
			}, func(t *testing.T, v view.View) int { tbl, _ := tableOf(t, v); return len(tbl.Rows) }},
			{"cnpg.storage", func(ns string) any {
				return volumeItems(aVolume("one", ns, "app-db", "app-db-1", pvcRoleData, "Bound"), aVolume("two", ns, "app-db", "app-db-2", pvcRoleData, "Bound"))
			}, func(t *testing.T, v view.View) int { tbl, _ := tableOf(t, v); return len(tbl.Rows) }},
		} {
			answers := map[string]any{}
			for _, ns := range namespaces {
				answers[ns] = tc.answers(ns)
			}
			servesPerNamespace(t, answers, nil, "")
			v, err := theListings[tc.id](context.Background(), req(map[string]any{"namespace": namespaces}))
			if err != nil {
				t.Fatalf("%s %v: %v", tc.id, namespaces, err)
			}
			if got, want := tc.rows(t, v), 2*len(namespaces); got != want {
				t.Errorf("%s %v: %d rows, want the %d read", tc.id, namespaces, got, want)
			}
		}
	}
}

func TestFetchReturnsWhatTheReadFilledAndTheErrorOfTheRead(t *testing.T) {
	servesPerNamespace(t, map[string]any{"alpha": backupItems(aCluster("app-db", "alpha"))}, map[string]string{"beta": forbiddenClusters("beta")}, "")
	got, verr := fetch[clusterList](context.Background(), selection{namespace: "alpha"}, clusterCRD, "")
	if verr != nil || len(got.Items) != 1 || got.Items[0].Metadata.Name != "app-db" {
		t.Errorf("fetch = %+v, %v, want the one cluster read", got, verr)
	}
	if _, verr := fetch[clusterList](context.Background(), selection{namespace: "beta"}, clusterCRD, ""); verr == nil || verr.Code != "cnpg.forbidden" {
		t.Errorf("error = %v, want cnpg.forbidden", verr)
	}
}

// The total is how many clusters there are, and the rows that say what could not
// be read or what was not found are not clusters.
func TestTheTotalOfTheClusterListCountsClustersAndNotTheRowsThatSayWhatWasNotRead(t *testing.T) {
	servesPerNamespace(t, map[string]any{"alpha": backupItems(aCluster("app-db", "alpha"))},
		map[string]string{"beta": forbiddenClusters("beta")}, "")
	v, err := runList(context.Background(), req(map[string]any{
		"namespace": []string{"alpha", "beta"}, "cluster": []string{"app-db", "ghost"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	if got, want := firstCells(tbl), []string{"alpha/app-db", "beta/—", "ghost"}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want the cluster, the namespace not read and the name not found", got)
	}
	if tbl.Total != 1 {
		t.Errorf("total = %d, want 1: one cluster, and two rows that say what is missing", tbl.Total)
	}

	servesPerNamespace(t, nil, map[string]string{"alpha": forbiddenClusters("alpha"), "beta": forbiddenClusters("beta")}, "")
	v, err = runList(context.Background(), req(map[string]any{"namespace": []string{"alpha", "beta"}}))
	if err != nil {
		t.Fatal(err)
	}
	if tbl := v.(view.Table); len(tbl.Rows) != 2 || tbl.Total != 0 {
		t.Errorf("rows = %d, total = %d, want two rows saying nothing was read and no cluster counted", len(tbl.Rows), tbl.Total)
	}
}

// Several clusters over several namespaces that all answered with no backups: the
// next call is the one that reads where the cluster is, over the namespaces that
// were read. cnpg.status takes one namespace and none of them is the right one.
func TestAnEmptyBackupListOfOneClusterOverSeveralNamespacesCarriesTheNamespacesRead(t *testing.T) {
	servesPerNamespace(t, nil, nil, "")
	for sf, want := range map[plugin.Surface]string{
		plugin.SurfaceCLI: "no backups of app-db in namespaces alpha, beta — " +
			"`rta cnpg list --cluster app-db --namespace alpha --namespace beta` shows where it is and whether anything is configured to take a backup",
		plugin.SurfaceMCP: "no backups of app-db in namespaces alpha, beta — " +
			"`cnpg_list {\"cluster\":[\"app-db\"],\"namespace\":[\"alpha\",\"beta\"]}` shows where it is and whether anything is configured to take a backup",
	} {
		v, err := runBackupList(context.Background(), req(map[string]any{
			"namespace": []string{"beta", "alpha"}, "cluster": []string{"app-db"},
		}).WithSurface(sf))
		if err != nil {
			t.Fatal(err)
		}
		if tbl, _ := tableOf(t, v); tbl.Empty != want {
			t.Errorf("%s: empty = %q, want %q", sf, tbl.Empty, want)
		}
	}
}

func TestAnEmptyBackupListCarriesTheProfileAndTheNamespacesItReadAndNoOther(t *testing.T) {
	servesPerNamespace(t, nil, map[string]string{"gamma": forbiddenBackups("gamma")}, "")
	v, err := runBackupList(context.Background(), req(map[string]any{
		"namespace": []string{"alpha", "beta", "gamma"}, "cluster": []string{"app-db"},
	}).WithProfile("staging", plugin.TunnelNone))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	want := "no backups of app-db in namespaces alpha, beta — " +
		"`rta cnpg list --cluster app-db --namespace alpha --namespace beta --profile staging` " +
		"shows where it is and whether anything is configured to take a backup"
	if tbl.Empty != want {
		t.Errorf("empty = %q, want %q: gamma was not read, so it is neither claimed nor asked about", tbl.Empty, want)
	}
}

func TestAnEmptyBackupListWhoseOtherNamespacesCouldNotBeReadIsAboutTheOneThatWas(t *testing.T) {
	servesPerNamespace(t, nil, map[string]string{"beta": forbiddenBackups("beta")}, "")
	v, err := runBackupList(context.Background(), req(map[string]any{
		"namespace": []string{"alpha", "beta"}, "cluster": []string{"app-db"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	want := "no backups of app-db in namespace alpha — `rta cnpg status --cluster app-db --namespace alpha` says whether anything is configured to take one"
	if tbl.Empty != want {
		t.Errorf("empty = %q, want %q", tbl.Empty, want)
	}
}

func TestAnEmptyBackupListOverManyNamespacesSaysHowManyWereReadAndAsksAboutThose(t *testing.T) {
	servesPerNamespace(t, nil, nil, "")
	six := namespacesNamed(6)
	v, err := runBackupList(context.Background(), req(map[string]any{"namespace": six, "cluster": []string{"app-db"}}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	if !strings.Contains(tbl.Empty, "no backups of app-db in the 6 namespaces read — ") || strings.Contains(tbl.Empty, "ns-00,") {
		t.Errorf("empty = %q, want the count of the namespaces read and no list of them", tbl.Empty)
	}
	if call := "--namespace " + strings.Join(six, " --namespace "); !strings.Contains(tbl.Empty, call) {
		t.Errorf("empty = %q, want the call to carry every namespace it read: %s", tbl.Empty, call)
	}
}

func TestAnEmptyBackupListOfSeveralClustersInOneNamespaceCarriesTheNamespace(t *testing.T) {
	servesPerNamespace(t, nil, nil, "")
	v, err := runBackupList(context.Background(), req(map[string]any{"namespace": "prod", "cluster": []string{"b", "a"}}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := tableOf(t, v)
	want := "no backups of a, b in namespace prod — `rta cnpg list --cluster a --cluster b --namespace prod` shows where they are and whether anything is configured to take a backup"
	if tbl.Empty != want {
		t.Errorf("empty = %q, want %q", tbl.Empty, want)
	}
}

// A call that is cut short.

// The context is cancelled once three of six namespaces have answered and the
// other three are still waiting for the cluster: the three are on the page, and
// the other three are rows that name them. The page is not an error, because
// part of it was read. Six are no more than run at once, so every one is asked
// and none waits for a place behind the ones that wait for the cluster.
func TestACallCancelledPartwayKeepsWhatAnsweredAndNamesTheRest(t *testing.T) {
	dir := t.TempDir()
	answering := []string{"ns-00", "ns-01", "ns-02"}
	for _, ns := range answering {
		writeFile(t, filepath.Join(dir, ns+".json"), mustJSON(t, backupItems(aCluster("app-db", ns))))
	}
	// The ones that wait say so, and wait for the cluster as `exec sleep` so that the process the
	// cancellation kills is the one holding the pipe. They start only when the answering ones are done.
	fakeKubectl(t, `ns=own
for a in "$@"; do
  case "$a" in --namespace=*) ns="${a#--namespace=}" ;; esac
done
case "$ns" in
  ns-00|ns-01|ns-02) cat `+dir+`/$ns.json; touch `+dir+`/done-$ns ;;
  *)
    while [ ! -f `+dir+`/done-ns-00 ] || [ ! -f `+dir+`/done-ns-01 ] || [ ! -f `+dir+`/done-ns-02 ]; do sleep 0.02; done
    touch `+dir+`/waiting-$ns
    exec sleep 30 ;;
esac
`)
	named := namespacesNamed(readsAtOnce)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		v   view.View
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := runList(ctx, req(map[string]any{"namespace": named}))
		done <- result{v, err}
	}()

	deadline := time.Now().Add(15 * time.Second)
	for len(filesIn(t, dir, "waiting-")) < len(named)-len(answering) {
		if time.Now().After(deadline) {
			t.Fatal("the namespaces that wait never all started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The answering children have written their marker as their last act; this is the time they take to exit.
	time.Sleep(150 * time.Millisecond)
	cancel()

	var got result
	select {
	case got = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the call did not return after the context was cancelled")
	}
	if got.err != nil {
		t.Fatalf("a call cancelled after part of it answered was an error: %v", got.err)
	}
	tbl := got.v.(view.Table)
	rows := firstCells(tbl)
	want := []string{"ns-00/app-db", "ns-01/app-db", "ns-02/app-db", "ns-03/—", "ns-04/—", "ns-05/—"}
	if !slices.Equal(rows, want) {
		t.Errorf("rows = %v, want the three that answered and the three that did not, by name", rows)
	}
	if tbl.Total != 3 {
		t.Errorf("total = %d, want the three clusters", tbl.Total)
	}
	if w := hasCode(tbl.Warnings, "cnpg.namespace.unreadable"); w == nil || !strings.Contains(w.Message, "3 of 6 namespaces") {
		t.Errorf("warnings = %+v, want one saying 3 of 6 namespaces could not be read", tbl.Warnings)
	}
}

// A context cancelled before anything is asked asks nothing, and every namespace
// is a row that says it was not read.
func TestACallCancelledBeforeItStartsAsksNothingAndNamesEveryNamespace(t *testing.T) {
	stub := servesPerNamespace(t, nil, nil, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	named := namespacesNamed(8)
	v, err := runList(ctx, req(map[string]any{"namespace": named}))
	if err != nil {
		t.Fatalf("a cancelled call was an error rather than a page of what was not read: %v", err)
	}
	tbl := v.(view.Table)
	if len(tbl.Rows) != len(named) || tbl.Total != 0 {
		t.Errorf("%d rows, total %d, want a row for each of the %d namespaces and no cluster", len(tbl.Rows), tbl.Total, len(named))
	}
	for i, row := range tbl.Rows {
		if row[0] != named[i]+"/—" || row[1] != "fail" {
			t.Errorf("row %d = %v, want the namespace %s as a failed row", i, row, named[i])
		}
	}
	if calls := stub.calls(); len(calls) != 0 {
		t.Errorf("a cancelled call still asked the cluster: %v", calls)
	}
}

// The cluster stops answering after two namespaces have: the two are on the page
// and the rest are rows that name themselves, each saying the cluster did not
// answer. The page is the error only when nothing was read.
func TestAConnectionThatFailsAfterSomeNamespacesAnsweredKeepsThemAndNamesTheRest(t *testing.T) {
	dir := t.TempDir()
	answering := []string{"a-0", "a-1"}
	for _, ns := range answering {
		writeFile(t, filepath.Join(dir, ns+".json"), mustJSON(t, backupItems(aCluster("app-db", ns))))
	}
	// Six at most run at once and these are six, so every one is asked; the ones that fail do so only
	// once the others have answered.
	fakeKubectl(t, `ns=own
for a in "$@"; do
  case "$a" in --namespace=*) ns="${a#--namespace=}" ;; esac
done
case "$ns" in
  a-*) cat `+dir+`/$ns.json; touch `+dir+`/done-$ns ;;
  *)
    while [ ! -f `+dir+`/done-a-0 ] || [ ! -f `+dir+`/done-a-1 ]; do sleep 0.02; done
    echo '`+unreachableErr+`' >&2; exit 1 ;;
esac
`)
	v, err := runList(context.Background(), req(map[string]any{
		"namespace": []string{"a-0", "a-1", "z-0", "z-1", "z-2", "z-3"},
	}))
	if err != nil {
		t.Fatalf("the cluster failing after two namespaces answered made the page an error: %v", err)
	}
	tbl := v.(view.Table)
	if got, want := firstCells(tbl), []string{"a-0/app-db", "a-1/app-db", "z-0/—", "z-1/—", "z-2/—", "z-3/—"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want the two that answered and the four that did not", got)
	}
	for _, row := range tbl.Rows[2:] {
		if row[1] != "fail" || !strings.Contains(row[5], "could not be read — Unable to connect to the server") {
			t.Errorf("row = %v, want it to say the cluster did not answer", row)
		}
	}
	if w := hasCode(tbl.Warnings, "cnpg.namespace.unreadable"); w == nil || !strings.Contains(w.Message, "4 of 6 namespaces") {
		t.Errorf("warnings = %+v, want one saying 4 of 6 namespaces could not be read", tbl.Warnings)
	}
}

// filesIn is the names in dir that start with prefix.
func filesIn(t *testing.T, dir, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// The text a reader is given about a list of clusters: that one name is asked of
// the API server and several are kept from the namespace's own list, which is
// read whole, and where a list may be written.
func TestTheDescriptionsSayWhereTheListOfClustersIsFilteredAndWhereAListIsWritten(t *testing.T) {
	for _, tc := range []struct {
		id    string
		texts func(plugin.Capability) []string
		says  string
	}{
		{"cnpg.list", func(c plugin.Capability) []string { return []string{c.Description, c.Agent} }, "namespace's whole list"},
		{"cnpg.backup.list", func(c plugin.Capability) []string { return []string{c.Description} }, "listed whole"},
	} {
		c := capabilityOf(t, tc.id)
		for _, text := range tc.texts(c) {
			if !strings.Contains(text, tc.says) {
				t.Errorf("%s: %q never says the namespace's list is read whole and filtered here (%q)", tc.id, text, tc.says)
			}
		}
	}
	if text := capabilityOf(t, "cnpg.list").Description; !strings.Contains(text, "resourceNames") {
		t.Errorf("cnpg.list: %q never says what a credential allowed clusters by name meets", text)
	}
	for _, id := range []string{"cnpg.list", "cnpg.backup.list", "cnpg.storage"} {
		text := capabilityOf(t, id).Description
		for _, want := range []string{"`with:`", "`only:`", "take them as text"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: %q never says where a list is written (%s)", id, text, want)
			}
		}
	}
}
