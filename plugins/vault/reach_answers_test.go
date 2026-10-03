package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Every refusal about a Vault that answered names it as its reader reaches it
// again. Through a forward the address is 127.0.0.1 and a port that closed
// with the call, so these name the profile, and the two that are raised
// outside classify, a mount that is not there and a wrap that came back with
// no token, are held to it by a test of their own.
func TestARefusalOutsideClassifyNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	srv, _ := recordingVault(t, map[string]string{
		"/v1/sys/mounts":        mountsBody,
		"/v1/sys/wrapping/wrap": `{"data":{}}`,
	})
	through := func(capID string, values map[string]any) plugin.Request {
		return mountReq(t, capID, srv.URL, values).WithProfile("prod", plugin.TunnelKube)
	}

	_, err := runKVList(context.Background(), through("vault.kv.list", map[string]any{"mount": "homleab"}))
	checkNamesProfile(t, "an unknown mount", err, "vault.kv.mount.unknown", srv.URL)

	_, err = runWrapSet(context.Background(), through("vault.wrap.set", map[string]any{"data": []string{"a=b"}}))
	checkNamesProfile(t, "a wrap that came back with no token", err, "vault.wrap.failed", srv.URL)
}

// What the Vault answered at the handshake names the profile too.
func TestATLSRefusalTheVaultGaveNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"an unknown issuer": {x509.UnknownAuthorityError{}, "vault.tls.untrusted"},
		"an expired certificate": {&tls.CertificateVerificationError{Err: x509.CertificateInvalidError{
			Cert: &x509.Certificate{}, Reason: x509.Expired}}, "vault.tls.rejected"},
	} {
		t.Run(name, func(t *testing.T) {
			r := req(t, "vault.kv.get", map[string]any{"address": "https://127.0.0.1:41233"}).
				WithProfile("prod", plugin.TunnelKube)
			checkNamesProfile(t, name, classify(tc.err, r), tc.code, "41233")
		})
	}
}

func checkNamesProfile(t *testing.T, what string, err error, code, end string) {
	t.Helper()
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != code {
		t.Fatalf("%s: err = %v, want %s", what, err, code)
	}
	if !strings.Contains(verr.Message, "profile prod (through its kube: forward)") || strings.Contains(verr.Message, end) {
		t.Errorf("%s: message = %q, want the profile and its forward, not the forward's end", what, verr.Message)
	}
}
