package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A restore names the server it writes into as its reader reaches it again.
// Through a profile's forward the host and port are the local end of a
// forward that closed when the call did, so a dry run or a receipt naming
// them named a port nothing listens on; the profile is what reaches the same
// server, and beside a direct address it says whose credentials were used.
func TestARestoreNamesTheServerAsTheReaderReachesItAgain(t *testing.T) {
	dir := t.TempDir()
	for _, tool := range restoreTools {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	fixture := filepath.Join(dir, "app.sql")
	if err := os.WriteFile(fixture, []byte("select 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, profile, host string
		port                int
		tunnel              plugin.Tunnel
		want                string
	}{
		{"no profile", "", "db.internal", 3306, plugin.TunnelNone, "app on db.internal:3306"},
		{"a profile reached directly", "prod", "db.internal", 3306, plugin.TunnelNone,
			"app on db.internal:3306 (profile prod)"},
		{"a profile through a forward", "prod", "127.0.0.1", 54321, plugin.TunnelKube,
			"app on profile prod (through its kube: forward)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := plugin.NewRequest(plugin.Resolve(capabilityByID(t, "mariadb.restore"), plugin.Inputs{Caller: map[string]any{
				"host": tc.host, "port": tc.port, "database": "app", "file": fixture,
			}}), true, false).WithProfile(tc.profile, tc.tunnel)
			v, err := runRestore(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if body := v.(view.Text).Body; !strings.Contains(body, tc.want) {
				t.Errorf("dry run = %q, want %q in it", body, tc.want)
			}
		})
	}
}
