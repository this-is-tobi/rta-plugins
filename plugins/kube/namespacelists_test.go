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

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The lists of objects take their namespace as a list, and read each namespace of
// it with a call of its own, the way kube.overview does: a credential bound by a
// Role in each namespace is refused a list across namespaces, and a fence is
// meant to keep the read inside what the connection reaches. These tests hold the
// eight of them to the same properties, with a kubectl that logs its calls.

// listCase is one of the lists, and what it costs and reads per namespace.
type listCase struct {
	id  string
	run func(context.Context, plugin.Request) (view.View, error)
	// resource is what `get` is asked for in each namespace, and calls is how many
	// kubectl calls one namespace costs.
	resource string
	calls    int
	// none is what a call that names no namespace passes to kubectl: nothing, for
	// kubectl to resolve, except where this plugin resolves the context's own
	// itself because what it reads is not a kubectl listing (the kept certificates
	// are kept under it, the usage list builds a path with it).
	none string
	// config is the `config view` calls a read of one namespace, or of none, makes.
	config int
	// reason is the column that carries the reason a namespace could not be read:
	// the one that names the object a row of the others has, except for the events,
	// whose message is the widest column and the one with the most to say in.
	reason string
	// seed sets what a namespace holds, and failWith makes it answer with stderr.
	seed     func(t *testing.T, c *scriptedCluster, ns string)
	failWith func(t *testing.T, c *scriptedCluster, ns, stderr string)
}

func eventFx(t *testing.T, ns, name string) string {
	t.Helper()
	at := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	return jsonOf(t, obj{
		"metadata":       obj{"name": name, "namespace": ns, "creationTimestamp": at},
		"type":           "Warning",
		"reason":         "BackOff",
		"message":        "back-off restarting " + name,
		"count":          3,
		"involvedObject": obj{"kind": "Pod", "name": name, "namespace": ns},
		"firstTimestamp": at,
		"lastTimestamp":  at,
	})
}

// threeDaysAgo is a creation time whose age is the same word on every run of a
// test, which an age of seconds is not.
func threeDaysAgo() string { return time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339) }

func pvcFx(t *testing.T, ns, name string) string {
	t.Helper()
	return jsonOf(t, obj{
		"metadata": obj{"name": name, "namespace": ns, "creationTimestamp": threeDaysAgo()},
		"spec":     obj{"storageClassName": "fast", "resources": obj{"requests": obj{"storage": "10Gi"}}},
		"status":   obj{"phase": "Bound", "capacity": obj{"storage": "10Gi"}},
	})
}

func accountFx(t *testing.T, ns, name string) string {
	t.Helper()
	return jsonOf(t, obj{"metadata": obj{
		"name": name, "namespace": ns, "creationTimestamp": threeDaysAgo(),
		"annotations": obj{"rta.dev/ttl": "1h", "rta.dev/issued-at": time.Now().UTC().Format(time.RFC3339)},
	}})
}

// failList makes the namespace's list of a resource answer with stderr.
func failList(resource string) func(*testing.T, *scriptedCluster, string, string) {
	return func(t *testing.T, c *scriptedCluster, ns, stderr string) { c.failInWith(t, resource, ns, stderr) }
}

// refuse makes the namespace what a Role that does not reach it is told, and
// reject what a cluster that does not accept the credential tells every call.
func (lc listCase) refuse(t *testing.T, c *scriptedCluster, ns string) {
	t.Helper()
	lc.failWith(t, c, ns, forbiddenIn(lc.resource, ns))
}

func (lc listCase) reject(t *testing.T, c *scriptedCluster, ns string) {
	t.Helper()
	lc.failWith(t, c, ns, serverUnauthorized)
}

func usagePath(ns string) string { return metricsRoot + "/namespaces/" + ns + "/pods" }

