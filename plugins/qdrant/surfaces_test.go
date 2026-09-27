package main

import (
	"errors"
	"net/http"
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
			other:   "endpoint is host[:port] with no scheme — set `tls` separately",
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
			name:    "anything else",
			cli:     "`rta explain qdrant.overview` lists every input and where each one can come from",
			other:   "ask the operator to run `rta explain qdrant.overview`, which lists every input",
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

// A switch is turned off on the CLI as --tls=false, joined: given as a
// separate word, false is an argument and the switch stays on.
func TestTurningTLSOffIsSpelledSoTheCLIReadsIt(t *testing.T) {
	if got := settingTo(plugin.SurfaceCLI, "tls", "false"); got != "--tls=false" {
		t.Errorf("the CLI reads %q, want --tls=false", got)
	}
	if got := settingTo(plugin.SurfaceMCP, "tls", "false"); got != "`tls` set to false" {
		t.Errorf("an agent reads %q, want the operator's setting named", got)
	}
}
