package main

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What the plugin says names a capability and an input the way the surface
// reading it gives them: at a terminal as it always read, and elsewhere as a
// tool, a form's box, or the connection setting the operator holds — never
// as a flag or an `rta` command line its reader has no terminal for.
func TestWhatItSaysNamesWhatItsSurfaceGives(t *testing.T) {
	refusal := func(verr *view.Error) string { return verr.Message + "\n" + verr.Hint }
	caRefusal := func(sf plugin.Surface, ca string) string {
		_, verr := httpClient(req(t, "qdrant.overview", map[string]any{"ca-file": ca}).WithSurface(sf))
		if verr == nil {
			t.Fatalf("a ca-file of %s was accepted", ca)
		}
		return refusal(verr)
	}
	for _, tc := range []struct {
		name, cli, other string
		surface          plugin.Surface
		say              func(sf plugin.Surface) string
	}{
		{
			name:    "a collection that is not there",
			cli:     "`rta qdrant collection list` shows what is there",
			other:   "the `qdrant_collection_list` tool shows what is there",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classifyStatus(http.StatusNotFound, []byte(`{"status":{"error":"Not found"}}`),
					req(t, "qdrant.overview", nil).WithSurface(sf)))
			},
		},
		{
			name:    "an endpoint written as a URL",
			cli:     "endpoint is host[:port] with no scheme — set --tls separately",
			other:   "endpoint is host[:port] with no scheme — set the operator's `tls` setting separately",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				_, verr := newRequest(t.Context(), req(t, "qdrant.overview", map[string]any{
					"endpoint": "http://qdrant.internal:6333" + string(rune(0x7f)),
				}).WithSurface(sf), http.MethodGet, "/", nil)
				if verr == nil {
					t.Fatal("an endpoint holding a control character was accepted")
				}
				return refusal(verr)
			},
		},
		{
			name:    "a certificate nothing here trusts",
			cli:     "the CA that issued it belongs in --ca-file (a self-signed certificate is its own CA)",
			other:   "the CA that issued it belongs in the operator's `ca-file` setting (a self-signed certificate is its own CA)",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(x509.UnknownAuthorityError{}, req(t, "qdrant.overview", nil).WithSurface(sf)))
			},
		},
		{
			name:    "anything else",
			cli:     "`rta explain qdrant.overview` lists every input and which of the command line, the rta config, a profile and the environment can set it",
			other:   "ask the operator to run `rta explain qdrant.overview`, which lists every setting and which of the rta config",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(errors.New("handshake went sideways"), req(t, "qdrant.overview", nil).WithSurface(sf)))
			},
		},
		{
			name:    "a snapshot that is not there",
			cli:     "`rta qdrant dump --collection <collection> --out <path>` writes one",
			other:   "`qdrant.dump collection=<collection> out=<path>` writes one",
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				return refusal(checkSnapshotFile(sf, filepath.Join(t.TempDir(), "nope.snapshot")))
			},
		},
		{
			name:    "a CA file that cannot be read",
			cli:     "--ca-file is a path on this machine",
			other:   "the operator's `ca-file` setting is a path on this machine",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return caRefusal(sf, filepath.Join(t.TempDir(), "absent.pem"))
			},
		},
		{
			// What the file must hold, never a refusal calling a self-signed
			// server's own certificate the wrong file: it is the file the
			// untrusted-certificate hint above sends the reader to name.
			name:    "a CA file with no PEM certificate in it",
			cli:     "--ca-file wants a PEM certificate — the CA's, or a self-signed server's own",
			other:   "the operator's `ca-file` setting wants a PEM certificate — the CA's, or a self-signed server's own",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				der := filepath.Join(t.TempDir(), "server.der")
				if err := os.WriteFile(der, []byte{0x30, 0x03, 0x02, 0x01, 0x01}, 0o600); err != nil {
					t.Fatal(err)
				}
				return caRefusal(sf, der)
			},
		},
		{
			// The file offered is named after the collection, which is the
			// server's to name: on a command line it is one quoted word, and a
			// name holding a command substitution runs nothing when pasted.
			name:    "a dump with nowhere to go",
			cli:     "--out './docs $(id).snapshot' — a collection is a file",
			other:   `the out box set to "./docs $(id).snapshot" — a collection is a file`,
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				_, err := runDump(context.Background(), req(t, "qdrant.dump",
					map[string]any{"collection": "docs $(id)"}).WithSurface(sf))
				var verr *view.Error
				if !errors.As(err, &verr) {
					t.Fatalf("err = %v, want a refusal", err)
				}
				return refusal(verr)
			},
		},
		{
			name:    "the restore a dump names",
			cli:     "rta qdrant restore '/backups/my docs.snapshot' --collection docs --endpoint qdrant.internal:6333",
			other:   `qdrant.restore file="/backups/my docs.snapshot" collection=docs endpoint=qdrant.internal:6333`,
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				return restoreCommand(req(t, "qdrant.dump", map[string]any{"endpoint": "qdrant.internal:6333"}).WithSurface(sf),
					"docs", "/backups/my docs.snapshot")
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
