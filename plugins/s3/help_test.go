package main

import (
	"strings"
	"testing"
)

// A description is what an agent reads, and an input it cannot pass is not
// something to tell it about: s3.object.get said a person at a terminal "also
// has `out`", which is a sentence about a file path no agent can give. The
// input's own help carries it, and the generated README shows that help beside
// the "never offered to MCP callers" note. `bucket` is the one Local input a
// description names on purpose, to say the agent cannot choose it.
func TestADescriptionDoesNotNameAnInputAnAgentCannotPass(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.HumanOnly {
			continue
		}
		for _, f := range c.Inputs {
			if f.Local && f.Name != "bucket" && strings.Contains(c.Description, "`"+f.Name+"`") {
				t.Errorf("%s: the description names `%s`, an input an agent cannot pass", c.ID, f.Name)
			}
		}
	}
}

// The help for content-type said the type was "guessed from the file's
// extension if omitted". Over MCP there is no file — `file` is Local — so for
// an agent the sentence described a path it cannot take, and the type its
// value was actually stored under (text/plain) was nowhere it could read.
func TestContentTypeSaysWhatAValueIsStoredAs(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.ID != "s3.object.set" {
			continue
		}
		for _, f := range c.Inputs {
			if f.Name == "content-type" {
				if !strings.Contains(f.Help, "text/plain for a value") {
					t.Errorf("content-type help = %q, want it to say a value is stored as text/plain", f.Help)
				}
				return
			}
		}
	}
	t.Fatal("s3.object.set declares no content-type")
}
