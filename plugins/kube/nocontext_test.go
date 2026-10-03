package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A kubeconfig that exists and names no current context made every call fail
// as kube.failed with kubectl's own sentence and no hint, when the two calls
// that fix it are this plugin's own: the listing of the contexts there are,
// and the switch that makes one current.
func TestAKubeconfigWithNoCurrentContextNamesTheCallsThatFixIt(t *testing.T) {
	for _, tc := range []struct {
		surface plugin.Surface
		want    string
	}{
		{plugin.SurfaceCLI, "`rta kube context list` shows the contexts this machine has, and `rta kube context set` makes one current"},
		{plugin.SurfaceMCP, "the `kube_context_list` tool shows the contexts this machine has, and the `kube_context_set` tool makes one current"},
	} {
		verr := classify(context.Background(), errors.New("exit status 1"), "error: current-context is not set\n",
			[]string{"get", "nodes"}, tc.surface)
		if verr.Code != "kube.context.none" {
			t.Errorf("%s: code = %q, want kube.context.none", tc.surface, verr.Code)
		}
		if !strings.Contains(verr.Hint, tc.want) {
			t.Errorf("%s: hint = %q, want %q in it", tc.surface, verr.Hint, tc.want)
		}
	}
}
