package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// classifyAs is classify for a call of this kind, through the surface a person
// reads at a terminal.
func classifyAs(t *testing.T, stderr string, args ...string) *view.Error {
	t.Helper()
	s, verr := selectionOf(req(map[string]any{"context": "staging"}).WithSurface(plugin.SurfaceCLI))
	if verr != nil {
		t.Fatal(verr)
	}
	return classify(context.Background(), &exec.ExitError{}, stderr, args, s.caller())
}

// A refusal names the resource and the verb the cluster actually refused.
//
// It used to word every one of them as not being allowed to read the Cluster
// resource with the verb get, so an identity that could read clusters and not
// volumes, or not backups, or not create one, was told it lacked a right it
// had — and sent to ask for it.
func TestARefusalNamesTheResourceAndTheVerbThatWasActuallyRefused(t *testing.T) {
	for _, tc := range []struct {
		name, stderr string
		args         []string
		message      string
		hint         []string
		notHint      string
	}{
		{
			name: "volumes, listed in a namespace",
			stderr: `Error from server (Forbidden): persistentvolumeclaims is forbidden: User "system:serviceaccount:ci:reader" ` +
				`cannot list resource "persistentvolumeclaims" in API group "" in the namespace "prod"`,
			args:    []string{"get", "persistentvolumeclaims", "-o", "json", "-l", "cnpg.io/cluster=shop"},
			message: "not allowed to list persistentvolumeclaims in namespace prod",
			hint: []string{"`list` on `persistentvolumeclaims`",
				"`kubectl --context=staging auth can-i list persistentvolumeclaims --namespace=prod`"},
			notHint: clusterCRD,
		},
		{
			name: "a backup, created",
			stderr: `Error from server (Forbidden): error when creating "STDIN": backups.postgresql.cnpg.io is forbidden: ` +
				`User "u" cannot create resource "backups" in API group "postgresql.cnpg.io" in the namespace "prod"`,
			args:    []string{"create", "-f", "-", "-o", "json"},
			message: "not allowed to create backups.postgresql.cnpg.io in namespace prod",
			hint:    []string{"`create` on `backups.postgresql.cnpg.io`"},
			notHint: clusterCRD,
		},
		{
			name: "clusters, read in every namespace",
			stderr: `Error from server (Forbidden): clusters.postgresql.cnpg.io is forbidden: User "u" ` +
				`cannot list resource "clusters" in API group "postgresql.cnpg.io" at the cluster scope`,
			args:    []string{"get", clusterCRD, "-o", "json", "--all-namespaces"},
			message: "not allowed to list clusters.postgresql.cnpg.io in every namespace",
			hint:    []string{"auth can-i list clusters.postgresql.cnpg.io --all-namespaces"},
		},
		{
			name:    "an answer that carries only the resource, and a get with a name",
			stderr:  `Error from server (Forbidden): clusters.postgresql.cnpg.io is forbidden`,
			args:    []string{"get", clusterCRD, "shop", "-o", "json"},
			message: "not allowed to get clusters.postgresql.cnpg.io",
			hint:    []string{"`get` on `clusters.postgresql.cnpg.io`"},
		},
		{
			name:    "an answer that carries only the resource, and a get with none",
			stderr:  `Error from server (Forbidden): backups.postgresql.cnpg.io is forbidden`,
			args:    []string{"get", backupCRD, "-o", "json"},
			message: "not allowed to list backups.postgresql.cnpg.io",
			hint:    []string{"`list` on `backups.postgresql.cnpg.io`"},
			notHint: clusterCRD,
		},
		{
			name:    "an answer that says nothing and a call that names nothing",
			stderr:  `forbidden`,
			message: "not allowed to read the resource kubectl asked for",
			hint:    []string{"the right it lacks is one to read"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAs(t, tc.stderr, tc.args...)
			if got.Code != "cnpg.denied" {
				t.Fatalf("code = %q, want cnpg.denied", got.Code)
			}
			if got.Message != tc.message {
				t.Errorf("message = %q, want %q", got.Message, tc.message)
			}
			for _, want := range tc.hint {
				if !strings.Contains(got.Hint, want) {
					t.Errorf("hint = %q, want %q in it", got.Hint, want)
				}
			}
			if tc.notHint != "" && (strings.Contains(got.Hint, tc.notHint) || strings.Contains(got.Message, tc.notHint)) {
				t.Errorf("the refusal of another resource names %s: %q / %q", tc.notHint, got.Message, got.Hint)
			}
		})
	}
}

// The answer is the only source that names a verb the argv cannot: a get with
// -l is a list, and the same call refused by an API server that says so must be
// worded by the server's own sentence, which can name a different right than
// the one the argv suggests.
func TestTheClustersOwnWordsBeatTheGuessFromTheArgv(t *testing.T) {
	got := classifyAs(t,
		`Error from server (Forbidden): persistentvolumeclaims is forbidden: User "u" cannot watch resource "persistentvolumeclaims" in API group "" in the namespace "prod"`,
		"get", "persistentvolumeclaims", "shop-1", "-o", "json")
	if !strings.Contains(got.Message, "watch persistentvolumeclaims") {
		t.Errorf("message = %q, want the verb the server named", got.Message)
	}
}

