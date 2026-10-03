package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A restore, and the schema description that keeps its header, name the
// server as the reader reaches it again. Through a kube: or ssh: profile the
// host and port are the local end of a forward that closed when the call did,
// so "restoring into prod on 127.0.0.1:54321" named a port nothing listens
// on; the profile is what reaches the same server, and beside a direct
// address it says whose credentials were used.
func TestWhatIsWrittenAboutAServerNamesItAsTheReaderReachesItAgain(t *testing.T) {
	needsRestoreTools(t)
	path := plainFixture(t)
	for _, tc := range []struct {
		name, profile, host string
		port                int
		tunnel              plugin.Tunnel
		want                string
	}{
		{"no profile", "", "db.internal", 6432, plugin.TunnelNone, "prod on db.internal:6432"},
		{"a profile reached directly", "prod", "db.internal", 6432, plugin.TunnelNone,
			"prod on db.internal:6432 (profile prod)"},
		{"a profile through a forward", "prod", "127.0.0.1", 54321, plugin.TunnelKube,
			"prod on profile prod (through its kube: forward)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]any{"file": path, "database": "prod", "host": tc.host, "port": tc.port}
			v, err := runRestore(context.Background(),
				dryRunReqFor(t, "pg.restore", values).WithProfile(tc.profile, tc.tunnel))
			if err != nil {
				t.Fatal(err)
			}
			if body := v.(view.Text).Body; !strings.Contains(body, tc.want) {
				t.Errorf("restore dry run = %q, want %q in it", body, tc.want)
			}

			header := renderDDL(reqFor(t, "pg.schema.dump", map[string]any{
				"database": "prod", "host": tc.host, "port": tc.port,
			}).WithProfile(tc.profile, tc.tunnel), "public", nil, dropped{})
			if want := `schema "public" of ` + tc.want; !strings.Contains(header, want) {
				t.Errorf("schema header = %q, want %q in it", firstLines(header, 1), want)
			}
		})
	}
}

// A refusal the server gave names the server as the reader reaches it again.
// Through a forward the address the driver dialled is 127.0.0.1 and a port that
// closed with the call, and "127.0.0.1:54321 rejected the credentials" named
// nothing the reader could change.
func TestARefusalTheServerGaveNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	values := map[string]any{"host": "127.0.0.1", "port": 54321, "user": "app", "database": "shop"}
	for name, pgErr := range map[string]*pgconn.PgError{
		"credentials": {Code: "28P01", Message: "password authentication failed"},
		"no database": {Code: "3D000", Message: "database does not exist"},
		"certificate": {Code: "28000", Message: "connection requires a valid client certificate"},
		"wrong role":  {Code: "28000", Message: `certificate authentication failed for user "app"`},
	} {
		t.Run(name, func(t *testing.T) {
			req := reqFor(t, "pg.status", values).WithProfile("prod", plugin.TunnelKube)
			got := classify(pgErr, req)
			if !strings.Contains(got.Message, "profile prod (through its kube: forward)") ||
				strings.Contains(got.Message, "54321") {
				t.Errorf("message = %q, want the profile and its forward, not the forward's end", got.Message)
			}
		})
	}
}
