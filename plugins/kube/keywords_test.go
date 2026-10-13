package main

import "testing"

// A keyword is there to make a capability findable by what it returns. The
// list of contexts is not found by "environments", which is what rta calls a
// profile, nor by "switch", which is the word of the call that does it.
func TestTheListOfContextsIsNotFoundByAnotherCallsWords(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.ID != "kube.context.list" {
			continue
		}
		for _, k := range c.Keywords {
			if k == "environments" || k == "switch" {
				t.Errorf("kube.context.list is also called %q", k)
			}
		}
		return
	}
	t.Fatal("kube.context.list is not declared")
}
