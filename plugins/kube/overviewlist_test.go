package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A tile of kube.overview on a connection fenced to several namespaces names
// them as a list, and the overview reads each one with a call of its own: a
// credential that may list in a namespace and not across them is the ordinary
// restricted identity, and a fence is meant to keep the read inside what the
// connection reaches. These are the seven namespaces of such a connection.
var homelabNamespaces = []string{
	"crowdsec", "gitea", "keycloak-system", "mattermost", "prometheus-stack", "rustfs", "vault-system",
}

// listIn sets what the cluster answers to `get <resource> --namespace=<ns>`.
func (c *scriptedCluster) listIn(t *testing.T, resource, namespace string, items ...string) {
	t.Helper()
	c.write(t, "list-"+resource+"."+namespace+".json", `{"items":[`+strings.Join(items, ",")+`]}`)
}

// secretsIn sets the TLS Secrets one namespace holds.
func (c *scriptedCluster) secretsIn(t *testing.T, namespace string, items ...string) {
	t.Helper()
	c.write(t, "secrets."+namespace+".json", `{"items":[`+strings.Join(items, ",")+`]}`)
}

// failIn makes one namespace's list of a resource fail the way the API server
// refuses a credential that may not read it.
func (c *scriptedCluster) failIn(t *testing.T, resource, namespace string) {
	t.Helper()
	c.failInWith(t, resource, namespace, forbiddenIn(resource, namespace))
}

// forbiddenIn is what the API server tells a credential that may not list a
// resource in a namespace.
func forbiddenIn(resource, namespace string) string {
	return `Error from server (Forbidden): ` + resource + ` is forbidden: User "system:serviceaccount:ops:rta" ` +
		`cannot list resource "` + resource + `" in API group "" in the namespace "` + namespace + `"`
}

// failInWith makes one namespace's list of a resource answer with stderr.
func (c *scriptedCluster) failInWith(t *testing.T, resource, namespace, stderr string) {
	t.Helper()
	if resource == "secrets" {
		c.write(t, "secrets."+namespace+".fail", stderr)
		return
	}
	c.write(t, "list-"+resource+"."+namespace+".fail", stderr)
}

// forbidClusterWide makes every list across namespaces refuse, which is all a
// credential bound by a Role in each namespace can be told.
func (c *scriptedCluster) forbidClusterWide(t *testing.T) {
	t.Helper()
	c.write(t, "forbid-clusterwide", "")
}

// aHomelab is the cluster of the connection above: two Ready nodes, nine
// namespaces of which seven are named, and in each of those two serving pods, a
// deployment with every replica, a quota with room and a certificate with a
// year left. A namespace beside them, payments, holds a crash loop that no
// page of the seven may mention.
func aHomelab(t *testing.T) *scriptedCluster {
	t.Helper()
	c := newScriptedCluster(t)
	c.currentContext("homelab", "")
	namespaces := []string{}
	for _, ns := range append(slices.Clone(homelabNamespaces), "kube-system", "payments") {
		namespaces = append(namespaces, jsonOf(t, obj{"metadata": obj{"name": ns}, "status": obj{"phase": "Active"}}))
	}
	c.list(t, "namespaces", namespaces...)
	c.list(t, "nodes",
		nodeFx(t, "n1", "True", false, "", "v1.31.4"),
		nodeFx(t, "n2", "True", false, "", "v1.31.4"))
	for _, ns := range homelabNamespaces {
		c.listIn(t, "pods", ns,
			podFx(t, ns, ns+"-0", "Running", true, 0, "", "n1"),
			podFx(t, ns, ns+"-1", "Running", true, 0, "", "n2"))
		c.listIn(t, "deployments", ns, depFx(t, ns, ns, 2, 2))
		c.listIn(t, "resourcequotas", ns, quotaFx(t, ns, "compute",
			map[string]string{"pods": "50"}, map[string]string{"pods": "4"}))
		c.secretsIn(t, ns, secretItem(ns, ns+"-tls", selfSignedPEM(t, ns, time.Now().Add(365*24*time.Hour)), "KEY"))
	}
	c.listIn(t, "pods", "payments", podFx(t, "payments", "ledger-0", "Running", false, 41, "CrashLoopBackOff", "n1"))
	return c
}

// namespacesRead is the namespace each of the lines asked for, sorted.
func namespacesRead(t *testing.T, lines []string) []string {
	t.Helper()
	var out []string
	for _, line := range lines {
		if strings.Contains(line, "--all-namespaces") {
			t.Errorf("%q reads every namespace", line)
		}
		var found []string
		for _, word := range strings.Fields(line) {
			if ns, ok := strings.CutPrefix(word, "--namespace="); ok {
				found = append(found, ns)
			}
		}
		if len(found) != 1 {
			t.Errorf("%q names %d namespaces, want one", line, len(found))
			continue
		}
		out = append(out, found[0])
	}
	sort.Strings(out)
	return out
}

func getCalls(t *testing.T, c *scriptedCluster) int {
	t.Helper()
	n := 0
	for _, line := range c.calls(t) {
		if strings.HasPrefix(line, "get ") {
			n++
		}
	}
	return n
}

func overviewOf(t *testing.T, values map[string]any) (view.View, error) {
	t.Helper()
	return runOverview(context.Background(), saReq(plugin.SurfaceCLI, false, values))
}

func TestAListOfNamespacesIsReadOneNamespaceAtATimeAndMergedIntoOnePage(t *testing.T) {
	c := aHomelab(t)
	pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
	if got := keysOf(pairs); strings.Join(got, "|") != strings.Join(compactKeys, "|") {
		t.Fatalf("rows = %v, want %v", got, compactKeys)
	}
	assertTileShape(t, pairs)

	for _, kind := range []string{"pods", "deployments", "resourcequotas", "secrets"} {
		got := namespacesRead(t, c.listed(t, kind))
		if strings.Join(got, ",") != strings.Join(homelabNamespaces, ",") {
			t.Errorf("%s was listed for %v, want one call for each of %v", kind, got, homelabNamespaces)
		}
	}
	for _, kind := range []string{"namespaces", "nodes"} {
		lines := c.listed(t, kind)
		if len(lines) != 1 || strings.Contains(lines[0], "--namespace") || strings.Contains(lines[0], "--all-namespaces") {
			t.Errorf("%s were listed as %q, want one cluster-scoped list", kind, lines)
		}
	}
	if n := getCalls(t, c); n != 30 {
		t.Errorf("%d lists for seven namespaces, want 30 (four kinds each, the namespaces and the nodes):\n%s",
			n, strings.Join(c.calls(t), "\n"))
	}

	rows := rowsOf(pairs)
	for key, want := range map[string]string{
		"status":      "ok",
		"nodes":       "2",
		"pods":        "14",
		"deployments": "7",
		"certs":       "7",
		"quotas":      "7",
		"pod slots":   "not counted — 7 namespaces are read",
		"context":     "homelab · 7 namespaces only",
	} {
		if !strings.HasPrefix(rows[key], want) {
			t.Errorf("%s = %q, want %q", key, rows[key], want)
		}
	}
	for key, value := range rows {
		if strings.Contains(value, "payments") || strings.Contains(value, "ledger") {
			t.Errorf("%s = %q names a namespace that was not asked for", key, value)
		}
	}
}

