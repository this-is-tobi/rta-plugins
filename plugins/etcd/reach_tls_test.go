package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// What a member answered at the handshake, or with Unavailable, names the
// profile too: the end of a forward is a port that closed with the call.
func TestAnAnswerTheMemberGaveAtTheHandshakeNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	r := req(t, "etcd.overview", map[string]any{"endpoint": "127.0.0.1:41233"}).WithProfile("prod", plugin.TunnelKube)
	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"unavailable": {status.Error(codes.Unavailable, "etcdserver: no leader"), "etcd.unavailable"},
		"an unknown issuer": {&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
			"etcd.tls.untrusted"},
		"another verdict": {&tls.CertificateVerificationError{Err: x509.CertificateInvalidError{
			Cert: &x509.Certificate{}, Reason: x509.Expired}}, "etcd.tls.rejected"},
		"a failed handshake": {handshakeError{errors.New("EOF")}, "etcd.tls.failed"},
	} {
		t.Run(name, func(t *testing.T) {
			got := classify(tc.err, r)
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
