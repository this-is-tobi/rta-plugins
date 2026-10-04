package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Given a CA, MariaDB's client from 11.4 checks the name, which verify-ca
// exists not to; the pin replaces the chain check's whole spelling, the CA
// included, with the fingerprint alone.
func TestThePinReplacesTheChainCheckSpelling(t *testing.T) {
	args := dumpArgs(req(t, "mariadb.dump", map[string]any{
		"database": "app", "tls": "verify-ca", "ca-file": "/etc/mysql/ca.pem",
	}))
	joined := strings.Join(pinned(args, "ab12"), " ")
	if !strings.Contains(joined, "--ssl --ssl-fp=ab12") || strings.Contains(joined, "--ssl-ca") ||
		strings.Contains(joined, "verify-server-cert") {
		t.Errorf("pinned argv = %q, want --ssl --ssl-fp=ab12 and no CA or verify flag", joined)
	}
}

// The pre-flight records the certificate whose chain it verified against
// ca-file, the one the child is then pinned to, and records nothing for a
// mode that verifies no chain of its own.
func TestThePreflightRecordsTheCertificateItVerified(t *testing.T) {
	ca := newPrivateCA(t)
	cert := ca.certFor(t, "MariaDB_Server_Auto_Generated_Server_Certificate", nil)
	port := tlsServer(t, cert)
	db, fp, verr := connectPinned(context.Background(), req(t, "mariadb.status", map[string]any{
		"host": "127.0.0.1", "port": port, "tls": "verify-ca", "ca-file": writeFile(t, "ca.pem", ca.pem),
	}))
	if verr != nil {
		t.Fatalf("verify-ca was refused: %s: %s", verr.Code, verr.Message)
	}
	_ = db.Close()
	sum := sha256.Sum256(cert.Certificate[0])
	if want := hex.EncodeToString(sum[:]); fp != want {
		t.Errorf("pin = %q, want the served certificate's %q", fp, want)
	}
	db, fp, verr = connectPinned(context.Background(), req(t, "mariadb.status", map[string]any{
		"host": "127.0.0.1", "port": port, "tls": "skip-verify",
	}))
	if verr != nil {
		t.Fatalf("skip-verify was refused: %s: %s", verr.Code, verr.Message)
	}
	_ = db.Close()
	if fp != "" {
		t.Errorf("skip-verify recorded a pin, %q, for a chain nothing checked", fp)
	}
}

// Whether the client takes the pin is read off its own --help: one that
// lists --ssl-fp is pinned, one that does not keeps the chain check, and the
// dry run shows which, naming the fingerprint it cannot know yet as what it
// will be.
func TestTheDryRunShowsWhichSpellingTheClientTakes(t *testing.T) {
	ca := writeFile(t, "ca.pem", newPrivateCA(t).pem)
	fixture := writeFile(t, "app.sql", []byte("select 1;\n"))
	for client, tc := range map[string]struct{ help, want, without string }{
		"11.4":  {"  --ssl-fp=name      Server certificate fingerprint", "--ssl --ssl-fp=" + pinPending, "--ssl-ca"},
		"10.11": {"  --ssl-verify-server-cert", "--ssl-ca=" + ca + " --skip-ssl-verify-server-cert", "--ssl-fp"},
	} {
		dir := t.TempDir()
		for _, tool := range []string{"mariadb-dump", "mariadb"} {
			script := "#!/bin/sh\necho '" + tc.help + "'\n"
			if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", dir)
		dry := func(id string, values map[string]any) plugin.Request {
			values["database"], values["tls"], values["ca-file"] = "app", "verify-ca", ca
			return plugin.NewRequest(plugin.Resolve(capabilityByID(t, id), plugin.Inputs{Caller: values}), true, false)
		}
		for name, run := range map[string]func() (view.View, error){
			"dump": func() (view.View, error) {
				return runDump(context.Background(), dry("mariadb.dump", map[string]any{"out": filepath.Join(dir, "app.sql")}))
			},
			"restore": func() (view.View, error) {
				return runRestore(context.Background(), dry("mariadb.restore", map[string]any{"file": fixture}))
			},
		} {
			v, err := run()
			if err != nil {
				t.Fatalf("client %s, %s: %v", client, name, err)
			}
			body := v.(view.Text).Body
			if !strings.Contains(body, tc.want) || strings.Contains(body, tc.without) {
				t.Errorf("client %s, %s dry run: %q, want %q and no %q", client, name, body, tc.want, tc.without)
			}
		}
	}
}

// A pinned child that meets another certificate says so — reissued in
// between, or something else answering — rather than passing the client's
// line through as a dump that failed.
func TestAPinnedChildRefusingTheCertificateIsNamed(t *testing.T) {
	stderr := `mariadb-dump: Got error: 2026: "TLS/SSL error: Fingerprint validation of peer certificate failed" ` +
		"when trying to connect\n"
	r := req(t, "mariadb.dump", map[string]any{"database": "app"})
	for name, got := range map[string]*view.Error{
		"dump":    classifyDump(errors.New("exit status 2"), stderr, r),
		"restore": classifyRestore(errors.New("exit status 1"), stderr, r),
	} {
		if got.Code != "mariadb.tls.changed" || !strings.Contains(got.Hint, "--ca-file") {
			t.Errorf("%s: %s (%s), want mariadb.tls.changed naming --ca-file", name, got.Code, got.Hint)
		}
	}
}
