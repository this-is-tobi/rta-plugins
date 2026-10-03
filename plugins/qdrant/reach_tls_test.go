package main

import (
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// What the server answered at the handshake names the profile too, not the
// end of the forward: a port that closed with the call.
func TestATLSRefusalTheServerGaveNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	cert := &x509.Certificate{DNSNames: []string{"vectors.example.internal"}}
	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"an unknown issuer": {x509.UnknownAuthorityError{}, "qdrant.tls.untrusted"},
		"an expired certificate": {&tls.CertificateVerificationError{Err: x509.CertificateInvalidError{
			Cert: cert, Reason: x509.Expired}}, "qdrant.tls.rejected"},
	} {
		t.Run(name, func(t *testing.T) {
			got := classify(tc.err, req(t, "qdrant.overview", map[string]any{"endpoint": "https://127.0.0.1:41233"}).
				WithProfile("prod", plugin.TunnelKube))
			if got.Code != tc.code {
				t.Fatalf("code = %s, want %s: %s", got.Code, tc.code, got.Message)
			}
			if !strings.Contains(got.Message, "profile prod (through its kube: forward)") ||
				strings.Contains(got.Message, "41233") {
				t.Errorf("message = %q, want the profile and its forward, not the forward's end", got.Message)
			}
		})
	}
}