var listCases = []listCase{
	{id: "kube.pod.list", reason: "pod", run: runPodList, resource: "pods", calls: 1,
		seed: func(t *testing.T, c *scriptedCluster, ns string) {
			c.listIn(t, "pods", ns, podFx(t, ns, ns+"-0", "Running", true, 0, "", "n1"))
		},
		failWith: failList("pods")},
	{id: "kube.deployment.list", reason: "deployment", run: runDeploymentList, resource: "deployments", calls: 1,
		seed: func(t *testing.T, c *scriptedCluster, ns string) {
			c.listIn(t, "deployments", ns, depFx(t, ns, ns, 2, 2))
		},
		failWith: failList("deployments")},
	{id: "kube.event.list", reason: "message", run: runEventList, resource: "events", calls: 1,
		seed: func(t *testing.T, c *scriptedCluster, ns string) {
			c.listIn(t, "events", ns, eventFx(t, ns, ns+"-0"))
		},
		failWith: failList("events")},
	{id: "kube.quota.list", reason: "quota", run: runQuotaList, resource: "resourcequotas", calls: 2,
		seed: func(t *testing.T, c *scriptedCluster, ns string) {
			c.listIn(t, "resourcequotas", ns, quotaFx(t, ns, "compute", map[string]string{"pods": "50"}, map[string]string{"pods": "4"}))
		},
		failWith: failList("resourcequotas")},
	{id: "kube.pvc.list", reason: "pvc", run: runPVCList, resource: "persistentvolumeclaims", calls: 1,
		seed: func(t *testing.T, c *scriptedCluster, ns string) {
			c.listIn(t, "persistentvolumeclaims", ns, pvcFx(t, ns, ns+"-data"))
		},
		failWith: failList("persistentvolumeclaims")},
	{id: "kube.cert.list", reason: "secret", run: runCertList, resource: "secrets", calls: 1, none: "--namespace=default", config: 1,
		seed: func(t *testing.T, c *scriptedCluster, ns string) {
			c.secretsIn(t, ns, secretItem(ns, ns+"-tls", selfSignedPEM(t, ns, time.Now().Add(365*24*time.Hour)), "KEY"))
		},
		failWith: failList("secrets")},
	{id: "kube.metrics.pod", reason: "pod", run: runMetricsPod, resource: "pods", calls: 2, none: "--namespace=default", config: 1,
		seed: func(t *testing.T, c *scriptedCluster, ns string) {
			c.rawAt(t, usagePath(ns), podUsageFx(t, ns, ns+"-0", "200m", "96Mi"))
			c.listIn(t, "pods", ns, podLimitFx(t, ns, ns+"-0", "500m", "128Mi"))
		},
		failWith: func(t *testing.T, c *scriptedCluster, ns, stderr string) {
			c.write(t, "raw"+strings.ReplaceAll(usagePath(ns), "/", "_")+".fail", stderr)
		}},
	{id: "kube.serviceaccount.list", reason: "Name", run: runServiceAccountList, resource: "serviceaccounts", calls: 1,
		seed: func(t *testing.T, c *scriptedCluster, ns string) {
			c.listIn(t, "serviceaccounts", ns, accountFx(t, ns, ns+"-agent"))
		},
		failWith: failList("serviceaccounts")},
}

// somewhere is a cluster that holds something in each of the namespaces, and in
// payments, which no call here names and no page may mention.
func (lc listCase) somewhere(t *testing.T, names ...string) *scriptedCluster {
	t.Helper()
	c := newScriptedCluster(t)
	for _, ns := range append(slices.Clone(names), "payments") {
		lc.seed(t, c, ns)
	}
	if lc.id == "kube.metrics.pod" {
		c.rawAt(t, metricsRoot+"/pods")
	}
	return c
}

func listCaseOf(t *testing.T, id string) listCase {
	t.Helper()
	for _, lc := range listCases {
		if lc.id == id {
			return lc
		}
	}
	t.Fatalf("no list %s", id)
	return listCase{}
}

func (lc listCase) read(t *testing.T, values map[string]any) (view.Table, error) {
	t.Helper()
	v, err := lc.run(context.Background(), saReq(plugin.SurfaceCLI, false, values))
	if err != nil {
		return view.Table{}, err
	}
	table, ok := v.(view.Table)
	if !ok {
		t.Fatalf("%s: got %T, want a table", lc.id, v)
	}
	return table, nil
}

func (lc listCase) mustRead(t *testing.T, values map[string]any) view.Table {
	t.Helper()
	table, err := lc.read(t, values)
	if err != nil {
		t.Fatalf("%s: %v", lc.id, err)
	}
	return table
}

