package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A profile fences a database by naming it: only: {schema: [app]}. The input
// was positional, which no fence can hold, so `rta profile set --only schema=app`
// was refused as reading nothing, and a profile fenced some other way let a call
// name any database the credential could see. Now the one-entry fence fills the
// input when a call names nothing, allows the database it names, and refuses
// every other.
func TestASchemaFenceRefusesEveryOtherDatabaseAndFillsTheOneItAllows(t *testing.T) {
	for _, id := range []string{"mysql.schema", "mysql.table.list"} {
		var c *plugin.Capability
		for _, p := range Plugin().Capabilities {
			if p.ID == id {
				cp := p
				c = &cp
			}
		}
		if c == nil {
			t.Fatalf("no %s", id)
		}
		fence := plugin.Fence{"schema": {"app"}}
		if breach := plugin.CheckFence(*c, plugin.Inputs{Fence: fence, Caller: map[string]any{}}); breach != nil {
			t.Errorf("%s: a call naming nothing was refused under a one-entry fence: %+v", id, breach)
		}
		req := plugin.ResolveRequest(*c, plugin.Inputs{Fence: fence, Caller: map[string]any{}}, false, false)
		if got := req.String("schema"); got != "app" {
			t.Errorf("%s: the fence filled schema = %q, want the one it allows", id, got)
		}
		if breach := plugin.CheckFence(*c, plugin.Inputs{Fence: fence, Caller: map[string]any{"schema": "app"}}); breach != nil {
			t.Errorf("%s: the allowed database was refused: %+v", id, breach)
		}
		if plugin.CheckFence(*c, plugin.Inputs{Fence: fence, Caller: map[string]any{"schema": "other_tenant_db"}}) == nil {
			t.Errorf("%s: a call naming a database outside the fence was let through", id)
		}
	}
}
