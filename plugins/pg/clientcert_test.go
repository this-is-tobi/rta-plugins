package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// pairIn writes a client certificate and its key into dir, the key readable by
// its owner alone as libpq requires, and returns their paths.
func pairIn(t *testing.T, dir, name string) (cert, key string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "postgres"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "postgres"}}, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, key = filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// A client certificate the server could not be shown is refused before
// anything dials, naming the setting that put the file there, instead of a
// connection that failed quoting the whole connection string, or a child that
// libpq refused for what the pre-flight never looked at.
func TestAClientCertificateThatCannotBePresentedIsNamedAsThat(t *testing.T) {
	dir := t.TempDir()
	cert, key := pairIn(t, dir, "good")
	_, otherKey := pairIn(t, dir, "other")
	loose := filepath.Join(dir, "loose.key")
	if err := os.WriteFile(loose, mustRead(t, key), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "legacy.key")
	if err := os.WriteFile(legacy, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY",
		Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00"}, Bytes: []byte("x")}), 0o600); err != nil {
		t.Fatal(err)
	}
	pkcs8 := filepath.Join(dir, "pkcs8.key")
	if err := os.WriteFile(pkcs8, pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("x")}), 0o600); err != nil {
		t.Fatal(err)
	}
	notPEM := filepath.Join(dir, "not.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		values map[string]any
		code   string
		hint   string
	}{
		{"the pair", map[string]any{"sslcert": cert, "sslkey": key}, "", ""},
		{"no key", map[string]any{"sslcert": cert}, "pg.tls.client.pair", "--sslcert and --sslkey go together"},
		{"no certificate", map[string]any{"sslkey": key}, "pg.tls.client.pair", "--sslcert and --sslkey go together"},
		{"a certificate that is not there", map[string]any{"sslcert": filepath.Join(dir, "absent.crt"), "sslkey": key},
			"pg.tls.client.unreadable", "--sslcert names a file on this machine"},
		{"a key that is not there", map[string]any{"sslcert": cert, "sslkey": filepath.Join(dir, "absent.key")},
			"pg.tls.client.unreadable", "--sslkey names a file on this machine"},
		{"a key others can read", map[string]any{"sslcert": cert, "sslkey": loose}, "pg.tls.client.key.perms",
			"`chmod 600` on the file that --sslkey names"},
		{"a key under a passphrase, legacy", map[string]any{"sslcert": cert, "sslkey": legacy},
			"pg.tls.client.key.encrypted", "--sslkey wants a key without one"},
		{"a key under a passphrase, PKCS#8", map[string]any{"sslcert": cert, "sslkey": pkcs8},
			"pg.tls.client.key.encrypted", "--sslkey wants a key without one"},
		{"a certificate that is no certificate", map[string]any{"sslcert": notPEM, "sslkey": key},
			"pg.tls.client.invalid", "--sslcert wants a PEM certificate"},
		{"a key for another certificate", map[string]any{"sslcert": cert, "sslkey": otherKey},
			"pg.tls.client.invalid", "the PEM private key that certificate was issued for"},
		{"disable reads none of it", map[string]any{"sslmode": "disable", "sslcert": filepath.Join(dir, "absent.crt")}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.code == "pg.tls.client.key.perms" {
				t.Skip("a key's mode is not a thing on Windows")
			}
			tc.values["sslmode"] = valueOr(tc.values["sslmode"], "verify-ca")
			tc.values["sslrootcert"] = cert
			verr := checkClientPair(reqFor(t, "pg.status", tc.values))
			switch {
			case tc.code == "" && verr != nil:
				t.Errorf("refused: %s: %s", verr.Code, verr.Message)
			case tc.code != "" && (verr == nil || verr.Code != tc.code || !strings.Contains(verr.Hint, tc.hint)):
				t.Errorf("got %v, want %s naming %q", verr, tc.code, tc.hint)
			}
		})
	}
}

