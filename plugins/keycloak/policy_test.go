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

	"github.com/this-is-tobi/rta/pkg/plugin"
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
	s := &session{req: plugin.NewRequest(nil, false, false), base: "https://sso.internal"}
	got := s.classifyTransport(&url.Error{Op: "Post", URL: "https://sso.internal/realms/demo/protocol/openid-connect/token",
		Err: &tls.CertificateVerificationError{
			UnverifiedCertificates: []*x509.Certificate{leaf},
			Err:                    errors.New("x509: " + open + "sso" + closing + " certificate is not standards compliant"),
		}})
	if got.Code != "keycloak.tls.rejected" || !strings.Contains(got.Hint, "825 days") {
		t.Errorf("got %s %q, want keycloak.tls.rejected naming the 825-day rule", got.Code, got.Hint)
	}
}
