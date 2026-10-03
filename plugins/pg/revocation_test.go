package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ssl-home asks for libpq's own files, and libpq reads a revocation list there
// that nothing in this plugin can honour: pgx has no way to read one, and the
// children are pointed away from it so that they agree with the pre-flight. A
// connection that verified the server and accepted a certificate the list
// revokes, with no word, is refused before anything dials. A list nobody asked
// for, or one beside a mode that verifies nothing, is left alone.
func TestARevocationListSSLHomeWouldFindIsRefusedWhereTheServerIsVerified(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("libpq's directory is under %APPDATA% there")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".postgresql")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "root.crl")
	if err := os.WriteFile(list, []byte("-----BEGIN X509 CRL-----\n-----END X509 CRL-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ca := testCA(t)

	for _, mode := range []string{"verify-ca", "verify-full"} {
		verr := checkTransport(reqFor(t, "pg.status", map[string]any{"sslmode": mode, "sslrootcert": ca, "ssl-home": true}))
		if verr == nil || verr.Code != "pg.tls.crl.unsupported" || !strings.Contains(verr.Message, list) ||
			!strings.Contains(verr.Hint, "drop --ssl-home and name the files with --sslrootcert, --sslcert and --sslkey") {
			t.Errorf("%s with a list there: %v, want pg.tls.crl.unsupported naming the file and the way out", mode, verr)
		}
	}
	for name, values := range map[string]map[string]any{
		"ssl-home off":    {"sslmode": "verify-full", "sslrootcert": ca},
		"nothing checked": {"sslmode": "prefer", "ssl-home": true},
		"plaintext":       {"sslmode": "disable", "ssl-home": true},
	} {
		if verr := checkRevocation(reqFor(t, "pg.status", values)); verr != nil {
			t.Errorf("%s: %s: %s, want it left alone", name, verr.Code, verr.Message)
		}
	}
}
