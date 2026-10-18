package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The compact answer is what a dashboard tile and an agent read by default, so
// what it must be is a verdict first and then the rows an on-call operator
// reads in five seconds: the order, the count and the status row are the
// contract these tests hold, and the particular words are checked where a
// reader would notice them missing.

var compactKeys = []string{"status", "nodes", "pods", "deployments", "certs", "quotas", "pod slots", "context"}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type obj = map[string]any

func nodeFx(t *testing.T, name, ready string, cordoned bool, pressure, version string) string {
	t.Helper()
	conds := []obj{{"type": "Ready", "status": ready}}
	for _, p := range []string{"MemoryPressure", "DiskPressure", "PIDPressure"} {
		status := "False"
		if p == pressure {
			status = "True"
		}
		conds = append(conds, obj{"type": p, "status": status})
	}
	return jsonOf(t, obj{
		"metadata": obj{"name": name},
		"spec":     obj{"unschedulable": cordoned},
		"status": obj{
			"allocatable": obj{"pods": "110"},
			"conditions":  conds,
			"nodeInfo":    obj{"kubeletVersion": version},
		},
	})
}

func podFx(t *testing.T, ns, name, phase string, ready bool, restarts int, waiting, node string) string {
	t.Helper()
	state := obj{"running": obj{}}
	if waiting != "" {
		state = obj{"waiting": obj{"reason": waiting}}
	}
	if phase == "Succeeded" {
		state = obj{"terminated": obj{"reason": "Completed", "exitCode": 0}}
	}
	readyWord := "False"
	if ready {
		readyWord = "True"
	}
	return jsonOf(t, obj{
		"metadata": obj{"name": name, "namespace": ns},
		"spec":     obj{"nodeName": node, "containers": []obj{{}}},
		"status": obj{
			"phase":      phase,
			"conditions": []obj{{"type": "Ready", "status": readyWord}, {"type": "Initialized", "status": "True"}},
			"containerStatuses": []obj{{
				"name": "app", "ready": ready, "restartCount": restarts, "started": ready, "state": state,
			}},
		},
	})
}

func depFx(t *testing.T, ns, name string, want, ready int) string {
	t.Helper()
	return jsonOf(t, obj{
		"metadata": obj{"name": name, "namespace": ns},
		"spec":     obj{"replicas": want},
		"status":   obj{"readyReplicas": ready, "updatedReplicas": ready, "availableReplicas": ready},
	})
}

func quotaFx(t *testing.T, ns, name string, hard, used map[string]string) string {
	t.Helper()
	return jsonOf(t, obj{
		"metadata": obj{"name": name, "namespace": ns},
		"status":   obj{"hard": hard, "used": used},
	})
}

// aHealthyCluster is three Ready nodes on one kubelet version, a few serving
// pods, a deployment with every replica, a quota with room and a certificate
// with a year left: the baseline every departure below is read against.
func aHealthyCluster(t *testing.T) *scriptedCluster {
	t.Helper()
	c := newScriptedCluster(t)
	c.list(t, "namespaces", jsonOf(t, obj{"metadata": obj{"name": "apps"}, "status": obj{"phase": "Active"}}))
	c.list(t, "nodes",
		nodeFx(t, "n1", "True", false, "", "v1.30.2"),
		nodeFx(t, "n2", "True", false, "", "v1.30.2"),
		nodeFx(t, "n3", "True", false, "", "v1.30.2"))
	c.list(t, "pods",
		podFx(t, "apps", "web-1", "Running", true, 0, "", "n1"),
		podFx(t, "apps", "web-2", "Running", true, 2, "", "n2"),
		podFx(t, "apps", "job-1", "Succeeded", false, 0, "", "n3"))
	c.list(t, "deployments", depFx(t, "apps", "web", 2, 2))
	c.list(t, "resourcequotas", quotaFx(t, "apps", "compute",
		map[string]string{"cpu": "10", "pods": "50"}, map[string]string{"cpu": "2", "pods": "5"}))
	c.secrets([]string{secretItem("apps", "web-tls", selfSignedPEM(t, "web", time.Now().Add(365*24*time.Hour)), "KEY")})
	return c
}

