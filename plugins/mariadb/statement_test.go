package main

import (
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A statement the server refuses is about the statement. Every number classify
// did not know fell to a hint that sent the reader to the connection's
// settings, so a typo in the SQL told an agent to ask the operator which
// settings exist — and the refusal it is certain to meet, a write inside the
// READ ONLY transaction, was left to the server's own words.
func TestAStatementTheServerRefusesIsNotBlamedOnTheConnection(t *testing.T) {
	r := req(t, "mariadb.query", map[string]any{"sql": "selec 1"}).WithSurface(plugin.SurfaceMCP)
	for _, tc := range []struct {
		name   string
		number uint16
		code   string
		hint   string
	}{
		{"a syntax error", 1064, "mariadb.query.failed", "the server rejected the statement as written"},
		{"an unknown column", 1054, "mariadb.query.failed", "what it says above is what to fix"},
		{"a write", 1792, "mariadb.query.readonly", "READ ONLY transaction"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := statementFailure(&mysql.MySQLError{Number: tc.number, Message: "server text"}, r)
			if got.Code != tc.code {
				t.Errorf("code = %q, want %q", got.Code, tc.code)
			}
			if !strings.Contains(got.Hint, tc.hint) {
				t.Errorf("hint = %q, want %q in it", got.Hint, tc.hint)
			}
			if strings.Contains(got.Hint, "rta explain") || strings.Contains(got.Hint, "every setting") {
				t.Errorf("hint = %q sends the reader to the connection's settings for a statement's error", got.Hint)
			}
		})
	}

	// The same number met away from a statement is still the generic answer:
	// what a server that answered oddly needs is its settings.
	away := classify(&mysql.MySQLError{Number: 1064, Message: "server text"}, r)
	if !strings.Contains(away.Hint, "every setting") {
		t.Errorf("hint = %q, want the settings page outside a statement", away.Hint)
	}
}

// No database selected is the one statement error with a way out: the table
// can be named with its database. The refusal before the call and the one
// from the server are one code with one hint, so they cannot send a reader to
// different places, and the setting is the operator's, never an argument.
func TestNoDatabaseIsOneRefusalWhereverItIsNoticed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		surface plugin.Surface
		inputs  string
		setting string
	}{
		{"a terminal", plugin.SurfaceCLI, "--schema", "--database"},
		{"an agent", plugin.SurfaceMCP, `the "schema" argument`, "the operator's `database` setting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t, "mariadb.schema", map[string]any{}).WithSurface(tc.surface)
			_, before := schemaOf(r)
			if before == nil || before.Code != "mariadb.schema.unset" {
				t.Fatalf("before the call: %+v, want mariadb.schema.unset", before)
			}
			if !strings.Contains(before.Hint, tc.inputs) || !strings.Contains(before.Hint, tc.setting) {
				t.Errorf("hint = %q, want %q and %q in it", before.Hint, tc.inputs, tc.setting)
			}
			if strings.HasSuffix(before.Message, "named") {
				t.Errorf("message = %q reads as a sentence that stops", before.Message)
			}

			fromServer := classify(&mysql.MySQLError{Number: 1046, Message: "No database selected"}, r)
			if fromServer.Code != "mariadb.schema.unset" {
				t.Fatalf("from the server: code = %q, want mariadb.schema.unset", fromServer.Code)
			}
			if !strings.Contains(fromServer.Hint, "database.table") || !strings.Contains(fromServer.Hint, tc.setting) {
				t.Errorf("hint = %q, want the qualified name and %q", fromServer.Hint, tc.setting)
			}
		})
	}
}
