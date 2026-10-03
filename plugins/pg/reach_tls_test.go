package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/this-is-tobi/rta/pkg/plugin"
)

// What the server answered at the TLS handshake names the profile too: its
// certificate, and its refusal of a connection without TLS. The local end of
// a forward is a port that closed with the call.
func TestATLSRefusalTheServerGaveNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	cert := &x509.Certificate{DNSNames: []string{"svc.example.internal"}}
	for name, tc := range map[string]struct {
		err    error
		values map[string]any
		code   string
	}{
		"an unknown issuer": {
			&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, nil, "pg.tls.untrusted"},
		"a certificate for another name": {
			&tls.CertificateVerificationError{Err: x509.HostnameError{Certificate: cert, Host: "db"}},
			map[string]any{"tls-server-name": "db"}, "pg.tls.name"},
		"an expired certificate": {
			&tls.CertificateVerificationError{Err: x509.CertificateInvalidError{
				Cert: cert, Reason: x509.Expired, Detail: time.Time{}.String()}}, nil, "pg.tls.rejected"},
		"no TLS offered": {errors.New("server does not support SSL, but SSL was required"), nil, "pg.tls.unsupported"},
		"TLS required":   {&pgconn.PgError{Code: "28000", Message: "no pg_hba.conf entry, no encryption"}, nil, "pg.tls.required"},
	} {
		t.Run(name, func(t *testing.T) {
			values := map[string]any{"host": "127.0.0.1", "port": 54321, "sslmode": "disable"}
			for k, v := range tc.values {
				values[k] = v
			}
			got := classify(tc.err, reqFor(t, "pg.status", values).WithProfile("prod", plugin.TunnelKube))
			if got.Code != tc.code {
				t.Fatalf("code = %s, want %s: %s", got.Code, tc.code, got.Message)
			}
			if !strings.Contains(got.Message, "profile prod (through its kube: forward)") ||
				strings.Contains(got.Message, "54321") {
				t.Errorf("message = %q, want the profile and its forward, not the forward's end", got.Message)
			}
		})
	}
}
