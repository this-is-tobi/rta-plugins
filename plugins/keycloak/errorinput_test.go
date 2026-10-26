package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// fileHolding writes body to a file nothing else uses and returns its path.
func fileHolding(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A refusal of the one input a call got wrong names it (view.Error.Input), by
// the name the capability that refused it declares: the form the call came
// from opens again on that box, and an agent is told which argument to send
// again. A name the capability does not declare is ignored by every surface,
// so each one here is also held to the declaration (ErrorInput).
//
// Each case is a call to the fake realm demo, as the service account the
// fixtures were captured for, with what the case changes over it. Beside the
// ones that name an input, those that name none on purpose: a TLS setting
// beside a URL that is not https:// (two inputs at odds), a secret the realm
// refuses, a port nothing listens on and a role the service account lacks.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cap     string
		surface plugin.Surface
		given   func(t *testing.T) map[string]any
		over    func(t *testing.T) map[string]any
		deny    []string
		dry     bool
		code    string
		input   string
	}{
		{name: "a user the realm does not have", cap: "keycloak.user.show",
			code: "keycloak.user.unknown", input: "user",
			given: func(*testing.T) map[string]any { return map[string]any{"user": "ghost"} }},
		{name: "an id the realm has no user for", cap: "keycloak.user.show",
			code: "keycloak.user.unknown", input: "user",
			given: func(*testing.T) map[string]any {
				return map[string]any{"user": "0badc0de-0000-4000-8000-000000000000"}
			}},
		{name: "a user the realm does not have, to an agent", cap: "keycloak.user.show", surface: plugin.SurfaceMCP,
			code: "keycloak.user.unknown", input: "user",
			given: func(*testing.T) map[string]any { return map[string]any{"user": "ghost"} }},
		{name: "a user the realm does not have", cap: "keycloak.session.list",
			code: "keycloak.user.unknown", input: "user",
			given: func(*testing.T) map[string]any { return map[string]any{"user": "ghost"} }},
		{name: "a user the realm does not have", cap: "keycloak.event.list",
			code: "keycloak.user.unknown", input: "user",
			given: func(*testing.T) map[string]any { return map[string]any{"user": "ghost"} }},
		{name: "a client the realm does not have", cap: "keycloak.client.show",
			code: "keycloak.client.unknown", input: "client",
			given: func(*testing.T) map[string]any { return map[string]any{"client": "ghost"} }},
		{name: "a client the realm does not have", cap: "keycloak.session.list",
			code: "keycloak.client.unknown", input: "client",
			given: func(*testing.T) map[string]any { return map[string]any{"client": "ghost"} }},
		{name: "a client the realm does not have", cap: "keycloak.role.list",
			code: "keycloak.client.unknown", input: "client",
			given: func(*testing.T) map[string]any { return map[string]any{"client": "ghost"} }},
		{name: "a flow the realm does not have", cap: "keycloak.flow.show",
			code: "keycloak.flow.unknown", input: "flow",
			given: func(*testing.T) map[string]any { return map[string]any{"flow": "ghost"} }},
		{name: "a realm to read that is not there, the client living in it", cap: "keycloak.overview",
			code: "keycloak.realm.unknown", input: "realm",
			over: func(*testing.T) map[string]any { return map[string]any{"realm": "ghost"} }},
		{name: "a realm for the client that is not there", cap: "keycloak.client.list",
			code: "keycloak.realm.unknown", input: "auth-realm",
			over: func(*testing.T) map[string]any { return map[string]any{"auth-realm": "ghost"} }},
		{name: "no realm", cap: "keycloak.flow.list",
			code: "keycloak.realm.missing", input: "realm",
			over: func(*testing.T) map[string]any { return map[string]any{"realm": ""} }},
		{name: "no client secret", cap: "keycloak.client.list",
			code: "keycloak.secret.missing", input: "client-secret",
			over: func(*testing.T) map[string]any { return map[string]any{"client-secret": ""} }},
		{name: "a URL that is no Keycloak base URL", cap: "keycloak.overview",
			code: "keycloak.url.invalid", input: "url",
			over: func(*testing.T) map[string]any { return map[string]any{"url": "ftp://sso.internal"} }},
		{name: "a URL that is no Keycloak base URL, in a dry run", cap: "keycloak.user.list", dry: true,
			code: "keycloak.url.invalid", input: "url",
			over: func(*testing.T) map[string]any { return map[string]any{"url": "sso.internal"} }},
		{name: "a CA file that is not there", cap: "keycloak.client.list",
			code: "keycloak.tls.ca.unreadable", input: "ca-file",
			over: func(t *testing.T) map[string]any {
				return map[string]any{"url": "https://127.0.0.1:1", "ca-file": filepath.Join(t.TempDir(), "absent.pem")}
			}},
		{name: "a CA file that is a directory", cap: "keycloak.role.list",
			code: "keycloak.tls.ca.unreadable", input: "ca-file",
			over: func(t *testing.T) map[string]any {
				return map[string]any{"url": "https://127.0.0.1:1", "ca-file": t.TempDir()}
			}},
		{name: "a CA file that holds no certificate", cap: "keycloak.audit",
			code: "keycloak.tls.ca.invalid", input: "ca-file",
			over: func(t *testing.T) map[string]any {
				return map[string]any{"url": "https://127.0.0.1:1", "ca-file": fileHolding(t, "ca.pem", "not a certificate\n")}
			}},

		{name: "a certificate's name beside a URL that is not https://", cap: "keycloak.overview",
			code: "keycloak.tls.plaintext",
			over: func(*testing.T) map[string]any { return map[string]any{"tls-server-name": "sso.internal"} }},
		{name: "a CA beside a URL that is not https://", cap: "keycloak.overview",
			code: "keycloak.tls.ca.unused",
			over: func(t *testing.T) map[string]any {
				return map[string]any{"ca-file": fileHolding(t, "ca.pem", "not a certificate\n")}
			}},
		{name: "a secret the realm refuses", cap: "keycloak.client.list",
			code: "keycloak.auth.failed",
			over: func(*testing.T) map[string]any { return map[string]any{"client-secret": "wrong"} }},
		{name: "a port nothing listens on", cap: "keycloak.client.list",
			code: "keycloak.conn.refused",
			over: func(*testing.T) map[string]any { return map[string]any{"url": "http://127.0.0.1:1"} }},
		{name: "a read the service account may not make", cap: "keycloak.client.show", deny: []string{"/clients"},
			code:  "keycloak.denied",
			given: func(*testing.T) map[string]any { return map[string]any{"client": "rta-audit"} }},
	} {
		t.Run(tc.cap+": "+tc.name, func(t *testing.T) {
			f := newFakeKeycloak(t)
			f.deny = tc.deny
			values := map[string]any{}
			if tc.given != nil {
				values = tc.given(t)
			}
			r := reqAt(t, f, tc.cap, values)
			if tc.over != nil {
				r = r.With(tc.over(t))
			}
			r.DryRun = tc.dry
			surface := tc.surface
			if surface == "" {
				surface = plugin.SurfaceCLI
			}
			c := capability(t, tc.cap)
			_, err := c.Run(context.Background(), r.WithSurface(surface))
			verr := view.AsError(err, "keycloak.test.none")
			if verr == nil || verr.Code != tc.code {
				t.Fatalf("%s answered %+v, want %s", tc.cap, verr, tc.code)
			}
			if verr.Input != tc.input {
				t.Errorf("%s names the input %q, want %q: %s", verr.Code, verr.Input, tc.input, verr.Message)
			}
			if _, declared := c.ErrorInput(verr); tc.input != "" && !declared {
				t.Errorf("%s: %s names %q, which it does not declare", tc.cap, verr.Code, verr.Input)
			}
		})
	}
}