// anUnwellCluster is the cluster the audit of the tiles read: a node the
// control plane lost, one cordoned, one short of disk; a pod crash-looping, one
// that cannot pull, one Ready with nineteen restarts; two deployments short;
// a quota near its limit; and an expired and an expiring certificate.
func anUnwellCluster(t *testing.T) *scriptedCluster {
	t.Helper()
	c := newScriptedCluster(t)
	c.list(t, "namespaces",
		jsonOf(t, obj{"metadata": obj{"name": "payments"}, "status": obj{"phase": "Active"}}),
		jsonOf(t, obj{"metadata": obj{"name": "checkout"}, "status": obj{"phase": "Active"}}))
	c.list(t, "nodes",
		nodeFx(t, "cp-1", "True", false, "", "v1.31.4"),
		nodeFx(t, "worker-1", "True", false, "", "v1.30.2"),
		nodeFx(t, "worker-2", "Unknown", false, "", "v1.30.2"),
		nodeFx(t, "worker-3", "True", true, "", "v1.30.2"),
		nodeFx(t, "worker-4", "True", false, "DiskPressure", "v1.31.4"))
	pods := []string{}
	for i := range 60 {
		ns := "checkout"
		if i%2 == 1 {
			ns = "payments"
		}
		pods = append(pods, podFx(t, ns, fmt.Sprintf("svc-%03d", i), "Running", true, 0, "", "worker-1"))
	}
	pods = append(pods,
		podFx(t, "payments", "ledger-7d9f-xk2lp", "Running", false, 41, "CrashLoopBackOff", "worker-1"),
		podFx(t, "checkout", "cart-5c8b-q9wzt", "Pending", false, 0, "ImagePullBackOff", ""),
		podFx(t, "monitoring", "prom-0", "Running", true, 19, "", "worker-1"),
		podFx(t, "payments", "nightly-1739", "Succeeded", false, 0, "", "worker-1"))
	c.list(t, "pods", pods...)
	c.list(t, "deployments",
		depFx(t, "payments", "ledger", 3, 1),
		depFx(t, "checkout", "cart", 2, 0),
		depFx(t, "checkout", "web", 4, 4))
	c.list(t, "resourcequotas", quotaFx(t, "payments", "compute",
		map[string]string{"cpu": "10", "pods": "50"}, map[string]string{"cpu": "9", "pods": "48"}))
	c.secrets([]string{
		secretItem("payments", "api-tls", selfSignedPEM(t, "api", time.Now().Add(-time.Hour)), "KEY"),
		secretItem("checkout", "web-tls", selfSignedPEM(t, "web", time.Now().Add(10*24*time.Hour)), "KEY"),
		secretItem("monitoring", "grafana-tls", selfSignedPEM(t, "grafana", time.Now().Add(300*24*time.Hour)), "KEY"),
	})
	return c
}

func compactOf(t *testing.T, values map[string]any) []view.Pair {
	t.Helper()
	v, err := runOverview(context.Background(), saReq(plugin.SurfaceCLI, false, values))
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("the compact answer is %T, want key/value rows", v)
	}
	return kv.Pairs
}

func rowsOf(pairs []view.Pair) map[string]string {
	out := map[string]string{}
	for _, p := range pairs {
		out[p.Key] = p.Value
	}
	return out
}

func keysOf(pairs []view.Pair) []string {
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.Key
	}
	return out
}

