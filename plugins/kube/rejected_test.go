package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// serverUnauthorized is what kubectl prints when the API server answered 401.
const serverUnauthorized = "error: You must be logged in to the server (Unauthorized)"

// A dashboard stops asking a tile whose call came back as an error that is a refused
// credential (view.Error.CredentialRefused), and only an error: a page cannot be one.
// The overview used to turn every failure of its reads into a page, so a tile of it on
// a cluster that did not accept its token kept sending the token, and a 401 of any
// read is now the overview's own error. A 403 stays a row, because the common
// restricted identity is refused one list and answered another.
func TestAnOverviewWhoseCredentialTheClusterDidNotAcceptIsTheRefusalAndNotAPage(t *testing.T) {
	for name, tc := range map[string]struct {
		file  string
		names []string
	}{
		"every read":                           {"get.fail", nil},
		"every read of several namespaces":     {"get.fail", homelabNamespaces},
		"the namespaces alone":                 {"list-namespaces.fail", nil},
		"the nodes alone":                      {"list-nodes.fail", nil},
		"the pods of every namespace":          {"list-pods.fail", nil},
		"the deployments of every namespace":   {"list-deployments.fail", nil},
		"the quotas of every namespace":        {"list-resourcequotas.fail", nil},
		"the certificates of every namespace":  {"secrets.fail", nil},
		"the nodes, naming namespaces":         {"list-nodes.fail", homelabNamespaces},
		"the pods of one namespace of several": {"list-pods.gitea.fail", homelabNamespaces},
		"the deployments of one namespace":     {"list-deployments.rustfs.fail", homelabNamespaces},
		"the quotas of one namespace":          {"list-resourcequotas.gitea.fail", homelabNamespaces},
		"the certificates of one namespace":    {"secrets.gitea.fail", homelabNamespaces},
		"the pods of the one namespace named":  {"list-pods.gitea.fail", []string{"gitea"}},
	} {
		for _, detail := range []bool{false, true} {
			t.Run(name+", detail "+strconv.FormatBool(detail), func(t *testing.T) {
				c := aHomelab(t)
				c.write(t, "conc.slow", "")
				c.write(t, tc.file, serverUnauthorized)
				values := map[string]any{"detail": detail}
				if tc.names != nil {
					values["namespace"] = tc.names
				}

				v, err := overviewOf(t, values)
				var verr *view.Error
				if !errors.As(err, &verr) {
					t.Fatalf("err = %v and the page %#v, want the *view.Error of the refusal", err, v)
				}
				if verr.Code != "kube.auth.failed" || !verr.CredentialRefused() {
					t.Errorf("code = %s (credential refused: %v), want kube.auth.failed refused", verr.Code, verr.CredentialRefused())
				}
				if file := tc.file; file == "get.fail" {
					if n := getCalls(t, c); n < 1 || n > 6 {
						t.Errorf("%d lists asked of a cluster that refuses the credential, want at most the 6 that fit at once", n)
					}
				}
			})
		}
	}
}

// The other side of the same reading. Nothing reached the API server to be refused
// when the credential plugin failed or is not there, so the overview is still the page
// that says the cluster was never contacted, and a tile keeps asking; and a 403 of one
// list is a row of a page that goes on.
func TestAnOverviewOfACredentialThatIsMissingOrMayNotListIsStillAPage(t *testing.T) {
	for name, tc := range map[string]struct{ stderr, status string }{
		"a credential plugin that refused":          {lapsedTsh, "fail — the cluster was never contacted"},
		"a credential plugin that is not installed": {missingPath, "fail — the cluster was never contacted"},
		"a credential plugin that could not run": {
			"Unable to connect to the server: getting credentials: exec: fork/exec /usr/local/bin/aws: permission denied",
			"fail — the cluster was never contacted"},
	} {
		t.Run(name, func(t *testing.T) {
			c := aHomelab(t)
			c.write(t, "get.fail", tc.stderr)
			pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
			if got := rowsOf(pairs)["status"]; got != tc.status {
				t.Errorf("status = %q, want %q", got, tc.status)
			}
		})
	}

	c := aHomelab(t)
	c.failIn(t, "pods", "gitea")
	pairs := compactOf(t, map[string]any{"namespace": homelabNamespaces})
	if want := "warn — pods could not be read in gitea"; rowsOf(pairs)["status"] != want {
		t.Errorf("status = %q, want %q: a 403 of one list is a row", rowsOf(pairs)["status"], want)
	}
}

