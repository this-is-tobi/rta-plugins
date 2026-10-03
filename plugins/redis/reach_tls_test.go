package main

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// What the server answered at the handshake names the profile too, not the
// end of the forward: a port that closed with the call.
func TestATLSRefusalTheServerGaveNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	cert := &x509.Certificate{DNSNames: []string{"cache.example.internal"}}
	for name, tc := range map[string]struct {
		err    error
		values map[string]any
		code   string
	}{
		"an unknown issuer": {x509.UnknownAuthorityError{}, nil, "redis.tls.untrusted"},
		"another name": {x509.HostnameError{Certificate: cert, Host: "db"},
			map[string]any{"tls-server-name": "db"}, "redis.tls.name"},
		"no name at all": {x509.HostnameError{Certificate: &x509.Certificate{}, Host: "db"},
			map[string]any{"tls-server-name": "db"}, "redis.tls.name"},
		"an expired certificate": {&tls.CertificateVerificationError{Err: x509.CertificateInvalidError{
			Cert: cert, Reason: x509.Expired}}, nil, "redis.tls.rejected"},
		"a hang-up": {io.EOF, nil, "redis.conn.closed"},
	} {
		t.Run(name, func(t *testing.T) {
			values := map[string]any{"address": "127.0.0.1:41233"}
			for k, v := range tc.values {
				values[k] = v
			}
			got := classifyDial(tc.err, "127.0.0.1:41233",
				req(t, "redis.overview", values).WithProfile("prod", plugin.TunnelKube))
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
