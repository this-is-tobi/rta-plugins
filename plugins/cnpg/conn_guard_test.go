package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Every shared connection input must be Local. `context` names which cluster
// a call reaches, and an MCP caller may not choose that — a kubeconfig lists
// every cluster this machine can reach with a working identity attached to
// each, so an agent free to pass it would not be reading "the cluster the
// operator is working in" but any of them, and a grant scoped to a cluster
// name would act in whichever one the caller pointed it at.
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
// happened to inherit — KUBECONFIG is exactly such a variable, and kubectl
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

// There is no password test here, unlike plugins/mysql's: the cluster is
// reached through kubectl and the identity the kubeconfig context already
// carries, so connFields() declares no credential at all.

// Every capability gets the connection inputs, because cap appends them. A
// capability that declared its own would drift from the rest, and the one that
// forgot would be unreachable against any cluster but the current one.
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
