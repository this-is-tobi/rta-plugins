package main

import (
	"context"
	"crypto/x509"
	"errors"
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
	for _, tc := range []struct {
		name, cli, mcp string
		say            func(sf plugin.Surface) string
	}{
		{
			name: "a flow that is not there",
			cli:  "`rta keycloak flow list` shows the aliases",
			mcp:  "the `keycloak_flow_list` tool shows the aliases",
			say: func(sf plugin.Surface) string {
				return refusal(sf, "keycloak.flow.show", map[string]any{"flow": "nope"})
			},
		},
		{
			name: "a user that is not there",
			cli:  "`rta keycloak user list nobody` searches by username, email and name",
			mcp:  "`keycloak_user_list {\"search\":\"nobody\"}` searches by username, email and name",
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
			name: "a certificate nothing here trusts",
			cli:  "a Keycloak behind an internal CA wants that CA in --ca-file rather than verification turned off",
			mcp:  "a Keycloak behind an internal CA wants that CA in `ca-file` rather than verification turned off",
			say: func(sf plugin.Surface) string {
				s := &session{req: req(t, "keycloak.overview", nil).WithSurface(sf), base: "https://sso.internal"}
				verr := s.classifyTransport(x509.UnknownAuthorityError{})
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