// assertTileShape holds the shape rule every state of the page keeps: at most
// eight lines once the renderer has set the values beside the widest key, and a
// row on one line of at most 80 columns. The one row allowed to wrap is the
// hint beside a cluster that could not be read (unanswered says why), and it
// pays for its lines out of the eight.
func assertTileShape(t *testing.T, pairs []view.Pair) {
	t.Helper()
	key := 0
	for _, p := range pairs {
		key = max(key, utf8.RuneCountInString(p.Key))
	}
	lines := 0
	for _, p := range pairs {
		if strings.ContainsAny(p.Value, "\n\r") {
			t.Errorf("%s spans lines: %q", p.Key, p.Value)
		}
		w := key + 2 + utf8.RuneCountInString(p.Value)
		switch {
		case w <= 80:
			lines++
		case p.Key == "what to check":
			lines += 1 + (utf8.RuneCountInString(p.Value)+77)/78
		default:
			t.Errorf("%s is %d columns wide, want at most 80: %q", p.Key, w, p.Value)
			lines++
		}
	}
	if lines > 8 {
		t.Errorf("%d lines, a tile shows eight: %v", lines, keysOf(pairs))
	}
}

func TestAHealthyClusterIsOneWordAndEightRowsInTheOrderOfWhatMatters(t *testing.T) {
	aHealthyCluster(t)
	pairs := compactOf(t, map[string]any{})
	if got := keysOf(pairs); strings.Join(got, "|") != strings.Join(compactKeys, "|") {
		t.Fatalf("rows = %v, want %v", got, compactKeys)
	}
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)
	for key, want := range map[string]string{
		"status":      "ok",
		"nodes":       "3",
		"pods":        "3",
		"deployments": "1",
		"certs":       "1",
		"quotas":      "1",
		"pod slots":   "2 of 330 used (0%)",
		"context":     "prod-eu · kubelet v1.30.2",
	} {
		if rows[key] != want {
			t.Errorf("%s = %q, want %q", key, rows[key], want)
		}
	}
	for _, gone := range []string{"server", "namespace", "cluster", "namespaces"} {
		if _, ok := rows[gone]; ok {
			t.Errorf("the compact answer still has %q: identity belongs to --detail", gone)
		}
	}
}

// The state comes before the address: the verdict is the first row and leads
// with a word the theme grades, and names the worst fact.
func TestAnUnwellClusterLeadsWithTheVerdictAndTheWorstFacts(t *testing.T) {
	anUnwellCluster(t)
	pairs := compactOf(t, map[string]any{})
	if got := keysOf(pairs); strings.Join(got, "|") != strings.Join(compactKeys, "|") {
		t.Fatalf("rows = %v, want %v", got, compactKeys)
	}
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)

	if !strings.HasPrefix(rows["status"], "fail — 1 node Unknown, 2 pods not ready") || !strings.HasSuffix(rows["status"], " +7") {
		t.Errorf("status = %q, want the node and the pods first and the rest counted", rows["status"])
	}
	for key, wants := range map[string][]string{
		"nodes":       {"5", "worker-2 Unknown", "and 2 more", "skew"},
		"pods":        {"64", "CrashLoopBackOff 1", "ImagePullBackOff 1", fmt.Sprintf("1 over %d restarts", restartStorm)},
		"deployments": {"3", "2 short", "checkout/cart 0/2"},
		"certs":       {"3", "1 expired", "payments/api-tls"},
		"quotas":      {"1", "2 over 80%", "payments/compute pods 96%"},
		"pod slots":   {"of 330 used"},
		"context":     {"prod-eu", "kubelets v1.30.2, v1.31.4"},
	} {
		for _, want := range wants {
			if !strings.Contains(rows[key], want) {
				t.Errorf("%s = %q, want it to say %q", key, rows[key], want)
			}
		}
	}
}

func TestOnlyWhatIsWarnedIsWarnedAndNotFailed(t *testing.T) {
	c := aHealthyCluster(t)
	c.list(t, "deployments", depFx(t, "apps", "web", 3, 2))
	rows := rowsOf(compactOf(t, map[string]any{}))
	if want := "warn — 1 deployment short"; rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}
	if want := "1 · 1 short: apps/web 2/3"; rows["deployments"] != want {
		t.Errorf("deployments = %q, want %q", rows["deployments"], want)
	}
}

