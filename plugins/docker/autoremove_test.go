package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A container started with --rm is removed by the daemon as it stops, so a stop
// of one is not the stop docker start undoes: a person is told so before and
// after it, and an agent, whose grant covers the stop that can be undone, is
// refused it.
func TestStoppingAContainerStartedWithRmSaysItIsGone(t *testing.T) {
	scriptedDocker(t, "case \"$*\" in\n"+
		"  *inspect*) echo '[{\"Id\":\"abcdef123456\",\"Name\":\"/web\",\"State\":{\"Status\":\"running\",\"Running\":true},\"HostConfig\":{\"AutoRemove\":true}}]' ;;\n"+
		"  *ps*) echo '{\"ID\":\"abcdef123456\",\"Names\":\"web\",\"Image\":\"nginx\",\"State\":\"running\"}' ;;\n"+
		"esac\n")
	values := map[string]any{"container": "web"}
	stop := capabilityNamed(t, "docker.container.stop")

	preview, err := runStop(t.Context(),
		plugin.NewRequest(plugin.Resolve(stop, plugin.Inputs{Caller: values}), true, false).WithSurface(plugin.SurfaceCLI))
	if err != nil {
		t.Fatal(err)
	}
	if line := preview.(view.Text).Body; !strings.Contains(line, "started to remove itself") ||
		!strings.Contains(line, "cannot be started again") {
		t.Errorf("the preview = %q, want it to say the daemon removes it", line)
	}

	v, err := runStop(t.Context(), capReq(t, "docker.container.stop", values).WithSurface(plugin.SurfaceCLI))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range v.(view.KeyValue).Pairs {
		if p.Key == "reversible" && !strings.HasPrefix(p.Value, "no — it was started to remove itself") {
			t.Errorf("reversible = %q over a container the daemon removed", p.Value)
		}
	}

	_, err = runStop(t.Context(), capReq(t, "docker.container.stop", values).WithSurface(plugin.SurfaceMCP))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "docker.container.autoremove" || !verr.Refusal {
		t.Fatalf("an agent's stop of a container started with --rm = %v, want a refusal", err)
	}
}

// A container started without --rm stops as before, for an agent too, with the
// line that starts it again.
func TestStoppingAContainerStartedWithoutRmIsReversible(t *testing.T) {
	scriptedDocker(t, "case \"$*\" in\n"+
		"  *inspect*) echo '[{\"Id\":\"abcdef123456\",\"Name\":\"/web\",\"State\":{\"Status\":\"running\",\"Running\":true},\"HostConfig\":{\"AutoRemove\":false}}]' ;;\n"+
		"  *ps*) echo '{\"ID\":\"abcdef123456\",\"Names\":\"web\",\"Image\":\"nginx\",\"State\":\"running\"}' ;;\n"+
		"esac\n")
	v, err := runStop(t.Context(), capReq(t, "docker.container.stop", map[string]any{"container": "web"}).
		WithSurface(plugin.SurfaceMCP))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range v.(view.KeyValue).Pairs {
		if p.Key == "reversible" && !strings.HasPrefix(p.Value, "yes") {
			t.Errorf("reversible = %q for a container docker start brings back", p.Value)
		}
	}
}

