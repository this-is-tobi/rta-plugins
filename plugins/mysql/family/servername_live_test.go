package family

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Against a real server that takes connections over TLS only
// (require_secure_transport) with a certificate for db.internal, reached as a
// forward's end would be, by its address. Run by hand, per engine:
// RTA_TEST_MYSQL_TLS_PORT is the port, RTA_TEST_MYSQL_TLS_DIR holds the CA as
// ca.pem, and RTA_TEST_MYSQL_PASSWORD is the root password — RTA_TEST_MARIADB_*
// for a MariaDB server. Skipped otherwise, as the other live tests are.
func TestLiveAServerThatInsistsOnTLSIsReachedThroughAForward(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		env := func(name string) string { return os.Getenv("RTA_TEST_" + strings.ToUpper(engine.NS) + "_" + name) }
		port, err := strconv.Atoi(env("TLS_PORT"))
		dir := env("TLS_DIR")
		if err != nil || dir == "" {
			t.Skipf("no %s server: RTA_TEST_%s_TLS_PORT and _TLS_DIR name none", engine.Product, strings.ToUpper(engine.NS))
		}
		through := func(extra map[string]any) plugin.Request {
			values := map[string]any{
				"host": "127.0.0.1", "port": port, "user": "root", "password": env("PASSWORD"),
				"tls": "false",
			}
			for k, v := range extra {
				values[k] = v
			}
			return req(t, engine.NS+".status", values).WithProfile("prod", plugin.TunnelKube)
		}
		ca := filepath.Join(dir, "ca.pem")

		if _, verr := connect(context.Background(), through(nil)); verr == nil || verr.Code != engine.NS+".tls.required" {
			t.Fatalf("with nothing asking for TLS: %v, want %s.tls.required", verr, engine.NS)
		}
		if _, verr := connect(context.Background(), through(map[string]any{"ca-file": ca})); verr == nil ||
			verr.Code != engine.NS+".tls.forward" {
			t.Fatalf("with a CA and no name: %v, want %s.tls.forward", verr, engine.NS)
		}
		if _, verr := connect(context.Background(), through(map[string]any{
			"ca-file": ca, "tls-server-name": "other.internal"})); verr == nil || verr.Code != engine.NS+".tls.name" {
			t.Fatalf("with a name the certificate is not for: %v, want %s.tls.name", verr, engine.NS)
		}

		db, verr := connect(context.Background(), through(map[string]any{"ca-file": ca, "tls-server-name": "db.internal"}))
		if verr != nil {
			t.Fatalf("with the CA and the name: %s: %s", verr.Code, verr.Message)
		}
		defer func() { _ = db.Close() }()
		var cipher, value string
		if err := db.QueryRowContext(context.Background(), "SHOW SESSION STATUS LIKE 'Ssl_cipher'").Scan(&cipher, &value); err != nil {
			t.Fatal(err)
		}
		if value == "" {
			t.Errorf("the connection that the name and CA opened is not over TLS: Ssl_cipher is empty")
		}
	})
}
