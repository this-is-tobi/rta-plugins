package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// kube.namespace.list prints every namespace of the cluster and kube.pvc.usage the
// namespace and claim of every volume a kubelet reports, and neither declared a
// namespace, so a profile that fenced the namespace did not bind them. They take
// it now as a list that keeps rows: nothing is read per namespace, because neither
// read can be asked for fewer than everything, and what is not named is dropped
// before anything is returned.

// kubeletVolumes sets what a node's kubelet reports: one volume claimed by each of
// the namespaces it is given, which the node's other pods mount besides.
func (c *scriptedCluster) kubeletVolumes(t *testing.T, node string, namespaces ...string) {
	t.Helper()
	volumes := []obj{{"name": "token", "usedBytes": 4096, "capacityBytes": 8192, "availableBytes": 4096}}
	for _, ns := range namespaces {
		volumes = append(volumes, obj{
			"name": "data", "usedBytes": 5 << 30, "capacityBytes": 10 << 30, "availableBytes": 5 << 30,
			"pvcRef": obj{"name": ns + "-data", "namespace": ns},
		})
	}
	path := "/api/v1/nodes/" + node + "/proxy/stats/summary"
	c.write(t, "raw"+strings.ReplaceAll(path, "/", "_")+".json",
		jsonOf(t, obj{"node": obj{"nodeName": node}, "pods": []obj{{"volume": volumes}}}))
}

// everyNamespace is what the cluster of these tests holds of anything: the seven
// namespaces of the connection, and two beside them that no page of the seven may
// mention.
var everyNamespace = func() []string {
	all := append(slices.Clone(homelabNamespaces), "kube-system", "payments")
	slices.Sort(all)
	return all
}()

type filterCase struct {
	id  string
	run func(context.Context, plugin.Request) (view.View, error)
	// cluster is a cluster that holds something in every namespace.
	cluster func(t *testing.T) *scriptedCluster
	// reads is the kubectl calls one read of the cluster costs.
	reads int
}

var filterCases = []filterCase{
	{id: "kube.namespace.list", run: runNamespaceList, reads: 1, cluster: aHomelab},
	{id: "kube.pvc.usage", run: runPVCUsage, reads: 3, cluster: func(t *testing.T) *scriptedCluster {
		c := aHomelab(t)
		c.kubeletVolumes(t, "n1", "crowdsec", "keycloak-system", "prometheus-stack", "vault-system", "payments")
		c.kubeletVolumes(t, "n2", "gitea", "mattermost", "rustfs", "kube-system", "payments")
		return c
	}},
}

func (fc filterCase) capability(t *testing.T) plugin.Capability { return capabilityNamed(t, fc.id) }

func (fc filterCase) read(t *testing.T, req plugin.Request) view.Table {
	t.Helper()
	v, err := fc.run(context.Background(), req)
	if err != nil {
		t.Fatalf("%s: %v", fc.id, err)
	}
	table, ok := v.(view.Table)
	if !ok {
		t.Fatalf("%s: got %T, want a table", fc.id, v)
	}
	return table
}

func (fc filterCase) readNamed(t *testing.T, names ...string) view.Table {
	t.Helper()
	values := map[string]any{}
	if names != nil {
		values["namespace"] = names
	}
	return fc.read(t, saReq(plugin.SurfaceCLI, false, values))
}

// shown is the namespaces a table has a row for, once each and sorted.
func shown(table view.Table) []string {
	var out []string
	for _, row := range table.Rows {
		if !slices.Contains(out, row[0]) {
			out = append(out, row[0])
		}
	}
	slices.Sort(out)
	return out
}

// The two declare the namespace as a list that keeps rows, with the short and the
// config key of every other namespace input so that a fence on the key binds it,
// no Scope because nothing here is a record a grant is narrowed to (so the host
// fills a call that names none from the fence), no switch to widen it and no cap,
// since nothing is read for each name.
func TestTheTwoReadsThatShowEveryNamespaceTakeTheNamespaceAsAListAFenceFills(t *testing.T) {
	for _, fc := range filterCases {
		t.Run(fc.id, func(t *testing.T) {
			c := fc.capability(t)
			f := inputOf(c, "namespace")
			if f.Type != plugin.StringSlice || f.Short != "n" || f.Config != "namespace" {
				t.Errorf("namespace is %+v, want a list with the short n bound to the config key namespace", f)
			}
			if c.Scope != "" || !plugin.ProfileFillable(c, f) {
				t.Errorf("scope = %q: a fence could not fill a call that names no namespace", c.Scope)
			}
			if !f.Live || f.Suggest == nil {
				t.Error("namespace is not completed from the cluster on a press, which a list takes since rta v0.48.0")
			}
			if all := inputOf(c, "all-namespaces"); all.Name != "" {
				t.Errorf("all-namespaces is declared (%+v): there is nothing to widen, since the input keeps rows", all)
			}
			if !strings.Contains(c.Description, filterNamespaceWords) {
				t.Errorf("the Description never says what the namespaces do:\n%s", c.Description)
			}
			if c.Agent != "" && !strings.Contains(c.Agent, agentFilterNamespaceWords) {
				t.Errorf("the text for an agent never says what the namespaces do:\n%s", c.Agent)
			}
		})
	}
	if err := Plugin().Validate(); err != nil {
		t.Errorf("the declaration is refused: %v", err)
	}
}

