package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The two listings of what is on a server answer to one bound, in every SQL
// plugin: 50 by default and 1000 at most. A model that leaves the limit out pays
// in tokens for all of what comes back, so the default is a screen, and the
// ceiling is what one answer holds before the bridge cuts it.
func TestTheListingsOfWhatIsOnAServerShareOneBound(t *testing.T) {
	for id, help := range map[string]string{
		"mysql.table.list":    "how many tables to show",
		"mysql.database.list": "how many databases to show",
	} {
		var limit *plugin.Field
		for _, c := range Plugin().Capabilities {
			if c.ID != id {
				continue
			}
			for i := range c.Inputs {
				if c.Inputs[i].Name == "limit" {
					limit = &c.Inputs[i]
				}
			}
		}
		if limit == nil {
			t.Fatalf("%s declares no limit", id)
		}
		if limit.Default != 50 || limit.Min != 1 || limit.Max != 1000 {
			t.Errorf("%s: limit is default %v, from %v to %v, want default 50, from 1 to 1000", id, limit.Default, limit.Min, limit.Max)
		}
		if limit.Help != help {
			t.Errorf("%s: limit help = %q, want %q", id, limit.Help, help)
		}
	}
}