// An agent's stop whose inspect failed for any reason but a name the daemon
// does not hold is refused, and nothing is stopped: read as "not started with
// --rm", a slow daemon or an answer that did not parse let the stop remove the
// container the refusal is there to keep. A person's stop goes ahead.
func TestAnAgentsStopThatCouldNotBeJudgedIsRefused(t *testing.T) {
	stopped := filepath.Join(t.TempDir(), "stopped")
	scriptedDocker(t, "case \"$*\" in\n"+
		"  *inspect*) echo 'error during connect: context deadline exceeded' >&2; exit 1 ;;\n"+
		"  *ps*) echo '{\"ID\":\"abcdef123456\",\"Names\":\"web\",\"Image\":\"nginx\",\"State\":\"running\"}' ;;\n"+
		"  *stop*) echo \"$*\" > "+stopped+" ;;\n"+
		"esac\n")
	values := map[string]any{"container": "web"}
	_, err := runStop(t.Context(), capReq(t, "docker.container.stop", values).WithSurface(plugin.SurfaceMCP))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "docker.container.unjudged" || !verr.Refusal {
		t.Fatalf("an agent's stop whose inspect failed = %v, want docker.container.unjudged", err)
	}
	if _, err := os.Stat(stopped); err == nil {
		t.Fatal("the stop was sent for a container nothing judged")
	}
	if _, err := runStop(t.Context(), capReq(t, "docker.container.stop", values).WithSurface(plugin.SurfaceCLI)); err != nil {
		t.Fatalf("a person's stop = %v, want it sent", err)
	}
	if _, err := os.Stat(stopped); err != nil {
		t.Error("a person's stop was not sent")
	}
}

// The stop is sent to the id the inspect judged, never to the name again: a
// name moved to another container between the two would be judged on one and
// stop the other.
func TestTheStopActsOnTheContainerItJudged(t *testing.T) {
	stopped := filepath.Join(t.TempDir(), "stopped")
	scriptedDocker(t, "case \"$*\" in\n"+
		"  *inspect*) echo '[{\"Id\":\"abcdef123456\",\"Name\":\"/web\",\"State\":{\"Status\":\"running\",\"Running\":true},\"HostConfig\":{\"AutoRemove\":false}}]' ;;\n"+
		"  *ps*) echo '{\"ID\":\"abcdef123456\",\"Names\":\"web\",\"Image\":\"nginx\",\"State\":\"running\"}' ;;\n"+
		"  *stop*) echo \"$*\" > "+stopped+" ;;\n"+
		"esac\n")
	if _, err := runStop(t.Context(), capReq(t, "docker.container.stop", map[string]any{"container": "web"}).
		WithSurface(plugin.SurfaceMCP)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(stopped)
	if err != nil {
		t.Fatal(err)
	}
	if line := strings.TrimSpace(string(got)); !strings.HasSuffix(line, " abcdef123456") || strings.Contains(line, "web") {
		t.Errorf("docker was run as `docker %s`, want the stop sent to the judged id", line)
	}
}

// A name refused before docker is asked is refused as itself, for an agent
// too: it is no inspect that failed, and "try again once docker answers" sent
// the agent to wait for something that was never the trouble.
func TestAStopOfANameDockerIsNeverAskedAboutIsRefusedAsTheName(t *testing.T) {
	scriptedDocker(t, "exit 0\n")
	_, err := runStop(t.Context(), capReq(t, "docker.container.stop", map[string]any{"container": "-web"}).
		WithSurface(plugin.SurfaceMCP))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "docker.name.invalid" || verr.Input != "container" {
		t.Fatalf("an agent's stop of -web = %v, want docker.name.invalid naming container", err)
	}
}

// docker inspect answers for whatever object holds a name, so the inspect asks
// for a container: an image called web is no container web, and its
// environment is never shown as the container's.
func TestAnImageOfTheSameNameIsNoContainer(t *testing.T) {
	scriptedDocker(t, "case \"$*\" in\n"+
		"  *\"inspect --type=container web\"*) echo 'Error: No such container: web' >&2; exit 1 ;;\n"+
		"  *inspect*) echo '[{\"Id\":\"sha256:image\",\"Config\":{\"Env\":[\"TOKEN=from-the-image\"]}}]' ;;\n"+
		"esac\n")
	for _, id := range []string{"docker.container.inspect", "docker.container.env"} {
		c := capabilityNamed(t, id)
		_, err := c.Run(t.Context(), capReq(t, id, map[string]any{"container": "web"}).WithSurface(plugin.SurfaceCLI))
		verr, ok := err.(*view.Error)
		if !ok || verr.Code != "docker.notfound" {
			t.Errorf("%s of a name only an image holds = %v, want docker.notfound", id, err)
		}
	}
}
