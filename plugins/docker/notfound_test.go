package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// scriptedDocker puts a script on dockerBin: the daemon is whatever the
// script says it is, for a test that needs a refusal in the CLI's own words.
func scriptedDocker(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake daemon is a shell script")
	}
	bin := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := dockerBin
	dockerBin = bin
	t.Cleanup(func() { dockerBin = old })
}

func capReq(t *testing.T, id string, values map[string]any) plugin.Request {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == id {
			return plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false)
		}
	}
	t.Fatalf("no %s capability", id)
	return plugin.Request{}
}

// A container the daemon does not hold is looked for again with the listing
// the refusal suggests, and the listing has to ask the daemon this call
// asked: bare, it listed whatever the configuration where it was pasted
// names, and a container the profile's daemon lacks may well be there. The
// refusal names that daemon too, beside the profile that chose it.
func TestAContainerNotFoundNamesTheDaemonAndTheListingReachesIt(t *testing.T) {
	scriptedDocker(t, "case \"$*\" in\n"+
		"  *inspect*) echo 'Error response from daemon: No such container: ghost' >&2; exit 1 ;;\n"+
		"esac\n")
	for _, tc := range []struct {
		name    string
		id      string
		values  map[string]any
		profile string
		surface plugin.Surface
		message []string
		hint    []string
		unhint  []string
	}{
		{"the default daemon, no profile", "docker.container.stop", map[string]any{},
			"", plugin.SurfaceCLI, []string{`no container named "ghost"`}, []string{"rta docker container list --all"},
			[]string{"--profile", "--host"}},
		{"a host through a profile", "docker.container.stop", map[string]any{"host": "tcp://prod:2375"},
			"prod", plugin.SurfaceCLI, []string{"on tcp://prod:2375 (profile prod)"},
			[]string{"rta docker container list", "--all", "--host tcp://prod:2375", "--profile prod"}, nil},
		{"a context through a profile, asked of the CLI's own refusal", "docker.container.inspect",
			map[string]any{"context": "lab"}, "prod", plugin.SurfaceCLI, []string{"on context lab (profile prod)"},
			[]string{"--context lab", "--profile prod"}, nil},
		{"an agent is given the profile alone", "docker.container.rm", map[string]any{"host": "tcp://prod:2375"},
			"prod", plugin.SurfaceMCP, []string{"on tcp://prod:2375 (profile prod)"},
			[]string{"docker_container_list", `"all":true`, `"profile":"prod"`}, []string{"host"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.values["container"] = "ghost"
			r := capReq(t, tc.id, tc.values).WithProfile(tc.profile, plugin.TunnelNone).WithSurface(tc.surface)
			run, ok := map[string]plugin.Handler{
				"docker.container.stop": runStop, "docker.container.inspect": runInspect, "docker.container.rm": runRemove,
			}[tc.id]
			if !ok {
				t.Fatalf("no handler for %s", tc.id)
			}
			_, err := run(t.Context(), r)
			verr, isView := err.(*view.Error)
			if !isView || verr.Code != "docker.notfound" {
				t.Fatalf("err = %v, want docker.notfound", err)
			}
			for _, w := range tc.message {
				if !strings.Contains(verr.Message, w) {
					t.Errorf("message %q does not hold %q", verr.Message, w)
				}
			}
			for _, w := range tc.hint {
				if !strings.Contains(verr.Hint, w) {
					t.Errorf("hint %q does not hold %q", verr.Hint, w)
				}
			}
			for _, w := range tc.unhint {
				if strings.Contains(verr.Hint, w) {
					t.Errorf("hint %q holds %q", verr.Hint, w)
				}
			}
		})
	}
}

// A daemon that is not there is the profile's to fix when the call came
// through one: its host and context chose it, and "the host input" named a
// setting of this call that the profile had set.
func TestAnUnreachableDaemonNamesTheProfileThatChoseIt(t *testing.T) {
	scriptedDocker(t, "echo 'Cannot connect to the Docker daemon at tcp://prod:2375. Is the docker daemon running?' >&2\nexit 1\n")
	for _, tc := range []struct {
		profile, hint string
	}{
		{"", "point this at the right daemon with the host input"},
		{"prod", "fix where profile prod points"},
	} {
		r := capReq(t, "docker.container.list", map[string]any{"host": "tcp://prod:2375"}).
			WithProfile(tc.profile, plugin.TunnelNone)
		c, verr := connectionOf(r)
		if verr != nil {
			t.Fatal(verr)
		}
		_, verr = fetchContainers(t.Context(), c, false)
		if verr == nil || verr.Code != "docker.unreachable" || !strings.Contains(verr.Hint, tc.hint) {
			t.Errorf("profile %q: %+v, want docker.unreachable with %q in the hint", tc.profile, verr, tc.hint)
		}
	}
}