// The list arrives as an array over MCP, a []string from the command line and
// from the host's own resolution, and a bare string from a caller that sends a
// scalar where a list is declared; one name read either way is the page the
// single namespace has always been.
func TestOneNamespaceInAListIsReadExactlyAsTheSingleNamespaceWas(t *testing.T) {
	var scalar []view.Pair
	var scalarCalls []string
	for name, value := range map[string]any{
		"bare string": "gitea", "[]string": []string{"gitea"}, "[]any": []any{"gitea"}, "padded and repeated": []string{" gitea", "gitea", ""},
	} {
		t.Run(name, func(t *testing.T) {
			c := aHomelab(t)
			pairs := compactOf(t, map[string]any{"namespace": value})
			calls := c.calls(t)
			sort.Strings(calls)
			if scalar == nil {
				scalar, scalarCalls = pairs, calls
				if got := rowsOf(pairs)["context"]; got != "homelab · namespace gitea only" {
					t.Errorf("context = %q, want the one namespace read named", got)
				}
				if got := rowsOf(pairs)["pod slots"]; got != "not counted — one namespace is read" {
					t.Errorf("pod slots = %q", got)
				}
				return
			}
			if fmt.Sprint(pairs) != fmt.Sprint(scalar) || fmt.Sprint(calls) != fmt.Sprint(scalarCalls) {
				t.Errorf("%s read differently from the other spellings:\n%v\n%v", name, pairs, scalar)
			}
		})
	}
}

func TestANamespaceNamedTwiceOrNotAtAllIsNotReadTwiceOrAtAll(t *testing.T) {
	c := aHomelab(t)
	compactOf(t, map[string]any{"namespace": []string{"gitea", "rustfs", "gitea", " ", "rustfs"}})
	if got := namespacesRead(t, c.listed(t, "pods")); strings.Join(got, ",") != "gitea,rustfs" {
		t.Errorf("pods were listed for %v, want gitea and rustfs once each", got)
	}

	c = aHomelab(t)
	compactOf(t, map[string]any{"namespace": []string{}})
	for _, kind := range []string{"pods", "deployments", "resourcequotas", "secrets"} {
		lines := c.listed(t, kind)
		if len(lines) != 1 || !strings.Contains(lines[0], "--all-namespaces") {
			t.Errorf("an empty list read %s as %q, want every namespace in one call", kind, lines)
		}
	}
}

func TestANameKubectlWouldReadAsAFlagIsRefusedBeforeAnythingIsAsked(t *testing.T) {
	c := aHomelab(t)
	_, err := overviewOf(t, map[string]any{"namespace": []string{"gitea", "--kubeconfig=/tmp/mine"}})
	if verr, ok := err.(*view.Error); !ok || verr.Code != "kube.name.invalid" {
		t.Errorf("error = %v, want kube.name.invalid", err)
	}
	if calls := c.calls(t); len(calls) != 0 {
		t.Errorf("kubectl was run before the names were checked:\n%s", strings.Join(calls, "\n"))
	}
}

// A namespace is a DNS label, and a name that is not one is not a namespace to be
// asked about thirty at a time: the looser rule for a context or a resource would
// let "a/b", "a:b" and "a@b" through, and each is one more call to a cluster that
// has no such namespace.
func TestANameThatIsNotANamespaceIsRefusedBeforeAnythingIsAsked(t *testing.T) {
	for _, bad := range []string{"a/b", "a:b", "a@b", "a.b", "a_b", "Gitea", "-gitea", "gitea-", "a b",
		strings.Repeat("a", 64)} {
		t.Run(bad, func(t *testing.T) {
			c := aHomelab(t)
			_, err := overviewOf(t, map[string]any{"namespace": []string{"gitea", bad}})
			if verr, ok := err.(*view.Error); !ok || verr.Code != "kube.name.invalid" || !strings.Contains(verr.Message, bad) {
				t.Errorf("error = %v, want kube.name.invalid naming %q", err, bad)
			}
			if calls := c.calls(t); len(calls) != 0 {
				t.Errorf("kubectl was run before the names were checked:\n%s", strings.Join(calls, "\n"))
			}
		})
	}

	c := aHomelab(t)
	good := []string{"a", "gitea", "kube-system", "team-00", "0-9", strings.Repeat("a", 63)}
	if _, err := overviewOf(t, map[string]any{"namespace": good}); err != nil {
		t.Errorf("namespaces that are labels were refused: %v", err)
	}
	want := slices.Clone(good)
	slices.Sort(want)
	if got := namespacesRead(t, c.listed(t, "pods")); !slices.Equal(got, want) {
		t.Errorf("pods were listed for %v, want %v", got, want)
	}
}

func TestAListBesideEveryNamespaceIsRefusedAsASingleNamespaceIs(t *testing.T) {
	c := aHomelab(t)
	_, err := overviewOf(t, map[string]any{"namespace": []string{"gitea", "rustfs"}, "all-namespaces": true})
	if verr, ok := err.(*view.Error); !ok || verr.Code != "kube.namespace.ambiguous" {
		t.Errorf("error = %v, want kube.namespace.ambiguous", err)
	}
	if calls := c.calls(t); len(calls) != 0 {
		t.Errorf("kubectl was run for a call that was refused:\n%s", strings.Join(calls, "\n"))
	}
	if _, err := overviewOf(t, map[string]any{"namespace": []string{}, "all-namespaces": true}); err != nil {
		t.Errorf("no namespace beside every namespace was refused: %v", err)
	}
}