func valueOr(v any, fallback string) any {
	if v == nil {
		return fallback
	}
	return v
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// **Nothing is read from the home directory unless asked.** libpq looks for a
// client certificate, its key and a root certificate under ~/.postgresql, and
// pgx for the same under the home the password database gives; with nothing
// named, the two found them their own ways, a root.crt nobody named turned
// sslmode=require into a verifying connection, and a postgresql.crt nobody
// named was presented to any server. Here a home holds all three, and a call
// that names none uses none, for the driver and for every child alike.
func TestNothingIsFoundUnderTheHomeDirectoryUnlessAskedFor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", home)
	dir := filepath.Join(home, ".postgresql")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(home, "postgresql")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cert, key := pairIn(t, dir, "postgresql")
	root := filepath.Join(dir, "root.crt")
	if err := os.WriteFile(root, mustRead(t, cert), 0o644); err != nil {
		t.Fatal(err)
	}

	values := map[string]any{"sslmode": "verify-ca"}
	driver, child := dsn(reqFor(t, "pg.dump", values)), childEnv(reqFor(t, "pg.dump", values))
	for _, file := range []string{root, cert, key} {
		if strings.Contains(driver, file) {
			t.Errorf("the driver was handed %s, which no setting named: %s", file, driver)
		}
		if slices.ContainsFunc(child, func(kv string) bool { return strings.HasSuffix(kv, "="+file) }) {
			t.Errorf("a child was handed %s, which no setting named: %v", file, child)
		}
	}
	if r := checkRootCert(reqFor(t, "pg.dump", values)); r == nil || r.Code != "pg.tls.ca.missing" {
		t.Errorf("verify-ca found a CA nobody named: %v", r)
	}

	// Asked for, rta finds them once and hands the same ones to both.
	values["ssl-home"] = true
	driver, child = dsn(reqFor(t, "pg.dump", values)), childEnv(reqFor(t, "pg.dump", values))
	for name, file := range map[string]string{"sslrootcert": root, "sslcert": cert, "sslkey": key} {
		if !strings.Contains(driver, name+"='"+file+"'") {
			t.Errorf("ssl-home: the driver lacks %s=%s: %s", name, file, driver)
		}
		if !slices.Contains(child, "PG"+strings.ToUpper(name)+"="+file) {
			t.Errorf("ssl-home: the child lacks PG%s=%s: %v", strings.ToUpper(name), file, child)
		}
	}
	if verr := checkTransport(reqFor(t, "pg.dump", values)); verr != nil {
		t.Errorf("the files ssl-home found were refused: %s: %s", verr.Code, verr.Message)
	}

	// A file a setting names wins over the one found for it.
	named, namedKey := pairIn(t, t.TempDir(), "named")
	values["sslcert"], values["sslkey"] = named, namedKey
	if driver := dsn(reqFor(t, "pg.dump", values)); !strings.Contains(driver, "sslcert='"+named+"'") ||
		strings.Contains(driver, cert) {
		t.Errorf("a named pair did not win over the one found: %s", driver)
	}

	// And nothing is found under disable, which reads none of it.
	values["sslmode"] = "disable"
	if driver := dsn(reqFor(t, "pg.dump", values)); strings.Contains(driver, dir) || strings.Contains(driver, named) {
		t.Errorf("disable reached for a file: %s", driver)
	}
}

