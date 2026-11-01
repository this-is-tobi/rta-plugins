package family

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The rows a caller's own statement may return is one concept in every SQL
// plugin, so it has one default, one ceiling and one sentence, plugins/pg's:
// 200 by default, 10000 at most, and a larger result refused and not shortened.
// This said 50, at most 1000, and "how many rows to allow before refusing", so
// a statement over 51 rows that ran on PostgreSQL was refused on MySQL at the
// defaults, and a caller that had learned the one met the other.
func TestTheQueryLimitIsTheOneEverySQLPluginDeclares(t *testing.T) {
	var query plugin.Capability
	for _, c := range Plugin(engine, "dev").Capabilities {
		if c.ID == "mysql.query" {
			query = c
		}
	}
	var limit *plugin.Field
	for i := range query.Inputs {
		if query.Inputs[i].Name == "limit" {
			limit = &query.Inputs[i]
		}
	}
	if limit == nil {
		t.Fatal("mysql.query declares no limit")
	}
	if limit.Default != 200 || limit.Min != 1 || limit.Max != 10000 {
		t.Errorf("limit is default %v, from %v to %v, want default 200, from 1 to 10000", limit.Default, limit.Min, limit.Max)
	}
	if want := "maximum rows to return; a larger result set is refused, not shortened"; limit.Help != want {
		t.Errorf("limit help = %q, want %q", limit.Help, want)
	}
	if !strings.Contains(query.Agent, "(default 200, at most 10000)") {
		t.Errorf("an MCP client is shown %q, which is not the numbers the input declares", query.Agent)
	}
}

// An agent reads the limit's sentence as "refused, not shortened", and that is
// the whole story of the row bound; what the bridge does to an answer too big
// for one tool result is its own and is said where the agent reads the rest of
// what the tool does.
func TestTheAgentIsToldWhatBecomesOfAnAnswerTooBigForOneToolResult(t *testing.T) {
	for _, c := range Plugin(engine, "dev").Capabilities {
		if c.ID != "mysql.query" {
			continue
		}
		if !strings.Contains(c.Agent, "cut to its first rows, with the true total") {
			t.Errorf("an MCP client is shown %q, which does not say what becomes of an answer too big for one tool result", c.Agent)
		}
		return
	}
	t.Fatal("no query capability")
}