// A pod that serves and has restarted over and over is a crash loop the
// readiness hides: a pod over restartStorm restarts is named, and one that is
// not ready is not counted twice.
func TestAServingPodOverTheRestartLimitIsWarned(t *testing.T) {
	c := aHealthyCluster(t)
	c.list(t, "pods",
		podFx(t, "apps", "web-1", "Running", true, restartStorm, "", "n1"),
		podFx(t, "apps", "web-2", "Running", true, restartStorm+1, "", "n2"))
	rows := rowsOf(compactOf(t, map[string]any{}))
	if want := fmt.Sprintf("2 · 1 over %d restarts", restartStorm); rows["pods"] != want {
		t.Errorf("pods = %q, want %q (only the pod over the limit)", rows["pods"], want)
	}
	if want := fmt.Sprintf("warn — 1 pod over %d restarts", restartStorm); rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}

	c.list(t, "pods",
		podFx(t, "apps", "web-1", "Running", true, restartStorm+1, "", "n1"),
		podFx(t, "apps", "web-3", "Running", false, restartStorm+9, "CrashLoopBackOff", "n3"))
	rows = rowsOf(compactOf(t, map[string]any{}))
	want := "fail — 1 pod not ready +1"
	if rows["status"] != want {
		t.Errorf("status = %q, want %q (one fact behind the failure: the serving pod over the limit, and not the pod that is not ready again)", rows["status"], want)
	}
	if want := fmt.Sprintf("2 · CrashLoopBackOff 1 · 1 over %d restarts", restartStorm); rows["pods"] != want {
		t.Errorf("pods = %q, want %q (the restarts are said before the names, which have no room)", rows["pods"], want)
	}

	c.list(t, "pods",
		podFx(t, "apps", "web-1", "Running", true, 0, "", "n1"),
		podFx(t, "apps", "web-3", "Running", false, 0, "CrashLoopBackOff", "n3"))
	rows = rowsOf(compactOf(t, map[string]any{}))
	if want := "2 · CrashLoopBackOff 1 · apps/web-3"; rows["pods"] != want {
		t.Errorf("pods = %q, want %q (the worst pod named where the line has room)", rows["pods"], want)
	}
}

// Kubelets on two minor versions are an upgrade half done, and the page says
// so beside the nodes; a patch apart is not skew.
func TestKubeletsOnTwoMinorVersionsAreSkewAndTwoPatchesAreNot(t *testing.T) {
	c := aHealthyCluster(t)
	c.list(t, "nodes",
		nodeFx(t, "n1", "True", false, "", "v1.30.2"),
		nodeFx(t, "n2", "True", false, "", "v1.31.4"))
	rows := rowsOf(compactOf(t, map[string]any{}))
	if rows["nodes"] != "2 · skew" || rows["status"] != "ok" {
		t.Errorf("nodes = %q, status = %q, want the skew said and nothing graded", rows["nodes"], rows["status"])
	}
	if rows["context"] != "prod-eu · kubelets v1.30.2, v1.31.4" {
		t.Errorf("context = %q", rows["context"])
	}

	c.list(t, "nodes",
		nodeFx(t, "n1", "True", false, "", "v1.30.2"),
		nodeFx(t, "n2", "True", false, "", "v1.30.5+k3s1"))
	rows = rowsOf(compactOf(t, map[string]any{}))
	if rows["nodes"] != "2" {
		t.Errorf("nodes = %q, want a patch apart left unsaid", rows["nodes"])
	}
}

func TestACertificateInsideItsWindowAndAFullQuotaAreWarnedByName(t *testing.T) {
	c := aHealthyCluster(t)
	c.secrets([]string{secretItem("apps", "web-tls", selfSignedPEM(t, "web", time.Now().Add(5*24*time.Hour)), "KEY")})
	c.list(t, "resourcequotas", quotaFx(t, "apps", "compute",
		map[string]string{"pods": "50"}, map[string]string{"pods": "50"}))
	rows := rowsOf(compactOf(t, map[string]any{}))
	if want := "warn — 1 cert expiring soon +1"; rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}
	if !strings.Contains(rows["certs"], "1 soon: apps/web-tls") {
		t.Errorf("certs = %q", rows["certs"])
	}
	if !strings.Contains(rows["quotas"], "1 over 80%: apps/compute pods 100%") {
		t.Errorf("quotas = %q", rows["quotas"])
	}
}

