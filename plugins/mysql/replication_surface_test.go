package main

import (
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// What the capability says about its own grants names the setting the surface
// reading it has: a flag at a terminal, the operator's setting for an agent
// that has neither a flag to pass nor a configuration file to edit. A hint
// telling an agent to pass --user is one it cannot act on, and one it may try.
func TestTheGrantHintNamesTheSettingTheSurfaceHas(t *testing.T) {
	denied := &mysql.MySQLError{Number: 1227, Message: "Access denied; you need (at least one of) the SUPER, REPLICATION CLIENT privilege(s) for this operation"}
	for _, tc := range []struct {
		name    string
		surface plugin.Surface
		want    string
		not     string
	}{
		{"a terminal", plugin.SurfaceCLI, "or --user can name an account that has it", "operator"},
		{"an agent over MCP", plugin.SurfaceMCP, "or the operator's `user` setting can name an account that has it", "--user"},
	} {
		r := req(t, "mysql.replication.status", map[string]any{"user": "mon"}).WithSurface(tc.surface)
		hint := unreadable(denied, sectionReplica, parseVersion("8.4.0"), r).Hint
		if !strings.Contains(hint, tc.want) || strings.Contains(hint, tc.not) {
			t.Errorf("%s: hint = %q, want %q and no %q", tc.name, hint, tc.want, tc.not)
		}
		if !strings.Contains(hint, "GRANT ") || !strings.Contains(hint, "'mon'@'<host>'") {
			t.Errorf("%s: hint = %q, want the grant that fixes it for the account that asked", tc.name, hint)
		}
	}
}

// A server too old, or not the server it looks like, says so as a warning of
// its own rather than as a failure of the whole answer, and names where to see
// what the server is.
func TestAServerThatKnowsNoSpellingOfTheStatementIsReportedAsUnsupported(t *testing.T) {
	syntax := &mysql.MySQLError{Number: 1064, Message: "You have an error in your SQL syntax"}
	e := unreadable(syntax, sectionReplica, parseVersion("4.1.0"), req(t, "mysql.replication.status", nil).WithSurface(plugin.SurfaceMCP))
	if e.Code != "mysql.replication.unsupported" || !strings.Contains(e.Hint, "`mysql_status`") {
		t.Errorf("error = %+v, want the unsupported code and the tool that says what the server is", e)
	}
}

// A statement refused for a reason that is neither a grant nor a spelling is
// the connection's own failure, classified as every other capability's is.
func TestAnyOtherRefusalIsClassifiedLikeTheRestOfThePlugin(t *testing.T) {
	verr := unreadable(&mysql.MySQLError{Number: 1045, Message: "Access denied for user"}, sectionReplica,
		parseVersion("8.4.0"), req(t, "mysql.replication.status", map[string]any{"user": "mon"}))
	if verr.Code != "mysql.auth.failed" {
		t.Errorf("code = %q, want mysql.auth.failed", verr.Code)
	}
}