// A profile that fences the namespace holds each name to it, so that neither read
// can be asked for a namespace outside what the connection reaches.
func TestAFenceRefusesANamespaceOutsideItOnTheTwoReads(t *testing.T) {
	fence := plugin.Fence{"namespace": toAny([]string{"crowdsec", "gitea", "rustfs"})}
	for _, fc := range filterCases {
		t.Run(fc.id, func(t *testing.T) {
			c := fc.capability(t)
			for name, caller := range map[string]map[string]any{
				"one inside":      {"namespace": []string{"gitea"}},
				"all three":       {"namespace": []string{"rustfs", "gitea", "crowdsec"}},
				"over MCP":        {"namespace": []any{"gitea", "crowdsec"}},
				"a bare name":     {"namespace": "gitea"},
				"none, filled":    {},
				"a repeated name": {"namespace": []string{"gitea", "gitea"}},
			} {
				if breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: caller}); breach != nil {
					t.Errorf("%s: a call inside the fence was refused: %+v", name, breach)
				}
			}
			for name, caller := range map[string]map[string]any{
				"one outside":      {"namespace": []string{"gitea", "payments"}},
				"only outside":     {"namespace": []string{"payments"}},
				"kube-system":      {"namespace": []string{"kube-system"}},
				"a blank element":  {"namespace": []string{"gitea", ""}},
				"an empty list":    {"namespace": []string{}},
				"an outside, bare": {"namespace": "payments"},
			} {
				breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: caller})
				if breach == nil || breach.Input.Name != "namespace" {
					t.Errorf("%s got past a fence on the namespace: %+v", name, breach)
				}
			}
		})
	}
}

func TestOnlyTheNamedNamespacesAreShownAndNothingOfTheOthersIsReturned(t *testing.T) {
	for _, fc := range filterCases {
		t.Run(fc.id, func(t *testing.T) {
			c := fc.cluster(t)
			table := fc.readNamed(t, "rustfs", "gitea", " rustfs", "")
			if got := shown(table); strings.Join(got, ",") != "gitea,rustfs" {
				t.Errorf("the page is made of %v, want gitea and rustfs", got)
			}
			page := fmt.Sprintf("%#v", table)
			for _, other := range []string{"payments", "kube-system", "crowdsec", "vault-system", "mattermost"} {
				if strings.Contains(page, other) {
					t.Errorf("the page names %s, which no call asked for:\n%s", other, page)
				}
			}
			if table.Total != len(table.Rows) {
				t.Errorf("total = %d for %d rows", table.Total, len(table.Rows))
			}
			if got := len(kubectlCalls(t, c)); got != fc.reads {
				t.Errorf("%d reads for two namespaces, want the %d the cluster costs: nothing is read for each name:\n%s",
					got, fc.reads, strings.Join(c.calls(t), "\n"))
			}
			for _, line := range c.calls(t) {
				if strings.Contains(line, "--namespace=") || strings.Contains(line, "--all-namespaces") {
					t.Errorf("%q: a namespace was passed to kubectl for a read that keeps rows", line)
				}
			}
		})
	}
}

// Naming none, with nothing to fence it, is the read it always was.
func TestNamingNoNamespaceOnTheTwoReadsShowsEveryNamespace(t *testing.T) {
	for _, fc := range filterCases {
		t.Run(fc.id, func(t *testing.T) {
			fc.cluster(t)
			table := fc.readNamed(t)
			if got := shown(table); strings.Join(got, ",") != strings.Join(everyNamespace, ",") {
				t.Errorf("the page is made of %v, want every namespace %v", got, everyNamespace)
			}
		})
	}
}

// The seam: under a fence a call that names none is handed the whole fence by the
// host (the namespace is no grant's scope here), and shows the namespaces of the
// fence and no other. Nothing is emulated; the handler reads the list it is given.
func TestACallThatNamesNoNamespaceUnderAFenceShowsOnlyTheFence(t *testing.T) {
	allowed := []string{"crowdsec", "gitea", "rustfs"}
	for _, fc := range filterCases {
		t.Run(fc.id, func(t *testing.T) {
			c := fc.capability(t)
			in := plugin.Inputs{Fence: plugin.Fence{"namespace": toAny(allowed)}, Caller: map[string]any{}}
			if breach := plugin.CheckFence(c, in); breach != nil {
				t.Fatalf("a call that names no namespace was refused under a fence: %+v", breach)
			}
			req := plugin.ResolveRequest(c, in, false, false)
			if got := req.StringSlice("namespace"); strings.Join(got, ",") != strings.Join(allowed, ",") {
				t.Fatalf("the host handed the handler %v, want the whole fence %v", got, allowed)
			}
			fc.cluster(t)
			table := fc.read(t, req)
			if got := shown(table); strings.Join(got, ",") != strings.Join(allowed, ",") {
				t.Errorf("the page is made of %v, want the fence %v and nothing outside it", got, allowed)
			}
			if page := fmt.Sprintf("%#v", table); strings.Contains(page, "payments") || strings.Contains(page, "kube-system") {
				t.Errorf("the page names a namespace outside the fence:\n%s", page)
			}
		})
	}
}

