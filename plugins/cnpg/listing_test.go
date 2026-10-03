package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The listing a cluster not found offers is a call, and it reaches the cluster
// the refused call read: through a profile, or in a context typed at a
// terminal, the listing bare read the current context, where the cluster was
// not found either. An agent is given the profile alone, the context being
// Local.
func TestTheListingAClusterNotFoundOffersReachesTheClusterItWasLookedFor(t *testing.T) {
	const notFound = `Error from server (NotFound): clusters.postgresql.cnpg.io "shop" not found`
	for _, tc := range []struct {
		name    string
		values  map[string]any
		profile string
		surface plugin.Surface
		want    string
	}{
		{"a terminal, nothing to say", map[string]any{}, "", plugin.SurfaceCLI,
			"`rta cnpg list --all-namespaces` shows what is there"},
		{"a terminal, a context", map[string]any{"context": "kind"}, "", plugin.SurfaceCLI,
			"`rta cnpg list --all-namespaces --context kind` shows what is there"},
		{"a terminal, a profile", map[string]any{"context": "kind"}, "prod", plugin.SurfaceCLI,
			"`rta cnpg list --all-namespaces --profile prod --context kind` shows what is there"},
		{"an agent, a profile", map[string]any{"context": "kind"}, "prod", plugin.SurfaceMCP,
			"`cnpg_list {\"all-namespaces\":true,\"profile\":\"prod\"}` shows what is there"},
		{"an agent, nothing to say", map[string]any{}, "", plugin.SurfaceMCP,
			"the `cnpg_list` tool with the \"all-namespaces\" argument shows what is there"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, verr := selectionOf(req(tc.values).WithProfile(tc.profile, plugin.TunnelNone).WithSurface(tc.surface))
			if verr != nil {
				t.Fatal(verr)
			}
			got := classify(context.Background(), &exec.ExitError{}, notFound, nil, s.caller())
			if !strings.Contains(got.Hint, tc.want) {
				t.Errorf("hint = %q, want %q in it", got.Hint, tc.want)
			}
		})
	}
}

// The kubectl commands a failure hands over as "the same question" are asked
// of the context the call read: bare they ask the context of the shell they
// are pasted into, which may well answer.
func TestTheKubectlCommandsAFailureOffersAskTheContextTheCallRead(t *testing.T) {
	const kubeContext = "arn:aws:eks:eu-west-3:1234:cluster/prod"
	s, verr := selectionOf(req(map[string]any{"context": kubeContext}).WithSurface(plugin.SurfaceCLI))
	if verr != nil {
		t.Fatal(verr)
	}
	flag := plugin.ShellWord("--context=" + kubeContext)
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	const missing = "error: the server doesn't have a resource type \"clusters\""
	for name, tc := range map[string]struct {
		ctx    context.Context
		stderr string
		want   string
	}{
		"timeout":     {expired, "", "`kubectl " + flag + " cluster-info`"},
		"no operator": {context.Background(), missing, "`kubectl " + flag + " get crd | grep cnpg`"},
	} {
		t.Run(name, func(t *testing.T) {
			got := classify(tc.ctx, &exec.ExitError{}, tc.stderr, nil, s.caller())
			if !strings.Contains(got.Hint, tc.want) {
				t.Errorf("hint = %q, want %q in it", got.Hint, tc.want)
			}
		})
	}
}

// The same listing, from the capabilities that were asked about no cluster at
// all: they name the cluster in a context a call chose, and the listing they
// point at read the current one.
func TestTheListingACallWithNoClusterOffersReachesTheContextItWasMadeIn(t *testing.T) {
	fakeKubectl(t, `echo '{"items":[]}'`)
	values := map[string]any{"context": "kind", "cluster": ""}
	want := "`rta cnpg list --all-namespaces --context kind`"
	for name, run := range map[string]func(context.Context, plugin.Request) (view.View, error){
		"backup request": runBackupRequest, "storage": runStorage,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := run(context.Background(), req(values).WithSurface(plugin.SurfaceCLI))
			var verr *view.Error
			if !errors.As(err, &verr) || !strings.Contains(verr.Hint, want) {
				t.Errorf("err = %v, want a hint naming %s", err, want)
			}
		})
	}
	v, err := runStorage(context.Background(), req(map[string]any{"context": "kind", "cluster": "shop"}).
		WithSurface(plugin.SurfaceCLI))
	if err != nil {
		t.Fatal(err)
	}
	if body := v.(view.Text).Body; !strings.Contains(body, want) {
		t.Errorf("body = %q, want %s in it", body, want)
	}
}