// The client and the flow keycloak.audit asks for by names of its own — the
// realm-management client, the realm's browser flow — are no box of the
// call's, and a realm without them is refused naming nothing: the audit takes
// neither input.
func TestANameThePluginAsksForItselfNamesNoInput(t *testing.T) {
	s := sessionOn(t, newFakeKeycloak(t))
	if _, verr := s.client(context.Background(), "ghost", ""); verr == nil ||
		verr.Code != "keycloak.client.unknown" || verr.Input != "" {
		t.Errorf("the audit's client: %+v, want keycloak.client.unknown naming none", verr)
	}
	if _, verr := s.executions(context.Background(), "ghost", ""); verr == nil ||
		verr.Code != "keycloak.flow.unknown" || verr.Input != "" {
		t.Errorf("the audit's flow: %+v, want keycloak.flow.unknown naming none", verr)
	}
}

// A CA file that is there and that this process may not read names no input:
// the path is right, and the fix is a chmod, not the box.
func TestACAFileThatCannotBeReadNamesNoInput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("mode bits do not stop this reader")
	}
	shut := fileHolding(t, "ca.pem", "something\n")
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Fatal(err)
	}
	r := reqAt(t, newFakeKeycloak(t), "keycloak.client.list", map[string]any{}).
		With(map[string]any{"url": "https://127.0.0.1:1", "ca-file": shut})
	_, err := capability(t, "keycloak.client.list").Run(context.Background(), r)
	if verr := view.AsError(err, "keycloak.test.none"); verr == nil || verr.Code != "keycloak.tls.ca.unreadable" || verr.Input != "" {
		t.Errorf("a CA file this process may not read: %+v, want keycloak.tls.ca.unreadable naming none", verr)
	}
}
