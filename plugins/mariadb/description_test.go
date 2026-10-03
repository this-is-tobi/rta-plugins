package main

import (
	"regexp"
	"strings"
	"testing"
)

// What a tool says about itself is read by the agent that decides whether to
// call it, and what it can do with a sentence is limited to calling tools. A
// description that told it to take `grant.allow` for a key could not be acted
// on: granting is the operator's, and rta already ends every description that
// needs a grant with the call to ask the operator for it, scoped to the one
// record. The same goes for a pointer into rta's own built-ins, which no tool
// of this plugin's reader can follow.
func TestWhatAnAgentReadsNamesNothingItCannotCall(t *testing.T) {
	bareKVGet := regexp.MustCompile(`(^|[^.\w])kv\.get`)
	for _, c := range Plugin().Capabilities {
		if c.HumanOnly {
			continue
		}
		texts := []string{c.Summary, c.Description}
		for _, f := range c.Inputs {
			texts = append(texts, f.Help)
		}
		for _, s := range texts {
			for _, bad := range []string{"grant.allow", "builtin/", "keys.backup", "kv.copy"} {
				if strings.Contains(s, bad) {
					t.Errorf("%s says %q — a call its reader cannot make: %.80s…", c.ID, bad, s)
				}
			}
			if bareKVGet.MatchString(s) {
				t.Errorf("%s points at rta's own kv.get — a call its reader cannot make: %.80s…", c.ID, s)
			}
		}
	}
}
