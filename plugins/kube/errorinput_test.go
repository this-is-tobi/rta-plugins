package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// noKubectl points kubectlBin at a binary that is not there, for a refusal that
// has to be made before anything is asked of a cluster: one that got through by
// mistake answers kube.kubectl.missing, and not the code the case wants.
func noKubectl(t *testing.T) {
	t.Helper()
	orig := kubectlBin
	kubectlBin = filepath.Join(t.TempDir(), "no-kubectl")
	t.Cleanup(func() { kubectlBin = orig })
}

// kubeconfigNaming is `kubectl config view -o json` of a kubeconfig that holds
// the one context prod, and whose current context is current: prod, a context
// it does not hold, or none.
func kubeconfigNaming(current string) string {
	return `{"current-context":"` + current + `","contexts":[{"name":"prod","context":{"cluster":"c","user":"u"}}],` +
		`"clusters":[{"name":"c","cluster":{"server":"https://10.0.0.1"}}]}`
}

// refusedInput runs capability id with values, as a call through sf, and
// returns the refusal it answered with. It fails the test when the refusal is
// not code, or when the input it names is one id does not declare: every
// surface ignores such a name, so a misspelt one would pass for no name at all.
func refusedInput(t *testing.T, id string, sf plugin.Surface, values map[string]any, code string) *view.Error {
	t.Helper()
	c := capabilityNamed(t, id)
	_, err := c.Run(context.Background(), saReq(sf, false, values))
	if err == nil {
		t.Fatalf("%s %v: answered, want %s", id, values, code)
	}
	ve := view.AsError(err, "kube.test")
	if ve.Code != code {
		t.Fatalf("%s %v: %s (%s), want %s", id, values, ve.Code, ve.Message, code)
	}
	if _, ok := c.ErrorInput(ve); ve.Input != "" && !ok {
		t.Fatalf("%s: %s names %q, which it does not declare", id, code, ve.Input)
	}
	return ve
}