// A cap on the list, so that a typo cannot start hundreds of calls a minute.
func TestMoreThanThirtyNamespacesAreRefusedBeforeAnythingIsAsked(t *testing.T) {
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("team-%02d", i)
		}
		return out
	}
	c := newScriptedCluster(t)
	_, err := overviewOf(t, map[string]any{"namespace": names(31)})
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "kube.namespaces.toomany" {
		t.Fatalf("error = %v, want kube.namespaces.toomany", err)
	}
	if !strings.Contains(verr.Message, "30") {
		t.Errorf("the message never says the cap: %s", verr.Message)
	}
	// The list may be the profile's fence filled in by the host, whose size is
	// an inventory a refusal must not hand to the caller.
	if strings.Contains(verr.Message, "31") {
		t.Errorf("the message says how many namespaces the call reaches: %s", verr.Message)
	}
	if !strings.Contains(verr.Hint, "fewer") {
		t.Errorf("the hint never says to name or fence fewer: %q", verr.Hint)
	}
	for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceMCP} {
		_, err := runOverview(context.Background(), saReq(sf, false, map[string]any{"namespace": names(31)}))
		if verr, ok := err.(*view.Error); !ok || verr.Hint == "" {
			t.Errorf("%s: no hint beside the refusal: %v", sf, err)
		}
	}
	if calls := c.calls(t); len(calls) != 0 {
		t.Errorf("kubectl was run for a list that was refused:\n%s", strings.Join(calls, "\n"))
	}

	if _, err := overviewOf(t, map[string]any{"namespace": names(30)}); err != nil {
		t.Errorf("thirty namespaces were refused: %v", err)
	}
	if n := getCalls(t, c); n != 30*4+2 {
		t.Errorf("%d lists for thirty namespaces, want %d", n, 30*4+2)
	}
}

// The host holds each element of a list to the fence, element by element: every
// one inside it passes and one outside it is refused, and so is the switch that
// reads past it. What a call that names none resolves to under a fence of
// several depends on the rta release this is built against (refused as blank
// before the list-fence change, the whole fence after it), so it is not asserted.
func TestAFenceOfSevenHoldsEachNamespaceOfTheList(t *testing.T) {
	overview := overviewCapability(t)
	fence := plugin.Fence{"namespace": toAny(homelabNamespaces)}

	for name, caller := range map[string]map[string]any{
		"all seven":   {"namespace": homelabNamespaces},
		"three":       {"namespace": []string{"gitea", "rustfs", "crowdsec"}},
		"over MCP":    {"namespace": []any{"gitea", "vault-system"}},
		"a bare name": {"namespace": "gitea"},
	} {
		in := plugin.Inputs{Fence: fence, Caller: caller}
		if breach := plugin.CheckFence(overview, in); breach != nil {
			t.Errorf("%s: a list inside the fence was refused: %+v", name, breach)
			continue
		}
		req := plugin.ResolveRequest(overview, in, false, false)
		if got := req.StringSlice("namespace"); len(got) == 0 {
			t.Errorf("%s: the handler reads no namespace from %v", name, caller)
		}
	}

	for name, caller := range map[string]map[string]any{
		"one outside":        {"namespace": []string{"gitea", "payments"}},
		"only outside":       {"namespace": []string{"payments"}},
		"every namespace":    {"all-namespaces": true},
		"a list and the all": {"namespace": []string{"gitea", "rustfs"}, "all-namespaces": true},
		"a blank element":    {"namespace": []string{"gitea", ""}},
	} {
		breach := plugin.CheckFence(overview, plugin.Inputs{Fence: fence, Caller: caller})
		if breach == nil {
			t.Errorf("%s got past a fence on the namespace", name)
			continue
		}
		if breach.Input.Name != "namespace" {
			t.Errorf("%s: the breach is of %q, want namespace", name, breach.Input.Name)
		}
	}
}

func toAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// A fence of one namespace still fills the input with that namespace, in the
// shape the input reads.
func TestAFenceOfOneNamespaceStillFillsAnOverviewThatNamesNone(t *testing.T) {
	c := aHomelab(t)
	overview := overviewCapability(t)
	in := plugin.Inputs{Fence: plugin.Fence{"namespace": {"gitea"}}, Caller: map[string]any{}}
	if breach := plugin.CheckFence(overview, in); breach != nil {
		t.Fatalf("a call that names nothing was refused under a one-entry fence: %+v", breach)
	}
	req := plugin.ResolveRequest(overview, in, false, false)
	if got := req.StringSlice("namespace"); len(got) != 1 || got[0] != "gitea" {
		t.Fatalf("the fence filled namespace = %v, want gitea", got)
	}
	if _, err := runOverview(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := namespacesRead(t, c.listed(t, "pods")); strings.Join(got, ",") != "gitea" {
		t.Errorf("pods were listed for %v, want gitea", got)
	}
}

// The credential a Role in each namespace makes: every list across namespaces is
// refused, every list inside one answers. A page that read across and kept what
// it was allowed would be empty for this identity, and one that asked would be
// refused outright.
func TestACredentialThatMayOnlyListInsideANamespaceGetsTheCompletePage(t *testing.T) {
	c := aHomelab(t)
	c.forbidClusterWide(t)
	pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)
	if rows["status"] != "ok" {
		t.Errorf("status = %q, want ok", rows["status"])
	}
	for key, want := range map[string]string{"pods": "14", "deployments": "7", "certs": "7", "quotas": "7"} {
		if rows[key] != want {
			t.Errorf("%s = %q, want %q", key, rows[key], want)
		}
	}
	for _, line := range c.calls(t) {
		if strings.Contains(line, "--all-namespaces") {
			t.Errorf("a list across namespaces was asked for: %q", line)
		}
	}
}

// A token with no cluster-scoped rights at all cannot list the nodes or the
// namespaces either, and the page is the rest of what it can read: the nodes row
// says so, and the namespaces it cannot count are not what refuses the page.
func TestACredentialThatMayNotListNodesOrNamespacesStillGetsThePodsOfItsNamespaces(t *testing.T) {
	c := aHomelab(t)
	c.forbidClusterWide(t)
	refusal := `Error from server (Forbidden): %s is forbidden: User "system:serviceaccount:ops:rta" cannot list resource "%s" in API group "" at the cluster scope`
	c.write(t, "list-nodes.fail", fmt.Sprintf(refusal, "nodes", "nodes"))
	c.write(t, "list-namespaces.fail", fmt.Sprintf(refusal, "namespaces", "namespaces"))

	pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)
	if rows["pods"] != "14" || rows["deployments"] != "7" {
		t.Errorf("pods = %q, deployments = %q, want the namespaces' own", rows["pods"], rows["deployments"])
	}
	if !strings.HasPrefix(rows["nodes"], "could not be read — ") {
		t.Errorf("nodes = %q, want the refusal named", rows["nodes"])
	}
	if want := "warn — nodes could not be read"; rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}

	detail, err := overviewOf(t, map[string]any{"namespace": homelabNamespaces, "detail": true})
	if err != nil {
		t.Fatal(err)
	}
	secs, ok := detail.(view.Sections)
	if !ok {
		t.Fatalf("detail is %T", detail)
	}
	if got := keyValues(t, secs.Items[0].View)["namespaces"]; !strings.HasPrefix(got, "could not be read — ") {
		t.Errorf("namespaces = %q, want the refusal named", got)
	}
}

