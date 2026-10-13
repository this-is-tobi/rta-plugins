package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The list of contexts needs a grant for an agent because it is the inventory
// of every destination this machine can reach, and a refusal that quoted the
// names, or that sent the agent to a call it cannot make, would hand the same
// map out one typo at a time and leave the agent no way to act. What it is told
// instead is what only the operator can fix.
func TestAnAgentIsNeitherToldTheContextsNorSentToTheListThatNeedsAGrant(t *testing.T) {
	const listTool, setTool = "kube_context_list", "kube_context_set"
	cfg := mustRawConfig(t)

	for name, refuse := range map[string]func(sf plugin.Surface) *view.Error{
		"a switch to a context that is not there": func(sf plugin.Surface) *view.Error {
			withFixtureKubectl(t, rawConfigFixture)
			_, err := runContextSet(context.Background(),
				plugin.NewRequest(map[string]any{"name": "nope"}, false, false).WithSurface(sf))
			return asViewError(t, err)
		},
		"the current context read when none is set": func(sf plugin.Surface) *view.Error {
			withFixtureKubectl(t, `{"contexts": [{"name": "kind", "context": {"cluster": "kind-cluster"}}]}`)
			_, err := runContextGet(context.Background(), plugin.NewRequest(nil, false, false).WithSurface(sf))
			return asViewError(t, err)
		},
		"a context read that the kubeconfig does not hold": func(sf plugin.Surface) *view.Error {
			withFixtureKubectl(t, rawConfigFixture)
			_, err := runContextGet(context.Background(),
				plugin.NewRequest(map[string]any{"context": "ghost-prod-eu"}, false, false).WithSurface(sf))
			return asViewError(t, err)
		},
		"a cluster looked up for a context that is not there": func(sf plugin.Surface) *view.Error {
			_, verr := coordinatesFor(cfg, selection{Context: "ghost-prod-eu", sf: sf})
			return verr
		},
		"a cluster looked up when no context is current": func(sf plugin.Surface) *view.Error {
			bare := cfg
			bare.CurrentContext = ""
			_, verr := coordinatesFor(bare, selection{sf: sf})
			return verr
		},
		"kubectl finding no current context": func(sf plugin.Surface) *view.Error {
			return classify(context.Background(), errors.New("exit status 1"),
				"error: current-context is not set\n", []string{"get", "nodes"}, sf)
		},
		"kubectl finding no such context": func(sf plugin.Surface) *view.Error {
			return classify(context.Background(), errors.New("exit status 1"),
				"error: context \"gone\" does not exist\n", nil, sf)
		},
		"an overview section with nothing to say": func(sf plugin.Surface) *view.Error {
			return &view.Error{Code: "kube.noconfig", Hint: hintOf(sf, nil)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			agent := refuse(plugin.SurfaceMCP)
			if agent == nil {
				t.Fatal("the call was not refused")
			}
			for _, leaked := range []string{listTool, setTool, "kind", "prod"} {
				if strings.Contains(agent.Hint, leaked) {
					t.Errorf("the agent's hint names %q, which it cannot use or may not be told:\n%s", leaked, agent.Hint)
				}
			}
			if !strings.Contains(agent.Hint, "ask the operator") {
				t.Errorf("the agent is not told it is the operator's to fix: %q", agent.Hint)
			}
			// The person at a terminal keeps the call that shows them.
			cli := refuse(plugin.SurfaceCLI)
			if cli == nil || !strings.Contains(cli.Hint, "rta kube context") {
				t.Errorf("the CLI lost the pointer to the contexts: %+v", cli)
			}
		})
	}
}

// An input's help is one sentence for every surface, so it cannot promise the
// list to a reader who may not call it.
func TestTheSwitchSaysWhoMayListTheContextsItTakes(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.ID != "kube.context.set" {
			continue
		}
		for _, f := range c.Inputs {
			if f.Name == "name" && !strings.Contains(f.Help, "needs a grant") {
				t.Errorf("the context input's help promises the list to everyone: %q", f.Help)
			}
		}
		return
	}
	t.Fatal("kube.context.set is not declared")
}

func asViewError(t *testing.T, err error) *view.Error {
	t.Helper()
	var verr *view.Error
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a *view.Error", err)
	}
	return verr
}

func mustRawConfig(t *testing.T) rawClusterConfig {
	t.Helper()
	withFixtureKubectl(t, rawConfigFixture)
	cfg, verr := readRawClusterConfig(context.Background(), plugin.SurfaceCLI)
	if verr != nil {
		t.Fatal(verr)
	}
	return cfg
}