// The driver and every child are handed the same files: whatever the driver
// reads, the child reads, and where the driver reads none the child is told
// the path that is not there.
func TestTheDriverAndTheChildrenReadTheSameFiles(t *testing.T) {
	dir := t.TempDir()
	cert, key := pairIn(t, dir, "good")
	for name, values := range map[string]map[string]any{
		"none":       {},
		"a CA":       {"sslmode": "verify-full", "sslrootcert": cert},
		"the pair":   {"sslmode": "require", "sslcert": cert, "sslkey": key},
		"all three":  {"sslmode": "verify-ca", "sslrootcert": cert, "sslcert": cert, "sslkey": key},
		"disabled":   {"sslmode": "disable", "sslrootcert": cert, "sslcert": cert, "sslkey": key},
		"the system": {"sslmode": "verify-full"},
	} {
		t.Run(name, func(t *testing.T) {
			r := reqFor(t, "pg.dump", values)
			driver, child := dsn(r), childEnv(r)
			for keyword, variable := range map[string]string{
				"sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY"} {
				var read string
				if _, after, ok := strings.Cut(driver, keyword+"='"); ok {
					read, _, _ = strings.Cut(after, "'")
				}
				var handed string
				for _, kv := range child {
					if after, ok := strings.CutPrefix(kv, variable+"="); ok {
						handed = after
					}
				}
				switch {
				case read == "" && values["sslmode"] == "verify-full" && keyword == "sslrootcert":
					if handed != "system" {
						t.Errorf("%s: the driver checks this machine's CAs and the child is handed %q", keyword, handed)
					}
				case read == "":
					if handed != unreadable {
						t.Errorf("%s: the driver reads none and the child is handed %q", keyword, handed)
					}
				case read != handed:
					t.Errorf("%s: the driver reads %q and the child %q", keyword, read, handed)
				}
			}
		})
	}
}

// The restore line a dump prints presents the same certificate, by the
// settings that named it, and asks for the same search when it was ssl-home
// that found it: a restore without the certificate the server asked this dump
// for is refused, and one that searches the home of the shell it is pasted
// into presents whatever it finds there.
func TestTheRestoreLineCarriesTheClientCertificate(t *testing.T) {
	dir := t.TempDir()
	cert, key := pairIn(t, dir, "good")
	line := restoreCommand(reqFor(t, "pg.dump", map[string]any{"host": "10.0.0.5", "sslmode": "verify-ca",
		"sslrootcert": cert, "sslcert": cert, "sslkey": key}), "/backups/app.dump")
	for _, want := range []string{"--sslcert " + cert, "--sslkey " + key} {
		if !strings.Contains(line, want) {
			t.Errorf("restore line = %q, want %q in it", line, want)
		}
	}
	line = restoreCommand(reqFor(t, "pg.dump", map[string]any{"host": "10.0.0.5", "ssl-home": true}), "/backups/app.dump")
	if !strings.Contains(line, "--ssl-home") {
		t.Errorf("restore line = %q, want --ssl-home", line)
	}
	if line := restoreCommand(reqFor(t, "pg.dump", map[string]any{"host": "10.0.0.5", "sslmode": "disable",
		"sslcert": cert, "sslkey": key}), "/backups/app.dump"); strings.Contains(line, "--sslcert") {
		t.Errorf("restore line = %q presents a certificate to a connection with no TLS", line)
	}
	line = restoreCommand(reqFor(t, "pg.dump", map[string]any{"host": "127.0.0.1", "sslcert": cert, "sslkey": key}).
		WithProfile("prod", plugin.TunnelKube), "/backups/app.dump")
	for _, want := range []string{"--profile prod", "--sslcert " + cert, "--sslkey " + key} {
		if !strings.Contains(line, want) {
			t.Errorf("through a forward: restore line = %q, want %q in it", line, want)
		}
	}
}

