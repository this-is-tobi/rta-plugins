package main

import (
	"strings"
	"testing"
)

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