// withoutTimes is t with the values of its lengths of time and its moments
// taken out. An age is valued from the moment it is read, and the scripted
// cluster mints its certificates at each read, so two reads of one list a
// moment apart can differ in those, a second apart, and in nothing else.
func withoutTimes(t view.Table) view.Table {
	if t.Values == nil {
		return t
	}
	values := make([][]string, len(t.Values))
	for i, row := range t.Values {
		if row == nil {
			continue
		}
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

// kubectlCalls is the lines of `get` the cluster was asked, raw reads included.
func kubectlCalls(t *testing.T, c *scriptedCluster) []string {
	t.Helper()
	var out []string
	for _, line := range c.calls(t) {
		if strings.HasPrefix(line, "get ") {
			out = append(out, line)
		}
	}
	return out
}

func configCalls(t *testing.T, c *scriptedCluster) int {
	t.Helper()
	n := 0
	for _, line := range c.calls(t) {
		if strings.HasPrefix(line, "config view") {
			n++
		}
	}
	return n
}

func namespaceColumn(cols []view.Column) int {
	for i, col := range cols {
		if strings.EqualFold(col.Name, "namespace") {
			return i
		}
	}
	return -1
}

func columnNamed(cols []view.Column, name string) int {
	for i, col := range cols {
		if col.Name == name {
			return i
		}
	}
	return -1
}

// namespacesOnPage is the namespaces the rows of a table are in, once each and
// sorted, and the number of rows that is not a row saying a namespace could not
// be read.
func namespacesOnPage(t *testing.T, table view.Table) (namespaces []string, read int) {
	t.Helper()
	col := namespaceColumn(table.Columns)
	if col < 0 {
		t.Fatalf("the table has no namespace column: %v", table.Columns)
	}
	seen := map[string]bool{}
	for _, row := range table.Rows {
		if !seen[row[col]] {
			seen[row[col]] = true
			namespaces = append(namespaces, row[col])
		}
		if !strings.Contains(strings.Join(row, "|"), unreadLead) {
			read++
		}
	}
	sort.Strings(namespaces)
	return namespaces, read
}

func unreadRowsOf(table view.Table) [][]string {
	var out [][]string
	for _, row := range table.Rows {
		if strings.Contains(strings.Join(row, "|"), unreadLead) {
			out = append(out, row)
		}
	}
	return out
}

func manyNamespaces(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("team-%02d", i)
	}
	return out
}

func TestEachNamespaceOfAListIsReadWithACallOfItsOwnAndMergedIntoOneTable(t *testing.T) {
	asked := []string{"rustfs", "gitea", "crowdsec"}
	want := []string{"crowdsec", "gitea", "rustfs"}
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := lc.somewhere(t, want...)
			table := lc.mustRead(t, map[string]any{"namespace": asked})

			if got := namespacesRead(t, c.listed(t, lc.resource)); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s was listed for %v, want one call for each of %v", lc.resource, got, want)
			}
			if got := len(kubectlCalls(t, c)); got != len(want)*lc.calls {
				t.Errorf("%d lists for three namespaces, want %d:\n%s", got, len(want)*lc.calls, strings.Join(c.calls(t), "\n"))
			}
			if lc.id == "kube.metrics.pod" {
				raw := c.rawCalls(t)
				if len(raw) != len(want) {
					t.Fatalf("%d usage reads, want one for each namespace: %v", len(raw), raw)
				}
				for _, ns := range want {
					if !strings.Contains(strings.Join(raw, "\n"), usagePath(ns)+" ") {
						t.Errorf("the usage of %s was never asked for:\n%s", ns, strings.Join(raw, "\n"))
					}
				}
			}

			got, read := namespacesOnPage(t, table)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("the table is made of %v, want %v and not payments", got, want)
			}
			if read != len(want) || table.Total != len(want) || len(table.Warnings) != 0 {
				t.Errorf("%d rows, total %d, warnings %v: want one row each, counted, and nothing partial",
					read, table.Total, table.Warnings)
			}
		})
	}
}

// A list of one is the read it always was: the same table, without the column
// that says which namespace, from one call, whichever way the one name is written.
func TestAListOfOneNamespaceIsExactlyTheSingleReadItWas(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			var first view.Table
			var firstCalls []string
			for name, value := range map[string]any{
				"bare string": "gitea", "[]string": []string{"gitea"}, "[]any": []any{"gitea"},
				"padded and repeated": []string{" gitea", "gitea", ""},
			} {
				c := lc.somewhere(t, "gitea")
				table := lc.mustRead(t, map[string]any{"namespace": value})
				calls := c.calls(t)
				sort.Strings(calls)
				if got := len(kubectlCalls(t, c)); got != lc.calls {
					t.Errorf("%s: %d lists for one namespace, want %d", name, got, lc.calls)
				}
				for _, line := range c.listed(t, lc.resource) {
					if !strings.Contains(line, "--namespace=gitea") || strings.Contains(line, "--all-namespaces") {
						t.Errorf("%s: %q is not the read of the one namespace", name, line)
					}
				}
				if lc.id != "kube.serviceaccount.list" && namespaceColumn(table.Columns) >= 0 {
					t.Errorf("%s: the table names a namespace per row for one namespace: %v", name, table.Columns)
				}
				if first.Columns == nil {
					first, firstCalls = table, calls
					continue
				}
				if fmt.Sprint(withoutTimes(table)) != fmt.Sprint(withoutTimes(first)) || fmt.Sprint(calls) != fmt.Sprint(firstCalls) {
					t.Errorf("%s was read differently from the other spellings:\n%v\n%v", name, table, first)
				}
			}
		})
	}
}