func TestPodSlotsNearlyFullAreWarned(t *testing.T) {
	c := aHealthyCluster(t)
	pods := []string{}
	for i := range 300 {
		pods = append(pods, podFx(t, "apps", fmt.Sprintf("p-%03d", i), "Running", true, 0, "", "n1"))
	}
	c.list(t, "pods", pods...)
	rows := rowsOf(compactOf(t, map[string]any{}))
	if want := "warn — pod slots 90% used"; !strings.HasPrefix(rows["status"], "warn — ") ||
		!strings.Contains(rows["status"], "pod slots 90% used") {
		t.Errorf("status = %q, want %q in it", rows["status"], want)
	}
}

// Rule five: a row the credential cannot read says so and the page goes on, and
// the verdict says that the page is partial — a missing row is what a healthy
// cluster looks like, so silence is the one wrong answer.
func TestARowTheCredentialCannotReadSaysSoAndTheVerdictWarns(t *testing.T) {
	for _, tc := range []struct {
		resource string
		key      string
		clause   string
	}{
		{"nodes", "nodes", "nodes could not be read"},
		{"pods", "pods", "pods could not be read"},
		{"deployments", "deployments", "deployments could not be read"},
		{"resourcequotas", "quotas", "quotas could not be read"},
		{"secrets", "certs", "certs could not be read"},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			c := aHealthyCluster(t)
			failing := `Error from server (Forbidden): ` + tc.resource + ` is forbidden: User "system:serviceaccount:demo:agent" ` +
				`cannot list resource "` + tc.resource + `" in API group "" at the cluster scope`
			scriptRefusing(t, c, tc.resource, failing)

			pairs := compactOf(t, map[string]any{})
			if got := keysOf(pairs); strings.Join(got, "|") != strings.Join(compactKeys, "|") {
				t.Fatalf("rows = %v, want all eight with one unread", got)
			}
			assertTileShape(t, pairs)
			rows := rowsOf(pairs)
			if !strings.HasPrefix(rows[tc.key], "could not be read — ") {
				t.Errorf("%s = %q, want 'could not be read — <why>'", tc.key, rows[tc.key])
			}
			if want := "warn — " + tc.clause; rows["status"] != want {
				t.Errorf("status = %q, want %q", rows["status"], want)
			}
		})
	}
}

// scriptRefusing makes the one resource's list fail the way an API server
// refuses a credential that may not read it, and every other list answer from
// the cluster's fixtures.
func scriptRefusing(t *testing.T, c *scriptedCluster, resource, stderr string) {
	t.Helper()
	if resource == "secrets" {
		c.write(t, "secrets.fail", stderr)
		return
	}
	c.write(t, "list-"+resource+".fail", stderr)
}

// Nodes are not readable by a namespaced credential, which is the ordinary
// restricted identity, and the slots are counted against the nodes.
func TestPodSlotsSayWhyTheyCannotBeCounted(t *testing.T) {
	c := aHealthyCluster(t)
	scriptRefusing(t, c, "nodes", `Error from server (Forbidden): nodes is forbidden: User "sa" cannot list resource "nodes"`)
	rows := rowsOf(compactOf(t, map[string]any{}))
	if !strings.HasPrefix(rows["pod slots"], "could not be read — ") {
		t.Errorf("pod slots = %q, want the unread nodes named as the reason", rows["pod slots"])
	}

	c = aHealthyCluster(t)
	c.list(t, "nodes", nodeFx(t, "n1", "True", true, "", "v1.30.2"))
	rows = rowsOf(compactOf(t, map[string]any{}))
	if !strings.HasPrefix(rows["pod slots"], "0 — no node") {
		t.Errorf("pod slots = %q, want it said that no node is schedulable", rows["pod slots"])
	}
}

