package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A cluster that does not answer is the most common failure here, and the
// question to ask next is kubectl's own, with this plugin out of the way:
// plugins/cnpg, reading the same kubectl, gives it. On the context the call
// used, since a bare line asks the context of the shell it is pasted into.
func TestAClusterThatDoesNotAnswerNamesTheKubectlLineThatAsksItWithoutThePlugin(t *testing.T) {
	refused := "Unable to connect to the server: dial tcp 10.0.0.1:6443: connect: connection refused"
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		args []string
		want string
	}{
		{"the current context", context.Background(), []string{"get", "namespaces", "--request-timeout=15s"},
			"`kubectl cluster-info` is the same question without this plugin in the way"},
		{"a named context", context.Background(), []string{"get", "namespaces", "--context=staging"},
			"`kubectl --context=staging cluster-info` is the same question without this plugin in the way"},
		{"a context that needs quoting", context.Background(), []string{"get", "nodes", "--context=a b"},
			"`kubectl '--context=a b' cluster-info`"},
		{"a call that timed out", expired, []string{"get", "pods", "--context=lab"},
			"`kubectl --context=lab cluster-info` is the same question without this plugin in the way"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verr := classify(tc.ctx, errors.New("exit status 1"), refused, tc.args, plugin.SurfaceCLI)
			if verr.Code != "kube.unreachable" {
				t.Fatalf("code = %q, want kube.unreachable", verr.Code)
			}
			if !strings.Contains(verr.Hint, tc.want) {
				t.Errorf("hint = %q, want %q in it", verr.Hint, tc.want)
			}
		})
	}
}
