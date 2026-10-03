package main

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The forward turns tls off, and a CA or a name to check turns TLS back on over
// it, at true: the one mode that checks a name, which a forward's end never
// is. Nothing else on the line says TLS, so without this a server that insists
// on it was unreachable through a profile at all.
func TestAServerNameOrACATurnsTLSOnOverAForward(t *testing.T) {
	ca := writeFile(t, "ca.pem", newPrivateCA(t).pem)
	for _, tc := range []struct {
		name   string
		extra  map[string]any
		tunnel plugin.Tunnel
		want   string
	}{
		{"nothing asks for TLS", nil, plugin.TunnelKube, "false"},
		{"a name", map[string]any{"tls-server-name": "db.internal"}, plugin.TunnelKube, "true"},
		{"a CA", map[string]any{"ca-file": ca}, plugin.TunnelKube, "true"},
		{"a name through an ssh forward", map[string]any{"tls-server-name": "db.internal"}, plugin.TunnelSSH, "true"},
		{"a CA, direct and off", map[string]any{"ca-file": ca}, plugin.TunnelNone, "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]any{"host": "127.0.0.1", "port": 54321, "tls": "false"}
			for k, v := range tc.extra {
				values[k] = v
			}
			r := req(t, "mysql.status", values).WithProfile("prod", tc.tunnel)
			if got := tlsMode(r); got != tc.want {
				t.Errorf("tlsMode = %q, want %q", got, tc.want)
			}
		})
	}
}

// A name beside a connection that would not check it is refused, since a name
// given is an operator expecting a check: beside false the call goes in the
// clear, and beside the modes below true no name is checked at all.
func TestAServerNameBesideAConnectionThatChecksNoNameIsRefused(t *testing.T) {
	ca := writeFile(t, "ca.pem", newPrivateCA(t).pem)
	for _, tc := range []struct {
		mode, code string
	}{
		{"false", "mysql.tls.name.plaintext"},
		{"preferred", "mysql.tls.name.unchecked"},
		{"skip-verify", "mysql.tls.name.unchecked"},
		{"verify-ca", "mysql.tls.name.unchecked"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			_, verr := tlsConfig(req(t, "mysql.status", map[string]any{
				"host": "10.0.0.5", "tls": tc.mode, "ca-file": ca, "tls-server-name": "db.internal",
			}))
			if verr == nil || verr.Code != tc.code {
				t.Fatalf("err = %v, want %s", verr, tc.code)
			}
		})
	}
}

// The whole path: a server whose certificate is for db.internal alone, at the
// address a forward's end would be. Given the name, the handshake verifies;
// without it the certificate is refused for 127.0.0.1 and the refusal sends the
// reader to the setting that cures it; a name the certificate is not for is
// the certificate's to explain.
func TestTheDriverChecksTheCertificateForTheNameGiven(t *testing.T) {
	ca := newPrivateCA(t)
	caFile := writeFile(t, "ca.pem", ca.pem)
	port := tlsServer(t, ca.certFor(t, "db.internal", nil, "db.internal"))
	through := func(extra map[string]any) plugin.Request {
		values := map[string]any{"host": "127.0.0.1", "port": port, "tls": "false", "ca-file": caFile}
		for k, v := range extra {
			values[k] = v
		}
		return req(t, "mysql.status", values).WithProfile("prod", plugin.TunnelKube)
	}

	_, verr := connect(context.Background(), through(nil))
	if verr == nil || verr.Code != "mysql.tls.forward" {
		t.Fatalf("through a forward with no name: %v, want mysql.tls.forward", verr)
	}
	if !strings.Contains(verr.Message, "profile prod (through its kube: forward) is for db.internal, not for 127.0.0.1") ||
		!strings.Contains(verr.Hint, "--tls-server-name") || strings.Contains(verr.Hint, "--tls ") {
		t.Errorf("refusal = %q, hint %q, want the profile, the name the certificate is for and tls-server-name",
			verr.Message, verr.Hint)
	}

	db, verr := connect(context.Background(), through(map[string]any{"tls-server-name": "db.internal"}))
	if verr != nil {
		t.Fatalf("the name the certificate is for was refused: %s: %s", verr.Code, verr.Message)
	}
	_ = db.Close()

	_, verr = connect(context.Background(), through(map[string]any{"tls-server-name": "other.internal"}))
	if verr == nil || verr.Code != "mysql.tls.name" || !strings.Contains(verr.Hint, "--tls-server-name is the name") ||
		!strings.Contains(verr.Message, "not other.internal") {
		t.Errorf("a name the certificate is not for: %v, want mysql.tls.name naming the setting and the name", verr)
	}

	// No CA beside the name: the certificate is checked against this machine's
	// own store, and a private CA's is not in it.
	_, verr = connect(context.Background(), through(map[string]any{"ca-file": "", "tls-server-name": "db.internal"}))
	if verr == nil || verr.Code != "mysql.tls.untrusted" {
		t.Errorf("a name with no CA: %v, want mysql.tls.untrusted", verr)
	}
}

