package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A certificate macOS refuses for its validity period is not one with dates
// to check: the rule and its fix are what the reader is owed. The rule is
// macOS's, so elsewhere the system's words are all there is.
func TestACertificateAppleRefusesForItsLengthSaysSo(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the validity rule is the system verifier's on macOS")
	}
	open, closing := string(rune(0x201c)), string(rune(0x201d))
	leaf := &x509.Certificate{
		DNSNames:  []string{"db.internal"},
		NotBefore: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:  time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	got := classify(&tls.CertificateVerificationError{
		UnverifiedCertificates: []*x509.Certificate{leaf},
		Err:                    errors.New("x509: " + open + "db" + closing + " certificate is not standards compliant"),
	}, req(t, map[string]any{"host": "db.internal", "port": 5432}))
	if got.Code != "pg.tls.rejected" || !strings.Contains(got.Hint, "825 days") {
		t.Errorf("got %s %q, want pg.tls.rejected naming the 825-day rule", got.Code, got.Hint)
	}
}
