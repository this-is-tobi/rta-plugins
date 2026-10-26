package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// noKubectl points kubectlBin at a binary that is not there, for a refusal that
// has to be made before anything is asked of a cluster: one that got through by
// mistake answers cnpg.kubectl.missing, and not the code the case wants.
func noKubectl(t *testing.T) {
	t.Helper()
	saved := kubectlBin
	kubectlBin = filepath.Join(t.TempDir(), "no-kubectl")
	t.Cleanup(func() { kubectlBin = saved })
}

// refusedInput runs capability id with values and returns the refusal it
// answered with. It fails the test when the refusal is not code, or when the
// input it names is one id does not declare: every surface ignores such a name,
// so a misspelt one would pass for no name at all.
func refusedInput(t *testing.T, id string, values map[string]any, code string) *view.Error {
	t.Helper()
	c := capabilityOf(t, id)
	_, err := c.Run(context.Background(), req(values).WithSurface(plugin.SurfaceCLI))
	if err == nil {
		t.Fatalf("%s %v: answered, want %s", id, values, code)
	}
	ve := view.AsError(err, "cnpg.test")
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
// kubectl would read as a flag, more namespaces or clusters than a listing
// reads, a context kubectl does not know, a cluster that is not there or that
// was left blank, a method, a target or an online that is none of the CRD's,
// a method the cluster is not set up for while it is for another, and no
// method where the cluster has several.
//
// Beside them, the refusals that name none on purpose: a namespace beside every
// namespace (two inputs at odds), online beside a method it means nothing for,
// the kubeconfig's own current context, a cluster that configures no backup or
// no mechanism to take one (the cluster's own spec is the fix), and a cluster
// that did not answer or refused the credential.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	const (
		missing   = `Error from server (NotFound): clusters.postgresql.cnpg.io "app-db" not found`
		forbidden = `Error from server (Forbidden): clusters.postgresql.cnpg.io "app-db" is forbidden: ` +
			`User "u" cannot get resource "clusters" in API group "postgresql.cnpg.io" in the namespace "prod"`
	)
	failing := func(stderr string) func(*testing.T) {
		return func(t *testing.T) { failingWith(t, stderr) }
	}
	serving := func(mutate ...func(map[string]any)) func(*testing.T) {
		return func(t *testing.T) { serves(t, aCluster("app-db", "prod", mutate...)) }
	}
	noMechanism := func(c map[string]any) {
		c["spec"].(map[string]any)["backup"] = map[string]any{"retentionPolicy": "30d"}
	}
	noBackup := func(c map[string]any) { delete(c["spec"].(map[string]any), "backup") }
	tooMany := make([]string, maxClusters+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("db-%02d", i)
	}

	for _, c := range []struct {
		why     string
		id      string
		kubectl func(*testing.T)
		values  map[string]any
		code    string
		input   string
	}{
		{why: "a namespace kubectl would read as a flag", id: "cnpg.list",
			values: map[string]any{"namespace": []string{"prod", "-bad"}}, code: "cnpg.name.invalid", input: "namespace"},
		{why: "a cluster kubectl would read as a flag", id: "cnpg.list",
			values: map[string]any{"cluster": []string{"--kubeconfig=/tmp/theirs"}}, code: "cnpg.name.invalid", input: "cluster"},
		{why: "more namespaces than a listing reads", id: "cnpg.list",
			values: map[string]any{"namespace": namespacesNamed(maxNamespaces + 1)}, code: "cnpg.namespaces.toomany", input: "namespace"},
		{why: "more clusters than a listing reads", id: "cnpg.list",
			values: map[string]any{"cluster": tooMany}, code: "cnpg.clusters.toomany", input: "cluster"},
		{why: "a context kubectl would read as a flag", id: "cnpg.list",
			values: map[string]any{"context": "-x"}, code: "cnpg.name.invalid", input: "context"},
		{why: "a namespace beside every namespace", id: "cnpg.list",
			values: map[string]any{"namespace": []string{"prod"}, "all-namespaces": true}, code: "cnpg.namespace.ambiguous", input: ""},
		{why: "a context kubectl does not know", id: "cnpg.list", kubectl: failing(`error: context "ghost" does not exist`),
			values: map[string]any{"context": "ghost"}, code: "cnpg.context.none", input: "context"},
		{why: "a current context kubectl does not know", id: "cnpg.list",
			kubectl: failing("Error in configuration: context was not found for specified context: gone"),
			values:  map[string]any{}, code: "cnpg.context.none", input: ""},
		{why: "a kubeconfig with no current context", id: "cnpg.list", kubectl: failing("error: current-context is not set"),
			values: map[string]any{}, code: "cnpg.context.none", input: ""},
		{why: "a cluster that does not answer the context given", id: "cnpg.list",
			kubectl: failing("Unable to connect to the server: dial tcp 10.0.0.1:6443: connect: connection refused"),
			values:  map[string]any{"context": "prod"}, code: "cnpg.unreachable", input: ""},
		{why: "a credential the API server refused", id: "cnpg.list", kubectl: failing(apiUnauthorized),
			values: map[string]any{"context": "prod"}, code: "cnpg.auth.failed", input: ""},
		{why: "a namespace kubectl would read as a flag", id: "cnpg.storage",
			values: map[string]any{"namespace": []string{"-bad"}}, code: "cnpg.name.invalid", input: "namespace"},
		{why: "more clusters than a listing reads", id: "cnpg.backup.list",
			values: map[string]any{"cluster": tooMany}, code: "cnpg.clusters.toomany", input: "cluster"},

		{why: "a cluster kubectl would read as a flag", id: "cnpg.status",
			values: map[string]any{"cluster": "-app-db"}, code: "cnpg.name.invalid", input: "cluster"},
		{why: "a namespace kubectl would read as a flag", id: "cnpg.status",
			values: map[string]any{"cluster": "app-db", "namespace": "-prod"}, code: "cnpg.name.invalid", input: "namespace"},
		{why: "a cluster that is not there", id: "cnpg.status", kubectl: failing(missing),
			values: map[string]any{"cluster": "app-db", "namespace": "prod"}, code: "cnpg.cluster.missing", input: "cluster"},
		{why: "a cluster the credential may not read", id: "cnpg.status", kubectl: failing(forbidden),
			values: map[string]any{"cluster": "app-db", "namespace": "prod"}, code: "cnpg.denied", input: ""},

		{why: "a cluster of white space alone", id: "cnpg.backup.show",
			values: map[string]any{"cluster": " "}, code: "cnpg.backup.nocluster", input: "cluster"},
		{why: "a cluster kubectl would read as a flag", id: "cnpg.backup.show",
			values: map[string]any{"cluster": "-app-db"}, code: "cnpg.name.invalid", input: "cluster"},
		{why: "a cluster that is not there", id: "cnpg.backup.show", kubectl: failing(missing),
			values: map[string]any{"cluster": "app-db"}, code: "cnpg.cluster.missing", input: "cluster"},

		{why: "a cluster of white space alone", id: "cnpg.backup.request",
			values: map[string]any{"cluster": " "}, code: "cnpg.backup.nocluster", input: "cluster"},
		{why: "a cluster kubectl would read as a flag", id: "cnpg.backup.request",
			values: map[string]any{"cluster": "-app-db"}, code: "cnpg.name.invalid", input: "cluster"},
		{why: "a cluster that is not there", id: "cnpg.backup.request", kubectl: failing(missing),
			values: map[string]any{"cluster": "app-db"}, code: "cnpg.cluster.missing", input: "cluster"},
		{why: "a method the CRD has none of", id: "cnpg.backup.request", kubectl: serving(),
			values: map[string]any{"cluster": "app-db", "method": "logical"}, code: "cnpg.backup.method.invalid", input: "method"},
		{why: "a target the CRD has none of", id: "cnpg.backup.request", kubectl: serving(),
			values: map[string]any{"cluster": "app-db", "target": "replica"}, code: "cnpg.backup.target.invalid", input: "target"},
		{why: "an online that is neither true nor false", id: "cnpg.backup.request", kubectl: serving(),
			values: map[string]any{"cluster": "app-db", "online": "maybe"}, code: "cnpg.backup.online.invalid", input: "online"},
		{why: "a method the cluster is not set up for, beside one it is", id: "cnpg.backup.request", kubectl: serving(),
			values: map[string]any{"cluster": "app-db", "method": "volumeSnapshot"},
			code:   "cnpg.backup.method.unconfigured", input: "method"},
		{why: "the in-tree method, of a cluster that archives through a plugin", id: "cnpg.backup.request",
			kubectl: serving(archivedByPlugin),
			values:  map[string]any{"cluster": "app-db", "method": "barmanObjectStore"},
			code:    "cnpg.backup.method.unconfigured", input: "method"},
		{why: "no method, of a cluster set up for two", id: "cnpg.backup.request", kubectl: serving(alsoSnapshots),
			values: map[string]any{"cluster": "app-db"}, code: "cnpg.backup.method.ambiguous", input: "method"},
		{why: "a method, of a cluster that configures no mechanism", id: "cnpg.backup.request", kubectl: serving(noMechanism),
			values: map[string]any{"cluster": "app-db", "method": "barmanObjectStore"},
			code:   "cnpg.backup.method.unconfigured", input: ""},
		{why: "no method, of a cluster that configures no mechanism", id: "cnpg.backup.request", kubectl: serving(noMechanism),
			values: map[string]any{"cluster": "app-db"}, code: "cnpg.backup.method.unconfigured", input: ""},
		{why: "a cluster that configures no backup", id: "cnpg.backup.request", kubectl: serving(noBackup),
			values: map[string]any{"cluster": "app-db"}, code: "cnpg.backup.unconfigured", input: ""},
		{why: "online beside a method it means nothing for", id: "cnpg.backup.request", kubectl: serving(),
			values: map[string]any{"cluster": "app-db", "online": "true"}, code: "cnpg.backup.online.unavailable", input: ""},
	} {
		t.Run(c.id+": "+c.why, func(t *testing.T) {
			kubectl := c.kubectl
			if kubectl == nil {
				kubectl = noKubectl
			}
			kubectl(t)
			if ve := refusedInput(t, c.id, c.values, c.code); ve.Input != c.input {
				t.Errorf("%s names input %q, want %q: %s", ve.Code, ve.Input, c.input, ve.Message)
			}
		})
	}
}
