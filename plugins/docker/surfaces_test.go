package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A refusal names the call to make next the way the surface reading it makes
// one: at a terminal as it always read, and to an agent as a tool and its
// argument, never as an `rta` command line it has no terminal for.
func TestARefusalNamesWhatItsSurfaceGives(t *testing.T) {
	refuse := func(sf plugin.Surface) string {
		verr := classify(context.Background(), errors.New("exit status 1"),
			"Error response from daemon: No such container: web", []string{"inspect", "web"}, connection{sf: sf})
		return verr.Message + "\n" + verr.Hint
	}
	const cli = "`rta docker container list --all` shows what is there, stopped ones included"
	if said := refuse(plugin.SurfaceCLI); !strings.Contains(said, cli) {
		t.Errorf("the CLI reads %q, want %q in it", said, cli)
	}
	const mcp = "`docker_container_list {\"all\":true}` shows what is there"
	if said := refuse(plugin.SurfaceMCP); !strings.Contains(said, mcp) || strings.Contains(said, cli) {
		t.Errorf("an agent reads %q, want %q in it", said, mcp)
	}
}

// The stop a running container's removal sends its reader to runs against
// the daemon the removal read, never the one the configuration where it is
// pasted points at: the host and the context it was given, and the profile it
// came through, which is the whole of what an agent can give. Without them it
// stopped a container of the same name on another daemon.
func TestTheStopARemovalOffersReachesTheDaemonItRead(t *testing.T) {
	fakeDocker(t)
	var rm plugin.Capability
	for _, c := range Plugin().Capabilities {
		if c.ID == "docker.container.rm" {
			rm = c
		}
	}
	for _, tc := range []struct {
		name    string
		values  map[string]any
		profile string
		sf      plugin.Surface
		want    string
	}{
		{"the daemon configured", map[string]any{}, "", plugin.SurfaceCLI, "`rta docker container stop web`"},
		{"another daemon", map[string]any{"host": "ssh://build@ci", "context": "ci"}, "", plugin.SurfaceCLI,
			"`rta docker container stop web --host ssh://build@ci --context ci`"},
		{"a profile", map[string]any{"host": "ssh://build@ci"}, "ci", plugin.SurfaceCLI,
			"`rta docker container stop web --host ssh://build@ci --profile ci`"},
		{"an agent through a profile", map[string]any{"host": "ssh://build@ci"}, "ci", plugin.SurfaceMCP,
			"`docker_container_stop {\"container\":\"web\",\"profile\":\"ci\"}`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.values["container"] = "web"
			r := plugin.NewRequest(plugin.Resolve(rm, plugin.Inputs{Caller: tc.values}), false, false).
				WithProfile(tc.profile, plugin.TunnelNone).WithSurface(tc.sf)
			_, err := runRemove(context.Background(), r)
			var verr *view.Error
			if !errors.As(err, &verr) || verr.Code != "docker.container.running" {
				t.Fatalf("err = %v, want docker.container.running", err)
			}
			if !strings.Contains(verr.Hint, tc.want) {
				t.Errorf("hint = %q, want %q in it", verr.Hint, tc.want)
			}
		})
	}
}