// Without a fence naming none is the context's own namespace, which is what
// kubectl does, and every namespace is the switch's.
func TestNamingNoNamespaceIsTheContextsOwnAndTheSwitchStillReadsEvery(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := lc.somewhere(t, "default")
			lc.mustRead(t, map[string]any{})
			if got := len(kubectlCalls(t, c)); got != lc.calls {
				t.Errorf("%d lists for no namespace, want %d", got, lc.calls)
			}
			for _, line := range c.listed(t, lc.resource) {
				if strings.Contains(line, "--all-namespaces") {
					t.Errorf("%q: a call that names no namespace read the cluster", line)
				}
				if has := strings.Contains(line, "--namespace="); has != (lc.none != "") ||
					(lc.none != "" && !strings.Contains(line, lc.none)) {
					t.Errorf("%q, want %q for a call that names no namespace", line, lc.none)
				}
			}
			if got := configCalls(t, c); got != lc.config {
				t.Errorf("%d kubeconfig reads for no namespace, want %d", got, lc.config)
			}

			c = lc.somewhere(t)
			table := lc.mustRead(t, map[string]any{"all-namespaces": true})
			if namespaceColumn(table.Columns) < 0 {
				t.Errorf("the table of every namespace never says which: %v", table.Columns)
			}
			lines := c.listed(t, lc.resource)
			if len(lines) != 1 || !strings.Contains(lines[0], "--all-namespaces") || strings.Contains(lines[0], "--namespace=") {
				t.Errorf("every namespace was read as %q, want one call across them", lines)
			}
		})
	}
}

func TestANamespaceListThatCannotBeReadIsRefusedBeforeAnythingIsAsked(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			for name, tc := range map[string]struct {
				values map[string]any
				code   string
			}{
				"a list beside every namespace": {map[string]any{"namespace": []string{"gitea", "rustfs"}, "all-namespaces": true},
					"kube.namespace.ambiguous"},
				"a name kubectl would read as a flag": {map[string]any{"namespace": []string{"gitea", "--kubeconfig=/tmp/mine"}},
					"kube.name.invalid"},
				"a name that is a path":    {map[string]any{"namespace": []string{"gitea", "x/../../nodes"}}, "kube.name.invalid"},
				"a name with a capital":    {map[string]any{"namespace": []string{"Gitea"}}, "kube.name.invalid"},
				"more than thirty of them": {map[string]any{"namespace": manyNamespaces(31)}, "kube.namespaces.toomany"},
			} {
				c := lc.somewhere(t)
				_, err := lc.read(t, tc.values)
				verr, ok := err.(*view.Error)
				if !ok || verr.Code != tc.code {
					t.Errorf("%s: error = %v, want %s", name, err, tc.code)
				}
				if calls := c.calls(t); len(calls) != 0 {
					t.Errorf("%s: kubectl was run for a call that was refused:\n%s", name, strings.Join(calls, "\n"))
				}
			}

			// The refusal of a long list says the cap and that this call reaches more,
			// and never how many: the list may be a fence the host filled in, whose size
			// is an inventory.
			_, err := lc.read(t, map[string]any{"namespace": manyNamespaces(31)})
			verr := err.(*view.Error)
			if !strings.Contains(verr.Message, "30") || strings.Contains(verr.Message, "31") || !strings.Contains(verr.Hint, "fewer") {
				t.Errorf("the refusal is %q (hint %q): it must say the cap, never the count, and what to do", verr.Message, verr.Hint)
			}

			c := lc.somewhere(t, manyNamespaces(30)...)
			if _, err := lc.read(t, map[string]any{"namespace": manyNamespaces(30)}); err != nil {
				t.Errorf("thirty namespaces were refused: %v", err)
			}
			if got := len(kubectlCalls(t, c)); got != 30*lc.calls {
				t.Errorf("%d lists for thirty namespaces, want %d", got, 30*lc.calls)
			}
		})
	}
}

