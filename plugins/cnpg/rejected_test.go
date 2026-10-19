package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

const apiUnauthorized = "error: You must be logged in to the server (Unauthorized)"

// failingWith is a kubectl that fails every call with stderr.
func failingWith(t *testing.T, stderr string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "stderr")
	writeFile(t, file, stderr)
	fakeKubectl(t, "cat "+file+" >&2\nexit 1\n")
}

// A dashboard stops asking a tile whose credential was refused twice, and reads that off
// the code (view.Error.CredentialRefused). A credential plugin that failed before any
// request was made has reached no API server to be refused, and a tile that stopped
// asking after it would hide a session that comes back the moment a person signs in. So
// each of the four ways such a failure is worded is cnpg.login, which has neither ending
// and ends the queue of a list, and none is read as the 401.
func TestEveryWayACredentialPluginFailsIsLoginAndNeverARefusedCredential(t *testing.T) {
	for name, stderr := range map[string]string{
		"getting credentials": "Unable to connect to the server: getting credentials: exec: executable tsh failed with exit code 1",
		"exec plugin":         `error: exec plugin: invalid apiVersion "client.authentication.k8s.io/v1alpha1"`,
		"credential plugin":   `error: the credential plugin of user "u" returned no credential`,
		"no Auth Provider":    `error: no Auth Provider found for name "gcp"`,
	} {
		t.Run(name, func(t *testing.T) {
			failingWith(t, stderr)
			_, err := runList(context.Background(), req(nil))
			verr := asViewError(t, err)
			if verr.Code != "cnpg.login" || verr.CredentialRefused() {
				t.Fatalf("code = %s (credential refused: %v), want cnpg.login and not refused", verr.Code, verr.CredentialRefused())
			}
			if !clusterWide(verr) {
				t.Errorf("%s is read as one namespace's trouble, and asked again for each of the others", verr.Code)
			}
		})
	}
}

// The order is deliberate: a plugin's failure arrives inside client-go's retry noise,
// which can carry the API server's own words as well, and the failure to get a
// credential is what happened. Read the other way round it would be a refusal, and a
// tile would stop asking over a session that lapsed.
func TestACredentialPluginsFailureIsReadBeforeAnUnauthorizedInTheSameStderr(t *testing.T) {
	failingWith(t, "Unable to connect to the server: getting credentials: exec: executable tsh failed with exit code 1\n"+
		apiUnauthorized)
	_, err := runList(context.Background(), req(nil))
	verr := asViewError(t, err)
	if verr.Code != "cnpg.login" || verr.CredentialRefused() {
		t.Errorf("code = %s (credential refused: %v), want cnpg.login", verr.Code, verr.CredentialRefused())
	}
}

// Every wording of the API server's 401 is the refusal, and it is the only thing that is
// cnpg.auth.failed.
func TestEveryWayTheAPIServerAnswers401IsARefusedCredential(t *testing.T) {
	for name, stderr := range map[string]string{
		"unauthorized":          "error: Unauthorized",
		"must be logged in":     apiUnauthorized,
		"asked for credentials": "error: You must be logged in to the server (the server has asked for the client to provide credentials)",
		"asked, said bare":      "the server has asked for the client to provide credentials",
	} {
		t.Run(name, func(t *testing.T) {
			failingWith(t, stderr)
			_, err := runList(context.Background(), req(nil))
			verr := asViewError(t, err)
			if verr.Code != "cnpg.auth.failed" || !verr.CredentialRefused() {
				t.Errorf("code = %s (credential refused: %v), want cnpg.auth.failed refused", verr.Code, verr.CredentialRefused())
			}
		})
	}
}

// A listing over several namespaces is one page, and the page of a credential the API
// server did not accept is not one: the 401 of any namespace is the error, even when
// another namespace answered before it, because a dashboard reads a refusal off the error
// a call returns and a page cannot be one. A 403 of a namespace is a row of the page,
// since a Role in each namespace is the common restricted identity.
func TestAListingWhoseCredentialAnyNamespaceDidNotAcceptIsTheRefusalAndNotAPage(t *testing.T) {
	named := []string{"alpha", "beta", "gamma"}
	for name, run := range map[string]func(context.Context, plugin.Request) (view.View, error){
		"cnpg.list":        runList,
		"cnpg.backup.list": runBackupList,
		"cnpg.storage":     runStorage,
	} {
		t.Run(name, func(t *testing.T) {
			servesPerNamespace(t, nil, map[string]string{"alpha": apiUnauthorized, "gamma": forbiddenClusters("gamma")}, "")
			_, err := run(context.Background(), req(map[string]any{"namespace": named}))
			verr := asViewError(t, err)
			if verr.Code != "cnpg.auth.failed" || !verr.CredentialRefused() {
				t.Errorf("code = %s (credential refused: %v), want cnpg.auth.failed refused", verr.Code, verr.CredentialRefused())
			}

			servesPerNamespace(t, nil, map[string]string{"gamma": forbiddenClusters("gamma")}, "")
			v, err := run(context.Background(), req(map[string]any{"namespace": named}))
			if err != nil {
				t.Fatalf("a namespace the credential may not read ended the listing: %v", err)
			}
			if v == nil {
				t.Error("no page")
			}
		})
	}
}

// The reads beside a cluster that backup.show makes are notes and a partial warning when
// they are refused or absent, and the page of a credential the API server did not accept
// is not a note.
func TestABackupShowWhoseCredentialTheServerDidNotAcceptAfterTheClusterWasReadIsTheRefusal(t *testing.T) {
	routedKubectl(t, answers{
		clusterCRD:         {body: pluginCluster()},
		objectStoreCRD:     {body: anObjectStore()},
		backupCRD:          {body: backupItems()},
		scheduledBackupCRD: {stderr: apiUnauthorized},
	})
	_, err := showOf(t, nil)
	verr := asViewError(t, err)
	if verr.Code != "cnpg.auth.failed" || !verr.CredentialRefused() {
		t.Errorf("code = %s (credential refused: %v), want cnpg.auth.failed refused", verr.Code, verr.CredentialRefused())
	}
}
