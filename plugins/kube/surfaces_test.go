package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A refusal names a capability and an input the way the surface reading it
// gives them: at a terminal as it always read, and elsewhere as a tool and
// its arguments or a form's box — never as a flag or an `rta` command line
// its reader has no terminal for.
func TestARefusalNamesWhatItsSurfaceGives(t *testing.T) {
	for _, tc := range []struct {
		name, cli, other string
		surface          plugin.Surface
		refuse           func(sf plugin.Surface) *view.Error
	}{
		{
			name:    "a namespace and every namespace at once",
			cli:     "--namespace and --all-namespaces ask for different things",
			other:   `the "namespace" argument and the "all-namespaces" argument ask for different things`,
			surface: plugin.SurfaceMCP,
			refuse: func(sf plugin.Surface) *view.Error {
				_, verr := selectionOf(plugin.NewRequest(map[string]any{"namespace": "db", "all-namespaces": true},
					false, false).WithSurface(sf))
				return verr
			},
		},
		{
			name:    "a context this machine does not have",
			cli:     "`rta kube context list` shows the contexts this machine has",
			other:   "the `kube_context_list` tool shows the contexts this machine has",
			surface: plugin.SurfaceMCP,
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(context.Background(), errors.New("exit status 1"),
					"error: context \"gone\" does not exist\n", nil, sf)
			},
		},
		{
			name:    "an identity with no grant",
			cli:     "--grant kube.pod.list (or logs, rollout…), repeatable",
			other:   "the grant box set to kube.pod.list (or logs, rollout…), repeatable",
			surface: plugin.SurfaceTUI,
			refuse: func(sf plugin.Surface) *view.Error {
				_, verr := rulesFor(sf, nil)
				return verr
			},
		},
		{
			name:    "a kubeconfig already there",
			cli:     "or pass --force to replace it",
			other:   "or pass the force box to replace it",
			surface: plugin.SurfaceTUI,
			refuse: func(sf plugin.Surface) *view.Error {
				path := filepath.Join(t.TempDir(), "kubeconfig")
				if err := os.WriteFile(path, []byte("A WORKING KUBECONFIG"), 0o600); err != nil {
					t.Fatal(err)
				}
				return writeKubeconfig(sf, path, []byte("MINTED"), false)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := tc.refuse(plugin.SurfaceCLI)
			if said := cli.Message + "\n" + cli.Hint; !strings.Contains(said, tc.cli) {
				t.Errorf("the CLI reads %q, want %q in it", said, tc.cli)
			}
			other := tc.refuse(tc.surface)
			said := other.Message + "\n" + other.Hint
			if !strings.Contains(said, tc.other) {
				t.Errorf("%s reads %q, want %q in it", tc.surface, said, tc.other)
			}
			if strings.Contains(said, tc.cli) {
				t.Errorf("%s reads the CLI's %q", tc.surface, tc.cli)
			}
		})
	}
}

// A token the cluster clamped below the ttl asked for says so on the receipt,
// naming the ttl the way the surface reading it gives one: --ttl at a
// terminal, the ttl box in the TUI, whose name for it already carries its
// article. The TUI once read "shorter than the the ttl box requested".
func TestAClampedTokenNamesTheTTLItFellShortOf(t *testing.T) {
	dir := t.TempDir()
	payload, err := json.Marshal(map[string]int64{"exp": time.Now().Add(15 * time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	for name, body := range map[string]string{"token": token, "config": rawConfigFixture} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// create -f - reads a manifest, create token prints the clamped token,
	// and config view --raw prints the kubeconfig it is set into.
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*\"create token\"*) cat '" + filepath.Join(dir, "token") + "' ;;\n" +
		"*\"config view\"*) cat '" + filepath.Join(dir, "config") + "' ;;\n" +
		"*) cat >/dev/null ;;\nesac\n"
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := kubectlBin
	kubectlBin = bin
	t.Cleanup(func() { kubectlBin = orig })

	for sf, want := range map[plugin.Surface]string{
		plugin.SurfaceCLI: "(shorter than the --ttl requested — this cluster's own token expiry ceiling clamped it)",
		plugin.SurfaceTUI: "(shorter than the ttl box requested — this cluster's own token expiry ceiling clamped it)",
	} {
		v, err := runServiceAccountProvision(context.Background(), saReq(sf, false, map[string]any{
			"name": "agent-x", "namespace": "team-a", "ttl": "1h", "grant": []string{"kube.pod.list"},
		}))
		if err != nil {
			t.Fatalf("%s: %v", sf, err)
		}
		sections, ok := v.(view.Sections)
		if !ok || len(sections.Items) == 0 {
			t.Fatalf("%s: answered %T, want the summary and the kubeconfig", sf, v)
		}
		expiry := ""
		for _, p := range sections.Items[0].View.(view.KeyValue).Pairs {
			if p.Key == "actual token expiry" {
				expiry = p.Value
			}
		}
		if !strings.HasSuffix(expiry, want) {
			t.Errorf("%s: actual token expiry = %q, want it to end %q", sf, expiry, want)
		}
	}
}
