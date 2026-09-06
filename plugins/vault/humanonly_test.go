package main

import "testing"

// What moves a whole Vault is for the person at the terminal, and the
// declaration is what says so. Before HumanOnly the handler's own refusal
// was the only gate, which left the capability advertised to agents,
// priced at a grant under the one-gate model, and then refused — an
// operator consenting to a call that could never run.
func TestMovingTheWholeVaultIsNeverATool(t *testing.T) {
	want := map[string]bool{
		"vault.snapshot": true,
		"vault.restore":  true,
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
