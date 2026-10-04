package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// fakeDocker puts a script on dockerBin that answers `ps` and refuses
// `images` — the ordinary socket-permission split, where a daemon answers
// one query and not another, rather than a daemon that is down.
func fakeDocker(t *testing.T) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    image) echo 'permission denied while trying to connect to the Docker daemon socket' >&2; exit 1 ;;\n" +
		"    ps) echo '{\"Names\":\"web\",\"State\":\"running\",\"Status\":\"Up 2 hours\"}'; exit 0 ;;\n" +
		"  esac\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := dockerBin
	dockerBin = bin
	t.Cleanup(func() { dockerBin = old })
}

func overviewReq(t *testing.T, values map[string]any) plugin.Request {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == "docker.overview" {
			return plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false)
		}
	}
	t.Fatal("no docker.overview capability")
	return plugin.Request{}
}

// **A dropped row reads as a compact report, not as a question nobody
// answered.**
//
// The images line was appended only when the image query succeeded, and the
// error was never looked at again — so a daemon answering `ps` and refusing
// `images` produced an overview with no images line at all, which is also
// exactly what a host with no images would look like here.
func TestOverviewSaysWhenTheImageQueryFailed(t *testing.T) {
	fakeDocker(t)

	v, err := runOverview(t.Context(), overviewReq(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("overview is %s, want a KeyValue", view.TypeOf(v))
	}
	var images string
	var present bool
	for _, p := range kv.Pairs {
		if p.Key == "images" {
			images, present = p.Value, true
		}
	}
	if !present {
		t.Fatalf("the images line was dropped rather than reported: %+v", kv.Pairs)
	}
	if !strings.HasPrefix(images, "unreadable") {
		t.Errorf("images = %q, want it to say the query failed", images)
	}
}

// A container that is up and failing its healthcheck is listed for the
// healthcheck, not for the state the daemon reports. The list was headed "not
// running or not healthy" and held "web (running)", which reads as the
// daemon's opinion that a container it says is running is not.
func TestAnUnhealthyContainerIsNamedForItsHealthNotItsState(t *testing.T) {
	scriptedDocker(t, "case \"$*\" in\n"+
		"  *ps*) echo '{\"Names\":\"web\",\"State\":\"running\",\"HealthStatus\":\"unhealthy\",\"Status\":\"Up 2 hours (unhealthy)\"}'\n"+
		"        echo '{\"Names\":\"old\",\"State\":\"exited\",\"Status\":\"Exited (0) 3 days ago\"}'\n"+
		"        echo '{\"Names\":\"ok\",\"State\":\"running\",\"HealthStatus\":\"healthy\",\"Status\":\"Up\"}' ;;\n"+
		"esac\n")
	v, err := runOverview(t.Context(), overviewReq(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	var sick string
	for _, p := range v.(view.KeyValue).Pairs {
		if p.Key == "not running or not healthy" {
			sick = p.Value
		}
	}
	if want := "old (exited), web (unhealthy)"; sick != want {
		t.Errorf("not running or not healthy = %q, want %q", sick, want)
	}
}

// And the detailed page carries the same fact where a page says it: in the
// warnings, which is what pkg/view built them for.
func TestTheDetailedOverviewWarnsAboutTheImageQuery(t *testing.T) {
	fakeDocker(t)

	v, err := runOverview(t.Context(), overviewReq(t, map[string]any{"detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("detailed overview is %s, want Sections", view.TypeOf(v))
	}
	if len(s.Warnings) == 0 {
		t.Fatal("a failed image query left the page looking complete")
	}
}
