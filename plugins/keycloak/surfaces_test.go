package main

import (
	"context"
	"crypto/x509"
	"errors"
	stdnet "net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What the plugin says names the call to make next the way the surface
// reading it makes one: at a terminal as it always read, and to an agent as
// a tool and its arguments — never as an `rta` command line it has no
// terminal for.
func TestWhatItSaysNamesWhatItsSurfaceGives(t *testing.T) {
	f := newFakeKeycloak(t)
	f.admin = true
	refusal := func(sf plugin.Surface, id string, values map[string]any) string {
		_, err := capability(t, id).Run(context.Background(), reqAt(t, f, id, values).WithSurface(sf))
		var verr *view.Error
		if !errors.As(err, &verr) {
			t.Fatalf("%s: err = %v, want a refusal", id, err)
		}
		return verr.Message + "\n" + verr.Hint
	}
	caRefusal := func(sf plugin.Surface, ca string) string {
		_, verr := httpClient(req(t, "keycloak.overview", map[string]any{"ca-file": ca}).WithSurface(sf))
		if verr == nil {
			t.Fatalf("a ca-file of %s was accepted", ca)
		}
		return verr.Message + "\n" + verr.Hint
	}
	for _, tc := range []struct {
		name, cli, mcp string
		say            func(sf plugin.Surface) string
	}{
		{
			name: "a flow that is not there",
			cli:  "`rta keycloak flow list --url " + f.URL + " --realm demo --client-id rta-audit` shows the aliases",
			mcp:  "the `keycloak_flow_list` tool shows the aliases",
			say: func(sf plugin.Surface) string {
				return refusal(sf, "keycloak.flow.show", map[string]any{"flow": "nope"})
			},
		},
		{
			name: "a user that is not there",
			cli: "`rta keycloak user list nobody --url " + f.URL + " --realm demo --client-id rta-audit` " +
				"searches by username, email and name",
			mcp: "`keycloak_user_list {\"search\":\"nobody\"}` searches by username, email and name",
			say: func(sf plugin.Surface) string {
				return refusal(sf, "keycloak.user.show", map[string]any{"user": "nobody"})
			},
		},
		{
			name: "the version the audit read",
			// The compact table clips a detail to one line, so the call and
			// the words after it are what is asserted.
			cli: "`rta eol check keycloak` says whether",
			mcp: "`eol_check {\"product\":\"keycloak\"}` says whether",
			say: func(sf plugin.Surface) string {
				v, err := capability(t, "keycloak.audit").Run(context.Background(),
					reqAt(t, f, "keycloak.audit", map[string]any{}).WithSurface(sf))
				if err != nil {
					t.Fatal(err)
				}
				return graded(t, v)["version"].detail
			},
		},
		{
			// The connection's settings are the operator's: each is named as
			// the flag at a terminal and as the setting to an agent, never
			// bare, which reads as neither.
			name: "a realm the client does not live in",
			cli:  "--auth-realm (or --realm) names the realm the client lives in",
			mcp:  "the operator's `auth-realm` setting (or the operator's `realm` setting) names the realm the client lives in",
			say: func(sf plugin.Surface) string {
				return refusal(sf, "keycloak.overview", map[string]any{"auth-realm": "elsewhere"})
			},
		},
		{
			name: "client credentials the server refuses",
			cli:  "--client-id names a confidential client with service accounts enabled",
			mcp:  "the operator's `client-id` setting names a confidential client with service accounts enabled",
			say: func(sf plugin.Surface) string {
				_, verr := connect(context.Background(), req(t, "keycloak.overview", map[string]any{
					"url": f.URL, "realm": "demo", "client-id": "someone-else", "client-secret": fakeSecret,
				}).WithSurface(sf))
				return verr.Message + "\n" + verr.Hint
			},
		},
		{
			// Where the secret comes from, never a verb for the reader: an
			// agent has no host environment to set and no secret to pass.
			name: "no client secret",
			cli:  "the secret is read from $RTA_KEYCLOAK_CLIENT_SECRET or --client-secret, or mapped",
			mcp:  "the secret is read from $RTA_KEYCLOAK_CLIENT_SECRET or the operator's `client-secret` setting, or mapped",
			say: func(sf plugin.Surface) string {
				_, verr := connect(context.Background(), req(t, "keycloak.overview", map[string]any{}).WithSurface(sf))
				return verr.Message + "\n" + verr.Hint
			},
		},
		{
			name: "nothing listening",
			cli:  "is the server up, and is --url right?",
			mcp:  "is the server up, and is the operator's `url` setting right?",
			say: func(sf plugin.Surface) string {
				s := &session{req: req(t, "keycloak.overview", nil).WithSurface(sf), base: "http://127.0.0.1:1"}
				verr := s.classifyTransport(&stdnet.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})
				return verr.Message + "\n" + verr.Hint
			},
		},
		{
			name: "a CA file that cannot be read",
			cli:  "--ca-file is a path on this machine",
			mcp:  "the operator's `ca-file` setting is a path on this machine",
			say: func(sf plugin.Surface) string {
				return caRefusal(sf, filepath.Join(t.TempDir(), "absent.pem"))
			},
		},
		{
			// What the file must hold, never a refusal calling a self-signed
			// server's own certificate the wrong file: it is the file the
			// untrusted-certificate hint sends the reader to name.
			name: "a CA file with no PEM certificate in it",
			cli:  "--ca-file wants a PEM certificate — the CA's, or a self-signed server's own",
			mcp:  "the operator's `ca-file` setting wants a PEM certificate — the CA's, or a self-signed server's own",
			say: func(sf plugin.Surface) string {
				der := filepath.Join(t.TempDir(), "server.der")
				if err := os.WriteFile(der, []byte{0x30, 0x03, 0x02, 0x01, 0x01}, 0o600); err != nil {
					t.Fatal(err)
				}
				return caRefusal(sf, der)
			},
		},
		{
			name: "a certificate nothing here trusts",
			cli:  "a Keycloak behind an internal CA wants that CA rather than verification turned off: the CA that issued it belongs in --ca-file",
			mcp:  "a Keycloak behind an internal CA wants that CA rather than verification turned off: the CA that issued it belongs in the operator's `ca-file` setting",
			say: func(sf plugin.Surface) string {
				s := &session{req: req(t, "keycloak.overview", nil).WithSurface(sf), base: "https://sso.internal"}
				verr := s.classifyTransport(x509.UnknownAuthorityError{})
				return verr.Message + "\n" + verr.Hint
			},
		},
		{
			// Which places can set an input, never "where each one can come
			// from": a secret has no config key, and an agent is told the
			// settings are the operator's to set.
			name: "anything else",
			cli:  "`rta explain keycloak.overview` lists every input and which of the command line, the rta config",
			mcp:  "ask the operator to run `rta explain keycloak.overview`, which lists every setting",
			say: func(sf plugin.Surface) string {
				s := &session{req: req(t, "keycloak.overview", nil).WithSurface(sf), base: "https://sso.internal"}
				verr := s.classifyTransport(errors.New("handshake went sideways"))
				return verr.Message + "\n" + verr.Hint
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if said := tc.say(plugin.SurfaceCLI); !strings.Contains(said, tc.cli) {
				t.Errorf("the CLI reads %q, want %q in it", said, tc.cli)
			}
			said := tc.say(plugin.SurfaceMCP)
			if !strings.Contains(said, tc.mcp) {
				t.Errorf("an agent reads %q, want %q in it", said, tc.mcp)
			}
			if strings.Contains(said, tc.cli) {
				t.Errorf("an agent reads the CLI's %q", tc.cli)
			}
		})
	}
}

