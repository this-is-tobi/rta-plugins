package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Idempotent says that running a capability again with the same inputs changes
// nothing more, and an MCP client may repeat a call marked so freely. A second
// stop finds the container stopped; a second restart stops it and starts it
// again, dropping its connections and losing its memory a second time.
func TestARestartIsNotDeclaredToChangeNothingMoreWhenRepeated(t *testing.T) {
	if capabilityNamed(t, "docker.container.restart").Idempotent {
		t.Error("restart is declared idempotent, and a repeated one is a second outage")
	}
	if !capabilityNamed(t, "docker.container.stop").Idempotent {
		t.Error("stop is not declared idempotent, and a second stop changes nothing")
	}
}

// The line that starts a stopped container again is pasted, so it names the
// daemon the stop reached and holds every value as one word: bare, it started
// a container of the same name on the daemon of the shell it was pasted into,
// and a host such as tcp://[::1]:2375 holds brackets a shell reads as a
// pattern.
func TestTheLineThatStartsAStoppedContainerAgainReachesItsDaemon(t *testing.T) {
	scriptedDocker(t, "case \"$*\" in\n"+
		"  *ps*) echo '{\"ID\":\"abcdef123456\",\"Names\":\"web,other/web\",\"Image\":\"nginx\",\"State\":\"running\"}' ;;\n"+
		"esac\n")
	for _, tc := range []struct {
		name   string
		values map[string]any
		want   string
	}{
		{"the default daemon", map[string]any{}, "`docker start web`"},
		{"a host", map[string]any{"host": "ssh://build@ci"}, "`docker --host=ssh://build@ci start web`"},
		{"a host with brackets and a context", map[string]any{"host": "tcp://[::1]:2375", "context": "lab"},
			"`docker '--host=tcp://[::1]:2375' --context=lab start web`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.values["container"] = "web"
			v, err := runStop(t.Context(), capReq(t, "docker.container.stop", tc.values).WithSurface(plugin.SurfaceCLI))
			if err != nil {
				t.Fatal(err)
			}
			var line string
			for _, p := range v.(view.KeyValue).Pairs {
				if p.Key == "reversible" {
					line = p.Value
				}
			}
			if !strings.Contains(line, tc.want) {
				t.Errorf("reversible = %q, want %q in it", line, tc.want)
			}
		})
	}
}
