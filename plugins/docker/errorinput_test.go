package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The daemons the refusals below are asked of, each a script on dockerBin:
// no test here reaches a docker of this machine's.
const (
	// unasked is a docker the call must refuse before asking: a run that
	// reached it fails as docker.failed, which no case wants.
	unasked = "echo 'docker was asked' >&2\nexit 1\n"

	// twoContainers holds a running web and an exited old, and its inspect
	// knows no container by the name it is asked about.
	twoContainers = "case \"$*\" in\n" +
		"  *inspect*) echo 'Error response from daemon: No such container: ghost' >&2; exit 1 ;;\n" +
		"  *ps*) echo '{\"ID\":\"abcdef123456\",\"Names\":\"web\",\"Image\":\"nginx\",\"State\":\"running\"}'\n" +
		"        echo '{\"ID\":\"0123456789ab\",\"Names\":\"old\",\"Image\":\"nginx\",\"State\":\"exited\"}' ;;\n" +
		"esac\n"

	// emptyInspect holds no container by any name, and says so with an empty
	// answer rather than an error.
	emptyInspect = "case \"$*\" in\n" +
		"  *inspect*) echo '[]' ;;\n" +
		"esac\n"

	// goneByTheAct holds web and old when they are looked at, and neither
	// when one is stopped, restarted or removed.
	goneByTheAct = "case \"$*\" in\n" +
		"  *inspect*) echo '[{\"Id\":\"abcdef123456\",\"Name\":\"/web\",\"State\":{\"Status\":\"running\",\"Running\":true}}]' ;;\n" +
		"  *ps*) echo '{\"ID\":\"abcdef123456\",\"Names\":\"web\",\"Image\":\"nginx\",\"State\":\"running\"}'\n" +
		"        echo '{\"ID\":\"0123456789ab\",\"Names\":\"old\",\"Image\":\"nginx\",\"State\":\"exited\"}' ;;\n" +
		"  *) echo 'Error response from daemon: No such container: abcdef123456' >&2; exit 1 ;;\n" +
		"esac\n"

	// saysBoom lists web and fails anything else in words nobody classified.
	saysBoom = "case \"$*\" in\n" +
		"  *ps*) echo '{\"ID\":\"abcdef123456\",\"Names\":\"web\",\"Image\":\"nginx\",\"State\":\"running\"}' ;;\n" +
		"  *) echo 'Error response from daemon: boom' >&2; exit 1 ;;\n" +
		"esac\n"

	// autoRemoving holds a running web that was started with --rm.
	autoRemoving = "case \"$*\" in\n" +
		"  *inspect*) echo '[{\"Id\":\"abcdef123456\",\"Name\":\"/web\",\"State\":{\"Status\":\"running\",\"Running\":true},\"HostConfig\":{\"AutoRemove\":true}}]' ;;\n" +
		"  *ps*) echo '{\"ID\":\"abcdef123456\",\"Names\":\"web\",\"Image\":\"nginx\",\"State\":\"running\"}' ;;\n" +
		"esac\n"

	// slowInspect lists web and does not answer an inspect in time.
	slowInspect = "case \"$*\" in\n" +
		"  *inspect*) echo 'error during connect: context deadline exceeded' >&2; exit 1 ;;\n" +
		"  *ps*) echo '{\"ID\":\"abcdef123456\",\"Names\":\"web\",\"Image\":\"nginx\",\"State\":\"running\"}' ;;\n" +
		"esac\n"

	unparsedInspect = "case \"$*\" in\n" +
		"  *inspect*) echo 'not json' ;;\n" +
		"esac\n"

	noSuchContext = "echo 'context \"lab\": context not found: open /nowhere/meta.json: no such file or directory' >&2\nexit 1\n"
	notRunning    = "echo 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?' >&2\nexit 1\n"
	deniedSocket  = "echo 'permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock' >&2\nexit 1\n"
)

