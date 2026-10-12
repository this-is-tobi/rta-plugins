package main

import "testing"

// Every context in the kubeconfig, with its cluster, user and API server, is
// the inventory of what this machine can reach. An agent that is not told the
// names of the operator's profiles is not handed this either without a grant,
// whatever the safety class says: a read that contacts no cluster is still not
// a free one when what it returns is a list of destinations.
func TestTheListOfContextsNeedsAGrantForAnAgent(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.ID != "kube.context.list" {
			continue
		}
		if !c.NeedsGrant {
			t.Error("kube.context.list is free to an agent: it hands over every destination this machine can reach")
		}
		return
	}
	t.Fatal("kube.context.list is not declared")
}