// The credential that lists inside a namespace and nowhere across them is the one
// that cannot list the namespaces either, and a call that names its namespaces is
// asking about them, not about the cluster's: the list of the cluster's is a row
// of the detailed page that says it could not be read, and not the end of the
// page, for one name as for several. A call that names none has only that list
// to say the cluster answers at all, and its refusal is still the page.
func TestACredentialThatMayNotListNamespacesStillGetsThePageOfTheNamespacesItNames(t *testing.T) {
	for name, tc := range map[string]struct {
		names []string
		pods  string
	}{
		"one namespace":    {[]string{"gitea"}, "2"},
		"two namespaces":   {[]string{"gitea", "rustfs"}, "4"},
		"seven namespaces": {homelabNamespaces, "14"},
	} {
		t.Run(name, func(t *testing.T) {
			c := aHomelab(t)
			c.forbidClusterWide(t)
			c.write(t, "list-namespaces.fail", `Error from server (Forbidden): namespaces is forbidden: User "system:serviceaccount:ops:rta" `+
				`cannot list resource "namespaces" in API group "" at the cluster scope`)

			pairs := compactOf(t, map[string]any{"namespace": tc.names})
			assertTileShape(t, pairs)
			rows := rowsOf(pairs)
			if rows["status"] != "ok" || rows["pods"] != tc.pods {
				t.Errorf("status = %q, pods = %q, want ok and the %s pods of the namespaces named", rows["status"], rows["pods"], tc.pods)
			}

			detail, err := overviewOf(t, map[string]any{"namespace": tc.names, "detail": true})
			if err != nil {
				t.Fatal(err)
			}
			secs, ok := detail.(view.Sections)
			if !ok {
				t.Fatalf("detail is %T, want the sections of a page", detail)
			}
			cluster := keyValues(t, secs.Items[0].View)
			if got := cluster["namespaces"]; !strings.HasPrefix(got, "could not be read — ") {
				t.Errorf("namespaces = %q, want the refusal named", got)
			}
			if cluster["cluster"] != "answering" {
				t.Errorf("cluster = %q, want the page of a cluster that answered", cluster["cluster"])
			}
		})
	}

	c := aHomelab(t)
	c.forbidClusterWide(t)
	c.write(t, "list-namespaces.fail", `Error from server (Forbidden): namespaces is forbidden: User "u" cannot list resource "namespaces"`)
	for name, values := range map[string]map[string]any{
		"none named":      {},
		"every namespace": {"all-namespaces": true},
	} {
		if got := rowsOf(compactOf(t, values))["status"]; got != "fail — the cluster answered, and refused" {
			t.Errorf("%s: status = %q, want the refusal to be the page", name, got)
		}
	}
}

// A page that could read none of what it is made of is a failure, not a warning,
// whatever the number of namespaces it was asked about: a tile that goes yellow
// for a token that is refused everything is a tile that says it is mostly well.
func TestAPageThatCouldReadNothingAtAllIsAFailureWhateverTheNamespacesNamed(t *testing.T) {
	for name, names := range map[string][]string{
		"one namespace":    {"gitea"},
		"two namespaces":   {"gitea", "rustfs"},
		"seven namespaces": homelabNamespaces,
		"none named":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			c := aHomelab(t)
			c.write(t, "get.fail", `Error from server (Forbidden): resource is forbidden: User "system:serviceaccount:ops:rta" `+
				`cannot list resource "resource" in API group "" at the cluster scope`)
			values := map[string]any{}
			if names != nil {
				values["namespace"] = names
			}
			pairs := compactOf(t, values)
			assertTileShape(t, pairs)
			if got := rowsOf(pairs)["status"]; !strings.HasPrefix(got, "fail — ") {
				t.Errorf("status = %q, want a failure", got)
			}
		})
	}

	// The nodes and every list of the namespaces named are refused: nothing was read.
	c := aHomelab(t)
	c.write(t, "list-nodes.fail", `Error from server (Forbidden): nodes is forbidden: User "u" cannot list resource "nodes"`)
	for _, kind := range []string{"pods", "deployments", "resourcequotas", "secrets"} {
		for _, ns := range []string{"gitea", "rustfs"} {
			c.failIn(t, kind, ns)
		}
	}
	if got := rowsOf(compactOf(t, map[string]any{"namespace": []string{"gitea", "rustfs"}}))["status"]; !strings.HasPrefix(got, "fail — ") {
		t.Errorf("status = %q, want a failure when nothing of the namespaces was read either", got)
	}
	// Some of it read is a warning of what was not, as it was.
	c = aHomelab(t)
	c.write(t, "list-nodes.fail", `Error from server (Forbidden): nodes is forbidden: User "u" cannot list resource "nodes"`)
	if got := rowsOf(compactOf(t, map[string]any{"namespace": []string{"gitea", "rustfs"}}))["status"]; got != "warn — nodes could not be read" {
		t.Errorf("status = %q, want only a warning when the namespaces were read", got)
	}
}

