package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A root certificate ssl-home found is held to the rules a named one is, and
// its refusals name the setting that put it there: beside prefer a CA verifies
// nothing in pgx and everything in libpq's children, which retry in plaintext
// when it fails, so it is refused there as an explicit one is; and a file that
// cannot be read is the home directory's to fix, not sslrootcert's, which
// names nothing.
func TestARootCertificateSSLHomeFoundIsHeldToTheSameRulesAndNamesSSLHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("libpq's directory is under %APPDATA% there")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".postgresql")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root.crt")
	if err := os.WriteFile(root, mustRead(t, testCA(t)), 0o644); err != nil {
		t.Fatal(err)
	}

	prefer := checkRootCert(reqFor(t, "pg.status", map[string]any{"sslmode": "prefer", "ssl-home": true}))
	if prefer == nil || prefer.Code != "pg.tls.ca.unused" || !strings.Contains(prefer.Message, "--ssl-home") {
		t.Errorf("prefer beside a found CA: %v, want pg.tls.ca.unused naming --ssl-home", prefer)
	}
	if got := checkRootCert(reqFor(t, "pg.status", map[string]any{"sslmode": "verify-ca", "ssl-home": true})); got != nil {
		t.Errorf("verify-ca with the CA found under the home directory was refused: %s: %s", got.Code, got.Message)
	}

	if err := os.WriteFile(root, []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	invalid := checkRootCert(reqFor(t, "pg.status", map[string]any{"sslmode": "verify-ca", "ssl-home": true}))
	if invalid == nil || invalid.Code != "pg.tls.ca.invalid" || !strings.Contains(invalid.Hint, "--ssl-home") ||
		strings.Contains(invalid.Hint, "--sslrootcert") {
		t.Errorf("a found file that is no CA: %v, want pg.tls.ca.invalid naming --ssl-home", invalid)
	}
}
