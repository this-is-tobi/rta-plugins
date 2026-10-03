package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// What the description says about an environment is what the result does with
// it. rta masks every column a plugin marks redacted, on every surface and for
// a caller holding a grant too, and this plugin marks every value — on
// purpose, so that a granted call does not copy a container's credentials into
// an agent's context. The description named the environment among what the call
// returns and said nothing of the masking. If a value is ever returned, this
// fails and the description has to follow.
func TestTheDescriptionSaysTheEnvironmentValuesComeBackMasked(t *testing.T) {
	tbl, ok := envView([]string{"DB_PASSWORD=hunter2", "TZ=UTC"}).(view.Table)
	if !ok {
		t.Fatal("an environment is not a table")
	}
	masked := slices.Contains(tbl.Redacted, "value")
	if slices.Contains(tbl.Redacted, "variable") {
		t.Error("the names are redacted — which variables a container sets is the question the call answers")
	}
	for _, c := range Plugin().Capabilities {
		if c.ID != "docker.container.inspect" {
			continue
		}
		if said := strings.Contains(c.Description, "values come back masked (••••••), on every surface"); said != masked {
			t.Errorf("the values are redacted = %v, and the description says so = %v", masked, said)
		}
		return
	}
	t.Fatal("docker.container.inspect is not declared")
}
