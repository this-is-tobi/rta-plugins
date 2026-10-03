package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A cluster that does not answer, and a kubeconfig with nothing to answer
// from, are what a first call meets. Both fell to "that message is kubectl's;
// this plugin shells out to it", which says whose words they are and nothing
// to try — where plugins/kube, reading the same kubectl, names the VPN and the
// context.
func TestAClusterThatDoesNotAnswerAndAContextThatIsNotThereSayWhatToTry(t *testing.T) {
	s, verr := selectionOf(req(map[string]any{}).WithSurface(plugin.SurfaceMCP))
	if verr != nil {
		t.Fatal(verr)
	}
	for _, tc := range []struct {
		name, stderr, code, hint string
	}{
		{"connection refused", "Unable to connect to the server: dial tcp 10.0.0.1:6443: connect: connection refused",
			"cnpg.unreachable", "check the VPN, the context, and that the cluster is up"},
		{"a timeout", "Unable to connect to the server: net/http: TLS handshake timeout",
			"cnpg.unreachable", "kubectl cluster-info"},
		{"no current context", "error: current-context is not set", "cnpg.context.none", "get-contexts"},
		{"no kubeconfig", "error: no configuration has been provided, try setting KUBERNETES_MASTER environment variable",
			"cnpg.context.none", "get-contexts"},
		{"a context that is not in the file", "error: context was not found for specified context: staging",
			"cnpg.context.none", "get-contexts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(context.Background(), &exec.ExitError{}, tc.stderr, nil, s.caller())
			if got.Code != tc.code {
				t.Errorf("code = %q, want %q", got.Code, tc.code)
			}
			if !strings.Contains(got.Hint, tc.hint) {
				t.Errorf("hint = %q, want %q in it", got.Hint, tc.hint)
			}
			if strings.Contains(got.Hint, "that message is kubectl's") {
				t.Errorf("hint = %q is the fallback that names nothing to try", got.Hint)
			}
		})
	}
}