// A namespace the credential may not read is named, what it could not read of it
// is said, the verdict says so, and the other namespaces still answer.
func TestANamespaceThatCannotBeReadIsNamedAndTheOthersStillAnswer(t *testing.T) {
	c := aHomelab(t)
	c.failIn(t, "pods", "gitea")
	pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)

	if !strings.HasPrefix(rows["pods"], "12 · could not be read in gitea") || !strings.Contains(rows["pods"], "forbidden") {
		t.Errorf("pods = %q, want the twelve that answered and gitea named with why", rows["pods"])
	}
	if want := "warn — pods could not be read in gitea"; rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}
	for key, want := range map[string]string{"deployments": "7", "certs": "7", "quotas": "7"} {
		if rows[key] != want {
			t.Errorf("%s = %q, want %q: the other lists are not affected", key, rows[key], want)
		}
	}

	// A pod that is not ready in a namespace that answered is still the worst fact.
	c.listIn(t, "pods", "rustfs",
		podFx(t, "rustfs", "rustfs-0", "Running", false, 7, "CrashLoopBackOff", "n1"),
		podFx(t, "rustfs", "rustfs-1", "Running", true, 0, "", "n2"))
	pairs = compactOf(t, map[string]any{"namespace": homelabNamespaces})
	assertTileShape(t, pairs)
	rows = rowsOf(pairs)
	if !strings.HasPrefix(rows["status"], "fail — 1 pod not ready") || !strings.Contains(rows["status"], "+1") {
		t.Errorf("status = %q, want the pod first and the unread pods counted behind it", rows["status"])
	}
	if !strings.Contains(rows["pods"], "could not be read in gitea") || !strings.Contains(rows["pods"], "CrashLoopBackOff 1") {
		t.Errorf("pods = %q, want both the pod that is not ready and the namespace unread", rows["pods"])
	}
	if !strings.Contains(rows["pods"], "rustfs/rustfs-0") && !strings.Contains(rows["pods"], "CrashLoopBackOff 1") {
		t.Errorf("pods = %q", rows["pods"])
	}

	detail, err := overviewOf(t, map[string]any{"namespace": homelabNamespaces, "detail": true})
	if err != nil {
		t.Fatal(err)
	}
	secs := detail.(view.Sections)
	cluster := keyValues(t, secs.Items[0].View)
	if got := cluster["pods unread"]; !strings.HasPrefix(got, "gitea — ") || !strings.Contains(got, "cannot list resource") {
		t.Errorf("pods unread = %q, want the namespace and the whole reason", got)
	}
}

// A row that names what is wrong in one namespace and what could not be read in
// another has two things it must say, and both stay on one line of a tile.
func TestARowThatNamesTroubleAndAnUnreadNamespaceStillFitsATile(t *testing.T) {
	for _, tc := range []struct {
		kind, key string
		trouble   func(c *scriptedCluster)
		wants     []string
	}{
		{"pods", "pods", func(c *scriptedCluster) {
			c.listIn(t, "pods", "rustfs", podFx(t, "rustfs", "rustfs-0", "Running", false, 41, "CrashLoopBackOff", "n1"))
		}, []string{"CrashLoopBackOff 1"}},
		{"deployments", "deployments", func(c *scriptedCluster) {
			c.listIn(t, "deployments", "rustfs", depFx(t, "rustfs", "rustfs-gateway-with-a-long-name", 3, 0))
		}, []string{"1 short: rustfs/rustfs-gateway"}},
		{"resourcequotas", "quotas", func(c *scriptedCluster) {
			c.listIn(t, "resourcequotas", "rustfs", quotaFx(t, "rustfs", "compute-resources",
				map[string]string{"requests.memory": "10Gi"}, map[string]string{"requests.memory": "9Gi"}))
		}, []string{"1 over 80%: rustfs/compute-resources"}},
		{"secrets", "certs", func(c *scriptedCluster) {
			c.secretsIn(t, "rustfs", secretItem("rustfs", "gateway-tls", selfSignedPEM(t, "g", time.Now().Add(-time.Hour)), "KEY"))
		}, []string{"1 expired: rustfs/gateway-tls"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			c := aHomelab(t)
			tc.trouble(c)
			c.failIn(t, tc.kind, "gitea")
			pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
			assertTileShape(t, pairs)
			row := rowsOf(pairs)[tc.key]
			for _, want := range append(tc.wants, "gitea") {
				if !strings.Contains(row, want) {
					t.Errorf("%s = %q, want it to say %q", tc.key, row, want)
				}
			}
		})
	}
}

func TestAKindNoNamespaceCouldBeReadForSaysSoWithTheFirstReason(t *testing.T) {
	c := aHomelab(t)
	for _, ns := range homelabNamespaces {
		c.failIn(t, "resourcequotas", ns)
	}
	pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)
	if !strings.HasPrefix(rows["quotas"], "could not be read in 7 namespaces — (Forbidden): resourcequot") {
		t.Errorf("quotas = %q", rows["quotas"])
	}
	if want := "warn — quotas could not be read in 7 namespaces"; rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}
	if rows["pods"] != "14" {
		t.Errorf("pods = %q, want the other lists unaffected", rows["pods"])
	}
}

func TestACertificateReadThatFailsInOneNamespaceIsNamedAndNotKept(t *testing.T) {
	c := aHomelab(t)
	c.failIn(t, "secrets", "vault-system")
	rows := rowsOf(compactOf(t, map[string]any{"namespace": homelabNamespaces}))
	if !strings.HasPrefix(rows["certs"], "6 · could not be read in vault-system") {
		t.Errorf("certs = %q", rows["certs"])
	}
	if want := "warn — certs could not be read in vault-system"; rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}
	if err := os.Remove(filepath.Join(c.dir, "secrets.vault-system.fail")); err != nil {
		t.Fatal(err)
	}
	rows = rowsOf(compactOf(t, map[string]any{"namespace": homelabNamespaces}))
	if rows["certs"] != "7" || rows["status"] != "ok" {
		t.Errorf("certs = %q, status = %q after the namespace answered, want 7 and ok", rows["certs"], rows["status"])
	}
	if n := len(c.secretLists(t)); n != 8 {
		t.Errorf("%d Secret reads, want 7 and the one that failed asked again:\n%s", n, strings.Join(c.secretLists(t), "\n"))
	}
}

// Each namespace's certificates are a read of their own, kept for a quarter of
// an hour under that namespace, so a call for fewer of them is served from the
// seven and one for a namespace not read yet costs one.
func TestTheCertificatesAreKeptPerNamespaceAndASubsetIsServedFromTheSeven(t *testing.T) {
	c := aHomelab(t)
	compactOf(t, map[string]any{"namespace": homelabNamespaces})
	if n := len(c.secretLists(t)); n != 7 {
		t.Fatalf("%d Secret reads for seven namespaces, want 7:\n%s", n, strings.Join(c.secretLists(t), "\n"))
	}
	for _, line := range c.secretLists(t) {
		if !strings.Contains(line, "--context=homelab") {
			t.Errorf("%q: the read is not pinned to the context it is kept under", line)
		}
	}

	rows := rowsOf(compactOf(t, map[string]any{"namespace": []string{"gitea", "rustfs", "crowdsec"}}))
	if rows["certs"] != "3" {
		t.Errorf("certs = %q, want the three of the subset", rows["certs"])
	}
	if n := len(c.secretLists(t)); n != 7 {
		t.Errorf("a subset made %d Secret reads in all, want the seven it was served from", n)
	}

	compactOf(t, map[string]any{"namespace": []string{"gitea", "payments"}})
	if got := c.secretLists(t); len(got) != 8 || !strings.Contains(got[7], "--namespace=payments") {
		t.Errorf("a namespace not read before cost %d reads, want one more for payments:\n%s", len(got)-7, strings.Join(got, "\n"))
	}

	certListRun(t, map[string]any{"namespace": "gitea"})
	if n := len(c.secretLists(t)); n != 8 {
		t.Errorf("kube.cert.list for a namespace the overview had read made a read of its own (%d in all)", n)
	}

	ageCertReads(certLife + time.Minute)
	compactOf(t, map[string]any{"namespace": []string{"gitea", "rustfs"}})
	if n := len(c.secretLists(t)); n != 10 {
		t.Errorf("%d Secret reads after the lifetime, want the two namespaces read again", n)
	}
}