// What a read costs in kubectl calls, from the log of the stub: a call that names
// no namespace, one, several and as many as the cap allows.
func TestWhatAListOfNamespacesCostsInKubectlCalls(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				names []string
				reads int
			}{
				{"none", nil, 1}, {"one", manyNamespaces(1), 1}, {"seven", manyNamespaces(7), 7}, {"thirty", manyNamespaces(30), 30},
			} {
				c := lc.somewhere(t, append(slices.Clone(tc.names), "default")...)
				values := map[string]any{}
				if tc.names != nil {
					values["namespace"] = tc.names
				}
				lc.mustRead(t, values)
				// A kubeconfig is asked for once for the whole page, not once per namespace.
				wantConfig := lc.config
				if lc.id == "kube.metrics.pod" && tc.names != nil {
					wantConfig = 0
				}
				if gets, config := len(kubectlCalls(t, c)), configCalls(t, c); gets != tc.reads*lc.calls || config != wantConfig {
					t.Errorf("%s: %d lists and %d kubeconfig reads, want %d and %d:\n%s",
						tc.name, gets, config, tc.reads*lc.calls, wantConfig, strings.Join(c.calls(t), "\n"))
				}
			}
		})
	}
}

// A namespace that cannot be read is a row that says so and a warning, and the
// others still answer; it does not become the error of the page, and it is not a
// cluster that is down. The row says why in the column its capability names for
// it and in no other, and the table counts the rows it sends: a Total below them
// is arithmetic no reader can make sense of.
func TestANamespaceThatCannotBeReadIsARowAndTheOthersStillAnswer(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := lc.somewhere(t, "crowdsec", "gitea", "rustfs")
			lc.refuse(t, c, "gitea")
			table := lc.mustRead(t, map[string]any{"namespace": []string{"crowdsec", "gitea", "rustfs"}})

			got, read := namespacesOnPage(t, table)
			if strings.Join(got, ",") != "crowdsec,gitea,rustfs" || read != 2 {
				t.Errorf("the table is made of %v with %d rows read, want the two namespaces that answered and the one that did not", got, read)
			}
			if table.Total != len(table.Rows) {
				t.Errorf("total = %d for %d rows: a table counts the rows it sends, the unread ones among them", table.Total, len(table.Rows))
			}
			unread := unreadRowsOf(table)
			if len(unread) != 1 {
				t.Fatalf("unread rows = %v, want one that names gitea and why", unread)
			}
			reasonAt, nsAt := columnNamed(table.Columns, lc.reason), namespaceColumn(table.Columns)
			if reasonAt < 0 {
				t.Fatalf("the table has no column %q for the reason: %v", lc.reason, table.Columns)
			}
			for i, cell := range unread[0] {
				switch i {
				case nsAt:
					if cell != "gitea" {
						t.Errorf("the namespace cell is %q, want gitea", cell)
					}
				case reasonAt:
					if !strings.HasPrefix(cell, unreadLead) || !strings.Contains(cell, "forbidden") {
						t.Errorf("the %q cell is %q, want the reason a namespace could not be read", lc.reason, cell)
					}
				default:
					if cell != "" {
						t.Errorf("the %q cell of the unread row is %q, want it empty: the reason is in %q", table.Columns[i].Name, cell, lc.reason)
					}
				}
			}
			if len(table.Warnings) != 1 || table.Warnings[0].Code != "kube.namespace.unreadable" ||
				!strings.Contains(table.Warnings[0].Message, "gitea") ||
				strings.Contains(table.Warnings[0].Message, "crowdsec") || strings.Contains(table.Warnings[0].Message, "rustfs") ||
				table.Warnings[0].Advisory {
				t.Errorf("warnings = %+v, want one that is partial and names gitea alone", table.Warnings)
			}
		})
	}
}