// The config key is the default of the lists of objects, and these two read it now:
// an operator who set one namespace there sees that namespace on both, and a caller
// that gives the input empty sees everything again, which only a fence can forbid.
func TestTheConfiguredNamespaceNarrowsTheTwoReadsAndAnEmptyOneLiftsIt(t *testing.T) {
	for _, fc := range filterCases {
		t.Run(fc.id, func(t *testing.T) {
			c := fc.capability(t)
			fc.cluster(t)
			config := map[string]any{"namespace": "gitea"}
			table := fc.read(t, plugin.ResolveRequest(c, plugin.Inputs{Config: config}, false, false))
			if got := shown(table); strings.Join(got, ",") != "gitea" {
				t.Errorf("the configured namespace shows %v, want gitea", got)
			}
			lifted := plugin.Inputs{Config: config, Caller: map[string]any{"namespace": []string{""}}}
			table = fc.read(t, plugin.ResolveRequest(c, lifted, false, false))
			if got := shown(table); strings.Join(got, ",") != strings.Join(everyNamespace, ",") {
				t.Errorf("an empty namespace shows %v, want every namespace", got)
			}
		})
	}
}

// More names than the lists of objects read is no trouble for a read that keeps
// rows, and a fence of dozens must not make the call that names none refuse.
func TestTheTwoReadsHaveNoCapOnTheNamespacesTheyKeep(t *testing.T) {
	names := append(manyNamespaces(40), "gitea")
	for _, fc := range filterCases {
		t.Run(fc.id, func(t *testing.T) {
			fc.cluster(t)
			table := fc.readNamed(t, names...)
			if got := shown(table); strings.Join(got, ",") != "gitea" {
				t.Errorf("the page is made of %v, want gitea, the one of forty-one that the cluster has", got)
			}
		})
	}
}

func TestANameThatIsNotANamespaceIsRefusedBeforeAnythingIsRead(t *testing.T) {
	for _, fc := range filterCases {
		t.Run(fc.id, func(t *testing.T) {
			c := fc.cluster(t)
			for _, name := range []string{"Gitea", "a/b", "--kubeconfig=/tmp/mine", "x/../../nodes"} {
				_, err := fc.run(context.Background(), saReq(plugin.SurfaceCLI, false, map[string]any{"namespace": []string{"gitea", name}}))
				if verr, ok := err.(*view.Error); !ok || verr.Code != "kube.name.invalid" {
					t.Errorf("%q: error = %v, want kube.name.invalid", name, err)
				}
			}
			if calls := c.calls(t); len(calls) != 0 {
				t.Errorf("kubectl was run for calls that were refused:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

// A namespace the cluster has nothing to show for is said and not left to read as
// a cluster with nothing in it: the sentence for a person, and for an agent, which
// is never shown an empty sentence, a warning.
func TestANamedNamespaceWithNothingToShowIsSaid(t *testing.T) {
	t.Run("kube.namespace.list", func(t *testing.T) {
		aHomelab(t)
		table := listOf(t, "gitea", "ghost", "phantom")
		if got := shown(table); strings.Join(got, ",") != "gitea" {
			t.Errorf("the page is made of %v, want gitea, the one that exists", got)
		}
		if len(table.Warnings) != 1 || table.Warnings[0].Code != "kube.namespace.missing" || !table.Warnings[0].Advisory ||
			!strings.Contains(table.Warnings[0].Message, "ghost, phantom") || strings.Contains(table.Warnings[0].Message, "gitea") {
			t.Errorf("warnings = %+v, want one that names ghost and phantom and is no gap in the page", table.Warnings)
		}

		table = listOf(t, "ghost")
		if len(table.Rows) != 0 || !strings.Contains(table.Empty, "ghost") || len(table.Warnings) != 1 {
			t.Errorf("rows %v, empty %q, warnings %v: a name the cluster lacks is an empty page that says so", table.Rows, table.Empty, table.Warnings)
		}
	})
	t.Run("kube.pvc.usage", func(t *testing.T) {
		fc := filterCaseOf(t, "kube.pvc.usage")
		fc.cluster(t)
		table := fc.readNamed(t, "ghost")
		if len(table.Rows) != 0 || !strings.Contains(table.Empty, "no volume claims are mounted in namespace ghost") {
			t.Errorf("rows %v, empty %q, want the sentence that says where it looked", table.Rows, table.Empty)
		}
	})
}

func filterCaseOf(t *testing.T, id string) filterCase {
	t.Helper()
	for _, fc := range filterCases {
		if fc.id == id {
			return fc
		}
	}
	t.Fatalf("no filter case %s", id)
	return filterCase{}
}

func listOf(t *testing.T, names ...string) view.Table {
	t.Helper()
	return filterCaseOf(t, "kube.namespace.list").readNamed(t, names...)
}
