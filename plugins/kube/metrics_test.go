package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// checkName's own char class — needed for context names shaped like
// "arn:aws:eks:..." — allows the dots and slashes a namespace embedded
// directly into a raw API path must not: this is the exact string that
// reaches nodes/proxy, the subresource rbac.go refuses to grant, if
// namespace were trusted at face value.
func TestCheckNamespaceLabelRejectsTraversal(t *testing.T) {
	bad := []string{
		"x/../../../../../api/v1/nodes/some-node/proxy",
		"../secrets",
		"prod/../../nodes",
		"prod.staging",
		"prod:staging",
	}
	for _, v := range bad {
		if verr := checkNamespaceLabel(v); verr == nil {
			t.Errorf("checkNamespaceLabel(%q) = nil, want a refusal", v)
		}
	}

	good := []string{"", "default", "prod", "kube-system", "team-a-staging"}
	for _, v := range good {
		if verr := checkNamespaceLabel(v); verr != nil {
			t.Errorf("checkNamespaceLabel(%q) = %v, want nil", v, verr)
		}
	}
}

// The second, independent layer inside getRawJSON itself: even a path
// built from a field neither checkName nor checkNamespaceLabel happened to
// validate must not reach kubectl carrying a traversal segment. Refused
// before run() is ever called, so this needs no kubectl binary on PATH.
func TestGetRawJSONRejectsPathTraversal(t *testing.T) {
	verr := getRawJSON(context.Background(), selection{},
		"/apis/metrics.k8s.io/v1beta1/namespaces/x/../../../../../api/v1/nodes/n/proxy", &struct{}{})
	if verr == nil {
		t.Fatal("a path containing .. was accepted")
	}
	if verr.Code != "kube.path.invalid" {
		t.Errorf("code = %q, want kube.path.invalid", verr.Code)
	}
}

// The end-to-end shape of the bug: an MCP-settable namespace reaching
// runMetricsPod must be refused before any path is built from it, not
// merely logged or silently truncated.
func TestMetricsPodRefusesATraversalNamespace(t *testing.T) {
	req := plugin.NewRequest(map[string]any{"namespace": "x/../../../../../api/v1/nodes/n/proxy"}, false, false)
	_, err := runMetricsPod(context.Background(), req)
	ve := view.AsError(err, "x")
	if ve == nil || ve.Code != "kube.name.invalid" {
		t.Fatalf("err = %v, want kube.name.invalid", err)
	}
}