// The cluster answering nothing is the verdict, and the rows that said why
// before still say it in the words the grammar sets.
func TestAClusterThatDoesNotAnswerIsTheVerdictAndKeepsItsReasonAndItsHint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stderr  string
		status  string
		cluster string
	}{
		{"silent", "Unable to connect to the server: dial tcp 10.0.0.1:6443: i/o timeout",
			"fail — the cluster did not answer", "did not answer — Unable to connect"},
		{"refused", `Error from server (Forbidden): namespaces is forbidden: User "system:serviceaccount:demo:agent" cannot list resource "namespaces" in API group "" at the cluster scope`,
			"fail — the cluster answered, and refused", "answered, and refused — "},
		{"never contacted", `error: context "prod" does not exist`,
			"fail — the cluster was never contacted", "was never contacted — "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusingKubectl(t, tc.stderr)
			pairs := compactOf(t, map[string]any{})
			assertTileShape(t, pairs)
			if got := keysOf(pairs); got[0] != "status" || got[len(got)-1] != "context" {
				t.Errorf("rows = %v, want the verdict first and the context last", got)
			}
			rows := rowsOf(pairs)
			if rows["status"] != tc.status {
				t.Errorf("status = %q, want %q", rows["status"], tc.status)
			}
			if !strings.HasPrefix(rows["cluster"], tc.cluster) {
				t.Errorf("cluster = %q, want it to begin %q", rows["cluster"], tc.cluster)
			}
			if rows["what to check"] == "" {
				t.Error("no hint beside a cluster that could not be read")
			}
		})
	}
}

// An empty cluster is a cluster, not an error: every row says there is nothing
// of its kind.
func TestAnEmptyClusterSaysNoneForEachKindAndIsOk(t *testing.T) {
	newScriptedCluster(t)
	pairs := compactOf(t, map[string]any{})
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)
	if rows["status"] != "ok" {
		t.Errorf("status = %q, want ok", rows["status"])
	}
	for _, key := range []string{"nodes", "pods", "deployments", "certs", "quotas"} {
		if rows[key] != "none" {
			t.Errorf("%s = %q, want none", key, rows[key])
		}
	}
	if !strings.HasPrefix(rows["pod slots"], "0 — no node") {
		t.Errorf("pod slots = %q", rows["pod slots"])
	}
}

// A page narrowed to one namespace reads the pods, deployments, quotas and
// certificates of that one and says so where the context is named, because a
// short list of pods is the answer to a different question than the cluster's.
func TestANarrowedPageSaysItIsOneNamespaceAndNamesNothingOutsideIt(t *testing.T) {
	c := anUnwellCluster(t)
	pairs := compactOf(t, map[string]any{"namespace": "payments"})
	assertTileShape(t, pairs)
	rows := rowsOf(pairs)
	if !strings.Contains(rows["context"], "namespace payments only") {
		t.Errorf("context = %q, want the one namespace read named", rows["context"])
	}
	if !strings.HasPrefix(rows["pod slots"], "not counted") {
		t.Errorf("pod slots = %q, want it not counted for one namespace", rows["pod slots"])
	}
	for _, kind := range []string{"pods", "deployments", "resourcequotas", "secrets"} {
		lines := c.listed(t, kind)
		if len(lines) != 1 || !strings.Contains(lines[0], "--namespace=payments") || strings.Contains(lines[0], "--all-namespaces") {
			t.Errorf("%s listed as %q, want that namespace alone", kind, lines)
		}
	}
	if strings.Contains(rows["deployments"], "payments/") {
		t.Errorf("deployments = %q, a name within the one namespace read needs no namespace in front", rows["deployments"])
	}
}

// Rule six: the compact page asks the cluster for what it asked for before and
// one more list, the deployments, and --detail asks for no more than that.
func TestTheCompactPageAsksForSixListsAndTheDetailedPageForNoOther(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(fmt.Sprintf("detail=%v", detail), func(t *testing.T) {
			c := anUnwellCluster(t)
			if _, err := runOverview(context.Background(), saReq(plugin.SurfaceCLI, false, map[string]any{"detail": detail})); err != nil {
				t.Fatal(err)
			}
			got := map[string]int{}
			for _, line := range c.calls(t) {
				fields := strings.Fields(line)
				if len(fields) > 1 && fields[0] == "get" {
					got[fields[1]]++
				}
			}
			want := map[string]int{"namespaces": 1, "nodes": 1, "pods": 1, "deployments": 1, "resourcequotas": 1, "secrets": 1}
			for kind, n := range want {
				if got[kind] != n {
					t.Errorf("%s was listed %d times, want %d (all: %v)", kind, got[kind], n, got)
				}
			}
			if len(got) != len(want) {
				t.Errorf("lists = %v, want only %v", got, want)
			}
		})
	}
}

