package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Every shared connection input must be Local. These
// fields together name which server a call reaches and as whom, and an MCP
// caller may not choose that — an agent that could would point rta at a
// server of its own and have the host supply the operator's credential
// beside it, which is exactly what it could do before those inputs were made Local.
//
// Written against connFields() rather than against a list of names so that
// an input added here later is covered the day it is added, which is the
// only version of this test worth having.
func TestEveryConnectionInputIsLocal(t *testing.T) {
	for _, f := range connFields() {
		if !f.Local {
			t.Errorf("%s: connection input is not Local — an MCP caller could redirect this call", f.Name)
		}
	}
}

// Where the service is has one name per shape across the plugins: `address`
// takes host[:port] and `url` takes a URL, so a profile's `set:` reads the
// same whichever plugin it fills and nobody needs the card to know which word
// this one chose. Written against the role the input declares rather than a
// list of names, so a destination added later is held to it the day it is added.
func TestTheDestinationInputIsNamedForItsRole(t *testing.T) {
	found := false
	for _, f := range connFields() {
		if f.Endpoint != plugin.EndpointURL {
			continue
		}
		found = true
		if f.Name != "url" || f.Config != "url" {
			t.Errorf("the %s input is %q (config %q), want %q: it takes URL, and that is the word the plugins share",
				f.Endpoint, f.Name, f.Config, "url")
		}
	}
	if !found {
		t.Error("no connection input declares the url role")
	}
}

// The mount is the container a scoped record sits in — kv.get/kv.set scope
// on "path", transit.decrypt scopes on "key" — but neither ever scopes on
// "mount" itself, so a caller-settable mount would let a grant on one
// record authorize the identical name in a mount the grant never named.
// Written against the declaration, so a gated capability that adds a mount
// input without binding it is caught the day it ships.
func TestEveryGatedCapabilityBindsItsMount(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if !c.NeedsGrant {
			continue
		}
		for _, f := range c.Inputs {
			if f.Name == "mount" && !f.Local {
				t.Errorf("%s: gated capability declares mount caller-settable — "+
					"a grant on %q authorizes it in any mount", c.ID, c.Scope)
			}
		}
	}
}

// Only a genuine credential opts into EnvFallback. A field that merely
// chooses a destination must not be fillable from an ambient variable the
// MCP server happened to inherit — the EnvFallback distinction this leans on.
func TestOnlySecretsUseEnvFallback(t *testing.T) {
	for _, f := range connFields() {
		if f.EnvFallback && f.Type != plugin.Secret {
			t.Errorf("%s: non-secret input declares EnvFallback (%s); a destination must come from a caller or config",
				f.Name, f.Type)
		}
	}
}
