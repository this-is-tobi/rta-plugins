package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A dump or a restore connects twice, and a kube: forward with TLS on it ends
// when the first connection closes, so the tool's was refused as "connection
// refused" after the pre-flight had answered. Measured against a real
// kubectl port-forward to a PostgreSQL 17 with TLS, and said before either
// connects. A forward with no TLS on it, an ssh: tunnel and a direct
// connection are not what that is about.
func TestADumpOrARestoreOverTLSThroughAKubeForwardIsRefused(t *testing.T) {
	needsRestoreTools(t)
	dump := filepath.Join(t.TempDir(), "app.dump")
	fixture := plainFixture(t)
	ca := testCA(t)
	run := map[string]func(plugin.Request) error{
		"pg.dump": func(r plugin.Request) error { _, err := runFullDump(context.Background(), r); return err },
		"pg.restore": func(r plugin.Request) error {
			_, err := runRestore(context.Background(), r)
			return err
		},
	}
	for id, do := range run {
		for _, tc := range []struct {
			name    string
			extra   map[string]any
			tunnel  plugin.Tunnel
			refused bool
		}{
			{"a name through a kube forward", map[string]any{"tls-server-name": "db.internal"}, plugin.TunnelKube, true},
			{"a CA through a kube forward", map[string]any{"sslrootcert": ca}, plugin.TunnelKube, true},
			{"nothing asking for TLS through a kube forward", nil, plugin.TunnelKube, false},
			{"a name through an ssh tunnel", map[string]any{"tls-server-name": "db.internal"}, plugin.TunnelSSH, false},
		} {
			t.Run(id+"/"+tc.name, func(t *testing.T) {
				values := map[string]any{"host": "127.0.0.1", "port": 54321, "database": "app", "sslmode": "disable"}
				if id == "pg.dump" {
					values["out"] = dump
				} else {
					values["file"] = fixture
				}
				for k, v := range tc.extra {
					values[k] = v
				}
				err := do(dryRunReqFor(t, id, values).WithProfile("prod", tc.tunnel))
				var verr *view.Error
				got := errors.As(err, &verr) && verr.Code == "pg.tls.client.forward"
				if got != tc.refused {
					t.Fatalf("err = %v, refused = %v, want %v", err, got, tc.refused)
				}
				if tc.refused && (!strings.Contains(verr.Message, "profile prod (through its kube: forward)") ||
					!strings.Contains(verr.Hint, "ends when the first TLS connection closes")) {
					t.Errorf("refusal = %q, hint %q", verr.Message, verr.Hint)
				}
			})
		}
	}
}
