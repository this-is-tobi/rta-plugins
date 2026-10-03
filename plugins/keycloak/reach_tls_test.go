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
	cert := &x509.Certificate{DNSNames: []string{"sso.example.internal"}}
	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"an unknown issuer": {x509.UnknownAuthorityError{}, "keycloak.tls.untrusted"},
		"an expired certificate": {&tls.CertificateVerificationError{Err: x509.CertificateInvalidError{
			Cert: cert, Reason: x509.Expired}}, "keycloak.tls.rejected"},
	} {
		t.Run(name, func(t *testing.T) {
			s := &session{
				req:  req(t, "keycloak.overview", map[string]any{}).WithProfile("lab", plugin.TunnelKube),
				base: "https://127.0.0.1:41233",
			}
			got := s.classifyTransport(tc.err)
			if got.Code != tc.code {
				t.Fatalf("code = %s, want %s: %s", got.Code, tc.code, got.Message)
			}
			if !strings.Contains(got.Message, "profile lab (through its kube: forward)") ||
				strings.Contains(got.Message, "41233") {
				t.Errorf("message = %q, want the profile and its forward, not the forward's end", got.Message)
			}
		})
	}
}
