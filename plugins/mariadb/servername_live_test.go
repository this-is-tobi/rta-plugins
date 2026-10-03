package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Against a real server that takes connections over TLS only
// (require_secure_transport) with a certificate for db.internal, reached as a
// forward's end would be, by its address. Run by hand: RTA_TEST_MARIADB_TLS_PORT
// is the port, RTA_TEST_MARIADB_TLS_DIR holds the CA as ca.pem, and
// RTA_TEST_MARIADB_PASSWORD is the root password. Skipped otherwise, as the other
// live tests are.
func TestLiveAServerThatInsistsOnTLSIsReachedThroughAForward(t *testing.T) {
	port, err := strconv.Atoi(os.Getenv("RTA_TEST_MARIADB_TLS_PORT"))
	dir := os.Getenv("RTA_TEST_MARIADB_TLS_DIR")
	if err != nil || dir == "" {
		t.Skip("RTA_TEST_MARIADB_TLS_PORT and RTA_TEST_MARIADB_TLS_DIR name no server")
	}
	through := func(extra map[string]any) plugin.Request {
		values := map[string]any{
			"host": "127.0.0.1", "port": port, "user": "root", "password": os.Getenv("RTA_TEST_MARIADB_PASSWORD"),
			"tls": "false",
		}
		for k, v := range extra {
			values[k] = v
		}
		return req(t, "mariadb.status", values).WithProfile("prod", plugin.TunnelKube)
	}
	ca := filepath.Join(dir, "ca.pem")

	if _, verr := connect(context.Background(), through(nil)); verr == nil || verr.Code != "mariadb.tls.required" {
		t.Fatalf("with nothing asking for TLS: %v, want mariadb.tls.required", verr)
	}
	if _, verr := connect(context.Background(), through(map[string]any{"ca-file": ca})); verr == nil ||
		verr.Code != "mariadb.tls.forward" {
		t.Fatalf("with a CA and no name: %v, want mariadb.tls.forward", verr)
	}
	if _, verr := connect(context.Background(), through(map[string]any{
		"ca-file": ca, "tls-server-name": "other.internal"})); verr == nil || verr.Code != "mariadb.tls.name" {
		t.Fatalf("with a name the certificate is not for: %v, want mariadb.tls.name", verr)
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
}
