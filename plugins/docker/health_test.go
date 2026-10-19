package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// docker/cli v27's `ps --format json` has no HealthStatus field: the verdict
// of a healthcheck is in the Status text the daemon writes ("Up 3 hours
// (unhealthy)"), and the overview, which promises what is unhealthy, could not
// see a container that was. v29 reads the word out of that text when the field
// is empty, and so does this.
func TestHealthIsReadFromTheStatusTextWhenTheCLIHasNoFieldForIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  containerRow
		want string
	}{
		{"the field, when the CLI has it", containerRow{HealthStatus: "unhealthy", Status: "Up 3 hours"}, "unhealthy"},
		{"unhealthy in the text", containerRow{Status: "Up 3 hours (unhealthy)"}, "unhealthy"},
		{"healthy in the text", containerRow{Status: "Up About an hour (healthy)"}, "healthy"},
		{"starting, as the daemon writes it", containerRow{Status: "Up 5 seconds (health: starting)"}, "starting"},
		{"a paused container is not a health verdict", containerRow{Status: "Up 3 hours (Paused)"}, ""},
		{"an exit code is not one", containerRow{Status: "Exited (137) 3 days ago"}, ""},
		{"a restart count is not one", containerRow{Status: "Restarting (1) 5 seconds ago"}, ""},
		{"no healthcheck", containerRow{Status: "Up 3 hours"}, ""},
		{"none, from a CLI that says so", containerRow{HealthStatus: "none", Status: "Up 3 hours"}, ""},
		{"an unclosed parenthesis", containerRow{Status: "Up 3 hours (unhealthy"}, ""},
	} {
		if got := health(tc.row); got != tc.want {
			t.Errorf("%s: health(%q, %q) = %q, want %q", tc.name, tc.row.HealthStatus, tc.row.Status, got, tc.want)
		}
	}
}

func TestAnUnhealthyContainerIsSeenWhenTheCLIReportsItOnlyInTheStatusText(t *testing.T) {
	scriptedDocker(t, "case \"$*\" in\n"+
		"  *ps*) echo '{\"Names\":\"web\",\"State\":\"running\",\"Status\":\"Up 3 hours (unhealthy)\"}'\n"+
		"        echo '{\"Names\":\"warming\",\"State\":\"running\",\"Status\":\"Up 5 seconds (health: starting)\"}'\n"+
		"        echo '{\"Names\":\"stopped\",\"State\":\"running\",\"Status\":\"Up 3 hours (Paused)\"}'\n"+
		"        echo '{\"Names\":\"fine\",\"State\":\"running\",\"Status\":\"Up 2 hours (healthy)\"}' ;;\n"+
		"esac\n")

	v, err := runOverview(t.Context(), overviewReq(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	var sick string
	for _, p := range v.(view.KeyValue).Pairs {
		if p.Key == "problems" {
			sick = p.Value
		}
	}
	if sick != "web (unhealthy)" {
		t.Errorf("problems = %q, want web (unhealthy) alone", sick)
	}

	table, ok := answer(t, "docker.container.list", map[string]any{}).(view.Table)
	if !ok {
		t.Fatal("the container list is not a table")
	}
	healthColumn := -1
	for i, c := range table.Columns {
		if c.Name == "health" {
			healthColumn = i
		}
	}
	got := map[string]string{}
	for _, row := range table.Rows {
		got[row[0]] = row[healthColumn]
	}
	for name, want := range map[string]string{"web": "unhealthy", "warming": "starting", "stopped": "", "fine": "healthy"} {
		if got[name] != want {
			t.Errorf("the health column of %s = %q, want %q", name, got[name], want)
		}
	}
}