func TestWhatTheSevenReadsKeepIsStillNothingOfTheSecret(t *testing.T) {
	c := aHomelab(t)
	const key = "MARKER-PRIVATE-KEY-MUST-NOT-BE-KEPT"
	crt := selfSignedPEM(t, "kept.example.com", time.Now().Add(90*24*time.Hour))
	c.secretsIn(t, "gitea", secretItem("gitea", "gitea-tls", crt, key))
	compactOf(t, map[string]any{"namespace": homelabNamespaces})

	var kept string
	certReads.Lock()
	for scope, e := range certReads.byScope {
		kept += fmt.Sprintf("%v %#v\n", scope, e.certs)
	}
	certReads.Unlock()
	if !strings.Contains(kept, "kept.example.com") {
		t.Fatalf("nothing of the certificate was kept:\n%s", kept)
	}
	for _, forbidden := range []string{key, crt} {
		if strings.Contains(kept, forbidden) {
			t.Errorf("the kept reads hold %q", forbidden)
		}
	}
	if got := len(certReads.byScope); got != 7 {
		t.Errorf("%d kept reads, want one for each namespace", got)
	}
}

// The kept reads are one for each namespace ever named, and a plugin lives as
// long as the server that started it: what is older than the lifetime is dropped
// when a read is kept, and a read that failed leaves nothing behind.
func TestTheKeptReadsOfNamespacesNotAskedForAreDroppedOnceTheyAreOld(t *testing.T) {
	kept := func() []string {
		certReads.Lock()
		defer certReads.Unlock()
		var out []string
		for scope := range certReads.byScope {
			out = append(out, scope.namespace)
		}
		sort.Strings(out)
		return out
	}
	c := aHomelab(t)
	compactOf(t, map[string]any{"namespace": homelabNamespaces})
	if got := kept(); strings.Join(got, ",") != strings.Join(homelabNamespaces, ",") {
		t.Fatalf("kept reads = %v, want one for each namespace named", got)
	}

	compactOf(t, map[string]any{"namespace": []string{"payments"}})
	if got := kept(); len(got) != 8 {
		t.Errorf("kept reads = %v, want the seven that are still fresh and payments", got)
	}

	ageCertReads(certLife + time.Minute)
	compactOf(t, map[string]any{"namespace": []string{"gitea", "ghost"}})
	if got := kept(); strings.Join(got, ",") != "ghost,gitea" {
		t.Errorf("kept reads = %v after the lifetime, want only the two that were just read", got)
	}

	c.failIn(t, "secrets", "rustfs")
	compactOf(t, map[string]any{"namespace": []string{"gitea", "rustfs", "vault-system"}})
	if got := kept(); strings.Join(got, ",") != "ghost,gitea,vault-system" {
		t.Errorf("kept reads = %v, want none for rustfs, whose read failed", got)
	}
}

func TestANamedNamespaceThatDoesNotExistIsASayingOfTheDetailAndAWarningOfTheVerdict(t *testing.T) {
	aHomelab(t)
	names := append(slices.Clone(homelabNamespaces), "ghost")
	pairs := compactOf(t, map[string]any{"namespace": names})
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)
	if want := "warn — namespace ghost does not exist"; rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}

	v, err := overviewOf(t, map[string]any{"namespace": names, "detail": true})
	if err != nil {
		t.Fatalf("a namespace that does not exist was an error for the page: %v", err)
	}
	cluster := keyValues(t, v.(view.Sections).Items[0].View)
	if cluster["namespaces"] != "7 of 8 named" {
		t.Errorf("namespaces = %q, want the named ones that exist counted", cluster["namespaces"])
	}
	if cluster["namespace missing"] != "ghost" {
		t.Errorf("namespace missing = %q, want ghost", cluster["namespace missing"])
	}
	if !strings.Contains(cluster["namespace"], "ghost") || !strings.Contains(cluster["namespace"], "the only namespaces") {
		t.Errorf("namespace = %q, want the list and that only those are read", cluster["namespace"])
	}

	healthy, err := overviewOf(t, map[string]any{"namespace": homelabNamespaces, "detail": true})
	if err != nil {
		t.Fatal(err)
	}
	cluster = keyValues(t, healthy.(view.Sections).Items[0].View)
	if cluster["namespaces"] != "7 of 7 named" {
		t.Errorf("namespaces = %q", cluster["namespaces"])
	}
	for key := range cluster {
		if strings.Contains(key, "missing") {
			t.Errorf("a row %q on a page whose namespaces all exist", key)
		}
	}
}

// Names of what is wrong carry the namespace in front, as they do when every
// namespace is read, and the page is the one a tile can show.
func TestTheCompactPageOfSeveralNamespacesNamesThingsWithTheirNamespaceAndFitsATile(t *testing.T) {
	c := aHomelab(t)
	c.listIn(t, "pods", "gitea",
		podFx(t, "gitea", "gitea-0", "Running", true, 0, "", "n1"),
		podFx(t, "gitea", "gitea-runner-0", "Running", false, 41, "CrashLoopBackOff", "n1"))
	c.listIn(t, "deployments", "gitea", depFx(t, "gitea", "gitea", 2, 2), depFx(t, "gitea", "runner", 1, 0))
	c.listIn(t, "resourcequotas", "prometheus-stack", quotaFx(t, "prometheus-stack", "compute",
		map[string]string{"pods": "50"}, map[string]string{"pods": "48"}))
	c.secretsIn(t, "keycloak-system", secretItem("keycloak-system", "kc-tls", selfSignedPEM(t, "kc", time.Now().Add(-time.Hour)), "KEY"))

	pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)
	for key, wants := range map[string][]string{
		"status":      {"fail — 1 pod not ready"},
		"pods":        {"14 · CrashLoopBackOff 1"},
		"deployments": {"8", "1 short", "gitea/runner 0/1"},
		"certs":       {"7", "1 expired", "keycloak-system/kc-tls"},
		"quotas":      {"7", "1 over 80%", "prometheus-stack/compute pods 96%"},
		"context":     {"homelab · 7 namespaces only"},
	} {
		for _, want := range wants {
			if !strings.Contains(rows[key], want) {
				t.Errorf("%s = %q, want it to say %q", key, rows[key], want)
			}
		}
	}
}

