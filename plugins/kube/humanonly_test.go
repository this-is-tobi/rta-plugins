package main

import "testing"

// What moves a cluster identity is for the person at the terminal, and the
// declaration is what says so. Before HumanOnly the handler's own refusal
// was the only gate, which left the capability advertised to agents,
// priced at a grant under the one-gate model, and then refused — an
// operator consenting to a call that could never run.
func TestMintingAnIdentityIsNeverATool(t *testing.T) {
	want := map[string]bool{
		"kube.serviceaccount.provision": true,
	}
	seen := 0
	for _, c := range Plugin().Capabilities {
		if c.HumanOnly != want[c.ID] {
			t.Errorf("%s: HumanOnly = %v, want %v", c.ID, c.HumanOnly, want[c.ID])
		}
		if want[c.ID] {
			seen++
		}
	}
	if seen != len(want) {
		t.Errorf("%d of %d human-only capabilities declared", seen, len(want))
	}
}
