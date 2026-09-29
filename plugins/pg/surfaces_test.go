package main

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What the plugin says names a capability and an input the way the surface
// reading it gives them: at a terminal as it always read, and elsewhere as a
// tool and its arguments, a form's box, or the connection setting the
// operator holds — never as a flag or an `rta` command line its reader has
// no terminal for.
func TestWhatItSaysNamesWhatItsSurfaceGives(t *testing.T) {
	refusal := func(verr *view.Error) string { return verr.Message + "\n" + verr.Hint }
	for _, tc := range []struct {
		name, cli, other string
		surface          plugin.Surface
		say              func(sf plugin.Surface) string
	}{
		{
			name:    "a database the server does not have",
			cli:     "`rta pg database list` shows what is there",
			other:   "the `pg_database_list` tool shows what is there",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&pgconn.PgError{Code: "3D000"}, reqFor(t, "pg.status", nil).WithSurface(sf)))
			},
		},
		{
			name:    "nothing listening on a remote host",
			cli:     "`rta net port db.internal --ports 5432` answers the second",
			other:   "`net_port {\"host\":\"db.internal\",\"ports\":5432}` answers the second",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&net.OpError{Op: "dial", Err: errors.New("connect: connection refused")},
					reqFor(t, "pg.status", map[string]any{"host": "db.internal"}).WithSurface(sf)))
			},
		},
		{
			// Where the password comes from, never a verb for the reader: an
			// agent has no host environment to set and no password to pass.
			name:    "credentials the server rejects",
			cli:     "the password is read from $RTA_PG_PASSWORD or --password — check it, and --user:",
			other:   "the password is read from $RTA_PG_PASSWORD or `password` — check it, and `user`:",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&pgconn.PgError{Code: "28P01"}, reqFor(t, "pg.status", nil).WithSurface(sf)))
			},
		},
		{
			name:    "a server that offers no TLS",
			cli:     "--sslmode disable if that is expected on this network",
			other:   "`sslmode` set to disable if that is expected on this network",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(errors.New("server does not support SSL"),
					reqFor(t, "pg.status", nil).WithSurface(sf)))
			},
		},
		{
			name:    "a certificate nothing here trusts",
			cli:     "it belongs in --sslrootcert (a self-signed certificate is its own CA)",
			other:   "it belongs in the operator's `sslrootcert` setting (a self-signed certificate is its own CA)",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(x509.UnknownAuthorityError{}, reqFor(t, "pg.status", nil).WithSurface(sf)))
			},
		},
		{
			name:    "anything else",
			cli:     "`rta explain pg.status` lists every input and where each one can come from",
			other:   "ask the operator to run `rta explain pg.status`, which lists every input",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(errors.New("handshake went sideways"), reqFor(t, "pg.status", nil).WithSurface(sf)))
			},
		},
		{
			name:    "a parallel dump in a format that cannot hold one",
			cli:     "--jobs needs --format directory, not custom",
			other:   "the jobs box needs the format box set to directory, not custom",
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				return refusal(checkParallel(reqFor(t, "pg.dump", map[string]any{"format": "custom", "jobs": 4}).WithSurface(sf)))
			},
		},
		{
			name:    "the restore a dump names",
			cli:     "rta pg restore '/backups/app db.dump' --host db.internal --port 5432",
			other:   `pg.restore file="/backups/app db.dump" host=db.internal port=5432`,
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				return restoreCommand(reqFor(t, "pg.dump", map[string]any{"host": "db.internal"}).WithSurface(sf),
					"/backups/app db.dump")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if said := tc.say(plugin.SurfaceCLI); !strings.Contains(said, tc.cli) {
				t.Errorf("the CLI reads %q, want %q in it", said, tc.cli)
			}
			said := tc.say(tc.surface)
			if !strings.Contains(said, tc.other) {
				t.Errorf("%s reads %q, want %q in it", tc.surface, said, tc.other)
			}
			if strings.Contains(said, tc.cli) {
				t.Errorf("%s reads the CLI's %q", tc.surface, tc.cli)
			}
		})
	}
}

// The one refusal an unqualified table earns over MCP hands the grant it
// needs to the operator, in the one phrase that may carry a command line to
// an agent, and names the listing as the tool the agent calls.
func TestAnUnqualifiedTableAsksTheOperatorForTheGrant(t *testing.T) {
	_, verr := resolveRelation(context.Background(), &recordingQuerier{},
		mcpReqFor(t, "pg.table.dump", map[string]any{"table": "users"}))
	if verr == nil {
		t.Fatal("an unqualified table name was accepted from an MCP caller")
	}
	for _, want := range []string{
		plugin.AskOperator("grant allow pg.table.dump <schema>.users"),
		"the `pg_table_list` tool shows the schema of each table",
	} {
		if !strings.Contains(verr.Hint, want) {
			t.Errorf("hint = %q, want %q in it", verr.Hint, want)
		}
	}
}
