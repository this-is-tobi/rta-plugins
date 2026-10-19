package main

import (
	"context"
	"errors"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A dashboard stops asking a tile whose credential was refused twice, and it
// reads that off the code (view.Error.CredentialRefused): the API server
// answering 401 or 403 is the refusal, and every other way a call fails is not.
// The two sign-in failures that never reach the API server stay out of it, as
// does a cluster that does not answer — a tile that stopped asking after those
// would be hiding a lapsed session or an outage, not sparing an account.
func TestOnlyTheAPIServerRefusingTheCredentialIsACredentialRefusal(t *testing.T) {
	for name, tc := range map[string]struct {
		stderr  string
		code    string
		refused bool
	}{
		"the credential is not accepted": {
			"error: You must be logged in to the server (Unauthorized)", "kube.auth.failed", true},
		"a bearer token that is not valid": {
			`Error from server (Unauthorized): invalid bearer token`, "kube.auth.failed", true},
		"the credential may not do this": {
			`Error from server (Forbidden): pods is forbidden: User "u" cannot list resource "pods" in the namespace "gitea"`,
			"kube.denied", true},
		"the server asking for credentials": {
			"error: You must be logged in to the server (the server has asked for the client to provide credentials)",
			"kube.auth.failed", true},
		"the server asking for credentials, said bare": {
			"the server has asked for the client to provide credentials", "kube.auth.failed", true},
		"a credential plugin that refused":          {lapsedTsh, "kube.login", false},
		"a credential plugin that is not installed": {missingBare, "kube.credential.missing", false},
		"a credential plugin that could not run": {
			"Unable to connect to the server: getting credentials: exec: fork/exec /usr/local/bin/aws: permission denied",
			"kube.login", false},
		"a credential plugin configured wrongly": {
			`error: exec plugin: invalid apiVersion "client.authentication.k8s.io/v1alpha1"`, "kube.login", false},
		"a credential plugin that gave nothing": {
			`error: the credential plugin of user "u" returned no credential`, "kube.login", false},
		"a failure that only mentions credentials": {
			"error: unable to refresh the user's credentials: the refresh token has expired", "kube.failed", false},
		"an auth provider kubectl no longer has": {
			`error: no Auth Provider found for name "gcp"`, "kube.credential.missing", false},
		"a plugin's failure beside the server's 401": {
			"Unable to connect to the server: getting credentials: exec: fork/exec /opt/x/auth: permission denied\n" +
				"error: You must be logged in to the server (Unauthorized)", "kube.login", false},
		"a cluster that does not answer": {
			"Unable to connect to the server: dial tcp 10.0.0.1:6443: connect: no route to host", "kube.unreachable", false},
		"no kubeconfig":               {"error: no configuration has been provided", "kube.noconfig", false},
		"an object that is not there": {`Error from server (NotFound): pods "x" not found`, "kube.notfound", false},
	} {
		t.Run(name, func(t *testing.T) {
			verr := classify(context.Background(), errors.New("exit status 1"), tc.stderr, []string{"get", "pods"}, plugin.SurfaceCLI)
			if verr.Code != tc.code {
				t.Fatalf("code = %s, want %s: %v", verr.Code, tc.code, verr)
			}
			if got := verr.CredentialRefused(); got != tc.refused {
				t.Errorf("%s.CredentialRefused() = %v, want %v", verr.Code, got, tc.refused)
			}
		})
	}
}

// A call the cluster refused as a whole reaches its caller with the code that a
// dashboard reads, not wrapped into a page that hides it.
func TestAWholeCallTheClusterRefusedReachesTheCallerAsACredentialRefusal(t *testing.T) {
	for name, tc := range map[string]struct{ stderr, code string }{
		"forbidden":    {`Error from server (Forbidden): pods is forbidden: User "u" cannot list resource "pods" in the namespace "gitea"`, "kube.denied"},
		"unauthorized": {"error: You must be logged in to the server (Unauthorized)", "kube.auth.failed"},
	} {
		t.Run(name, func(t *testing.T) {
			refusingKubectl(t, tc.stderr)
			_, err := runPodList(context.Background(), plugin.NewRequest(map[string]any{"namespace": "gitea"}, false, false))
			var verr *view.Error
			if !errors.As(err, &verr) {
				t.Fatalf("err = %#v, want a *view.Error", err)
			}
			if verr.Code != tc.code || !verr.CredentialRefused() {
				t.Errorf("code = %s (credential refused: %v), want %s refused", verr.Code, verr.CredentialRefused(), tc.code)
			}
		})
	}
}