// A missing CRD is told apart from an API server that answers 404 for a type
// it lists, and neither is called a missing operator: that is a process
// kubectl cannot see, and the one thing a registered CRD with the operator
// gone shows is a normal answer.
func TestAMissingCRDAndAnUnservedTypeAreToldApart(t *testing.T) {
	missing := classifyAs(t, `error: the server doesn't have a resource type "clusters"`,
		"get", clusterCRD, "-o", "json")
	unserved := classifyAs(t, `Error from server (NotFound): the server could not find the requested resource`,
		"get", clusterCRD, "-o", "json")

	if missing.Code != "cnpg.crd.missing" || unserved.Code != "cnpg.resource.unserved" {
		t.Fatalf("codes = %q and %q", missing.Code, unserved.Code)
	}
	if strings.Contains(missing.Message+unserved.Message, "operator") {
		t.Errorf("a message claims what kubectl cannot see: %q / %q", missing.Message, unserved.Message)
	}
	for _, got := range []*view.Error{missing, unserved} {
		if !strings.Contains(got.Message, clusterCRD) {
			t.Errorf("message = %q, want the type that was asked for", got.Message)
		}
	}
	if !strings.Contains(unserved.Hint, "aggregator") || !strings.Contains(unserved.Hint, "api-resources") {
		t.Errorf("hint = %q, want the causes of a 404 for a registered type", unserved.Hint)
	}
}

// Any other absent type is named as what it is, with the command that
// confirms it — the Cluster wording is about the Cluster type.
func TestAnotherAbsentTypeIsNamedAndNotCalledTheClusterCRD(t *testing.T) {
	got := classifyAs(t, `error: the server doesn't have a resource type "objectstores"`,
		"get", "objectstores.barmancloud.cnpg.io", "s3-store", "-o", "json")
	if got.Code != "cnpg.crd.missing" || !strings.Contains(got.Message, "objectstores.barmancloud.cnpg.io") {
		t.Errorf("got %q %q", got.Code, got.Message)
	}
	if strings.Contains(got.Hint, "cnpg-system") || strings.Contains(got.Hint, "operator has never") {
		t.Errorf("hint = %q speaks about the operator of another type", got.Hint)
	}
	if !strings.Contains(got.Hint, "kubectl --context=staging get crd objectstores.barmancloud.cnpg.io") {
		t.Errorf("hint = %q, want the command that confirms it", got.Hint)
	}
}

// A cluster that is not found keeps the listing that finds it. Any other
// object that is not found does not: a listing of clusters is no help finding
// an object store.
func TestAnObjectThatIsNotFoundIsNamedForWhatItIs(t *testing.T) {
	cluster := classifyAs(t, `Error from server (NotFound): clusters.postgresql.cnpg.io "shop" not found`,
		"get", clusterCRD, "shop", "-o", "json")
	if cluster.Code != "cnpg.cluster.missing" || !strings.Contains(cluster.Hint, "list --all-namespaces") {
		t.Errorf("cluster: %q %q", cluster.Code, cluster.Hint)
	}

	other := classifyAs(t, `Error from server (NotFound): objectstores.barmancloud.cnpg.io "s3-store" not found`,
		"get", "objectstores.barmancloud.cnpg.io", "s3-store", "-o", "json")
	if other.Code != "cnpg.resource.missing" {
		t.Errorf("code = %q, want cnpg.resource.missing", other.Code)
	}
	if strings.Contains(other.Hint, "cnpg list") {
		t.Errorf("hint = %q sends a missing object store to the listing of clusters", other.Hint)
	}
	if !strings.Contains(other.Hint, "get objectstores.barmancloud.cnpg.io --all-namespaces") {
		t.Errorf("hint = %q, want the command that shows where one is", other.Hint)
	}
}

// The path a real call takes, from the process that prints the refusal to the
// error the reader gets: the argv the call built is what the fallback reads.
func TestAFailedVolumeReadIsRefusedInTheWordsOfTheVolumeRead(t *testing.T) {
	fakeKubectl(t, `echo 'Error from server (Forbidden): persistentvolumeclaims is forbidden: User "u" cannot list resource "persistentvolumeclaims" in API group "" in the namespace "prod"' >&2
exit 1
`)
	_, err := runStorage(context.Background(), req(map[string]any{"cluster": "shop", "namespace": "prod"}))
	verr := asViewError(t, err)
	if verr.Code != "cnpg.denied" || !strings.Contains(verr.Message, "list persistentvolumeclaims in namespace prod") {
		t.Errorf("got %q %q, want the refusal of the volume read", verr.Code, verr.Message)
	}
}
