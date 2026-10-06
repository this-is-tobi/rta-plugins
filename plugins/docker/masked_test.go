package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func capabilityNamed(t *testing.T, id string) plugin.Capability {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("%s is not declared", id)
	return plugin.Capability{}
}

// What the description says about an environment is what the result does with
// it. rta masks every column a plugin marks redacted, on every surface and for
// a caller holding a grant too, and inspect marks every value — on purpose, so
// that a grant to look at a container's state does not copy its credentials
// into an agent's context. If a value is ever returned there, this fails and
// the description has to follow.
func TestTheDescriptionSaysTheInspectedEnvironmentValuesComeBackMasked(t *testing.T) {
	tbl, ok := envView([]string{"DB_PASSWORD=hunter2", "TZ=UTC"}, true).(view.Table)
	if !ok {
		t.Fatal("an environment is not a table")
	}
	masked := slices.Contains(tbl.Redacted, "value")
	if slices.Contains(tbl.Redacted, "variable") {
		t.Error("the names are redacted — which variables a container sets is the question the call answers")
	}
	c := capabilityNamed(t, "docker.container.inspect")
	if said := strings.Contains(c.Description, "values come back masked (••••••), on every surface"); said != masked {
		t.Errorf("the values are redacted = %v, and the description says so = %v", masked, said)
	}
	if c.Reveals {
		t.Error("docker.container.inspect declares Reveals, and it masks what it shows")
	}
}

// docker.container.env is the reveal of this plugin: the values as set, behind a
// grant that names the container. The declaration, the class, the grant and the
// scope are one decision, and a mask on it would hide the values from every
// reader whatever the grant.
func TestTheEnvironmentOfAContainerIsADeclaredRevealReturnedAsSet(t *testing.T) {
	c := capabilityNamed(t, "docker.container.env")
	if !c.Reveals || c.Safety != plugin.Write || !c.NeedsGrant || c.Scope != "container" {
		t.Errorf("docker.container.env: Reveals=%v Safety=%s NeedsGrant=%v Scope=%q; want a reveal, write, granted, scoped to the container",
			c.Reveals, c.Safety, c.NeedsGrant, c.Scope)
	}
	tbl, ok := envView([]string{"DB_PASSWORD=hunter2", "TZ=UTC"}, false).(view.Table)
	if !ok {
		t.Fatal("an environment is not a table")
	}
	if len(tbl.Redacted) != 0 {
		t.Errorf("the reveal masks %v", tbl.Redacted)
	}
	if got := strings.Join(tbl.Rows[0], "="); got != "DB_PASSWORD=hunter2" {
		t.Errorf("first row = %q, want the variable and its value as set", got)
	}
	for _, text := range []string{c.Description, c.AgentText()} {
		if strings.Contains(text, "come back masked") {
			t.Errorf("docker.container.env says its values are masked, and they are not: %.80s…", text)
		}
	}
}

const inspectedWeb = `[{"Id":"abc123abc123abc123","Name":"/web","State":{"Status":"running","Running":true},` +
	`"Config":{"Image":"nginx","Env":["DB_PASSWORD=hunter2","TZ=UTC"]},"HostConfig":{"RestartPolicy":{"Name":"no"}}}]`

// A mask says where the value can be read. Inspect's environment is masked, and
// the page it sits in carries the call that returns it, worded for the surface
// that is looking: a command line at a terminal, a tool and its arguments to
// an agent. The reveal itself returns what the daemon holds, unmarked.
func TestTheInspectedEnvironmentPointsAtTheCallThatReadsIt(t *testing.T) {
	scriptedDocker(t, "echo '"+inspectedWeb+"'\n")
	for _, tc := range []struct {
		surface plugin.Surface
		want    string
	}{
		{plugin.SurfaceCLI, "rta docker container env web"},
		{plugin.SurfaceMCP, "docker_container_env"},
	} {
		r := capReq(t, "docker.container.inspect", map[string]any{"container": "web"}).WithSurface(tc.surface)
		v, err := runInspect(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		pointer := ""
		for _, sec := range v.(view.Sections).Items {
			if kv, ok := sec.View.(view.KeyValue); ok {
				for _, p := range kv.Pairs {
					if p.Key == view.RevealKey {
						pointer = p.Value
					}
				}
			}
		}
		if !strings.Contains(pointer, tc.want) {
			t.Errorf("surface %v: the reveal pair = %q, want it to hold %q", tc.surface, pointer, tc.want)
		}
	}

	v, err := runEnv(t.Context(), capReq(t, "docker.container.env", map[string]any{"container": "web"}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	if len(tbl.Redacted) != 0 || len(tbl.Rows) != 2 || tbl.Rows[0][1] != "hunter2" {
		t.Errorf("the environment = %+v, want both variables, as set and unmasked", tbl)
	}
}