// The createdb a missing restore target offers connects the way the restore
// did: createdb has no flag for any of it, so the assignments ride the line,
// each one word as a shell reads it.
func TestTheCreatedbLineConnectsTheWayTheRestoreDid(t *testing.T) {
	dir := t.TempDir()
	cert, key := pairIn(t, dir, "good")
	r := reqFor(t, "pg.restore", map[string]any{"database": "prod", "host": "10.0.0.5", "user": "app",
		"sslmode": "verify-full", "sslrootcert": cert, "sslcert": cert, "sslkey": key, "tls-server-name": "db.internal"})
	hint := createdbHint(r)
	for _, want := range []string{"PGSSLMODE=verify-full", "PGSSLROOTCERT=" + cert, "PGSSLCERT=" + cert, "PGSSLKEY=" + key,
		"PGHOSTADDR=10.0.0.5", "createdb --host=db.internal --port=5432 --username=app prod"} {
		if !strings.Contains(hint, want) {
			t.Errorf("createdb line = %q, want %q in it", hint, want)
		}
	}
	if plain := createdbHint(reqFor(t, "pg.restore", map[string]any{"database": "prod", "host": "10.0.0.5", "user": "app"})); strings.Contains(plain, "PG") {
		t.Errorf("createdb line = %q sets what the restore did not use", plain)
	}
}

// A server that wants a client certificate answers a connection without a good
// one with the same FATAL 28000 a rejected password gets, and the reader was
// sent to check a password nothing had looked at. By the server's own words,
// each is named as what it is, with the setting that changes it.
func TestAServerThatWantsAClientCertificateIsNotAPasswordRefused(t *testing.T) {
	required := &pgconn.PgError{Code: "28000", Message: "connection requires a valid client certificate"}
	role := &pgconn.PgError{Code: "28000", Message: `certificate authentication failed for user "app"`}
	dir := t.TempDir()
	cert, key := pairIn(t, dir, "good")

	none := classify(required, reqFor(t, "pg.status", map[string]any{"host": "db.internal"}))
	if none.Code != "pg.tls.client.required" || !strings.Contains(none.Message, "none was presented") ||
		!strings.Contains(none.Hint, "--sslcert and --sslkey name the certificate") ||
		strings.Contains(none.Hint, "password") {
		t.Errorf("none presented: %s %q %q", none.Code, none.Message, none.Hint)
	}
	for sf, want := range map[plugin.Surface]string{
		plugin.SurfaceTUI: "the sslcert and sslkey boxes name the certificate",
		plugin.SurfaceMCP: "the operator's `sslcert` and `sslkey` settings name the certificate",
	} {
		got := classify(required, reqFor(t, "pg.status", map[string]any{}).WithSurface(sf))
		if !strings.Contains(got.Hint, want) {
			t.Errorf("%s: hint = %q, want %q", sf, got.Hint, want)
		}
	}
	presented := classify(required, reqFor(t, "pg.status", map[string]any{"sslcert": cert, "sslkey": key}))
	if presented.Code != "pg.tls.client.required" || !strings.Contains(presented.Message, "refused the client certificate "+cert) ||
		!strings.Contains(presented.Hint, "ssl_ca_file") {
		t.Errorf("presented, refused: %s %q %q", presented.Code, presented.Message, presented.Hint)
	}
	if got := classify(role, reqFor(t, "pg.status", map[string]any{"user": "app"})); got.Code != "pg.tls.client.role" ||
		!strings.Contains(got.Hint, "common name is the role it logs in as") {
		t.Errorf("another role: %s %q", got.Code, got.Hint)
	}
	alert := errors.New("failed to receive message: remote error: tls: unknown certificate authority")
	if got := classify(alert, reqFor(t, "pg.status", map[string]any{})); got.Code != "pg.tls.client.required" {
		t.Errorf("a TLS alert about the certificate: %s, want pg.tls.client.required", got.Code)
	}
	if got := classify(&pgconn.PgError{Code: "28P01", Message: "password authentication failed"},
		reqFor(t, "pg.status", map[string]any{})); got.Code != "pg.auth.failed" {
		t.Errorf("a rejected password: %s, want pg.auth.failed", got.Code)
	}
}

