package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The reads that mean "every cluster" when none is named take lists: the
// namespaces to look in and the clusters to keep. A profile fenced to several of
// either is what a tile on that connection names, and one String could not be
// handed them.

const (
	forbiddenIn = `Error from server (Forbidden): %s is forbidden: User "system:serviceaccount:ci:reader" ` +
		`cannot list resource "%s" in API group "%s" in the namespace "%s"`
	unreachableErr = `Unable to connect to the server: dial tcp 10.0.0.1:6443: i/o timeout`
)

func forbiddenClusters(namespace string) string {
	return fmt.Sprintf(forbiddenIn, clusterCRD, "clusters", "postgresql.cnpg.io", namespace)
}

// perNamespace is a kubectl that answers by the namespace it is asked about:
// <namespace>.json in the directory is what a read of it prints, and
// <namespace>.err what it says before it exits 1. "all" stands for
// --all-namespaces and "own" for no namespace at all; one with no file is an
// empty list. Every call is recorded, one line each, and the number running at
// once is recorded beside it when the call lingers for a while.
type perNamespace struct {
	dir string
}

func servesPerNamespace(t *testing.T, answers map[string]any, failures map[string]string, linger string) perNamespace {
	t.Helper()
	dir := t.TempDir()
	for ns, doc := range answers {
		writeFile(t, filepath.Join(dir, ns+".json"), mustJSON(t, doc))
	}
	for ns, stderr := range failures {
		writeFile(t, filepath.Join(dir, ns+".err"), stderr)
	}
	if err := os.Mkdir(filepath.Join(dir, "running"), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeKubectl(t, `echo "$*" >> `+dir+`/calls
ns=own
for a in "$@"; do
  case "$a" in
    --namespace=*) ns="${a#--namespace=}" ;;
    --all-namespaces) ns=all ;;
  esac
done
touch `+dir+`/running/$$
ls `+dir+`/running | wc -l | tr -d ' ' >> `+dir+`/concurrency
`+linger+`
rm -f `+dir+`/running/$$
if [ -f `+dir+`/$ns.err ]; then cat `+dir+`/$ns.err >&2; exit 1; fi
if [ -f `+dir+`/$ns.json ]; then cat `+dir+`/$ns.json; else echo '{"items":[]}'; fi
`)
	return perNamespace{dir: dir}
}

// calls is every argv the stub was run with, as one line each, sorted: the
// reads of a list run at once, so their order is not the test's to hold.
func (p perNamespace) calls() []string {
	raw, _ := os.ReadFile(filepath.Join(p.dir, "calls"))
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	slices.Sort(out)
	return out
}

// namespaces is the namespace each call asked for, sorted; "" for a call that
// named none and "*" for one that asked for every namespace.
func (p perNamespace) namespaces() []string {
	var out []string
	for _, line := range p.calls() {
		ns := ""
		for _, word := range strings.Fields(line) {
			switch {
			case strings.HasPrefix(word, "--namespace="):
				ns = strings.TrimPrefix(word, "--namespace=")
			case word == "--all-namespaces":
				ns = "*"
			}
		}
		out = append(out, ns)
	}
	slices.Sort(out)
	return out
}

// mostAtOnce is the most calls that were running at the same moment.
func (p perNamespace) mostAtOnce() int {
	raw, _ := os.ReadFile(filepath.Join(p.dir, "concurrency"))
	most := 0
	for _, line := range strings.Fields(string(raw)) {
		if n, err := strconv.Atoi(line); err == nil {
			most = max(most, n)
		}
	}
	return most
}

func namespacesNamed(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("ns-%02d", i)
	}
	return out
}

func firstCells(tbl view.Table) []string {
	out := make([]string, len(tbl.Rows))
	for i, row := range tbl.Rows {
		out[i] = row[0]
	}
	return out
}

