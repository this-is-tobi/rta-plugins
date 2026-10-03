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
//
// The client-certificate tests want a second server, started the same way, with
// a pg_hba.conf of `hostnossl all all all reject` and `hostssl all all all cert
// clientcert=verify-full` (the role is the certificate's CN), on
// RTA_TEST_PG_CERT_PORT, and in the same directory: client.crt and client.key
// issued by the CA the server trusts for clients (ssl_ca_file), other-client.crt
// and other-client.key issued by another, and client-enc.key, the first key
// under a passphrase:
//
//	RTA_TEST_PG_TLS_DIR=$TLS RTA_TEST_PG_PORT=5498 RTA_TEST_PG_PASSWORD=lab \
//	  RTA_TEST_PG_CERT_PORT=5497 go test . -tags livepg -count=1 -v -run 'TestTLS'
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
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

// clientCertValues connects to the server that asks every TCP client for a
// certificate (RTA_TEST_PG_CERT_PORT, pg_hba.conf's `hostssl all all all cert
// clientcert=verify-full`) as the role the certificate is for, with no
// password: the certificate is the credential.
func clientCertValues(t *testing.T, extra map[string]any) map[string]any {
	t.Helper()
	port := os.Getenv("RTA_TEST_PG_CERT_PORT")
	if port == "" {
		t.Skip("set RTA_TEST_PG_CERT_PORT to a server that asks for a client certificate — setup is the package doc above")
	}
	dir := tlsDir(t)
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("RTA_TEST_PG_CERT_PORT = %q, want a port", port)
	}
	values := map[string]any{
		"host": "127.0.0.1", "port": p, "user": "postgres", "password": "",
		"sslmode": "verify-ca", "sslrootcert": filepath.Join(dir, "ca.crt"),
		"sslcert": filepath.Join(dir, "client.crt"), "sslkey": filepath.Join(dir, "client.key"),
	}
	for k, v := range extra {
		values[k] = v
	}
	return values
}

// certExec runs one statement against the named database of the server that
// asks for client certificates, presenting the certificate.
func certExec(t *testing.T, database, sql string, extra map[string]any) {
	t.Helper()
	ctx := context.Background()
	values := clientCertValues(t, extra)
	values["database"] = database
	conn, verr := connect(ctx, reqFor(t, "pg.status", values))
	if verr != nil {
		t.Fatalf("connecting to %s: %s: %s", database, verr.Code, verr.Message)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// A server that asks every client for a certificate (clientcert=verify-full)
// is reached by presenting one: the driver's connection and the children a
// dump and a restore run both present the file the settings name, and nothing
// else. A call that presents none, or one a CA the server trusts did not
// issue, is named as that and not as a password to check; and ssl-home finds
// the files libpq would, once, for both.
func TestTLSClientCertificateIsPresentedByTheDriverAndTheChildren(t *testing.T) {
	ctx := context.Background()
	dir := tlsDir(t)

	conn, verr := connect(ctx, reqFor(t, "pg.status", clientCertValues(t, nil)))
	if verr != nil {
		t.Fatalf("the certificate was refused: %s: %s\n%s", verr.Code, verr.Message, verr.Hint)
	}
	var dn *string
	if err := conn.QueryRow(ctx, "select client_dn from pg_stat_ssl where pid = pg_backend_pid()").Scan(&dn); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)
	if dn == nil || !strings.Contains(*dn, "CN=postgres") {
		t.Fatalf("the server saw the client certificate %v, want CN=postgres", dn)
	}

	// pg.query itself, the capability an agent reaches the server through:
	// the rows come back over the certificate.
	for _, c := range Plugin().Capabilities {
		if c.ID != "pg.query" {
			continue
		}
		q := plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: clientCertValues(t, map[string]any{
			"sql": "select current_user as who, (select client_dn from pg_stat_ssl where pid = pg_backend_pid()) as dn",
		})}), false, false)
		v, err := c.Run(ctx, q)
		if err != nil {
			t.Fatalf("pg.query over the client certificate: %v", err)
		}
		table, ok := v.(view.Table)
		if !ok || len(table.Rows) != 1 || table.Rows[0][0] != "postgres" || !strings.Contains(table.Rows[0][1], "CN=postgres") {
			t.Fatalf("pg.query answered %#v, want the role and the certificate the server saw", v)
		}
	}

	for name, extra := range map[string]map[string]any{
		"none":     {"sslcert": "", "sslkey": ""},
		"other CA": {"sslcert": filepath.Join(dir, "other-client.crt"), "sslkey": filepath.Join(dir, "other-client.key")},
	} {
		_, verr := connect(ctx, reqFor(t, "pg.status", clientCertValues(t, extra)))
		if verr == nil || verr.Code != "pg.tls.client.required" || strings.Contains(verr.Hint, "password") {
			t.Errorf("%s: %v, want pg.tls.client.required naming the certificate", name, verr)
		}
	}
	_, verr = connect(ctx, reqFor(t, "pg.status", clientCertValues(t,
		map[string]any{"sslkey": filepath.Join(dir, "client-enc.key")})))
	if verr == nil || verr.Code != "pg.tls.client.key.encrypted" {
		t.Errorf("a key under a passphrase: %v, want pg.tls.client.key.encrypted", verr)
	}

	const src, tgt = "rta_cert_src", "rta_cert_tgt"
	for _, name := range []string{src, tgt} {
		certExec(t, "postgres", "drop database if exists "+name, nil)
		certExec(t, "postgres", "create database "+name, nil)
		name := name
		t.Cleanup(func() { certExec(t, "postgres", "drop database if exists "+name, nil) })
	}
	certExec(t, src, "create table orders (id int primary key, note text)", nil)
	certExec(t, src, "insert into orders values (1, 'one'), (2, 'two'), (3, 'three')", nil)
	for _, format := range []string{"plain", "custom"} {
		out := filepath.Join(t.TempDir(), "cert-"+format+backupSuffix(format))
		if _, err := runFullDump(ctx, reqFor(t, "pg.dump", clientCertValues(t,
			map[string]any{"database": src, "out": out, "format": format}))); err != nil {
			t.Fatalf("%s dump: %v", format, err)
		}
		certExec(t, tgt, "drop schema public cascade; create schema public", nil)
		if _, err := runRestore(ctx, reqFor(t, "pg.restore", clientCertValues(t,
			map[string]any{"database": tgt, "file": out}))); err != nil {
			t.Fatalf("%s restore: %v", format, err)
		}
		verify, verr := connect(ctx, reqFor(t, "pg.status", clientCertValues(t, map[string]any{"database": tgt})))
		if verr != nil {
			t.Fatal(verr)
		}
		var count int
		err := verify.QueryRow(ctx, "select count(*) from orders").Scan(&count)
		_ = verify.Close(ctx)
		if err != nil || count != 3 {
			t.Fatalf("%s: the restore left %d rows (%v), want 3", format, count, err)
		}
	}

	// A dump that presents none is refused at the pre-flight, naming the
	// setting, before a child is ever run.
	_, err := runFullDump(ctx, reqFor(t, "pg.dump", clientCertValues(t, map[string]any{
		"database": src, "out": filepath.Join(t.TempDir(), "none.sql"), "format": "plain",
		"sslcert": "", "sslkey": "",
	})))
	var refused *view.Error
	if !errors.As(err, &refused) || refused.Code != "pg.tls.client.required" {
		t.Fatalf("a dump presenting no certificate = %v, want pg.tls.client.required", err)
	}
}

