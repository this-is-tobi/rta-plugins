package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"runtime"
	"strings"
	"testing"
)

// A certificate that names no host is read as one that would be refused by
// name the moment its chain was trusted, and answered with verify-ca, which
// checks the chain against a CA file without a name. On macOS the system's
// verifier answers when no ca-file is named, and says "revoked" untyped; a
// ca-file runs Go's verifier in its place, which checks no revocation, so a
// revoked certificate answered that way was a server the check had caught,
// reached by the operator who followed the hint. It keeps the system's
// words, with no way round.
func TestARevokedCertificateWithNoNamesIsNotAnsweredWithTheModeThatChecksNoRevocation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("a revoked verdict reaches Go untyped only from macOS's own verifier")
	}
	open, closing := string(rune(0x201c)), string(rune(0x201d))
	err := &tls.CertificateVerificationError{
		UnverifiedCertificates: []*x509.Certificate{{}},
		Err:                    errors.New("x509: " + open + "mysql" + closing + " certificate is revoked"),
	}
	got := classify(err, req(t, "mysql.overview", map[string]any{"host": "db.internal"}))
	if got.Code == "mysql.tls.name" || strings.Contains(got.Hint, "verify-ca") {
		t.Errorf("got %s %q %q, want the system's verdict with no way round it", got.Code, got.Message, got.Hint)
	}
	if !strings.Contains(got.Message, "certificate is revoked") {
		t.Errorf("message %q does not quote the verdict", got.Message)
	}
}