func TestCPUCores(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.5, "500m"},
		{0.05, "50m"},
		{1, "1.00"},
		{2.5, "2.50"},
	}
	for _, c := range cases {
		if got := cpuCores(c.in); got != c.want {
			t.Errorf("cpuCores(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBudgetOf(t *testing.T) {
	p := podSpecItem{}
	p.Spec.Containers = []struct {
		Resources struct {
			Limits map[string]string `json:"limits"`
		} `json:"resources"`
	}{
		{Resources: struct {
			Limits map[string]string `json:"limits"`
		}{Limits: map[string]string{"cpu": "500m", "memory": "256Mi"}}},
		{Resources: struct {
			Limits map[string]string `json:"limits"`
		}{Limits: map[string]string{"cpu": "250m", "memory": "128Mi"}}},
	}
	b := budgetOf(p)
	if b.cpuLimit != 0.75 {
		t.Errorf("cpuLimit = %v, want 0.75 (sum of both containers)", b.cpuLimit)
	}
	if b.memLimit != 384*1024*1024 {
		t.Errorf("memLimit = %v, want 384Mi in bytes", b.memLimit)
	}
}

func TestBudgetOfNoLimits(t *testing.T) {
	// A container with no limits set is the ordinary case, not an error —
	// the pod simply has no ceiling to measure pressure against.
	p := podSpecItem{}
	p.Spec.Containers = []struct {
		Resources struct {
			Limits map[string]string `json:"limits"`
		} `json:"resources"`
	}{{}}
	b := budgetOf(p)
	if b.cpuLimit != 0 || b.memLimit != 0 {
		t.Errorf("budgetOf(no limits) = %+v, want the zero value", b)
	}
}

// The command a missing metrics-server is checked with is asked of the context
// the call read: bare it asks the context of the shell it is pasted into.
func TestTheApiservicesCheckAMissingMetricsServerOffersAsksTheContextTheCallRead(t *testing.T) {
	script := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'Error from server (NotFound): not found' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := kubectlBin
	kubectlBin = script
	t.Cleanup(func() { kubectlBin = orig })

	const kubeContext = "arn:aws:eks:eu-west-3:1234:cluster/prod"
	verr := getRawJSON(context.Background(), selection{Context: kubeContext}, "/apis/metrics.k8s.io/v1beta1/nodes", &struct{}{})
	want := "`kubectl " + plugin.ShellWord("--context="+kubeContext) + " get apiservices`"
	if verr == nil || !strings.Contains(verr.Hint, want) {
		t.Errorf("error = %+v, want a hint naming %s", verr, want)
	}
}

func TestRawArgs(t *testing.T) {
	got := rawArgs(selection{Context: "kind-lab"}, "/apis/metrics.k8s.io/v1beta1/nodes")
	want := []string{"get", "--raw", "/apis/metrics.k8s.io/v1beta1/nodes",
		"--request-timeout=" + requestTimeout, "--context=kind-lab"}
	if len(got) != len(want) {
		t.Fatalf("rawArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rawArgs[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// No --namespace, no --all-namespaces, ever — --raw does not understand
	// them, and rawArgs' whole reason to exist rather than reusing
	// selection.args is not emitting them.
	//
	// Checked as two selections rather than one carrying both, because
	// selectionOf refuses that combination: a single case asserting about a
	// state no caller can produce would look like coverage and be none.
	for _, s := range []selection{{AllNS: true}, {Namespace: "prod"}} {
		got = rawArgs(s, "/apis/metrics.k8s.io/v1beta1/pods")
		for _, a := range got {
			if a == "--all-namespaces" || a == "--namespace=prod" {
				t.Errorf("rawArgs leaked a resource-listing flag into a --raw call: %v", got)
			}
		}
	}
}

// A pod's memory is the sum of its containers' usage, read against the sum
// of their limits: 96Mi and 32Mi under 192Mi and 64Mi of limits is one row
// of 128 MiB at 50%. kubectl is a script answering the two calls the view
// makes, so what is checked is the row the table carries, not a helper on
// the way to it.
func TestAPodsMemoryIsItsContainersSummedAgainstTheirLimits(t *testing.T) {
	metrics := `{"items":[{"metadata":{"name":"api","namespace":"prod"},"containers":[` +
		`{"usage":{"cpu":"100m","memory":"96Mi"}},{"usage":{"cpu":"50m","memory":"32Mi"}}]}]}`
	specs := `{"items":[{"metadata":{"name":"api","namespace":"prod"},"spec":{"containers":[` +
		`{"resources":{"limits":{"memory":"192Mi"}}},{"resources":{"limits":{"memory":"64Mi"}}}]}}]}`
	// Single-quoted for the shell, which is safe because this test writes
	// every byte of both answers and neither holds a single quote.
	script := filepath.Join(t.TempDir(), "kubectl")
	body := "#!/bin/sh\ncase \"$*\" in\n" +
		"*--raw*) printf '%s\\n' '" + metrics + "' ;;\n" +
		"*\"get pods\"*) printf '%s\\n' '" + specs + "' ;;\n" +
		"*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := kubectlBin
	kubectlBin = script
	t.Cleanup(func() { kubectlBin = orig })

	v, err := runMetricsPod(context.Background(), plugin.NewRequest(map[string]any{"namespace": "prod"}, false, false))
	if err != nil {
		t.Fatal(err)
	}
	rows := v.(view.Table).Rows
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want one pod", rows)
	}
	if got := rows[0][3]; got != "128.0 MiB" {
		t.Errorf("memory = %q, want 128.0 MiB, both containers summed", got)
	}
	if got := rows[0][4]; got != "50%" {
		t.Errorf("memory %% = %q, want 50%% of both limits summed", got)
	}
}