// A refusal of the value one input was given names that input, by the name the
// capability that refused it declares, so the form the call came from opens
// again on that box and an agent knows which argument to send again: a name
// kubectl would read as a flag, a namespace or a node that is no DNS name, a
// context the kubeconfig does not hold, a node the cluster does not have, more
// namespaces than one call reads, a ttl under the TokenRequest floor, a grant
// that maps to no rules, an out that cannot be written, a ServiceAccount that is
// not there.
//
// Beside them, the refusals that name none on purpose: a namespace beside every
// namespace (two inputs at odds), a cluster that did not answer or refused the
// credential, the kubeconfig's own current context (no box of the call holds
// it), a ServiceAccount that is there and is not provision's, and provision
// asked for over MCP.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(existing, []byte("A WORKING KUBECONFIG"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(dir, "a-directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	answering := func(stdout string) func(*testing.T) {
		return func(t *testing.T) { withFixtureKubectl(t, stdout) }
	}
	refusing := func(stderr string) func(*testing.T) {
		return func(t *testing.T) { refusingKubectl(t, stderr) }
	}
	const nodes = `{"items":[{"metadata":{"name":"worker-1"}}]}`
	provision := func(values map[string]any) map[string]any {
		out := map[string]any{"name": "agent-a", "namespace": "team-prod", "grant": []string{"kube.pod.list"}, "ttl": "15m"}
		for k, v := range values {
			out[k] = v
		}
		return out
	}

	for _, c := range []struct {
		why     string
		id      string
		sf      plugin.Surface
		kubectl func(*testing.T)
		values  map[string]any
		code    string
		input   string
	}{
		{why: "a name kubectl would read as a flag", id: "kube.context.set",
			values: map[string]any{"name": "--kubeconfig=/tmp/theirs"}, code: "kube.name.invalid", input: "name"},
		{why: "a name of white space alone", id: "kube.context.set",
			values: map[string]any{"name": "   "}, code: "kube.context.empty", input: "name"},
		{why: "a name the kubeconfig holds no context of", id: "kube.context.set", kubectl: answering(kubeconfigNaming("prod")),
			values: map[string]any{"name": "ghost"}, code: "kube.context.unknown", input: "name"},

		{why: "a context kubectl would read as a flag", id: "kube.context.get",
			values: map[string]any{"context": "-x"}, code: "kube.name.invalid", input: "context"},
		{why: "a context the kubeconfig does not hold", id: "kube.context.get", kubectl: answering(kubeconfigNaming("prod")),
			values: map[string]any{"context": "ghost"}, code: "kube.context.unknown", input: "context"},
		{why: "a current context the kubeconfig does not hold", id: "kube.context.get", kubectl: answering(kubeconfigNaming("gone")),
			values: map[string]any{}, code: "kube.context.unknown", input: ""},
		{why: "a kubeconfig with no current context", id: "kube.context.get", kubectl: answering(kubeconfigNaming("")),
			values: map[string]any{}, code: "kube.context.none", input: ""},

		{why: "a namespace that is no DNS label", id: "kube.pod.list",
			values: map[string]any{"namespace": []string{"gitea", "Gitea"}}, code: "kube.name.invalid", input: "namespace"},
		{why: "more namespaces than one call reads", id: "kube.pod.list",
			values: map[string]any{"namespace": manyNamespaces(maxNamespaces + 1)}, code: "kube.namespaces.toomany", input: "namespace"},
		{why: "a namespace beside every namespace", id: "kube.pod.list",
			values: map[string]any{"namespace": []string{"gitea"}, "all-namespaces": true}, code: "kube.namespace.ambiguous", input: ""},
		{why: "a context kubectl does not know", id: "kube.pod.list", kubectl: refusing(`error: context "ghost" does not exist`),
			values: map[string]any{"context": "ghost"}, code: "kube.context.unknown", input: "context"},
		{why: "a current context kubectl does not know", id: "kube.pod.list",
			kubectl: refusing("Error in configuration: context was not found for specified context: gone"),
			values:  map[string]any{}, code: "kube.context.unknown", input: ""},
		{why: "a kubeconfig kubectl finds no current context in", id: "kube.pod.list", kubectl: refusing("error: current-context is not set"),
			values: map[string]any{}, code: "kube.context.none", input: ""},
		{why: "a cluster that does not answer the context given", id: "kube.pod.list",
			kubectl: refusing("The connection to the server 10.0.0.1:6443 was refused - did you specify the right host or port?"),
			values:  map[string]any{"context": "prod"}, code: "kube.unreachable", input: ""},
		{why: "a credential the API server refused", id: "kube.pod.list",
			kubectl: refusing("error: You must be logged in to the server (Unauthorized)"),
			values:  map[string]any{"context": "prod"}, code: "kube.auth.failed", input: ""},

		{why: "a namespace that is a path", id: "kube.overview",
			values: map[string]any{"namespace": []string{"x/../../nodes"}}, code: "kube.name.invalid", input: "namespace"},
		{why: "more namespaces than one call reads", id: "kube.overview",
			values: map[string]any{"namespace": manyNamespaces(maxNamespaces + 1)}, code: "kube.namespaces.toomany", input: "namespace"},
		{why: "a namespace that is no DNS label", id: "kube.namespace.list",
			values: map[string]any{"namespace": []string{"Bad_ns"}}, code: "kube.name.invalid", input: "namespace"},
		{why: "a context kubectl would read as a flag", id: "kube.node.list",
			values: map[string]any{"context": "--kubeconfig=/tmp/theirs"}, code: "kube.name.invalid", input: "context"},

		{why: "a node that would reach exec through the proxy", id: "kube.metrics.pressure",
			values: map[string]any{"node": "pi1/proxy/exec/default/mypod/mycontainer"}, code: "kube.name.invalid", input: "node"},
		{why: "a node the cluster does not have", id: "kube.metrics.pressure", kubectl: answering(nodes),
			values: map[string]any{"node": "ghost"}, code: "kube.node.unknown", input: "node"},
		{why: "a node name in capitals", id: "kube.pvc.usage",
			values: map[string]any{"node": "PI1"}, code: "kube.name.invalid", input: "node"},
		{why: "a node the cluster does not have", id: "kube.pvc.usage", kubectl: answering(nodes),
			values: map[string]any{"node": "ghost"}, code: "kube.node.unknown", input: "node"},
		{why: "a namespace kubectl would read as a flag", id: "kube.pvc.usage",
			values: map[string]any{"namespace": []string{"-bad"}}, code: "kube.name.invalid", input: "namespace"},

		{why: "a name kubectl would read as a flag", id: "kube.serviceaccount.provision",
			values: provision(map[string]any{"name": "-agent"}), code: "kube.name.invalid", input: "name"},
		{why: "a name of white space alone", id: "kube.serviceaccount.provision",
			values: provision(map[string]any{"name": " "}), code: "kube.serviceaccount.name.empty", input: "name"},
		{why: "a namespace kubectl would read as a flag", id: "kube.serviceaccount.provision",
			values: provision(map[string]any{"namespace": "-team"}), code: "kube.name.invalid", input: "namespace"},
		{why: "a namespace of white space alone", id: "kube.serviceaccount.provision",
			values: provision(map[string]any{"namespace": " "}), code: "kube.serviceaccount.namespace.empty", input: "namespace"},
		{why: "a ttl under the TokenRequest floor", id: "kube.serviceaccount.provision",
			values: provision(map[string]any{"ttl": "5m"}), code: "kube.serviceaccount.ttl.invalid", input: "ttl"},
		{why: "a ttl that is no duration", id: "kube.serviceaccount.provision",
			values: provision(map[string]any{"ttl": "soon"}), code: "kube.serviceaccount.ttl.invalid", input: "ttl"},
		{why: "no grant at all", id: "kube.serviceaccount.provision",
			values: provision(map[string]any{"grant": []string{}}), code: "kube.serviceaccount.norules", input: "grant"},
		{why: "a grant that maps to no rules", id: "kube.serviceaccount.provision",
			values: provision(map[string]any{"grant": []string{"kube.pod.list", "kube.secret.dump"}}),
			code:   "kube.serviceaccount.ungrantable", input: "grant"},
		{why: "a context kubectl would read as a flag", id: "kube.serviceaccount.provision",
			values: provision(map[string]any{"context": "--kubeconfig=/tmp/theirs"}), code: "kube.name.invalid", input: "context"},
		{why: "an out that is a file already", id: "kube.serviceaccount.provision",
			values: provision(map[string]any{"out": existing}), code: "kube.serviceaccount.out.exists", input: "out"},
		{why: "an out under a file, once the identity is minted", id: "kube.serviceaccount.provision",
			kubectl: answering(rawConfigFixture),
			values:  provision(map[string]any{"out": filepath.Join(existing, "kubeconfig")}),
			code:    "kube.serviceaccount.out.unwritable", input: "out"},
		{why: "an out that is a directory, forced, once the identity is minted", id: "kube.serviceaccount.provision",
			kubectl: answering(rawConfigFixture),
			values:  provision(map[string]any{"out": directory, "force": true}),
			code:    "kube.serviceaccount.out.unwritable", input: "out"},
		{why: "a cluster that would not create the ServiceAccount", id: "kube.serviceaccount.provision",
			kubectl: refusing("error: the server is having a bad day"),
			values:  provision(nil), code: "kube.failed", input: ""},
		{why: "a call from an agent", id: "kube.serviceaccount.provision", sf: plugin.SurfaceMCP,
			values: provision(nil), code: "kube.serviceaccount.mcp", input: ""},

		{why: "a name kubectl would read as a flag", id: "kube.serviceaccount.revoke",
			values: map[string]any{"name": "-agent", "namespace": "team-prod"}, code: "kube.name.invalid", input: "name"},
		{why: "a namespace kubectl would read as a flag", id: "kube.serviceaccount.revoke",
			values: map[string]any{"name": "agent-a", "namespace": "-team"}, code: "kube.name.invalid", input: "namespace"},
		{why: "a ServiceAccount that is not there", id: "kube.serviceaccount.revoke",
			kubectl: refusing(`Error from server (NotFound): serviceaccounts "agent-a" not found`),
			values:  map[string]any{"name": "agent-a", "namespace": "team-prod"}, code: "kube.serviceaccount.notfound", input: "name"},
		{why: "a ServiceAccount that is there and is not provision's", id: "kube.serviceaccount.revoke",
			kubectl: answering(`{"metadata":{"name":"agent-a","labels":{}}}`),
			values:  map[string]any{"name": "agent-a", "namespace": "team-prod"}, code: "kube.serviceaccount.notprovisioned", input: ""},
	} {
		t.Run(c.id+": "+c.why, func(t *testing.T) {
			kubectl := c.kubectl
			if kubectl == nil {
				kubectl = noKubectl
			}
			kubectl(t)
			sf := c.sf
			if sf == "" {
				sf = plugin.SurfaceCLI
			}
			if ve := refusedInput(t, c.id, sf, c.values, c.code); ve.Input != c.input {
				t.Errorf("%s names input %q, want %q: %s", ve.Code, ve.Input, c.input, ve.Message)
			}
		})
	}
}

