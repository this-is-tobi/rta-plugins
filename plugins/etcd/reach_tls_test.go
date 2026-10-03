package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A deadline, a listener that speaks no etcd and a snapshot refused to a user
// who is not root are as much the profile's server as the answers above, and
// each quoted the end of the forward, a port that closed with the call.
func TestATimeoutAProtocolMismatchAndASnapshotDenialNameTheProfileNotTheEndOfItsForward(t *testing.T) {
	r := req(t, "etcd.snapshot", map[string]any{"endpoint": "127.0.0.1:41233"}).WithProfile("prod", plugin.TunnelKube)
	for name, got := range map[string]*view.Error{
		"a deadline":        classify(context.DeadlineExceeded, r),
		"a status deadline": classify(status.Error(codes.DeadlineExceeded, "slow"), r),
		"no connection":     noConnection(r),
		"a wrong port":      wrongPort(r, target{}),
		"a snapshot denied": classifySnapshot(status.Error(codes.PermissionDenied, "etcdserver: permission denied"), r),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(got.Message, "profile prod (through its kube: forward)") ||
				strings.Contains(got.Message, "41233") {
				t.Errorf("%s: message = %q, want the profile and its forward, not the forward's end", got.Code, got.Message)
			}
		})
	}
}

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