func rowFor(t *testing.T, tbl view.Table, prefix string) []string {
	t.Helper()
	for _, row := range tbl.Rows {
		if strings.HasPrefix(row[0], prefix) {
			return row
		}
	}
	t.Fatalf("no row for %q in %v", prefix, firstCells(tbl))
	return nil
}

func hasCode(warnings []view.Error, code string) *view.Error {
	for i := range warnings {
		if warnings[i].Code == code {
			return &warnings[i]
		}
	}
	return nil
}

// The listing of clusters, over several namespaces.

func TestAListOfNamespacesIsReadWithOneCallEachAndMergedIntoOnePage(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{
		"alpha": backupItems(aCluster("app-db", "alpha"), aCluster("cache", "alpha")),
		"beta":  backupItems(aCluster("app-db", "beta")),
		"gamma": backupItems(),
	}, nil, "")
	v, err := runList(context.Background(), req(map[string]any{"namespace": []string{"gamma", "alpha", "beta"}}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	if got, want := firstCells(tbl), []string{"alpha/app-db", "alpha/cache", "beta/app-db"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want the namespaces' clusters in namespace order", got)
	}
	if got, want := stub.namespaces(), []string{"alpha", "beta", "gamma"}; !slices.Equal(got, want) {
		t.Errorf("namespaces asked = %v, want one call each and none for the whole cluster", got)
	}
	for _, call := range stub.calls() {
		if strings.Contains(call, "--all-namespaces") || strings.Contains(call, "--field-selector") {
			t.Errorf("call %q reads across namespaces or filters, want a plain read of one", call)
		}
	}
	if tbl.Total != 3 || len(tbl.Warnings) != 0 {
		t.Errorf("total = %d, warnings = %v", tbl.Total, tbl.Warnings)
	}
}

func TestAListOfOneNamespaceIsExactlyTheSingleNamespace(t *testing.T) {
	serves := func(t *testing.T) perNamespace {
		return servesPerNamespace(t, map[string]any{"prod": backupItems(aCluster("app-db", "prod"), aCluster("cache", "prod"))}, nil, "")
	}
	var pages [][]byte
	var calls [][]string
	for _, value := range []any{"prod", []string{"prod"}, []any{"prod"}, []string{"prod", "prod", ""}} {
		stub := serves(t)
		v, err := runList(context.Background(), req(map[string]any{"namespace": value}))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := view.Marshal(view.Envelope{View: v})
		if err != nil {
			t.Fatal(err)
		}
		pages, calls = append(pages, raw), append(calls, stub.calls())
	}
	for i := 1; i < len(pages); i++ {
		if string(pages[i]) != string(pages[0]) {
			t.Errorf("form %d answers\n%s\nwant what the bare name answers\n%s", i, pages[i], pages[0])
		}
		if !reflect.DeepEqual(calls[i], calls[0]) {
			t.Errorf("form %d asks %v, want %v", i, calls[i], calls[0])
		}
	}
	if len(calls[0]) != 1 || !strings.Contains(calls[0][0], "--namespace=prod") {
		t.Errorf("calls = %v, want one read of prod", calls[0])
	}
}

func TestNoNamespaceAndAllNamespacesAreStillOneCallEach(t *testing.T) {
	for name, tc := range map[string]struct {
		values map[string]any
		want   []string
	}{
		"none named":     {map[string]any{}, []string{""}},
		"an empty list":  {map[string]any{"namespace": []string{}}, []string{""}},
		"all-namespaces": {map[string]any{"all-namespaces": true}, []string{"*"}},
	} {
		t.Run(name, func(t *testing.T) {
			stub := servesPerNamespace(t, nil, nil, "")
			if _, err := runList(context.Background(), req(tc.values)); err != nil {
				t.Fatal(err)
			}
			if got := stub.namespaces(); !slices.Equal(got, tc.want) {
				t.Errorf("namespaces asked = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSevenNamespacesAreSevenCallsAndNeverAClusterWideList(t *testing.T) {
	stub := servesPerNamespace(t, nil, nil, "")
	seven := namespacesNamed(7)
	if _, err := runList(context.Background(), req(map[string]any{"namespace": seven})); err != nil {
		t.Fatal(err)
	}
	if got := stub.namespaces(); !slices.Equal(got, seven) {
		t.Errorf("namespaces asked = %v, want the seven", got)
	}
}

func TestANamespaceListIsSortedOnceEachAndBlanksAreDropped(t *testing.T) {
	stub := servesPerNamespace(t, nil, nil, "")
	if _, err := runList(context.Background(), req(map[string]any{"namespace": []string{"b", "a", "", "b"}})); err != nil {
		t.Fatal(err)
	}
	if got := stub.namespaces(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("namespaces asked = %v, want a and b once each", got)
	}
}

func TestAListOfNamespacesBesideEveryNamespaceIsRefusedAsAmbiguous(t *testing.T) {
	stub := servesPerNamespace(t, nil, nil, "")
	for name, run := range map[string]func(context.Context, plugin.Request) (view.View, error){
		"cnpg.list": runList, "cnpg.backup.list": runBackupList, "cnpg.storage": runStorage,
	} {
		_, err := run(context.Background(), req(map[string]any{"namespace": []string{"a", "b"}, "all-namespaces": true}))
		if got := asViewError(t, err).Code; got != "cnpg.namespace.ambiguous" {
			t.Errorf("%s: code = %q, want cnpg.namespace.ambiguous", name, got)
		}
	}
	if calls := stub.calls(); len(calls) != 0 {
		t.Errorf("a refused call still asked the cluster: %v", calls)
	}
}

// The clusters of a list narrow the read within the namespaces.

func TestAListOfClustersNarrowsWithinTheNamespacesAndAMissingOneIsARow(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{
		"alpha": backupItems(aCluster("app-db", "alpha"), aCluster("cache", "alpha"), aCluster("queue", "alpha")),
		"beta":  backupItems(aCluster("app-db", "beta"), aCluster("search", "beta")),
	}, nil, "")
	v, err := runList(context.Background(), req(map[string]any{
		"namespace": []string{"alpha", "beta"}, "cluster": []string{"app-db", "cache", "ghost"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	if got, want := firstCells(tbl), []string{"alpha/app-db", "alpha/cache", "beta/app-db", "ghost"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want the named clusters where they are, then the one that is nowhere", got)
	}
	ghost := rowFor(t, tbl, "ghost")
	if ghost[1] != "warn" || !strings.Contains(ghost[5], "not found in namespaces alpha, beta") {
		t.Errorf("the missing cluster reads %v, want a warn row saying where it was looked for", ghost)
	}
	if got := stub.namespaces(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("namespaces asked = %v, want one call each however many clusters are named", got)
	}
}

func TestOneClusterIsStillAskedOfTheAPIServerInEachNamespace(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{
		"alpha": backupItems(aCluster("app-db", "alpha")),
		"beta":  backupItems(aCluster("app-db", "beta")),
	}, nil, "")
	v, err := runList(context.Background(), req(map[string]any{
		"namespace": []string{"alpha", "beta"}, "cluster": []string{"app-db"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range stub.calls() {
		if !strings.Contains(call, "--field-selector metadata.name=app-db") {
			t.Errorf("call %q, want the API server asked for that cluster alone", call)
		}
	}
	if got := firstCells(v.(view.Table)); !slices.Equal(got, []string{"alpha/app-db", "beta/app-db"}) {
		t.Errorf("rows = %v", got)
	}
}

func TestEveryNamespaceWithSeveralClustersIsOneCallThatKeepsTheNamedOnes(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{
		"all": backupItems(aCluster("app-db", "alpha"), aCluster("cache", "beta"), aCluster("queue", "beta")),
	}, nil, "")
	v, err := runList(context.Background(), req(map[string]any{
		"all-namespaces": true, "cluster": []string{"cache", "app-db", "ghost"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	if got, want := firstCells(tbl), []string{"alpha/app-db", "beta/cache", "ghost"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want the named clusters and the one that is nowhere", got)
	}
	if ghost := rowFor(t, tbl, "ghost"); !strings.Contains(ghost[5], "not found in every namespace") {
		t.Errorf("the missing cluster reads %v", ghost)
	}
	if got := stub.namespaces(); !slices.Equal(got, []string{"*"}) {
		t.Errorf("namespaces asked = %v, want the one read of every namespace", got)
	}
}

func TestSeveralClustersInTheContextsOwnNamespaceAreOneCall(t *testing.T) {
	stub := servesPerNamespace(t, map[string]any{"own": backupItems(aCluster("app-db", "default"))}, nil, "")
	v, err := runList(context.Background(), req(map[string]any{"cluster": []string{"app-db", "ghost"}}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	if got := firstCells(tbl); !slices.Equal(got, []string{"default/app-db", "ghost"}) {
		t.Errorf("rows = %v", got)
	}
	if ghost := rowFor(t, tbl, "ghost"); !strings.Contains(ghost[5], "not found in the context's own namespace") {
		t.Errorf("the missing cluster reads %v", ghost)
	}
	if got := stub.namespaces(); !slices.Equal(got, []string{""}) {
		t.Errorf("namespaces asked = %v, want the one read of the context's own", got)
	}
}

// The cluster that is nowhere is only said when something was read to look in:
// a namespace that could not be read may be where it is.
func TestAClusterIsNotSaidToBeMissingFromNamespacesNothingWasReadIn(t *testing.T) {
	servesPerNamespace(t, nil, map[string]string{
		"alpha": forbiddenClusters("alpha"), "beta": forbiddenClusters("beta"),
	}, "")
	v, err := runList(context.Background(), req(map[string]any{
		"namespace": []string{"alpha", "beta"}, "cluster": []string{"app-db", "cache"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := firstCells(v.(view.Table)); !slices.Equal(got, []string{"alpha/—", "beta/—"}) {
		t.Errorf("rows = %v, want the two namespaces that could not be read and no claim about the clusters", got)
	}
}

func TestANamedClusterNowhereInSeveralNamespacesIsOneEmptySentence(t *testing.T) {
	servesPerNamespace(t, nil, nil, "")
	v, err := runList(context.Background(), req(map[string]any{
		"namespace": []string{"alpha", "beta"}, "cluster": "ghost",
	}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	if len(tbl.Rows) != 0 || !strings.Contains(tbl.Empty, `no CloudNativePG cluster called "ghost" in namespaces alpha, beta`) {
		t.Errorf("rows = %v, empty = %q", tbl.Rows, tbl.Empty)
	}
}

// A target that cannot be read is a row that says so, and the others answer.

func TestANamespaceThatCannotBeReadIsARowAndTheOthersAnswer(t *testing.T) {
	servesPerNamespace(t, map[string]any{
		"alpha": backupItems(aCluster("app-db", "alpha")),
		"gamma": backupItems(aCluster("app-db", "gamma")),
	}, map[string]string{"beta": forbiddenClusters("beta")}, "")
	v, err := runList(context.Background(), req(map[string]any{"namespace": []string{"alpha", "beta", "gamma"}}))
	if err != nil {
		t.Fatalf("one namespace refused made the page an error: %v", err)
	}
	tbl := v.(view.Table)
	if got, want := firstCells(tbl), []string{"alpha/app-db", "beta/—", "gamma/app-db"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want the refused namespace where its clusters would be", got)
	}
	beta := rowFor(t, tbl, "beta/")
	if beta[1] != "fail" || !strings.Contains(beta[5], "could not be read — not allowed to list clusters.postgresql.cnpg.io in namespace beta") {
		t.Errorf("the refused namespace reads %v", beta)
	}
	warning := hasCode(tbl.Warnings, "cnpg.namespace.unreadable")
	if warning == nil || !strings.Contains(warning.Message, "1 of 3 namespaces") || !strings.Contains(warning.Message, "beta") {
		t.Errorf("warnings = %+v, want one saying 1 of 3 namespaces could not be read and naming beta", tbl.Warnings)
	}
	if warning != nil && warning.Advisory {
		t.Error("the warning is advisory: a namespace missing from the page is a partial answer")
	}
}

func TestEveryNamespaceRefusedIsStillRowsBecauseEachNamesItsOwn(t *testing.T) {
	servesPerNamespace(t, nil, map[string]string{
		"alpha": forbiddenClusters("alpha"), "beta": forbiddenClusters("beta"),
	}, "")
	v, err := runList(context.Background(), req(map[string]any{"namespace": []string{"alpha", "beta"}}))
	if err != nil {
		t.Fatalf("every namespace refused made an error: %v", err)
	}
	tbl := v.(view.Table)
	if got := firstCells(tbl); !slices.Equal(got, []string{"alpha/—", "beta/—"}) {
		t.Errorf("rows = %v", got)
	}
}

func TestOneNamespaceRefusedIsTheErrorItAlwaysWas(t *testing.T) {
	servesPerNamespace(t, nil, map[string]string{"beta": forbiddenClusters("beta")}, "")
	_, err := runList(context.Background(), req(map[string]any{"namespace": []string{"beta"}}))
	verr := asViewError(t, err)
	if verr.Code != "cnpg.denied" {
		t.Errorf("code = %q, want cnpg.denied", verr.Code)
	}
	if !verr.CredentialRefused() {
		t.Error("a namespace the credential may not read does not reach the caller as a refused credential")
	}
}

// A cluster that does not answer is one failure, not one per namespace.
func TestAClusterThatDoesNotAnswerIsAskedSixTimesAndNotThirty(t *testing.T) {
	// Every namespace fails the same way: the connection is down.
	failures := map[string]string{}
	named := namespacesNamed(maxNamespaces)
	for _, ns := range named {
		failures[ns] = unreachableErr
	}
	stub := servesPerNamespace(t, nil, failures, "sleep 0.2")
	_, err := runList(context.Background(), req(map[string]any{"namespace": named}))
	if got := asViewError(t, err).Code; got != "cnpg.unreachable" {
		t.Fatalf("code = %q, want one cnpg.unreachable for the connection", got)
	}
	if n := len(stub.calls()); n > 6 {
		t.Errorf("%d calls were made, want no more than the six that run at once", n)
	}
}

// A credential the API server does not accept is the same answer from every
// namespace, and it reaches the caller as the refusal a dashboard stops asking
// after; a credential plugin that failed is the same for the queue, and is not.
func TestACredentialThatIsNotAcceptedIsOneFailureAndReachesTheCallerAsARefusal(t *testing.T) {
	for _, tc := range []struct {
		name, stderr, code string
		refused            bool
	}{
		{"a 401", "error: You must be logged in to the server (Unauthorized)", "cnpg.auth.failed", true},
		{"a credential plugin", "Unable to connect to the server: getting credentials: exec: executable tsh failed",
			"cnpg.login", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures := map[string]string{}
			named := namespacesNamed(maxNamespaces)
			for _, ns := range named {
				failures[ns] = tc.stderr
			}
			stub := servesPerNamespace(t, nil, failures, "sleep 0.2")
			_, err := runList(context.Background(), req(map[string]any{"namespace": named}))
			verr := asViewError(t, err)
			if verr.Code != tc.code || verr.CredentialRefused() != tc.refused {
				t.Errorf("code = %q (credential refused: %v), want %s (%v)", verr.Code, verr.CredentialRefused(), tc.code, tc.refused)
			}
			if n := len(stub.calls()); n > 6 {
				t.Errorf("%d calls were made, want no more than the six that run at once", n)
			}
		})
	}
}

func TestTheReadsOfAListRunAtOnceButNeverMoreThanSix(t *testing.T) {
	stub := servesPerNamespace(t, nil, nil, "sleep 0.3")
	if _, err := runList(context.Background(), req(map[string]any{"namespace": namespacesNamed(12)})); err != nil {
		t.Fatal(err)
	}
	if got := len(stub.calls()); got != 12 {
		t.Errorf("%d calls, want 12", got)
	}
	if most := stub.mostAtOnce(); most > 6 || most < 2 {
		t.Errorf("%d calls ran at once, want several and no more than six", most)
	}
}

// The cap, and what a name may be.

func TestMoreThanThirtyNamespacesOrClustersIsRefusedAndNothingIsAsked(t *testing.T) {
	stub := servesPerNamespace(t, nil, nil, "")
	// A number no other part of the text can contain: the refusal must not say how many the call reaches.
	const reached = 47
	for _, tc := range []struct {
		input, noun, code string
	}{
		{"namespace", "namespaces", "cnpg.namespaces.toomany"},
		{"cluster", "clusters", "cnpg.clusters.toomany"},
	} {
		for name, run := range map[string]func(context.Context, plugin.Request) (view.View, error){
			"cnpg.list": runList, "cnpg.backup.list": runBackupList, "cnpg.storage": runStorage,
		} {
			for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceMCP} {
				_, err := run(context.Background(), req(map[string]any{tc.input: namespacesNamed(reached)}).WithSurface(sf))
				verr := asViewError(t, err)
				label := fmt.Sprintf("%s %s %s", name, tc.input, sf)
				if verr.Code != tc.code {
					t.Errorf("%s: code = %q, want %s", label, verr.Code, tc.code)
				}
				want := "a listing reads at most 30 " + tc.noun + ", and this call reaches more"
				if verr.Message != want {
					t.Errorf("%s: message = %q, want %q", label, verr.Message, want)
				}
				if !strings.Contains(verr.Hint, "fewer") || !strings.Contains(verr.Hint, "fence") {
					t.Errorf("%s: hint = %q, want it to say to name fewer or fence fewer", label, verr.Hint)
				}
				// The list may be the profile's fence filled in by the host, and the host keeps a fence's
				// contents from an agent: neither its size nor a claim that the caller named it is the text's to make.
				for _, text := range []string{verr.Message, verr.Hint} {
					if strings.Contains(text, strconv.Itoa(reached)) || strings.Contains(text, "named") {
						t.Errorf("%s: %q says how many the call reaches or that it was named", label, text)
					}
				}
			}
		}
	}
	if calls := stub.calls(); len(calls) != 0 {
		t.Errorf("a refused list still asked the cluster: %v", calls)
	}

	// Thirty is allowed, and a name given twice counts once.
	if _, err := runList(context.Background(), req(map[string]any{
		"namespace": append(namespacesNamed(maxNamespaces), namespacesNamed(maxNamespaces)...),
	})); err != nil {
		t.Errorf("thirty names, each given twice, were refused: %v", err)
	}
	if got := len(stub.calls()); got != maxNamespaces {
		t.Errorf("%d calls, want %d", got, maxNamespaces)
	}
}

func TestANameThatWouldBeReadAsAFlagInAListIsRefusedBeforeAnythingIsAsked(t *testing.T) {
	stub := servesPerNamespace(t, nil, nil, "")
	for _, values := range []map[string]any{
		{"namespace": []string{"ok", "--kubeconfig=/tmp/mine"}},
		{"cluster": []string{"ok", "-o=yaml"}},
	} {
		_, err := runList(context.Background(), req(values))
		if got := asViewError(t, err).Code; got != "cnpg.name.invalid" {
			t.Errorf("%v: code = %q, want cnpg.name.invalid", values, got)
		}
	}
	if calls := stub.calls(); len(calls) != 0 {
		t.Errorf("a refused name still asked the cluster: %v", calls)
	}
}

// What the host does with the lists: it holds each element to the fence.

func TestTheHostHoldsEveryElementOfTheListsToTheFence(t *testing.T) {
	fence := plugin.Fence{"namespace": {"alpha", "beta", "gamma"}, "cluster": {"app-db", "cache"}}
	for _, id := range []string{"cnpg.list", "cnpg.backup.list", "cnpg.storage"} {
		c := capabilityOf(t, id)
		check := func(caller map[string]any) *plugin.FenceBreach {
			return plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: caller})
		}
		if breach := check(map[string]any{"namespace": []string{"alpha", "gamma"}, "cluster": []string{"app-db"}}); breach != nil {
			t.Errorf("%s: names inside the fence were refused: %+v", id, breach)
		}
		if breach := check(map[string]any{"namespace": []string{"alpha", "delta"}, "cluster": []string{"app-db"}}); breach == nil {
			t.Errorf("%s: a namespace outside the fence got through in a list", id)
		}
		if breach := check(map[string]any{"namespace": []string{"alpha"}, "cluster": []string{"app-db", "billing"}}); breach == nil {
			t.Errorf("%s: a cluster outside the fence got through in a list", id)
		}
		if breach := check(map[string]any{"namespace": []string{"alpha"}, "cluster": []string{"app-db"}, "all-namespaces": true}); breach == nil {
			t.Errorf("%s: all-namespaces got past a fence on the namespace", id)
		}
	}
}

// The declarations.

func TestTheReadsTakeListsAndTheCapabilitiesOnOneClusterKeepTheirSingleValues(t *testing.T) {
	for _, id := range []string{"cnpg.list", "cnpg.backup.list", "cnpg.storage"} {
		c := capabilityOf(t, id)
		for input, short := range map[string]string{"cluster": "c", "namespace": "n"} {
			f, ok := inputOf(c, input)
			switch {
			case !ok:
				t.Errorf("%s has no %s", id, input)
			case f.Type != plugin.StringSlice:
				t.Errorf("%s: %s is a %s, want a list", id, input, f.Type)
			case f.Short != short || f.Config != input:
				t.Errorf("%s: %s has short %q and config %q, want %q and %q", id, input, f.Short, f.Config, short, input)
			case f.Required || !f.Live || f.Suggest == nil:
				t.Errorf("%s: %s is required or does not complete on a press, which a list does since rta v0.48.0", id, input)
			}
		}
		all, _ := inputOf(c, "all-namespaces")
		if all.Widens != "namespace" {
			t.Errorf("%s: all-namespaces widens %q, want namespace", id, all.Widens)
		}
		if c.Scope != "" {
			t.Errorf("%s declares the scope %q: a listing names no record", id, c.Scope)
		}
	}
	for _, id := range []string{"cnpg.status", "cnpg.backup.show", "cnpg.backup.request"} {
		c := capabilityOf(t, id)
		for _, input := range []string{"cluster", "namespace"} {
			if f, _ := inputOf(c, input); f.Type != plugin.String || !f.Live {
				t.Errorf("%s: %s is a %s (live %v), want the single value it was", id, input, f.Type, f.Live)
			}
		}
	}
	if got := capabilityOf(t, "cnpg.backup.request").Scope; got != "cluster" {
		t.Errorf("cnpg.backup.request scope = %q, want cluster", got)
	}
	for _, id := range []string{"cnpg.status", "cnpg.backup.show"} {
		if got := capabilityOf(t, id).Scope; got != "" {
			t.Errorf("%s scope = %q, want none", id, got)
		}
	}
}

func TestTheDescriptionsSayTheSelectorsAreListsAndWhatNamingNoneReads(t *testing.T) {
	for _, id := range []string{"cnpg.list", "cnpg.backup.list", "cnpg.storage"} {
		c := capabilityOf(t, id)
		for _, text := range []string{c.Description, c.Agent} {
			for _, want := range []string{"30", "one call"} {
				if !strings.Contains(text, want) {
					t.Errorf("%s: %q never says %q", id, text, want)
				}
			}
		}
	}
}
