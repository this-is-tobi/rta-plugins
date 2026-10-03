package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// What the plugin says names a capability and an input the way the surface
// reading it gives them: at a terminal as it always read, and to an agent as
// a tool and its arguments — never as a flag or an `rta` command line it has
// no terminal for.
func TestWhatItSaysNamesWhatItsSurfaceGives(t *testing.T) {
	for _, tc := range []struct {
		name, cli, mcp string
		say            func(sf plugin.Surface) string
	}{
		{
			name: "a namespace and every namespace at once",
			cli:  "--namespace and --all-namespaces ask for different things",
			mcp:  `the "namespace" argument and the "all-namespaces" argument ask for different things`,
			say: func(sf plugin.Surface) string {
				_, verr := selectionOf(req(map[string]any{"namespace": "db", "all-namespaces": true}).WithSurface(sf))
				return verr.Message + "\n" + verr.Hint
			},
		},
		{
			name: "a cluster kubectl did not find",
			cli:  "`rta cnpg list --all-namespaces` shows what is there",
			mcp:  "the `cnpg_list` tool with the \"all-namespaces\" argument shows what is there",
			say: func(sf plugin.Surface) string {
				verr := classify(context.Background(), &exec.ExitError{},
					`Error from server (NotFound): clusters.postgresql.cnpg.io "shop" not found`, nil, sf)
				return verr.Hint
			},
		},
		{
			name: "a cluster nothing has backed up",
			cli:  "`rta cnpg status --cluster shop --namespace prod` says whether anything is configured to take one",
			mcp:  "`cnpg_status {\"cluster\":\"shop\",\"namespace\":\"prod\"}` says whether anything is configured to take one",
			say: func(sf plugin.Surface) string {
				return emptyBackupBody("shop", selected(t, map[string]any{"namespace": "prod"}, sf))
			},
		},
		{
			name: "a backup to watch",
			cli:  "`rta cnpg backup list --cluster shop --namespace prod`",
			mcp:  "`cnpg_backup_list {\"cluster\":\"shop\",\"namespace\":\"prod\"}`",
			say: func(sf plugin.Surface) string {
				recordingKubectl(t, mustJSON(t, aCluster("shop", "prod")))
				v, err := runBackupRequest(context.Background(),
					req(map[string]any{"cluster": "shop", "namespace": "prod"}).WithSurface(sf))
				if err != nil {
					t.Fatal(err)
				}
				return renderPairs(t, v)
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