// ssl-home is libpq's search, done once: with the files under ~/.postgresql and
// none named, the call is refused for presenting nothing, and with ssl-home it
// presents the pair found there to the driver and to the children alike.
func TestTLSHomeFindsWhatLibpqWouldAndBothClientsAgree(t *testing.T) {
	ctx := context.Background()
	dir := tlsDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	libpq := filepath.Join(home, ".postgresql")
	if err := os.MkdirAll(libpq, 0o700); err != nil {
		t.Fatal(err)
	}
	for from, to := range map[string]string{"ca.crt": "root.crt", "client.crt": "postgresql.crt", "client.key": "postgresql.key"} {
		data, err := os.ReadFile(filepath.Join(dir, from))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(libpq, to), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bare := map[string]any{"sslmode": "verify-ca", "sslrootcert": "", "sslcert": "", "sslkey": ""}

	_, verr := connect(ctx, reqFor(t, "pg.status", clientCertValues(t, bare)))
	if verr == nil || verr.Code != "pg.tls.ca.missing" {
		t.Fatalf("verify-ca with the files only under the home directory: %v, want pg.tls.ca.missing", verr)
	}
	_, verr = connect(ctx, reqFor(t, "pg.status", clientCertValues(t,
		map[string]any{"sslmode": "require", "sslrootcert": "", "sslcert": "", "sslkey": ""})))
	if verr == nil || verr.Code != "pg.tls.client.required" {
		t.Fatalf("require with the files only under the home directory: %v, want pg.tls.client.required", verr)
	}

	home2 := map[string]any{"sslmode": "verify-ca", "sslrootcert": "", "sslcert": "", "sslkey": "", "ssl-home": true}
	conn, verr := connect(ctx, reqFor(t, "pg.status", clientCertValues(t, home2)))
	if verr != nil {
		t.Fatalf("ssl-home: %s: %s\n%s", verr.Code, verr.Message, verr.Hint)
	}
	_ = conn.Close(ctx)

	out := filepath.Join(t.TempDir(), "home.sql")
	home2["database"], home2["out"], home2["format"] = "postgres", out, "plain"
	if _, err := runFullDump(ctx, reqFor(t, "pg.dump", clientCertValues(t, home2))); err != nil {
		t.Fatalf("ssl-home: the child did not find what the driver did: %v", err)
	}
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
