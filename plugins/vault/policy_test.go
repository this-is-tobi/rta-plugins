package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A certificate macOS refuses for its validity period is not one with dates
// to check: the rule and its fix are what the reader is owed, and the hint
// naming the dates, the use and the issuer sent them past it. The rule is
// macOS's, so elsewhere the system's words are all there is.
func TestACertificateAppleRefusesForItsLengthSaysSo(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the validity rule is the system verifier's on macOS")
	}
	open, closing := string(rune(0x201c)), string(rune(0x201d))
	leaf := &x509.Certificate{
		NotBefore: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:  time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	err := &url.Error{Op: "Get", URL: "https://vault.internal:8200/", Err: &tls.CertificateVerificationError{
		UnverifiedCertificates: []*x509.Certificate{leaf},
		Err:                    errors.New("x509: " + open + "vault" + closing + " certificate is not standards compliant"),
	}}
	got := classify(err, req(t, "vault.seal.status", map[string]any{"address": "https://vault.internal:8200"}))
	if got.Code != "vault.tls.rejected" || !strings.Contains(got.Hint, "825 days") {
		t.Errorf("got %s %q, want vault.tls.rejected naming the 825-day rule", got.Code, got.Hint)
	}
}