// The children say it in libpq's words, pinned to the C locale (childEnv). The
// stderr here is what psql 18 printed against a postgres:17 asking for client
// certificates, with the files each case describes.
func TestWhatLibpqSaysAboutTheTLSFilesIsNamedAsThat(t *testing.T) {
	r := reqFor(t, "pg.restore", map[string]any{"host": "127.0.0.1", "sslmode": "verify-ca",
		"sslrootcert": "/etc/pg/ca.crt", "sslcert": "/etc/pg/client.crt", "sslkey": "/etc/pg/client.key"})
	for _, tc := range []struct {
		name, stderr, code, hint string
	}{
		{"no certificate", `psql: error: connection to server at "127.0.0.1", port 55417 failed: FATAL:  connection requires a valid client certificate`,
			"pg.tls.client.required", "ssl_ca_file"},
		{"a certificate no CA of the server's issued", `psql: error: connection to server at "127.0.0.1", port 55417 failed: SSL error: tlsv1 alert unknown ca`,
			"pg.tls.client.required", "ssl_ca_file"},
		{"a key others can read", `psql: error: connection to server at "127.0.0.1", port 55417 failed: private key file "/etc/pg/client.key" has group or world access; file must have permissions u=rw (0600) or less if owned by the current user, or permissions u=rw,g=r (0640) or less if owned by root`,
			"pg.tls.client.key.perms", "`chmod 600` on the file that --sslkey names"},
		{"a root certificate that is not there", "psql: error: connection to server at \"127.0.0.1\", port 55417 failed: root certificate file \"/etc/pg/ca.crt\" does not exist\nEither provide the file, use the system's trusted roots with sslrootcert=system, or change sslmode to disable server certificate verification.",
			"pg.tls.ca.unreadable", "--sslrootcert names a file on this machine"},
		{"system, to a libpq that does not know it", "psql: error: connection to server at \"127.0.0.1\", port 55417 failed: root certificate file \"system\" does not exist\nEither provide the file or change sslmode to disable server certificate verification.",
			"pg.tls.ca.system", "older than 16"},
		{"a key that is no key", `psql: error: connection to server at "127.0.0.1", port 55417 failed: could not load private key file "/etc/pg/client.key": unsupported`,
			"pg.tls.client.invalid", "--sslkey wants the PEM private key"},
		{"a CA that did not issue the server's certificate", `psql: error: connection to server at "127.0.0.1", port 55417 failed: SSL error: certificate verify failed`,
			"pg.tls.untrusted", "--sslrootcert"},
		{"a name the certificate is not for", `psql: error: connection to server at "127.0.0.1", port 55417 failed: server certificate for "pg.test.internal" (and 1 other name) does not match host name "127.0.0.1"`,
			"pg.tls.name", "--tls-server-name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dump := classifyDump(errors.New("exit status 2"), tc.stderr, r)
			restore := classifyRestore(errors.New("exit status 2"), tc.stderr, r, formatPlain, versions{})
			for what, got := range map[string][2]string{"dump": {dump.Code, dump.Hint}, "restore": {restore.Code, restore.Hint}} {
				if got[0] != tc.code || !strings.Contains(got[1], tc.hint) {
					t.Errorf("%s: %s %q, want %s naming %q", what, got[0], got[1], tc.code, tc.hint)
				}
			}
		})
	}
}

// The new settings are the operator's, as every other that names a file or a
// destination is: an agent that could choose a path would be reading files off
// this machine.
func TestTheTLSFileSettingsAreTheOperators(t *testing.T) {
	for _, name := range []string{"sslcert", "sslkey", "ssl-home", "tls-server-name", "sslrootcert"} {
		found := false
		for _, f := range connFields() {
			if f.Name != name {
				continue
			}
			found = true
			if !f.Local || f.Config != name || f.TLSAdjacent {
				t.Errorf("%s: Local %v, Config %q, TLSAdjacent %v; want Local, configurable and not TLSAdjacent",
					name, f.Local, f.Config, f.TLSAdjacent)
			}
		}
		if !found {
			t.Errorf("no %s input", name)
		}
	}
}
