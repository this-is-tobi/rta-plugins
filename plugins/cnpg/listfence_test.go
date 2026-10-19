package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A profile that fences `cluster` to one name has said which cluster the
// connection is about. `cnpg list` took no cluster, so the fence had nothing to
// hold it to and a connection fenced to abrege-pg-cluster listed every cluster
// of the namespace, mcr-pg-cluster among them, under a target that was not
// about it. The listing now takes the input, and the one-entry fence fills it.
func TestAListUnderAClusterFenceReadsThatClusterAndNoOther(t *testing.T) {
	var list *plugin.Capability
	for _, c := range Plugin().Capabilities {
		if c.ID == "cnpg.list" {
			list = &c
		}
	}
	if list == nil {
		t.Fatal("no cnpg.list")
	}
	in := plugin.Inputs{Fence: plugin.Fence{"cluster": {"abrege-pg-cluster"}}, Caller: map[string]any{}}
	if breach := plugin.CheckFence(*list, in); breach != nil {
		t.Fatalf("a call that names nothing was refused under a one-entry fence: %+v", breach)
	}
	req := plugin.ResolveRequest(*list, in, false, false)
	if got := req.StringSlice("cluster"); !slices.Equal(got, []string{"abrege-pg-cluster"}) {
		t.Fatalf("the fence filled cluster = %q, want the one it allows as a list of one", got)
	}

	argv := servesWithArgs(t, clusterList{})
	if _, err := runList(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := argv(); !strings.Contains(got, "--field-selector metadata.name=abrege-pg-cluster") {
		t.Errorf("argv = %q, want the API server asked for that cluster alone", got)
	}

	other := plugin.Inputs{Fence: in.Fence, Caller: map[string]any{"cluster": "mcr-pg-cluster"}}
	if plugin.CheckFence(*list, other) == nil {
		t.Error("a call naming a cluster outside the fence was let through")
	}
}

func TestAListWithNoClusterStillReadsEveryClusterOfTheNamespace(t *testing.T) {
	argv := servesWithArgs(t, clusterList{})
	if _, err := runList(context.Background(), req(map[string]any{})); err != nil {
		t.Fatal(err)
	}
	if got := argv(); strings.Contains(got, "--field-selector") {
		t.Errorf("argv = %q, want no filter when no cluster is named", got)
	}
}

func TestAnEmptyListForANamedClusterSaysWhichOneItLookedFor(t *testing.T) {
	servesWithArgs(t, clusterList{})
	v, err := runList(context.Background(), req(map[string]any{"cluster": "abrege-pg-cluster", "namespace": "prod"}))
	if err != nil {
		t.Fatal(err)
	}
	body := v.(view.Table).Empty
	if !strings.Contains(body, `no CloudNativePG cluster called "abrege-pg-cluster" in namespace prod`) {
		t.Errorf("body = %q", body)
	}
}
