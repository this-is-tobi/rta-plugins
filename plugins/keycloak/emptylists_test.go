package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// An empty listing is still its table (docs: writing a plugin): a program reads
// zero rows under the columns it knows, and a person is told what the nothing
// is, where a bare grid of headings reads like a listing that failed.
func TestAnEmptyListingIsItsTable(t *testing.T) {
	f := newFakeKeycloak(t)
	f.bodies = map[string][]byte{
		"/clients":              []byte("[]"),
		"/users":                []byte("[]"),
		"/events":               []byte("[]"),
		"/admin-events":         []byte("[]"),
		"/roles":                []byte("[]"),
		"/client-session-stats": []byte("[]"),
		"/authentication/flows": []byte("[]"),
	}
	for id, sentence := range map[string]string{
		"keycloak.client.list":  "no clients",
		"keycloak.user.list":    "no users",
		"keycloak.event.list":   "no events",
		"keycloak.event.admin":  "no admin events",
		"keycloak.role.list":    "no roles",
		"keycloak.session.list": "no active sessions",
		"keycloak.flow.list":    "no flows",
	} {
		t.Run(id, func(t *testing.T) {
			v := run(t, f, id, map[string]any{})
			tbl, ok := v.(view.Table)
			if !ok {
				t.Fatalf("an empty answer is a %T, want the table", v)
			}
			if len(tbl.Rows) != 0 || tbl.Total != 0 || len(tbl.Columns) == 0 {
				t.Errorf("table = %d columns, %d rows, total %d, want its columns and no rows",
					len(tbl.Columns), len(tbl.Rows), tbl.Total)
			}
			if tbl.Empty != sentence {
				t.Errorf("empty sentence = %q, want %q", tbl.Empty, sentence)
			}
			raw, err := view.Marshal(view.Envelope{View: v})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"rows":[]`) || !strings.Contains(string(raw), `"type":"table"`) {
				t.Errorf("an agent reads %s, want a table with rows []", raw)
			}
		})
	}
}

// The sessions of one user or one client are the same listing, and say so.
func TestAnEmptySessionListOfOneUserOrClientIsItsTable(t *testing.T) {
	if got := sessionTable(nil); got.Empty != "no sessions" || len(got.Rows) != 0 || len(got.Columns) == 0 {
		t.Errorf("sessionTable(nil) = %+v, want its columns, no rows and the sentence", got)
	}
}