func TestTheDetailedPageOfSeveralNamespacesListsWhatItListedAndSaysWhichNamespacesItRead(t *testing.T) {
	c := aHomelab(t)
	c.listIn(t, "pods", "gitea",
		podFx(t, "gitea", "gitea-runner-0", "Running", false, 41, "CrashLoopBackOff", "n1"))
	v, err := overviewOf(t, map[string]any{"namespace": homelabNamespaces, "detail": true})
	if err != nil {
		t.Fatal(err)
	}
	secs := v.(view.Sections)
	ids := map[string]bool{}
	for _, s := range secs.Items {
		ids[s.ID] = true
	}
	for _, id := range []string{"cluster", "nodes", "pods", "deployments"} {
		if !ids[id] {
			t.Errorf("no %q section in %v", id, ids)
		}
	}
	cluster := keyValues(t, secs.Items[0].View)
	for _, name := range homelabNamespaces {
		if !strings.Contains(cluster["namespace"], name) {
			t.Errorf("namespace = %q, want %s named", cluster["namespace"], name)
		}
	}
	if !strings.HasPrefix(cluster["pods"], "13, 1 not ready") || cluster["not ready"] != "gitea/gitea-runner-0" {
		t.Errorf("pods = %q, not ready = %q", cluster["pods"], cluster["not ready"])
	}
}

// At most six lists run at once, whatever the number of namespaces: thirty
// subprocesses started together are a laptop's fans and an API server's
// throttle.
func TestNoMoreThanSixListsRunAtOnce(t *testing.T) {
	c := aHomelab(t)
	c.write(t, "conc.slow", "")
	compactOf(t, map[string]any{"namespace": homelabNamespaces})
	raw, err := os.ReadFile(filepath.Join(c.dir, "conc"))
	if err != nil {
		t.Fatal(err)
	}
	running, peak := 0, 0
	for _, word := range strings.Fields(string(raw)) {
		switch word {
		case "up":
			running++
			peak = max(peak, running)
		case "down":
			running--
		}
	}
	if peak > 6 {
		t.Errorf("%d lists ran at once, want at most 6", peak)
	}
	if peak < 2 {
		t.Errorf("%d list at a time: the reads are not concurrent", peak)
	}
}

// A cluster that does not answer is asked six times and not thirty: the reads
// that were waiting for a place are given the answer the first ones got. The gate
// can only say so when classify() has, and kubectl words a cluster that cannot be
// reached in more ways than one: each of these once fell to kube.failed, which is
// one namespace's own trouble, and asked the cluster all thirty times.
func TestAClusterThatDoesNotAnswerIsNotAskedAgainForEachNamespace(t *testing.T) {
	const unreachable = "fail — the cluster did not answer"
	for name, tc := range map[string]struct{ stderr, code, status string }{
		"an i/o timeout": {"Unable to connect to the server: dial tcp 10.0.0.1:6443: i/o timeout",
			"kube.unreachable", unreachable},
		"a refused connection": {"The connection to the server 10.0.0.1:6443 was refused - did you specify the right host or port?",
			"kube.unreachable", unreachable},
		"a TLS handshake timeout": {"Unable to connect to the server: net/http: TLS handshake timeout",
			"kube.unreachable", unreachable},
		"a request timeout": {"context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
			"kube.unreachable", unreachable},
		"a request timeout, said by kubectl": {"Unable to connect to the server: context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
			"kube.unreachable", unreachable},
		"a connection closed": {"Unable to connect to the server: EOF",
			"kube.unreachable", unreachable},
		"a connection reset": {"Unable to connect to the server: read tcp 10.0.0.2:5555->10.0.0.1:6443: read: connection reset by peer",
			"kube.unreachable", unreachable},
		"no route": {"Unable to connect to the server: dial tcp 10.0.0.1:6443: connect: no route to host",
			"kube.unreachable", unreachable},
		"a credential plugin that refused": {lapsedTsh, "kube.login", "fail — the cluster was never contacted"},
		"a credential plugin that is gone": {missingPath, "kube.credential.missing", "fail — the cluster was never contacted"},
	} {
		t.Run(name, func(t *testing.T) {
			if verr := classify(context.Background(), errors.New("exit status 1"), tc.stderr,
				[]string{"get", "pods"}, plugin.SurfaceCLI); verr.Code != tc.code || !clusterWide(verr.Code) {
				t.Errorf("classified as %q (cluster wide: %v), want %s", verr.Code, clusterWide(verr.Code), tc.code)
			}

			c := aHomelab(t)
			c.write(t, "conc.slow", "")
			c.write(t, "get.fail", tc.stderr)
			pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
			if got := rowsOf(pairs)["status"]; got != tc.status {
				t.Errorf("status = %q, want %q", got, tc.status)
			}
			if n := getCalls(t, c); n < 1 || n > 6 {
				t.Errorf("%d lists asked of a cluster that does not answer, want at most the 6 that fit at once", n)
			}
		})
	}
}

// What is the trouble of one object, or one namespace, is never the cluster's: it
// is read as it was and does not stop the asking. A webhook that timed out is the
// API server saying so about the object, with the same words a dead cluster uses.
func TestAFailureOfOneThingIsNotTakenForAClusterThatIsDown(t *testing.T) {
	for name, tc := range map[string]struct{ stderr, code string }{
		"a refusal": {`Error from server (Forbidden): pods is forbidden: User "u" cannot list resource "pods" in the namespace "gitea"`,
			"kube.denied"},
		"a name that is not there": {`Error from server (NotFound): namespaces "ghost" not found`, "kube.notfound"},
		"a webhook that timed out": {`Error from server (InternalError): Internal error occurred: failed calling webhook "v.example.io": ` +
			`failed to call webhook: Post "https://svc.ns.svc:443/validate": context deadline exceeded`, "kube.failed"},
		"a webhook that could not be dialled": {`Error from server (InternalError): failed calling webhook "v.example.io": ` +
			`failed to call webhook: Post "https://svc.ns.svc:443/validate": net/http: request canceled ` +
			`(Client.Timeout exceeded while awaiting headers)`, "kube.failed"},
		"a request the server timed out": {`Error from server (Timeout): the server was unable to return a response in the ` +
			`time allotted, but may still be processing the request (get pods)`, "kube.failed"},
	} {
		t.Run(name, func(t *testing.T) {
			verr := classify(context.Background(), errors.New("exit status 1"), tc.stderr,
				[]string{"get", "pods"}, plugin.SurfaceCLI)
			if verr.Code != tc.code || clusterWide(verr.Code) {
				t.Errorf("classified as %q (cluster wide: %v), want %s and not the cluster's", verr.Code, clusterWide(verr.Code), tc.code)
			}
		})
	}
}

