//go:build livepg

// The TLS settings against a real server that insists on TLS and presents a
// certificate for a name, not for the address it is reached at — what a
// service behind a kube: or ssh: forward looks like, whose end is 127.0.0.1.
// The questions the unit tests cannot answer: that the driver and libpq's
// children check the same name, and verify the same CA.
//
//	TLS=$(pwd)/certs   # ca.crt, and server.crt and server.key for the DNS name pg.test.internal alone
//	docker run -d --rm --name rta-pg-tls -p 127.0.0.1:5498:5432 -e POSTGRES_PASSWORD=lab \
//	  -v "$TLS":/in:ro --entrypoint sh postgres:17 -c '
//	    mkdir -p /certs && cp /in/server.crt /in/server.key /in/ca.crt /in/pg_hba.conf /certs/ &&
//	    chown -R postgres:postgres /certs && chmod 600 /certs/server.key &&
//	    exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/certs/server.crt \
//	      -c ssl_key_file=/certs/server.key -c ssl_ca_file=/certs/ca.crt -c hba_file=/certs/pg_hba.conf'
//	RTA_TEST_PG_TLS_DIR=$TLS RTA_TEST_PG_PORT=5498 RTA_TEST_PG_PASSWORD=lab \
//	  go test . -tags livepg -count=1 -v -run 'TestTLS'
//
// with a pg_hba.conf of `hostnossl all all all reject` and `hostssl all all
// all scram-sha-256`. The certificates are anything a CA issues; the server's
// holds the DNS name pg.test.internal and no address.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

func tlsDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("RTA_TEST_PG_TLS_DIR")
	if dir == "" {
		t.Skip("set RTA_TEST_PG_TLS_DIR to the directory holding ca.crt — setup is the package doc above")
	}
	return dir
}

func tlsValues(t *testing.T, extra map[string]any) map[string]any {
	t.Helper()
	values := map[string]any{"sslmode": "verify-full", "sslrootcert": filepath.Join(tlsDir(t), "ca.crt")}
	for k, v := range extra {
		values[k] = v
	}
	return liveValues(t, values)
}

// encrypted reports whether the connection that asked is on TLS, as the
// server itself says.
func onTLS(t *testing.T, values map[string]any) bool {
	t.Helper()
	ctx := context.Background()
	conn, verr := connect(ctx, reqFor(t, "pg.status", values))
	if verr != nil {
		t.Fatalf("connecting: %s: %s\n%s", verr.Code, verr.Message, verr.Hint)
	}
	defer func() { _ = conn.Close(ctx) }()
	var ssl bool
	if err := conn.QueryRow(ctx, "select ssl from pg_stat_ssl where pid = pg_backend_pid()").Scan(&ssl); err != nil {
		t.Fatal(err)
	}
	return ssl
}

// The certificate is for a name, and the call reaches the server at an
// address: refused for it, with the setting that cures it, until the name is
// given; and then the driver and every child check that name.
func TestTLSServerNameChecksTheNameTheCertificateIsFor(t *testing.T) {
	ctx := context.Background()

	_, verr := connect(ctx, reqFor(t, "pg.status", tlsValues(t, nil)))
	if verr == nil || verr.Code != "pg.tls.name" || !strings.Contains(verr.Hint, "--tls-server-name") {
		t.Fatalf("the address, with no name: %v, want pg.tls.name naming --tls-server-name", verr)
	}

	named := map[string]any{"tls-server-name": "pg.test.internal"}
	if !onTLS(t, tlsValues(t, named)) {
		t.Fatal("verify-full with the certificate's name was not on TLS")
	}

	_, verr = connect(ctx, reqFor(t, "pg.status", tlsValues(t, map[string]any{"tls-server-name": "other.test.internal"})))
	if verr == nil || verr.Code != "pg.tls.name" {
		t.Fatalf("a name the certificate is not for: %v, want pg.tls.name", verr)
	}

	// The children: pg_dump connects with the name as its host and the address
	// as its hostaddr, and psql restores into the same server the same way.
	const src, tgt = "rta_tls_src", "rta_tls_tgt"
	for _, name := range []string{src, tgt} {
		admin(t, "postgres", "drop database if exists "+name)
		admin(t, "postgres", "create database "+name)
		name := name
		t.Cleanup(func() { admin(t, "postgres", "drop database if exists "+name) })
	}
	admin(t, src, "create table orders (id int primary key, note text)")
	admin(t, src, "insert into orders values (1, 'one'), (2, 'two')")

	out := filepath.Join(t.TempDir(), "tls.sql")
	if _, err := runFullDump(ctx, reqFor(t, "pg.dump", tlsValues(t, map[string]any{
		"database": src, "out": out, "format": "plain", "tls-server-name": "pg.test.internal",
	}))); err != nil {
		t.Fatalf("dump: %v", err)
	}
	if _, err := runRestore(ctx, reqFor(t, "pg.restore", tlsValues(t, map[string]any{
		"database": tgt, "file": out, "tls-server-name": "pg.test.internal",
	}))); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if count, _ := rowsIn(t, tgt); count != 2 {
		t.Fatalf("the restore left %d rows, want 2", count)
	}

	// And the children refuse a name the certificate is not for, as the driver
	// does: a dump that connected under a name nothing checked would be the
	// disagreement this is here to prevent.
	_, err := runFullDump(ctx, reqFor(t, "pg.dump", tlsValues(t, map[string]any{
		"database": src, "out": filepath.Join(t.TempDir(), "refused.sql"), "format": "plain",
		"tls-server-name": "other.test.internal",
	})))
	var refused *view.Error
	if !errors.As(err, &refused) {
		t.Fatalf("a dump under a name the certificate is not for = %v, want a refusal", err)
	}
}
