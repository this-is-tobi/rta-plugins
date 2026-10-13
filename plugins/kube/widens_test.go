package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A profile's `only:` fences the namespace to what an operator named, and the
// host holds every value of the input to the list. It cannot read a handler to
// learn that `--all-namespaces` reads past the input, so the switch says so
// (Widens), and a call that sets it while the namespace is fenced is refused
// by the host before anything is dialled. Held on every capability that has
// the switch, run through the host's own check rather than the declaration's
// spelling.
func TestAllNamespacesIsHeldByAFenceOnTheNamespace(t *testing.T) {
	fence := plugin.Fence{"namespace": {"staging"}}
	found := 0
	for _, c := range Plugin().Capabilities {
		for _, f := range c.Inputs {
			if f.Name != "all-namespaces" {
				continue
			}
			found++
			if f.Widens != "namespace" {
				t.Errorf("%s: all-namespaces widens %q, want namespace", c.ID, f.Widens)
			}
			wide := plugin.Inputs{Fence: fence, Caller: map[string]any{"namespace": "staging", "all-namespaces": true}}
			if plugin.CheckFence(c, wide) == nil {
				t.Errorf("%s: all-namespaces got past a fence on the namespace", c.ID)
			}
			narrow := plugin.Inputs{Fence: fence, Caller: map[string]any{"namespace": "staging"}}
			if err := plugin.CheckFence(c, narrow); err != nil {
				t.Errorf("%s: a call inside the fence was refused: %v", c.ID, err)
			}
		}
	}
	if found == 0 {
		t.Fatal("no capability has an all-namespaces switch, so this holds nothing")
	}
}