// A refusal of the one input a call got wrong names it (view.Error.Input), by
// the name the capability that refused it declares: the form the call came
// from opens again on that box, and an agent is told which argument to send
// again. A name the capability does not declare is ignored by every surface,
// so each one here is also held to the declaration (ErrorInput).
//
// Beside them, the refusals that name none on purpose: a daemon that did not
// answer, refused this account or said something nobody classified, a
// container whose state is in the way rather than its name, the CLI's own
// current context, and the stops an agent is refused. No box of the call holds
// the fix to those, and a form reopened on one would point at the wrong thing.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      string
		surface plugin.Surface
		script  string
		values  map[string]any
		code    string
		input   string
	}{
		{name: "a container name docker would read as a flag", id: "docker.container.inspect", script: unasked,
			values: map[string]any{"container": "--host=tcp://elsewhere"}, code: "docker.name.invalid", input: "container"},
		{name: "a container name docker would read as a flag", id: "docker.container.env", script: unasked,
			values: map[string]any{"container": "-web"}, code: "docker.name.invalid", input: "container"},
		{name: "a container name docker would read as a flag", id: "docker.container.stop", script: unasked,
			values: map[string]any{"container": "-web"}, code: "docker.name.invalid", input: "container"},
		{name: "a container name docker would read as a flag", id: "docker.container.restart", script: unasked,
			values: map[string]any{"container": "-web"}, code: "docker.name.invalid", input: "container"},
		{name: "a container name docker would read as a flag", id: "docker.container.rm", script: unasked,
			values: map[string]any{"container": "-web"}, code: "docker.name.invalid", input: "container"},
		{name: "a context docker would read as a flag", id: "docker.container.list", script: unasked,
			values: map[string]any{"context": "-lab"}, code: "docker.name.invalid", input: "context"},
		{name: "a context docker would read as a flag", id: "docker.overview", script: unasked,
			values: map[string]any{"context": "--host=tcp://elsewhere"}, code: "docker.name.invalid", input: "context"},
		{name: "a daemon address docker would read as a flag", id: "docker.image.list", script: unasked,
			values: map[string]any{"host": "-H"}, code: "docker.host.invalid", input: "host"},
		{name: "a daemon address docker would read as a flag", id: "docker.container.rm", script: unasked,
			values: map[string]any{"container": "old", "host": "--context=prod"}, code: "docker.host.invalid", input: "host"},

		{name: "a container docker says it does not hold", id: "docker.container.inspect", script: twoContainers,
			values: map[string]any{"container": "ghost"}, code: "docker.notfound", input: "container"},
		{name: "a container an inspect answers nothing for", id: "docker.container.env", script: emptyInspect,
			values: map[string]any{"container": "ghost"}, code: "docker.notfound", input: "container"},
		{name: "a container the daemon does not list", id: "docker.container.stop", script: twoContainers,
			values: map[string]any{"container": "ghost"}, code: "docker.notfound", input: "container"},
		{name: "a container the daemon does not list, to an agent", id: "docker.container.stop",
			surface: plugin.SurfaceMCP, script: twoContainers,
			values: map[string]any{"container": "ghost"}, code: "docker.notfound", input: "container"},
		{name: "a container the daemon does not list", id: "docker.container.restart", script: twoContainers,
			values: map[string]any{"container": "ghost"}, code: "docker.notfound", input: "container"},
		{name: "a container the daemon does not list", id: "docker.container.rm", script: twoContainers,
			values: map[string]any{"container": "ghost"}, code: "docker.notfound", input: "container"},
		{name: "a container gone between the look and the stop", id: "docker.container.stop", script: goneByTheAct,
			values: map[string]any{"container": "web"}, code: "docker.notfound", input: "container"},
		{name: "a container gone between the look and the restart", id: "docker.container.restart", script: goneByTheAct,
			values: map[string]any{"container": "web"}, code: "docker.notfound", input: "container"},
		{name: "a container gone between the look and the removal", id: "docker.container.rm", script: goneByTheAct,
			values: map[string]any{"container": "old"}, code: "docker.notfound", input: "container"},
		{name: "a context docker does not have", id: "docker.container.list", script: noSuchContext,
			values: map[string]any{"context": "lab"}, code: "docker.context.unknown", input: "context"},

		{name: "the CLI's own current context, which the call did not give", id: "docker.container.list",
			script: noSuchContext, values: map[string]any{}, code: "docker.context.unknown"},
		{name: "a daemon that is not running", id: "docker.container.inspect", script: notRunning,
			values: map[string]any{"container": "web"}, code: "docker.unreachable"},
		{name: "a socket that refuses this account", id: "docker.container.rm", script: deniedSocket,
			values: map[string]any{"container": "web"}, code: "docker.denied"},
		{name: "a daemon failing in words nobody classified", id: "docker.container.restart", script: saysBoom,
			values: map[string]any{"container": "web"}, code: "docker.failed"},
		{name: "an inspect whose answer does not parse", id: "docker.container.inspect", script: unparsedInspect,
			values: map[string]any{"container": "web"}, code: "docker.unreadable"},
		{name: "a running container, which a stop is the fix to and not another name", id: "docker.container.rm",
			script: twoContainers, values: map[string]any{"container": "web"}, code: "docker.container.running"},
		{name: "an agent's stop of a container started with --rm", id: "docker.container.stop",
			surface: plugin.SurfaceMCP, script: autoRemoving,
			values: map[string]any{"container": "web"}, code: "docker.container.autoremove"},
		{name: "an agent's stop docker could not be asked about", id: "docker.container.stop",
			surface: plugin.SurfaceMCP, script: slowInspect,
			values: map[string]any{"container": "web"}, code: "docker.container.unjudged"},
	} {
		t.Run(tc.id+": "+tc.name, func(t *testing.T) {
			scriptedDocker(t, tc.script)
			surface := tc.surface
			if surface == "" {
				surface = plugin.SurfaceCLI
			}
			c := capabilityOf(t, tc.id)
			_, err := c.Run(t.Context(), capReq(t, tc.id, tc.values).WithSurface(surface))
			verr := view.AsError(err, "docker.test.none")
			if verr == nil || verr.Code != tc.code {
				t.Fatalf("%s answered %+v, want %s", tc.id, verr, tc.code)
			}
			if verr.Input != tc.input {
				t.Errorf("%s names the input %q, want %q: %s", verr.Code, verr.Input, tc.input, verr.Message)
			}
			if _, declared := c.ErrorInput(verr); tc.input != "" && !declared {
				t.Errorf("%s: %s names %q, which it does not declare", tc.id, verr.Code, verr.Input)
			}
		})
	}
}

// A docker that is not on the PATH is no input's: whatever the call was
// given, the fix is installing the CLI.
func TestAMissingDockerNamesNoInput(t *testing.T) {
	old := dockerBin
	dockerBin = "rta-test-no-docker-on-this-path"
	t.Cleanup(func() { dockerBin = old })
	_, err := runInspect(t.Context(), capReq(t, "docker.container.inspect", map[string]any{"container": "web"}))
	if verr := view.AsError(err, "docker.test.none"); verr == nil || verr.Code != "docker.cli.missing" || verr.Input != "" {
		t.Errorf("a missing docker: %+v, want docker.cli.missing naming none", verr)
	}
}