// The warning counts the namespaces that could not be read, which the page names
// anyway, and never how many were asked about: the list may be a fence the host
// filled in, and its size is an inventory.
func TestTheWarningOfUnreadNamespacesNeverSaysHowManyWereAskedAbout(t *testing.T) {
	asked := manyNamespaces(7)
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := lc.somewhere(t, asked...)
			lc.refuse(t, c, asked[2])
			lc.refuse(t, c, asked[5])
			table := lc.mustRead(t, map[string]any{"namespace": asked})
			if len(table.Warnings) != 1 {
				t.Fatalf("warnings = %+v, want the one for the two namespaces", table.Warnings)
			}
			w := table.Warnings[0]
			text := w.Message + " " + w.Hint
			if !strings.Contains(w.Message, asked[2]) || !strings.Contains(w.Message, asked[5]) || !strings.HasPrefix(w.Message, "2 ") {
				t.Errorf("message = %q, want the two namespaces that could not be read, named and counted", w.Message)
			}
			if strings.Contains(text, "7") || strings.Contains(strings.ToLower(text), "asked") {
				t.Errorf("%q says how many namespaces were asked about", text)
			}
		})
	}
}

// The merged page is the same whichever way the list is written: in another order,
// with a name twice, with blanks and padding, or as the array a client over MCP
// sends. Each namespace is read once and the answers are merged in the order of the
// sorted names, so that a refresh of a tile does not shuffle its rows.
func TestThePageOfSeveralNamespacesIsTheSameWhicheverWayTheListIsWritten(t *testing.T) {
	spellings := []struct {
		name  string
		value any
	}{
		{"sorted", []string{"crowdsec", "gitea", "rustfs"}},
		{"reversed", []string{"rustfs", "gitea", "crowdsec"}},
		{"shuffled", []string{"gitea", "rustfs", "crowdsec"}},
		{"with repeats", []string{"rustfs", "gitea", "rustfs", "crowdsec", "gitea"}},
		{"blank and padded", []string{"", " gitea", "rustfs ", "crowdsec", "  "}},
		{"over MCP", []any{"rustfs", "crowdsec", "gitea"}},
	}
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := lc.somewhere(t, "crowdsec", "gitea", "rustfs")
			var first view.Table
			for i, sp := range spellings {
				table := lc.mustRead(t, map[string]any{"namespace": sp.value})
				if i == 0 {
					if len(table.Rows) < 3 {
						t.Fatalf("%d rows for three namespaces: the comparison below would prove nothing", len(table.Rows))
					}
					first = table
					continue
				}
				if fmt.Sprintf("%#v", withoutTimes(table)) != fmt.Sprintf("%#v", withoutTimes(first)) {
					t.Errorf("%s: the page is not the one of the sorted list:\n%#v\n%#v", sp.name, table, first)
				}
			}
			// Each namespace is asked for once however many times a spelling repeats it.
			seen := map[string]int{}
			for _, line := range c.listed(t, lc.resource) {
				seen[line]++
			}
			for line, n := range seen {
				if n > len(spellings) {
					t.Errorf("%q was asked %d times for %d reads", line, n, len(spellings))
				}
			}
		})
	}
}

// A namespace the API server answers 401 in is the error of the call and not a row of
// it, as it is for the overview: the credential is the connection's and not the
// namespace's, and a dashboard stops asking a tile that came back as one. A 403 of one
// namespace stays the row above.
func TestANamespaceTheClusterDidNotAcceptTheCredentialInIsTheErrorOfTheCall(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := lc.somewhere(t, "crowdsec", "gitea", "rustfs")
			lc.reject(t, c, "gitea")
			_, err := lc.read(t, map[string]any{"namespace": []string{"crowdsec", "gitea", "rustfs"}})
			var verr *view.Error
			if !errors.As(err, &verr) || verr.Code != "kube.auth.failed" || !verr.CredentialRefused() {
				t.Errorf("err = %v, want the refusal kube.auth.failed as the error of the call", err)
			}
		})
	}
}

// What the conformance suite holds every table to, run over a page that has
// namespaces it could not read: its rows are laid out under its columns on every
// surface, and its Total is never below the rows it holds. The lists are NoPreview
// because they reach a cluster, so the suite is handed copies it may drive, against
// the stub.
func TestAPageWithUnreadNamespacesPassesTheViewRules(t *testing.T) {
	named := []string{"crowdsec", "gitea", "rustfs"}
	c := newScriptedCluster(t)
	caps := make([]plugin.Capability, 0, len(listCases))
	inputs := map[string]map[string]any{}
	for _, lc := range listCases {
		for _, ns := range named {
			lc.seed(t, c, ns)
		}
		lc.refuse(t, c, "gitea")
		capability := capabilityNamed(t, lc.id)
		capability.NoPreview = false
		caps = append(caps, capability)
		inputs[lc.id] = map[string]any{"namespace": named}
	}
	partial := plugin.Plugin{Name: "kube", Summary: Plugin().Summary, Version: "dev", Needs: Plugin().Needs, Capabilities: caps}
	sdktest.Check(t, partial, sdktest.WithInputs(func(string) map[string]map[string]any { return inputs }),
		sdktest.Skip(sdktest.RuleRedaction, "kube.cert.list", "its secret column is the name of a Secret and holds no value"))
}

