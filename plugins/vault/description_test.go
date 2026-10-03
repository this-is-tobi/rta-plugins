package main

import (
	"strings"
	"testing"
)

// `version` is a number and `versions` is a list of text: the host has no list
// of numbers to offer, and one that is handed a number where it expects text
// refuses the call. An agent that read `version: integer` on vault.kv.get sent
// [1] to vault.kv.delete and was refused as "an array of strings"; the schema
// says so, but the one sentence that tells it how to write them is the input's
// own help.
func TestTheVersionsArgumentSaysItsNumbersAreText(t *testing.T) {
	seen := 0
	for _, c := range Plugin().Capabilities {
		if c.HumanOnly {
			continue
		}
		for _, f := range c.Inputs {
			if f.Name != "versions" {
				continue
			}
			seen++
			if !strings.Contains(f.Help, "written as text") {
				t.Errorf("%s: versions help = %q, want it to say the numbers are written as text", c.ID, f.Help)
			}
		}
	}
	if seen == 0 {
		t.Error("no capability takes `versions` — the check found nothing to check")
	}
}

// What a tool says about itself is read by the agent that decides whether to
// call it, and an agent cannot follow a pointer into rta's source. These were
// all in descriptions: a comparison to "builtin/kv's kv.get" and "gpg.sign's
// design", the declaration's own field names ("Write+NeedsGrant", "No Scope"),
// and the Vault CLI's commands, none of which an agent can run or look up.
// The reasoning behind a classification belongs in a comment above the
// capability, which is where the ones this replaced already were.
func TestWhatAnAgentReadsIsWrittenForAnAgent(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.HumanOnly {
			continue
		}
		texts := []string{c.Summary, c.Description}
		for _, f := range c.Inputs {
			texts = append(texts, f.Help)
		}
		for _, s := range texts {
			for _, bad := range []string{
				"builtin/", "NeedsGrant", "Write+", "Read+", "No Scope", "gpg.", "`vault kv", "`vault lease",
			} {
				if strings.Contains(s, bad) {
					t.Errorf("%s says %q — reasoning or a command its reader cannot use: %.80s…", c.ID, bad, s)
				}
			}
		}
	}
}
