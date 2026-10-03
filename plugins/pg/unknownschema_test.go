package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// schemaNameRows yields one text column per name, for the catalogue query that
// lists the schemas a role can see.
type schemaNameRows struct {
	names []string
	at    int
}

func (r *schemaNameRows) Close()                        {}
func (r *schemaNameRows) Err() error                    { return nil }
func (r *schemaNameRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *schemaNameRows) FieldDescriptions() []pgconn.FieldDescription {
	return []pgconn.FieldDescription{{Name: "nspname"}}
}
func (r *schemaNameRows) Next() bool { r.at++; return r.at <= len(r.names) }
func (r *schemaNameRows) Scan(dest ...any) error {
	*(dest[0].(*string)) = r.names[r.at-1]
	return nil
}
func (r *schemaNameRows) Values() ([]any, error) { return []any{r.names[r.at-1]}, nil }
func (r *schemaNameRows) RawValues() [][]byte    { return nil }
func (r *schemaNameRows) Conn() *pgx.Conn        { return nil }

type schemasSeen []string

func (s schemasSeen) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &schemaNameRows{names: s}, nil
}
func (schemasSeen) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

// A schema that is not there is answered as itself wherever its name is typed.
// pg.schema.dump did; pg.table.list answered an empty table, which reads as a
// schema with nothing in it and sends somebody looking for a permissions
// problem that is not there. Both go through one function now, so a wrong name
// cannot read differently in the two.
func TestAWrongSchemaNameIsRefusedAsItselfWhereverItIsTyped(t *testing.T) {
	r := req(t, map[string]any{"database": "app"})

	verr := unknownSchema(t.Context(), schemasSeen{"public", "audit"}, r, "pubic")
	if verr == nil || verr.Code != "pg.schema.missing" {
		t.Fatalf("verr = %+v, want pg.schema.missing", verr)
	}
	if !strings.Contains(verr.Hint, "public, audit") {
		t.Errorf("hint = %q, want the schemas there are", verr.Hint)
	}

	if verr := unknownSchema(t.Context(), schemasSeen{"public"}, r, "public"); verr != nil {
		t.Errorf("a schema that is there was refused: %+v", verr)
	}

	blind := unknownSchema(t.Context(), schemasSeen{}, r, "public")
	if blind == nil || !strings.Contains(blind.Hint, "no schemas at all") {
		t.Errorf("a role that sees none: %+v, want the hint that says so", blind)
	}
}