// Nothing answered: the page is the one error, as the read of one namespace was
// when it failed, and a refused credential reaches the dashboard as one.
func TestWhenNoNamespaceCouldBeReadThePageIsTheFirstError(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := lc.somewhere(t, "crowdsec", "gitea")
			lc.refuse(t, c, "crowdsec")
			lc.refuse(t, c, "gitea")
			_, err := lc.read(t, map[string]any{"namespace": []string{"gitea", "crowdsec"}})
			verr, ok := err.(*view.Error)
			if !ok || verr.Code != "kube.denied" || !verr.CredentialRefused() || !strings.Contains(verr.Message, "crowdsec") {
				t.Errorf("error = %v, want the refusal of crowdsec, the first of them, as a credential refused", err)
			}
		})
	}
}

// A cluster that does not answer is asked as many times as fit at once and not
// once for each namespace: the calls still waiting are given the answer the first
// ones got.
func TestAClusterThatDoesNotAnswerIsNotAskedOnceForEachNamespace(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := lc.somewhere(t, manyNamespaces(30)...)
			c.write(t, "conc.slow", "")
			c.write(t, "get.fail", "Unable to connect to the server: dial tcp 10.0.0.1:6443: i/o timeout")
			_, err := lc.read(t, map[string]any{"namespace": manyNamespaces(30)})
			if verr, ok := err.(*view.Error); !ok || verr.Code != "kube.unreachable" {
				t.Errorf("error = %v, want kube.unreachable", err)
			}
			if n := len(kubectlCalls(t, c)); n < 1 || n > readsAtOnce {
				t.Errorf("%d lists asked of a cluster that does not answer, want at most the %d that fit at once", n, readsAtOnce)
			}
		})
	}
}

// At most six lists run at once, whatever the number of namespaces.
func TestNoMoreThanSixListsOfAListingRunAtOnce(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := lc.somewhere(t, manyNamespaces(30)...)
			c.write(t, "conc.slow", "")
			lc.mustRead(t, map[string]any{"namespace": manyNamespaces(30)})
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
			if peak > readsAtOnce || peak < 2 {
				t.Errorf("%d lists ran at once, want more than one and at most %d", peak, readsAtOnce)
			}
		})
	}
}

// A token bound by a Role in each namespace is refused a list across them, and a
// fence is meant to keep the read inside what the profile reaches: the namespaces
// are listed one by one, and the cluster is never listed and filtered.
func TestACredentialThatMayOnlyListInsideANamespaceReadsEveryNamespaceOfTheList(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := lc.somewhere(t, "crowdsec", "gitea", "rustfs")
			c.forbidClusterWide(t)

			if _, err := lc.read(t, map[string]any{"all-namespaces": true}); err == nil {
				t.Fatal("the fixture does not refuse a list across namespaces, so it proves nothing")
			}
			c.write(t, "calls", "")

			table := lc.mustRead(t, map[string]any{"namespace": []string{"crowdsec", "gitea", "rustfs"}})
			if got, read := namespacesOnPage(t, table); read != 3 || len(got) != 3 {
				t.Errorf("the table is made of %v (%d rows read), want the three namespaces", got, read)
			}
			for _, line := range kubectlCalls(t, c) {
				if strings.Contains(line, "--all-namespaces") {
					t.Errorf("%q: a list across namespaces was asked of a credential that may only list inside them", line)
				}
				if strings.HasPrefix(line, "get --raw ") && !strings.Contains(line, "/namespaces/") {
					t.Errorf("%q: the usage of the cluster was asked for, and filtered", line)
				}
			}
		})
	}
}