// The search a missing user offers reads the Keycloak and the realm the
// lookup read, never whatever the configuration where it is pasted points
// at: through a profile it names the profile, whose secret the lookup may have
// used; reached directly, the URL too, which may be one typed over the
// profile's; through a forward the host opened, not the URL, which was the
// forward's end on 127.0.0.1; and the realm and the client every time, which
// no forward fills. An agent gives the profile and nothing Local.
func TestTheSearchAMissingUserOffersReadsTheRealmTheLookupRead(t *testing.T) {
	f := newFakeKeycloak(t)
	f.admin = true
	for _, tc := range []struct {
		name    string
		profile string
		tunnel  plugin.Tunnel
		sf      plugin.Surface
		want    string
	}{
		{"no profile", "", plugin.TunnelNone, plugin.SurfaceCLI,
			"`rta keycloak user list nobody --url " + f.URL + " --realm demo --client-id rta-audit`"},
		{"a profile reached directly", "lab", plugin.TunnelNone, plugin.SurfaceCLI,
			"`rta keycloak user list nobody --profile lab --url " + f.URL + " --realm demo --client-id rta-audit`"},
		{"a profile through a forward", "lab", plugin.TunnelKube, plugin.SurfaceCLI,
			"`rta keycloak user list nobody --profile lab --realm demo --client-id rta-audit`"},
		{"an agent through a profile", "lab", plugin.TunnelNone, plugin.SurfaceMCP,
			"`keycloak_user_list {\"profile\":\"lab\",\"search\":\"nobody\"}`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := reqAt(t, f, "keycloak.user.show", map[string]any{"user": "nobody"}).
				WithProfile(tc.profile, tc.tunnel).WithSurface(tc.sf)
			_, err := capability(t, "keycloak.user.show").Run(context.Background(), r)
			verr := view.AsError(err, "")
			if verr == nil || verr.Code != "keycloak.user.unknown" || !strings.Contains(verr.Hint, tc.want) {
				t.Errorf("%v, want keycloak.user.unknown offering %s", verr, tc.want)
			}
		})
	}
}
