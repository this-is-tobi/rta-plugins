package main

import (
	"crypto/x509"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What the plugin says names a capability and an input the way the surface
// reading it gives them: at a terminal as it always read, and elsewhere as a
// tool, a form's box, or the connection setting the operator holds — never
// as a flag or an `rta` command line its reader has no terminal for.
func TestWhatItSaysNamesWhatItsSurfaceGives(t *testing.T) {
	refusal := func(verr *view.Error) string { return verr.Message + "\n" + verr.Hint }
	r := func(sf plugin.Surface) plugin.Request {
		return req(t, "vault.kv.get", map[string]any{"path": "app/db", "address": "https://vault.internal:8200"}).WithSurface(sf)
	}
	for _, tc := range []struct {
		name, cli, other string
		surface          plugin.Surface
		say              func(sf plugin.Surface) string
	}{
		{
			name:    "a token its policy refuses",
			cli:     "`rta vault token status` shows what the current token can do",
			other:   "the `vault_token_status` tool shows what the current token can do",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&vaultapi.ResponseError{StatusCode: 403}, r(sf)))
			},
		},
		{
			name:    "a CA file that cannot be used",
			cli:     "--ca-file is a path on this machine, read by rta rather than by Vault",
			other:   "`ca-file` is a path on this machine, read by rta rather than by Vault",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				_, verr := connect(req(t, "vault.seal.status", map[string]any{
					"address": "https://vault.internal:8200", "ca-file": filepath.Join(t.TempDir(), "absent.pem"),
				}).WithSurface(sf))
				if verr == nil {
					t.Fatal("a ca-file that is not there was accepted")
				}
				return refusal(verr)
			},
		},
		{
			name:    "a name DNS does not know",
			cli:     "no address for \"vault.internal\"\n`rta net dns vault.internal` shows what DNS returns",
			other:   "`net_dns {\"name\":\"vault.internal\"}` shows what DNS returns",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&net.DNSError{Err: "no such host", Name: "vault.internal"}, r(sf)))
			},
		},
		{
			name:    "a certificate nothing here trusts",
			cli:     "it belongs in --ca-file rather than verification turned off",
			other:   "it belongs in `ca-file` rather than verification turned off",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(x509.UnknownAuthorityError{}, r(sf)))
			},
		},
		{
			name:    "anything else",
			cli:     "`rta explain vault.seal.status` lists every input and where each can come from",
			other:   "ask the operator to run `rta explain vault.seal.status`, which lists every input",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(errors.New("handshake went sideways"), r(sf)))
			},
		},
		{
			name:    "a data pair with no value",
			cli:     "each --data is one key=value pair",
			other:   `each value in the "data" argument is one key=value pair, one per field`,
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				_, verr := dataFields(sf, []string{"user"})
				return refusal(verr)
			},
		},
		{
			name:    "a snapshot that is not there",
			cli:     "`rta vault snapshot --out <path>` writes one",
			other:   "`vault.snapshot out=<path>` writes one",
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				return refusal(checkSnapshotFile(sf, filepath.Join(t.TempDir(), "nope.snap")))
			},
		},
		{
			name:    "the restore a snapshot names",
			cli:     "rta vault restore '/backups/my vault.snap' --address https://vault.internal:8200",
			other:   `vault.restore file="/backups/my vault.snap" address=https://vault.internal:8200`,
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				return restoreCommand(r(sf), "/backups/my vault.snap")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if said := tc.say(plugin.SurfaceCLI); !strings.Contains(said, tc.cli) {
				t.Errorf("the CLI reads %q, want %q in it", said, tc.cli)
			}
			said := tc.say(tc.surface)
			if !strings.Contains(said, tc.other) {
				t.Errorf("%s reads %q, want %q in it", tc.surface, said, tc.other)
			}
			if strings.Contains(said, tc.cli) {
				t.Errorf("%s reads the CLI's %q", tc.surface, tc.cli)
			}
		})
	}
}

// The host DNS was asked for is the name in address, never the URL around
// it: a bracketed IPv6 literal loses its brackets and the port its colon, and
// an address with no host to take is named as it was given.
func TestTheHostIsTheNameInTheAddress(t *testing.T) {
	for address, want := range map[string]string{
		"https://vault.internal:8200": "vault.internal",
		"http://[fd00::1]:8200":       "fd00::1",
		"vault.internal":              "vault.internal",
	} {
		if got := hostOf(address); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", address, got, want)
		}
	}
}
