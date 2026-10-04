package main

import (
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"
)

// A certificate the verifier refuses for a reason of its own, a date it is
// not valid on among them, was "could not reach" a server that had answered
// with it, and the page of every input for a hint. It is named for why, in
// the verifier's words, beside what a certificate is checked for.
func TestACertificateThatDoesNotVerifyIsNamedForWhy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]any
		want   string
	}{
		{"checked for the address's host", nil, "the host in --address"},
		{"checked for the name given", map[string]any{"tls-server-name": "cache.internal"},
			"the name in --tls-server-name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDial(&tls.CertificateVerificationError{
				Err: x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired}},
				"cache.internal:6379", req(t, "redis.overview", tc.values))
			if got.Code != "redis.tls.rejected" || !strings.Contains(got.Message, "expired") ||
				!strings.Contains(got.Hint, tc.want) {
				t.Errorf("got %s %q %q, want redis.tls.rejected naming %q", got.Code, got.Message, got.Hint, tc.want)
			}
		})
	}
	untrusted := classifyDial(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
		"cache.internal:6379", req(t, "redis.overview", nil))
	if untrusted.Code != "redis.tls.untrusted" {
		t.Errorf("an issuer nothing vouches for: %s, want redis.tls.untrusted", untrusted.Code)
	}
}