// The 401 of a read the listing of quotas makes beside its first is the call's error
// too, as it is the overview's, and the 403 of the same read stays the warning it was.
func TestTheQuotaListWhoseLimitRangesTheClusterDidNotAcceptIsTheRefusal(t *testing.T) {
	c := newScriptedCluster(t)
	c.write(t, "list-limitranges.fail", serverUnauthorized)
	_, err := runQuotaList(context.Background(), plugin.NewRequest(map[string]any{"namespace": "gitea"}, false, false))
	var verr *view.Error
	if !errors.As(err, &verr) || verr.Code != "kube.auth.failed" || !verr.CredentialRefused() {
		t.Fatalf("err = %v, want the refusal kube.auth.failed", err)
	}

	c.write(t, "list-limitranges.fail", `Error from server (Forbidden): limitranges is forbidden: User "u" cannot list resource "limitranges"`)
	v, err := runQuotaList(context.Background(), plugin.NewRequest(map[string]any{"namespace": "gitea"}, false, false))
	if err != nil {
		t.Fatalf("a 403 of the limit ranges ended the call: %v", err)
	}
	if sections, ok := v.(view.Sections); !ok || len(sections.Warnings) != 1 {
		t.Errorf("a 403 of the limit ranges came back as %#v, want the quotas and a warning", v)
	}
}

// A fan-out over the nodes' kubelets is the same: the nodes were listed, and a 401 of
// what each answered is the call's error, where a 403 (nodes/proxy is the permission
// a restricted identity lacks) is the row it was.
func TestTheNodeStatsWhoseCredentialTheClusterDidNotAcceptAreTheRefusal(t *testing.T) {
	for name, run := range map[string]func(context.Context, plugin.Request) (view.View, error){
		"kube.metrics.pressure": runMetricsPressure,
		"kube.pvc.usage":        runPVCUsage,
	} {
		t.Run(name, func(t *testing.T) {
			c := newScriptedCluster(t)
			c.list(t, "nodes", nodeFx(t, "n1", "True", false, "", "v1.31.4"), nodeFx(t, "n2", "True", false, "", "v1.31.4"))
			c.write(t, "list---raw.fail", serverUnauthorized)
			_, err := run(context.Background(), plugin.NewRequest(map[string]any{}, false, false))
			var verr *view.Error
			if !errors.As(err, &verr) || verr.Code != "kube.auth.failed" || !verr.CredentialRefused() {
				t.Fatalf("err = %v, want the refusal kube.auth.failed", err)
			}

			c.write(t, "list---raw.fail", `Error from server (Forbidden): nodes "n1" is forbidden: User "u" cannot get resource "nodes/proxy"`)
			if _, err := run(context.Background(), plugin.NewRequest(map[string]any{}, false, false)); err != nil {
				t.Errorf("a 403 of the kubelets' stats ended the call: %v", err)
			}
		})
	}
}

// A credential plugin that could not give a credential never reached the API server.
// It is a sign-in that has to be done again and a plugin to check, and nothing the
// cluster turned down, so none of the words a plugin's failure comes in is
// kube.auth.failed: a tile that stopped asking after it would hide a local fault.
func TestACredentialPluginThatGaveNothingIsNeverTheServerRefusingTheCredential(t *testing.T) {
	for name, stderr := range map[string]string{
		"getting credentials": "Unable to connect to the server: getting credentials: exec: fork/exec /usr/local/bin/aws: permission denied",
		"exec plugin":         `error: exec plugin: invalid apiVersion "client.authentication.k8s.io/v1alpha1"`,
		"credential plugin":   `error: the credential plugin of user "u" returned no credential`,
	} {
		t.Run(name, func(t *testing.T) {
			verr := classify(context.Background(), errors.New("exit status 1"), stderr, nil, plugin.SurfaceCLI)
			if verr.Code != "kube.login" || verr.CredentialRefused() {
				t.Fatalf("code = %s (credential refused: %v), want kube.login and not refused", verr.Code, verr.CredentialRefused())
			}
			if !strings.Contains(verr.Hint, "nothing was refused") {
				t.Errorf("hint = %q, want it to say nothing was refused", verr.Hint)
			}
			if !clusterWide(verr.Code) {
				t.Errorf("%s is not read as the cluster's whole, and each namespace would be asked again", verr.Code)
			}
		})
	}
	verr := classify(context.Background(), errors.New("exit status 1"), `error: no Auth Provider found for name "gcp"`, nil, plugin.SurfaceCLI)
	if verr.Code != "kube.credential.missing" || verr.CredentialRefused() || !clusterWide(verr.Code) {
		t.Errorf("code = %s (credential refused: %v, cluster wide: %v), want kube.credential.missing, not refused, cluster wide",
			verr.Code, verr.CredentialRefused(), clusterWide(verr.Code))
	}
}