// What a refresh costs, counted in the kubectl calls the stub logged: the kubeconfig
// once, the namespaces and the nodes once each, and the pods, deployments, quotas
// and Secrets of each scope read.
func TestWhatARefreshCostsInKubectlCalls(t *testing.T) {
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("team-%02d", i)
		}
		return out
	}
	for name, tc := range map[string]struct {
		values       map[string]any
		config, gets int
	}{
		"every namespace":   {map[string]any{}, 1, 6},
		"one namespace":     {map[string]any{"namespace": []string{"gitea"}}, 1, 6},
		"seven namespaces":  {map[string]any{"namespace": homelabNamespaces}, 1, 30},
		"thirty namespaces": {map[string]any{"namespace": names(30)}, 1, 122},
		"seven, once kept":  {map[string]any{"namespace": homelabNamespaces}, 1, 30 - 7},
		"thirty, once kept": {map[string]any{"namespace": names(30)}, 1, 122 - 30},
		"one, once kept":    {map[string]any{"namespace": []string{"gitea"}}, 1, 6 - 1},
		"every one, kept":   {map[string]any{}, 1, 6 - 1},
	} {
		t.Run(name, func(t *testing.T) {
			c := aHomelab(t)
			if strings.Contains(name, "kept") {
				compactOf(t, tc.values)
				c.write(t, "calls", "")
			}
			compactOf(t, tc.values)
			config := 0
			for _, line := range c.calls(t) {
				if strings.HasPrefix(line, "config view") {
					config++
				}
			}
			if config != tc.config || getCalls(t, c) != tc.gets {
				t.Errorf("%d config view and %d gets, want %d and %d:\n%s",
					config, getCalls(t, c), tc.config, tc.gets, strings.Join(c.calls(t), "\n"))
			}
		})
	}
}

// A kubeconfig that cannot be read is asked for once and not again for the
// certificates of each namespace: those would each ask the same file the same
// question, and the page is the same one that has no cluster named.
func TestAKubeconfigThatCannotBeReadIsAskedForOnceAndNotOncePerNamespace(t *testing.T) {
	c := aHomelab(t)
	c.write(t, "config.fail", "error: something nobody has classified yet")
	pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)
	if rows["pods"] != "14" || rows["certs"] != "7" {
		t.Errorf("pods = %q, certs = %q: the page is not the one a cluster that answered gets", rows["pods"], rows["certs"])
	}
	config := 0
	for _, line := range c.calls(t) {
		if strings.HasPrefix(line, "config view") {
			config++
		}
	}
	if config != 1 {
		t.Errorf("the kubeconfig was asked for %d times, want once:\n%s", config, strings.Join(c.calls(t), "\n"))
	}
	if n := len(c.secretLists(t)); n != 7 {
		t.Errorf("%d Secret reads, want one for each namespace:\n%s", n, strings.Join(c.secretLists(t), "\n"))
	}
	if len(certReads.byScope) != 0 {
		t.Errorf("%d reads were kept under a context nobody could name", len(certReads.byScope))
	}
}

// The single namespace and every namespace are the paths they were.
func TestEveryNamespaceIsStillOneCallPerKindAndTheSingleNamespaceOneCallPerKind(t *testing.T) {
	for name, tc := range map[string]struct {
		values map[string]any
		flag   string
	}{
		"every namespace": {map[string]any{}, "--all-namespaces"},
		"one namespace":   {map[string]any{"namespace": []string{"gitea"}}, "--namespace=gitea"},
	} {
		t.Run(name, func(t *testing.T) {
			c := aHomelab(t)
			compactOf(t, tc.values)
			if n := getCalls(t, c); n != 6 {
				t.Errorf("%d lists, want 6:\n%s", n, strings.Join(c.calls(t), "\n"))
			}
			for _, kind := range []string{"pods", "deployments", "resourcequotas", "secrets"} {
				lines := c.listed(t, kind)
				if len(lines) != 1 || !strings.Contains(lines[0], tc.flag) {
					t.Errorf("%s listed as %q, want one call with %s", kind, lines, tc.flag)
				}
			}
		})
	}
}

func TestTheNamespaceOfAnOverviewIsAListTheWayAPlainNamespaceWas(t *testing.T) {
	c := overviewCapability(t)
	var field plugin.Field
	for _, f := range c.Inputs {
		if f.Name == "namespace" {
			field = f
		}
	}
	if field.Type != plugin.StringSlice || field.Short != "n" || field.Config != "namespace" {
		t.Errorf("namespace is %+v, want a list with the short n bound to the config key namespace", field)
	}
	for name, text := range map[string]string{"Description": c.Description, "Agent": c.Agent, "Help": field.Help} {
		if !strings.Contains(text, "namespaces") {
			t.Errorf("%s never says that namespace is a list: %s", name, text)
		}
	}
	for name, text := range map[string]string{"Description": c.Description, "Agent": c.Agent} {
		if !strings.Contains(text, "its own") {
			t.Errorf("%s never says that each namespace is read with a call of its own: %s", name, text)
		}
		if !strings.Contains(text, fmt.Sprint(maxNamespaces)) {
			t.Errorf("%s never says the cap of %d: %s", name, maxNamespaces, text)
		}
	}
	if err := Plugin().Validate(); err != nil {
		t.Errorf("the declaration is refused: %v", err)
	}
	// The two siblings that act on one namespace (serviceaccount provision and
	// revoke) declare the same config key as text and refuse a list there, so the
	// one place a list is written in the config is the one place it is said not to
	// be; and the next edit of the text is not the one that trips the limit.
	if !strings.Contains(c.Description, "plugins.kube.namespace") {
		t.Errorf("the Description never says that a list does not belong in plugins.kube.namespace: %s", c.Description)
	}
	if n := utf8.RuneCountInString(c.Description); n > 1900 {
		t.Errorf("the Description is %d runes, which leaves less than 100 of the 2000 the declaration allows", n)
	}
	if c.Refresh != time.Minute {
		t.Errorf("refresh = %v, want the minute it was", c.Refresh)
	}
}
