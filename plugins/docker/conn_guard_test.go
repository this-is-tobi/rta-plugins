package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Every shared connection input must be Local. `host` and `context` together
// name which daemon a call reaches, and an MCP caller may not choose that —
// an agent that could would aim a stop or an rm at a machine the operator
// never named, and a grant scoped to a container name would act on whichever
// daemon the caller pointed it at.
//
// Written against connFields() rather than against a list of names, so an
// input added here later is covered the day it is added. That is the only
// version of this test worth having: a list of names passes forever while the
// thing it is guarding grows past it.
func TestEveryConnectionInputIsLocal(t *testing.T) {
	for _, f := range connFields() {
		if !f.Local {
			t.Errorf("%s: connection input is not Local — an MCP caller could redirect this call", f.Name)
		}
	}
}

// Only a genuine credential opts into EnvFallback. A field that merely chooses
// a destination must not be fillable from an ambient variable the MCP server
// happened to inherit — DOCKER_HOST is exactly such a variable, and the CLI
// reads it itself, on this machine, which is a different thing from rta
// handing an agent's choice of it through.
func TestOnlySecretsUseEnvFallback(t *testing.T) {
	for _, f := range connFields() {
		if f.EnvFallback && f.Type != plugin.Secret {
			t.Errorf("%s: non-secret input declares EnvFallback (%s); a destination must come from a caller or config",
				f.Name, f.Type)
		}
	}
}

// There is no password test here, unlike plugins/mysql's: the daemon is
// reached over a socket or a context the CLI already holds credentials for,
// so connFields() declares no credential at all.

// Every capability gets the connection inputs, because cap appends them. A
// capability that declared its own would drift from the rest, and the one that
// forgot would be unreachable against any daemon but the default.
func TestEveryCapabilityCarriesTheConnectionInputs(t *testing.T) {
	shared := map[string]bool{}
	for _, f := range connFields() {
		shared[f.Name] = true
	}
	for _, c := range Plugin().Capabilities {
		got := map[string]bool{}
		for _, f := range c.Inputs {
			got[f.Name] = true
		}
		for name := range shared {
			if !got[name] {
				t.Errorf("%s: missing connection input %q — was it declared without cap()?", c.ID, name)
			}
		}
	}
}
