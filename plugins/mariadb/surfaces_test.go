package main

import (
	"context"
	"errors"
	stdnet "net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What the plugin says names a capability and an input the way the surface
// reading it gives them: at a terminal as it always read, and elsewhere as a
// tool, a form's box, or the connection setting the operator holds — never
// as a flag or an `rta` command line its reader has no terminal for.
func TestWhatItSaysNamesWhatItsSurfaceGives(t *testing.T) {
	refusal := func(verr *view.Error) string { return verr.Message + "\n" + verr.Hint }
	for _, tc := range []struct {
		name, cli, other string
		surface          plugin.Surface
		say              func(sf plugin.Surface) string
	}{
		{
			// Where the password comes from, never a verb for the reader: an
			// agent has no host environment to set and no password to pass.
			name:    "credentials the server rejected",
			cli:     "the password is read from $RTA_MARIADB_PASSWORD or --password — check it, and --user",
			other:   "the password is read from $RTA_MARIADB_PASSWORD or `password` — check it, and `user`",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&mysql.MySQLError{Number: 1045, Message: "Access denied"},
					req(t, "mariadb.overview", nil).WithSurface(sf)))
			},
		},
		{
			name:    "a database the server does not have",
			cli:     "`rta mariadb database list` shows what is there",
			other:   "the `mariadb_database_list` tool shows what is there",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&mysql.MySQLError{Number: 1049, Message: "Unknown database"},
					req(t, "mariadb.overview", nil).WithSurface(sf)))
			},
		},
		{
			name:    "nothing listening",
			cli:     "is the server up, and is --host/--port right?",
			other:   "is the server up, and are `host` and `port` right?",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&stdnet.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
					req(t, "mariadb.overview", nil).WithSurface(sf)))
			},
		},
		{
			name:    "a dump with no database",
			cli:     "--database <name> — `rta mariadb database list` shows what is there",
			other:   "the database box set to <name> — `mariadb.database.list` shows what is there",
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				_, err := runDump(context.Background(), req(t, "mariadb.dump", map[string]any{
					"out": filepath.Join(t.TempDir(), "app.sql"),
				}).WithSurface(sf))
				var verr *view.Error
				if !errors.As(err, &verr) {
					t.Fatalf("err = %v, want a refusal", err)
				}
				return refusal(verr)
			},
		},
		{
			name:    "the restore a dump names",
			cli:     "rta mariadb restore /backups/app.sql --host db.internal --port 3306 --user root --database app",
			other:   "mariadb.restore file=/backups/app.sql host=db.internal port=3306 user=root database=app",
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				return restoreCommand(req(t, "mariadb.dump", map[string]any{
					"host": "db.internal", "database": "app",
				}).WithSurface(sf), "/backups/app.sql")
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
