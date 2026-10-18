package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The headline row of the overview says which of four things happened, in the
// words kube.overview uses for the same four, because "did not answer" sends
// somebody to check a daemon and a network, and said of a missing binary, a
// socket that refused this account or a context that does not exist it sent
// them the wrong way.
func TestTheDaemonRowSaysWhichKindOfFailureItHit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   string
	}{
		{"the daemon is not running",
			"echo 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?' >&2\nexit 1\n",
			"did not answer — Cannot connect to the Docker daemon"},
		{"the socket refuses this account",
			"echo 'permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock' >&2\nexit 1\n",
			"answered, and refused — permission denied while trying to connect"},
		{"the context does not exist",
			"echo 'context \"lab\": context not found: open /nowhere/lab: no such file or directory' >&2\nexit 1\n",
			"was never contacted — context \"lab\": context not found"},
		{"the daemon answers with something nobody classified",
			"echo 'Error response from daemon: boom' >&2\nexit 1\n",
			"could not be read — boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scriptedDocker(t, tc.script)
			if got := daemonRow(t); !strings.HasPrefix(got, tc.want) {
				t.Errorf("daemon = %q, want it to begin %q", got, tc.want)
			}
		})
	}
}

func TestTheDaemonRowSaysThatAMissingDockerNeverReachedAnyDaemon(t *testing.T) {
	old := dockerBin
	dockerBin = "rta-test-no-docker-on-this-path"
	t.Cleanup(func() { dockerBin = old })
	if got, want := daemonRow(t), "was never contacted — docker is not on this machine's PATH"; got != want {
		t.Errorf("daemon = %q, want %q", got, want)
	}
}

// The words are kube's, one for each code that says what happened, and the
// vaguest for the rest: a code nobody mapped may well be a daemon that answered.
func TestTheWordsForTheDaemonAreKubesAndTheDefaultClaimsNothing(t *testing.T) {
	for code, want := range map[string]string{
		"docker.unreachable":     "did not answer",
		"docker.denied":          "answered, and refused",
		"docker.cli.missing":     "was never contacted",
		"docker.context.unknown": "was never contacted",
		"docker.cancelled":       "was never contacted",
		"docker.failed":          "could not be read",
		"docker.new.code":        "could not be read",
	} {
		if got := daemonAnswer(code); got != want {
			t.Errorf("%s reads %q, want %q", code, got, want)
		}
	}
}

func daemonRow(t *testing.T) string {
	t.Helper()
	v, err := runOverview(t.Context(), overviewReq(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range v.(view.KeyValue).Pairs {
		if p.Key == "daemon" {
			return p.Value
		}
	}
	t.Fatal("no daemon row")
	return ""
}