// The MySQL client checks a certificate for the host it dials and no other name,
// so a dump or a restore handed a name to check is refused before it runs, in
// the dry run too. A kube: forward is not a reason to refuse, which was measured.
func TestADumpOrARestoreThatCouldNotCheckTheCertificateIsRefused(t *testing.T) {
	ca := writeFile(t, "ca.pem", newPrivateCA(t).pem)
	dry := func(id string, values map[string]any, tunnel plugin.Tunnel) plugin.Request {
		values["database"] = "app"
		switch id {
		case "mysql.dump":
			values["out"] = filepath.Join(t.TempDir(), "app.sql")
		default:
			values["file"] = writeFile(t, "app.sql", []byte("select 1;\n"))
		}
		return plugin.NewRequest(plugin.Resolve(capabilityByID(t, id), plugin.Inputs{Caller: values}), true, false).
			WithProfile("prod", tunnel)
	}
	run := map[string]func(plugin.Request) error{
		"mysql.dump":    func(r plugin.Request) error { _, err := runDump(context.Background(), r); return err },
		"mysql.restore": func(r plugin.Request) error { _, err := runRestore(context.Background(), r); return err },
	}
	for id, do := range run {
		for _, tc := range []struct {
			name, code string
			values     map[string]any
			tunnel     plugin.Tunnel
		}{
			{"a name", "mysql.tls.client.name", map[string]any{"tls": "true", "ca-file": ca, "tls-server-name": "db.internal"},
				plugin.TunnelNone},
			{"a name through a kube forward", "mysql.tls.client.name",
				map[string]any{"tls": "false", "ca-file": ca, "tls-server-name": "db.internal"}, plugin.TunnelKube},
			// Measured: a kube forward serves a TLS connection's close and the next, so the child has
			// one, and a CA with no name is checked by both for the forward's end.
			{"a CA through a kube forward", "", map[string]any{"tls": "false", "ca-file": ca}, plugin.TunnelKube},
		} {
			t.Run(id+"/"+tc.name, func(t *testing.T) {
				var verr *view.Error
				err := do(dry(id, tc.values, tc.tunnel))
				switch refused := errors.As(err, &verr) && strings.HasPrefix(verr.Code, "mysql.tls.client."); {
				case tc.code == "" && refused:
					t.Fatalf("err = %v, want it left to run", err)
				case tc.code != "" && (!refused || verr.Code != tc.code):
					t.Fatalf("err = %v, want %s", err, tc.code)
				}
			})
		}
	}
}

// What turned TLS on goes on the restore line a dump prints, and on every
// call a message hands over: the line pasted where the profile's config is
// absent would otherwise connect without it.
func TestTheLineACallHandsOverCarriesWhatTurnedTLSOn(t *testing.T) {
	ca := writeFile(t, "ca.pem", newPrivateCA(t).pem)
	port := strconv.Itoa(54321)
	for _, tc := range []struct {
		name   string
		values map[string]any
		tunnel plugin.Tunnel
		want   []string
		not    []string
	}{
		{"through a forward", map[string]any{"ca-file": ca, "tls-server-name": "db.internal", "tls": "false"},
			plugin.TunnelKube, []string{"--profile prod", "--ca-file " + ca, "--tls-server-name db.internal"},
			[]string{"--tls ", "--host", port}},
		{"direct", map[string]any{"ca-file": ca, "tls-server-name": "db.internal", "tls": "true"},
			plugin.TunnelNone, []string{"--tls true", "--ca-file " + ca, "--tls-server-name db.internal"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.values["host"], tc.values["port"] = "127.0.0.1", 54321
			got := restoreCommand(req(t, "mysql.dump", tc.values).WithProfile("prod", tc.tunnel), "/backups/app.sql")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("line = %q, want %q", got, w)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("line = %q names %q", got, n)
				}
			}
		})
	}
}
