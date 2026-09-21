package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A context whose user is an exec credential plugin fails through that
// plugin's own words: tsh prints `ERROR: Not logged in.` onto kubectl's
// stderr, client-go retries a few times, and kubectl's diagnosis comes last.
// The first-line switch took the plugin's line and matched nothing in it,
// so a lapsed Teleport session — the failure a person hits every morning —
// came out as `kube.failed ERROR: Not logged in.`, with the word twice and
// no hint.

// lapsedTsh is kubectl's stderr, verbatim, for a tsh session that lapsed:
// captured against a kubeconfig whose exec plugin printed what tsh prints.
const lapsedTsh = `ERROR: Not logged in.
E0921 02:57:38.196791   96330 memcache.go:265] "Unhandled Error" err="couldn't get current server API group list: Get \"https://127.0.0.1:1/api?timeout=32s\": getting credentials: exec: executable /opt/homebrew/bin/tsh failed with exit code 1"
ERROR: Not logged in.
Unable to connect to the server: getting credentials: exec: executable /opt/homebrew/bin/tsh failed with exit code 1
`

func TestACredentialPluginRefusingIsASignInProblemNamedAfterThePlugin(t *testing.T) {
	verr := classify(context.Background(), errors.New("exit status 1"), lapsedTsh, []string{"get", "namespaces"})
	if verr.Code != "kube.login" {
		t.Fatalf("code = %s, want kube.login: %v", verr.Code, verr)
	}
	if !strings.Contains(verr.Message, "tsh") || !strings.Contains(verr.Message, "Not logged in.") {
		t.Errorf("message = %q, want the plugin named and its own words kept", verr.Message)
	}
	if strings.Contains(verr.Message, "ERROR:") {
		t.Errorf("message = %q, still carries the plugin's ERROR: prefix", verr.Message)
	}
	if !strings.Contains(verr.Hint, "tsh login") {
		t.Errorf("hint = %q, want the command that refreshes the session", verr.Hint)
	}
}

func TestACredentialPluginIsNamedByItsBaseNameAndItsExitCodeWhenSilent(t *testing.T) {
	cases := []struct {
		name, stderr, wantExe, wantSaid, wantHint string
	}{
		{"aws sso, silent",
			"Unable to connect to the server: getting credentials: exec: executable /usr/local/bin/aws failed with exit code 255\n",
			"aws", "exit code 255, with nothing said", "aws sso login"},
		{"a plugin nothing here knows",
			"token expired\nUnable to connect to the server: getting credentials: exec: executable /opt/acme/acme-auth failed with exit code 3\n",
			"acme-auth", "token expired", "sign in with acme-auth"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verr := classify(context.Background(), errors.New("exit status 1"), c.stderr, nil)
			if verr.Code != "kube.login" {
				t.Fatalf("code = %s, want kube.login: %v", verr.Code, verr)
			}
			if !strings.HasPrefix(verr.Message, c.wantExe+",") || !strings.Contains(verr.Message, c.wantSaid) {
				t.Errorf("message = %q, want %s and %q", verr.Message, c.wantExe, c.wantSaid)
			}
			if !strings.Contains(verr.Hint, c.wantHint) {
				t.Errorf("hint = %q, want %q", verr.Hint, c.wantHint)
			}
		})
	}
}

// A refusal that is not a credential plugin's stays what it was: the arm
// above must not swallow the API server's own unauthorized.
func TestAServerUnauthorizedIsNotMistakenForAPluginRefusing(t *testing.T) {
	verr := classify(context.Background(), errors.New("exit status 1"),
		"error: You must be logged in to the server (Unauthorized)\n", nil)
	if verr.Code != "kube.unauthorized" {
		t.Errorf("code = %s, want kube.unauthorized", verr.Code)
	}
}

// overview reports it as the cluster never having been contacted, which is
// what happened: the plugin refused before any request left the machine.
func TestOverviewSaysALapsedSessionNeverReachedTheCluster(t *testing.T) {
	refusingKubectl(t, strings.TrimSpace(lapsedTsh))
	v, err := runOverview(context.Background(), plugin.NewRequest(map[string]any{}, false, false))
	if err != nil {
		t.Fatalf("overview refused outright instead of reporting: %v", err)
	}
	body := strings.ToLower(fmt.Sprintf("%#v", v))
	if !strings.Contains(body, "was never contacted") || !strings.Contains(body, "tsh") {
		t.Errorf("overview said:\n%s", body)
	}
	if strings.Contains(body, "did not answer") {
		t.Errorf("overview claimed silence over a sign-in problem:\n%s", body)
	}
}

// A plugin the kubeconfig names and this machine does not have is the
// mechanism's other failure, and it read as an expired credential: the
// first line of stderr is client-go's own log line, which carries the word
// credentials, so the hint told the person to sign in with a binary they
// did not have. Both shapes kubectl prints, captured against a kubeconfig
// naming a plugin that is not there: client-go's own words for a bare name
// found nowhere on PATH, the operating system's for a path.
const (
	missingBare = `E0921 04:14:02.117054   94480 memcache.go:265] "Unhandled Error" err=<
	couldn't get current server API group list: Get "https://127.0.0.1:1/api?timeout=32s": getting credentials: exec: executable acme-auth not found

	It looks like you are trying to use a client-go credential plugin that is not installed.

	To learn more about this feature, consult the documentation available at:
	      https://kubernetes.io/docs/reference/access-authn-authz/authentication/#client-go-credential-plugins
 >
Unable to connect to the server: getting credentials: exec: executable acme-auth not found

It looks like you are trying to use a client-go credential plugin that is not installed.

To learn more about this feature, consult the documentation available at:
      https://kubernetes.io/docs/reference/access-authn-authz/authentication/#client-go-credential-plugins
`
	missingPath = `E0921 04:13:36.061482   94227 memcache.go:265] "Unhandled Error" err="couldn't get current server API group list: Get \"https://127.0.0.1:1/api?timeout=32s\": getting credentials: exec: fork/exec /opt/acme/acme-auth: no such file or directory"
Unable to connect to the server: getting credentials: exec: fork/exec /opt/acme/acme-auth: no such file or directory
`
)

func TestAMissingCredentialPluginIsNamedAndNotMistakenForAnExpiredOne(t *testing.T) {
	for name, stderr := range map[string]string{"a bare name": missingBare, "a path": missingPath} {
		t.Run(name, func(t *testing.T) {
			verr := classify(context.Background(), errors.New("exit status 1"), stderr, []string{"get", "namespaces"})
			if verr.Code != "kube.credential.missing" {
				t.Fatalf("code = %s, want kube.credential.missing: %v", verr.Code, verr)
			}
			if !strings.HasPrefix(verr.Message, "acme-auth,") {
				t.Errorf("message = %q, want the plugin named by its base name", verr.Message)
			}
			if strings.Contains(verr.Hint, "sign in") || !strings.Contains(verr.Hint, "install") {
				t.Errorf("hint = %q, want an install, not a sign-in", verr.Hint)
			}
		})
	}
}

func TestOverviewSaysAMissingPluginNeverReachedTheCluster(t *testing.T) {
	refusingKubectl(t, strings.TrimSpace(missingPath))
	v, err := runOverview(context.Background(), plugin.NewRequest(map[string]any{}, false, false))
	if err != nil {
		t.Fatalf("overview refused outright instead of reporting: %v", err)
	}
	body := strings.ToLower(fmt.Sprintf("%#v", v))
	if !strings.Contains(body, "was never contacted") || !strings.Contains(body, "acme-auth") {
		t.Errorf("overview said:\n%s", body)
	}
}
