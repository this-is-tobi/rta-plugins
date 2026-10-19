package main

import (
	"context"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

const metricsRoot = "/apis/metrics.k8s.io/v1beta1"

// rawAt sets the items the cluster answers to `get --raw <path>`.
func (c *scriptedCluster) rawAt(t *testing.T, path string, items ...string) {
	t.Helper()
	c.write(t, "raw"+strings.ReplaceAll(path, "/", "_")+".json", `{"items":[`+strings.Join(items, ",")+`]}`)
}

// rawCalls is the `get --raw` lines the cluster was asked.
func (c *scriptedCluster) rawCalls(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, line := range c.calls(t) {
		if strings.HasPrefix(line, "get --raw ") {
			out = append(out, line)
		}
	}
	return out
}

// podUsageFx is one pod as metrics.k8s.io reports it: its containers' usage.
func podUsageFx(t *testing.T, ns, name, cpu, memory string) string {
	t.Helper()
	return jsonOf(t, obj{
		"metadata":   obj{"name": name, "namespace": ns},
		"containers": []obj{{"usage": obj{"cpu": cpu, "memory": memory}}},
	})
}

// podLimitFx is the same pod as the API server holds it, with the limits its
// usage is measured against.
func podLimitFx(t *testing.T, ns, name, cpu, memory string) string {
	t.Helper()
	return jsonOf(t, obj{
		"metadata": obj{"name": name, "namespace": ns},
		"spec":     obj{"containers": []obj{{"resources": obj{"limits": obj{"cpu": cpu, "memory": memory}}}}},
	})
}

func metricsPodOf(t *testing.T, values map[string]any) view.Table {
	t.Helper()
	v, err := runMetricsPod(context.Background(), saReq(plugin.SurfaceCLI, false, values))
	if err != nil {
		t.Fatal(err)
	}
	table, ok := v.(view.Table)
	if !ok {
		t.Fatalf("got %T, want a table", v)
	}
	return table
}

// A call that names no namespace is the context's own, which is what it means
// for every list here and what kubectl does: the usage list read the whole
// cluster's instead, beside the limits of the one namespace, and drew no column
// that said which namespace a pod was in. A credential bound by a Role in its
// namespace was refused the cluster's list, and one that could read it was shown
// every pod of every namespace with no limit to measure all but its own against.
func TestThePodUsageOfNoNamespaceIsThatOfTheContextsOwn(t *testing.T) {
	for name, tc := range map[string]struct {
		context, namespace string
		values             map[string]any
		want               string
	}{
		"the namespace the current context states": {"prod-eu", "shop", map[string]any{}, "shop"},
		"a current context that states none":       {"prod-eu", "", map[string]any{}, "default"},
		"a context named in the call":              {"prod-eu", "shop", map[string]any{"context": "staging"}, "default"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newScriptedCluster(t)
			c.currentContext(tc.context, tc.namespace)
			c.forbidClusterWide(t)
			c.rawAt(t, metricsRoot+"/namespaces/"+tc.want+"/pods", podUsageFx(t, tc.want, "web-0", "200m", "96Mi"))
			c.listIn(t, "pods", tc.want, podLimitFx(t, tc.want, "web-0", "500m", "128Mi"))

			table := metricsPodOf(t, tc.values)
			if len(table.Rows) != 1 || table.Rows[0][0] != "web-0" || len(table.Columns) != 5 {
				t.Fatalf("the table is %v over %v, want web-0 alone and no namespace column", table.Rows, table.Columns)
			}
			if table.Rows[0][4] == "" {
				t.Errorf("web-0 has no memory %%: its limit was read from another namespace than its usage: %v", table.Rows[0])
			}
			raw := c.rawCalls(t)
			if len(raw) != 1 || !strings.HasPrefix(raw[0], "get --raw "+metricsRoot+"/namespaces/"+tc.want+"/pods ") {
				t.Errorf("the usage was read with %q, want the namespaced list of %s", raw, tc.want)
			}
			for _, line := range c.listed(t, "pods") {
				if !strings.Contains(line, "--namespace="+tc.want) {
					t.Errorf("%q: the limits are not those of the namespace the usage was read in", line)
				}
			}
		})
	}
}

// Every namespace is still the cluster's list, with the column that says which.
func TestThePodUsageOfEveryNamespaceIsStillTheClustersList(t *testing.T) {
	c := newScriptedCluster(t)
	c.currentContext("prod-eu", "shop")
	c.rawAt(t, metricsRoot+"/pods",
		podUsageFx(t, "shop", "web-0", "200m", "96Mi"), podUsageFx(t, "payments", "ledger-0", "100m", "32Mi"))
	table := metricsPodOf(t, map[string]any{"all-namespaces": true})
	if len(table.Rows) != 2 || table.Columns[0].Name != "namespace" {
		t.Errorf("the table is %v over %v, want both pods and a namespace column", table.Rows, table.Columns)
	}
	if raw := c.rawCalls(t); len(raw) != 1 || !strings.HasPrefix(raw[0], "get --raw "+metricsRoot+"/pods ") {
		t.Errorf("the usage was read with %q, want the cluster's list", raw)
	}
}