// --detail carries everything the old default carried, plus the verdict: the
// identity rows, the node and pod lists by name and the tables.
func TestTheDetailedPageKeepsWhatTheCompactOneMoved(t *testing.T) {
	anUnwellCluster(t)
	v, err := runOverview(context.Background(), saReq(plugin.SurfaceCLI, false, map[string]any{"detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	secs, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("detail is %T, want sections", v)
	}
	if secs.Items[0].ID != "cluster" {
		t.Fatalf("first section is %q, want cluster", secs.Items[0].ID)
	}
	rows := keyValues(t, secs.Items[0].View)
	for _, key := range []string{"status", "context", "server", "namespace", "cluster", "namespaces", "nodes",
		"not ready", "cordoned", "under pressure", "pods", "pod slots", "quotas over 80%", "cert expired", "cert expiring soon",
		"deployments short", fmt.Sprintf("pods over %d restarts", restartStorm)} {
		if _, ok := rows[key]; !ok {
			t.Errorf("the detailed cluster section lost %q (has %v)", key, rows)
		}
	}
	if !strings.HasPrefix(rows["status"], "fail — ") {
		t.Errorf("status = %q", rows["status"])
	}
	ids := map[string]bool{}
	for _, s := range secs.Items {
		ids[s.ID] = true
	}
	for _, id := range []string{"nodes", "pods", "deployments", "restarts", "quotas", "certs"} {
		if !ids[id] {
			t.Errorf("no %q section in %v", id, ids)
		}
	}
}

func TestACompactUnreadableReasonIsClippedToTheLine(t *testing.T) {
	c := aHealthyCluster(t)
	scriptRefusing(t, c, "nodes", `Error from server (Forbidden): nodes is forbidden: User "system:serviceaccount:a-very-long-namespace-name:an-equally-long-service-account-name" cannot list resource "nodes" in API group "" at the cluster scope`)
	pairs := compactOf(t, map[string]any{})
	assertTileShape(t, pairs)
	if got := rowsOf(pairs)["nodes"]; !strings.HasSuffix(got, "…") {
		t.Errorf("nodes = %q, want the long reason cut with an ellipsis", got)
	}
}

func TestEveryRowTheCompactAnswerPrintsFitsInEveryStateOfTheUnwellCluster(t *testing.T) {
	anUnwellCluster(t)
	for _, values := range []map[string]any{{}, {"namespace": "payments"}} {
		assertTileShape(t, compactOf(t, values))
	}
}

// The words an operator and an agent read about the page say the numbers the
// page uses, so that changing one without the other is a failing test and not
// a description that lies.
func TestTheOverviewDescriptionsSayTheRestartLimitAndTheThresholdsTheRowsUse(t *testing.T) {
	c := overviewCapability(t)
	for name, text := range map[string]string{"Description": c.Description, "Agent": c.Agent} {
		if want := fmt.Sprintf("over %d", restartStorm); !strings.Contains(text, want) {
			t.Errorf("%s does not say %q: %s", name, want, text)
		}
		if want := fmt.Sprintf("over %d%%", int(quotaPressureThreshold*100)); !strings.Contains(text, want) {
			t.Errorf("%s does not say %q", name, want)
		}
	}
	if !strings.Contains(c.Description, fmt.Sprintf("within %d days", warnDays)) {
		t.Error("the description does not say the window a certificate is graded in")
	}
	if c.Refresh != overviewRefresh || !c.Detailed {
		t.Errorf("refresh = %v, detailed = %v, want the declared pace and the detailed form", c.Refresh, c.Detailed)
	}
}