func TestTheLimitRangesOfSeveralNamespacesAreCountedAndEachOneThatCannotBeReadIsNamed(t *testing.T) {
	c := newScriptedCluster(t)
	for _, ns := range []string{"crowdsec", "gitea", "rustfs"} {
		c.listIn(t, "resourcequotas", ns, quotaFx(t, ns, "compute", map[string]string{"pods": "50"}, map[string]string{"pods": "4"}))
	}
	c.listIn(t, "limitranges", "crowdsec", `{"metadata":{"name":"defaults"}}`)
	c.listIn(t, "limitranges", "rustfs", `{"metadata":{"name":"defaults"}}`, `{"metadata":{"name":"bounds"}}`)
	c.failIn(t, "limitranges", "gitea")

	v, err := runQuotaList(context.Background(), saReq(plugin.SurfaceCLI, false,
		map[string]any{"namespace": []string{"crowdsec", "gitea", "rustfs"}}))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("got %T, want the quotas with a warning for the limit ranges that were not read", v)
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0].Message, "gitea") {
		t.Errorf("warnings = %+v, want the one namespace whose limit ranges were refused", s.Warnings)
	}
	if table := s.Items[0].View.(view.Table); len(table.Rows) != 3 {
		t.Errorf("quota rows = %v, want all three namespaces: a limit range refused is not a quota refused", table.Rows)
	}
}

func TestTheCertificatesOfSeveralNamespacesAreKeptEachUnderItsOwnNamespace(t *testing.T) {
	lc := listCaseOf(t, "kube.cert.list")
	c := lc.somewhere(t, "crowdsec", "gitea", "rustfs")
	lc.mustRead(t, map[string]any{"namespace": []string{"crowdsec", "gitea", "rustfs"}})
	if n := len(c.secretLists(t)); n != 3 {
		t.Fatalf("%d Secret reads for three namespaces, want one each", n)
	}
	table := lc.mustRead(t, map[string]any{"namespace": []string{"gitea", "rustfs"}})
	if n := len(c.secretLists(t)); n != 3 {
		t.Errorf("%d Secret reads after a subset was asked for, want the kept three", n)
	}
	if len(table.Rows) != 2 || len(table.Warnings) != 0 {
		t.Errorf("the subset is %v with warnings %v, want its two certificates and no note on age", table.Rows, table.Warnings)
	}
}

func TestACertificateReadIsAsOldAsTheOldestOfTheKeptReadsItIsMadeOf(t *testing.T) {
	now := time.Now()
	got := mergeCertReads([]certRead{
		{certs: []tlsCert{{Name: "a"}}, at: now.Add(-time.Minute)},
		{certs: []tlsCert{{Name: "b"}}, at: now.Add(-time.Hour), reused: true},
		{certs: []tlsCert{{Name: "c"}}, at: now.Add(-2 * time.Hour), reused: true},
		{certs: []tlsCert{{Name: "d"}}, at: now.Add(-3 * time.Hour)},
	})
	if len(got.certs) != 4 || !got.reused || !got.at.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("merged = %d certificates, reused %v, at %v: want all four, reused, as old as the oldest kept read",
			len(got.certs), got.reused, got.at)
	}
	if fresh := mergeCertReads([]certRead{{at: now}}); fresh.reused {
		t.Error("a read that was just made is called an earlier one")
	}
}

// The pods of several namespaces are ordered by the pressure they are under and
// then by where they are, so that the page is the same whichever way the list was
// written and ties do not shuffle between refreshes.
func TestTheUsageOfSeveralNamespacesIsOrderedByPressureAndThenByNamespaceAndName(t *testing.T) {
	c := newScriptedCluster(t)
	for _, ns := range []string{"crowdsec", "gitea", "rustfs"} {
		c.rawAt(t, usagePath(ns), podUsageFx(t, ns, "b", "100m", "10Mi"), podUsageFx(t, ns, "a", "100m", "10Mi"))
	}
	c.listIn(t, "pods", "rustfs", podLimitFx(t, "rustfs", "b", "1", "20Mi"))
	for _, order := range [][]string{{"rustfs", "gitea", "crowdsec"}, {"crowdsec", "gitea", "rustfs"}} {
		table := listCaseOf(t, "kube.metrics.pod").mustRead(t, map[string]any{"namespace": order})
		var got []string
		for _, row := range table.Rows {
			got = append(got, row[0]+"/"+row[1])
		}
		want := "rustfs/b crowdsec/a crowdsec/b gitea/a gitea/b rustfs/a"
		if strings.Join(got, " ") != want {
			t.Errorf("rows = %v for %v, want %s: the pod at 50%% of its memory limit first, then the rest by place", got, order, want)
		}
	}
}
