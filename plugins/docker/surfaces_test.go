package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A refusal names the call to make next the way the surface reading it makes
// one: at a terminal as it always read, and to an agent as a tool and its
// argument, never as an `rta` command line it has no terminal for.
func TestARefusalNamesWhatItsSurfaceGives(t *testing.T) {
	refuse := func(sf plugin.Surface) string {
		verr := classify(context.Background(), errors.New("exit status 1"),
			"Error response from daemon: No such container: web", []string{"inspect", "web"}, sf)
		return verr.Message + "\n" + verr.Hint
	}
	const cli = "`rta docker container list --all` shows what is there, stopped ones included"
	if said := refuse(plugin.SurfaceCLI); !strings.Contains(said, cli) {
		t.Errorf("the CLI reads %q, want %q in it", said, cli)
	}
	const mcp = "the `docker_container_list` tool with the \"all\" argument shows what is there"
	if said := refuse(plugin.SurfaceMCP); !strings.Contains(said, mcp) || strings.Contains(said, cli) {
		t.Errorf("an agent reads %q, want %q in it", said, mcp)
	}
}