// The context provision mints against is looked up again in the raw
// kubeconfig, after the cluster has been asked: a context the call named that
// is not there names the context, and the file's own current context, gone or
// never set, names none. selectionOf's own check of a namespace, behind the one
// provision and revoke make first, names the namespace.
func TestTheContextAndNamespaceHelpersNameTheirInput(t *testing.T) {
	provision := capabilityNamed(t, "kube.serviceaccount.provision")
	held := func(t *testing.T, c plugin.Capability, verr *view.Error, code, input string) {
		t.Helper()
		if verr == nil || verr.Code != code {
			t.Fatalf("%+v, want %s", verr, code)
		}
		if verr.Input != input {
			t.Errorf("%s names input %q, want %q", verr.Code, verr.Input, input)
		}
		if _, ok := c.ErrorInput(verr); verr.Input != "" && !ok {
			t.Errorf("%s: %s names %q, which it does not declare", c.ID, verr.Code, verr.Input)
		}
	}
	var cfg rawClusterConfig
	if err := json.Unmarshal([]byte(rawConfigFixture), &cfg); err != nil {
		t.Fatal(err)
	}

	_, verr := coordinatesFor(cfg, selection{Context: "does-not-exist"})
	held(t, provision, verr, "kube.context.unknown", "context")

	gone := cfg
	gone.CurrentContext = "gone"
	_, verr = coordinatesFor(gone, selection{})
	held(t, provision, verr, "kube.context.unknown", "")

	unset := cfg
	unset.CurrentContext = ""
	_, verr = coordinatesFor(unset, selection{})
	held(t, provision, verr, "kube.context.none", "")

	_, verr = selectionOf(saReq(plugin.SurfaceCLI, false, map[string]any{"namespace": "-team"}))
	held(t, capabilityNamed(t, "kube.serviceaccount.revoke"), verr, "kube.name.invalid", "namespace")
}
