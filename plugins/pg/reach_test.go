package main

import (
	"context"
	"strings"
	"testing"

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
