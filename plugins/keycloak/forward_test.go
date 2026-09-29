package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A Keycloak behind a kube: or ssh: forward is dialled at the forward's end,
// 127.0.0.1, and its certificate is for the name it answers as, which it
// rarely is. These run the real client against a server with a certificate
// for svc.example.internal alone, as a cluster service's is, dialled at
// 127.0.0.1 the way a forward's end is.

// keycloakBehindAForward issues a token to every client over TLS on
// 127.0.0.1 with a certificate for svc.example.internal, issued by a CA
// written to a file whose path it returns beside the URL's host and port.
func keycloakBehindAForward(t *testing.T) (hostport, caFile string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rta test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "svc.example.internal"},
		DNSNames:  []string{"svc.example.internal"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"t"}`))
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host, caFile
}

// mint connects the way every capability here does, minting the run's token,
// and returns its refusal, or nil when a token was issued.
func mint(t *testing.T, values map[string]any, tunnel plugin.Tunnel) *view.Error {
	t.Helper()
	values["client-secret"] = "s"
	_, verr := connect(context.Background(), req(t, "keycloak.overview", values).WithProfile("lab", tunnel))
	return verr
}

// Through a forward, a certificate for the server's own name is refused as
// one checked for the forward's end, with the name it is for and the setting
// that checks that name instead; given the name, a token is issued; given
// another, the certificate is refused for it. Reached directly, the URL is
// what the reader chose, and its refusal names it.
func TestThroughAForwardTheCertificateIsCheckedForTheNameGiven(t *testing.T) {
	hostport, ca := keycloakBehindAForward(t)
	at := func(extra map[string]any) map[string]any {
		values := map[string]any{"url": "https://" + hostport, "ca-file": ca}
		for k, v := range extra {
			values[k] = v
		}
		return values
	}

	verr := mint(t, at(nil), plugin.TunnelKube)
	if verr == nil || verr.Code != "keycloak.tls.forward" {
		t.Fatalf("through a forward with no name: %+v, want keycloak.tls.forward", verr)
	}
	for _, want := range []string{"profile lab's kube: forward", "svc.example.internal", "127.0.0.1"} {
		if !strings.Contains(verr.Message, want) {
			t.Errorf("message %q does not name %q", verr.Message, want)
		}
	}
	if !strings.Contains(verr.Hint, "--tls-server-name") {
		t.Errorf("hint %q does not name the setting", verr.Hint)
	}

	if verr := mint(t, at(map[string]any{"tls-server-name": "svc.example.internal"}), plugin.TunnelKube); verr != nil {
		t.Fatalf("the name the certificate is for was refused: %s: %s", verr.Code, verr.Message)
	}

	verr = mint(t, at(map[string]any{"tls-server-name": "other.example.internal"}), plugin.TunnelKube)
	if verr == nil || verr.Code != "keycloak.tls.rejected" || !strings.Contains(verr.Hint, "the name in --tls-server-name") {
		t.Errorf("a name the certificate is not for: %+v, want keycloak.tls.rejected naming the setting", verr)
	}

	verr = mint(t, at(nil), plugin.TunnelNone)
	if verr == nil || verr.Code != "keycloak.tls.rejected" || !strings.Contains(verr.Hint, "the host in --url") {
		t.Errorf("reached directly: %+v, want keycloak.tls.rejected naming the URL", verr)
	}
}

// A certificate's name given to a call over plain HTTP checks nothing, and
// the call is refused before it goes in the clear; through a forward the way
// out is the connection's tunnelTLS, since the host wrote the http://.
func TestAServerNameOverPlainHTTPIsRefused(t *testing.T) {
	values := func() map[string]any {
		return map[string]any{"url": "http://127.0.0.1:54321", "tls-server-name": "svc.example.internal"}
	}
	verr := mint(t, values(), plugin.TunnelKube)
	if verr == nil || verr.Code != "keycloak.tls.plaintext" || !strings.Contains(verr.Hint, "tunnelTLS: true") ||
		!strings.Contains(verr.Message, "(profile lab, through its kube: forward)") {
		t.Errorf("through a forward: %+v, want keycloak.tls.plaintext naming the forward and tunnelTLS", verr)
	}
	verr = mint(t, values(), plugin.TunnelNone)
	if verr == nil || verr.Code != "keycloak.tls.plaintext" || !strings.Contains(verr.Hint, "an https:// URL") {
		t.Errorf("reached directly: %+v, want keycloak.tls.plaintext naming the scheme", verr)
	}
}

// The refusal names the setting as its reader changes it: an agent is told
// it is the operator's, never an argument it could pass.
func TestAForwardRefusalNamesTheSettingForItsSurface(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://127.0.0.1:54321/", Err: &tls.CertificateVerificationError{
		Err: x509.HostnameError{Certificate: &x509.Certificate{DNSNames: []string{"a", "b", "c", "d"}}, Host: "127.0.0.1"}}}
	for sf, want := range map[plugin.Surface]string{
		plugin.SurfaceCLI: "--tls-server-name",
		plugin.SurfaceTUI: "the tls-server-name box",
		plugin.SurfaceMCP: "the operator's `tls-server-name` setting",
	} {
		s := &session{req: req(t, "keycloak.overview", nil).WithProfile("lab", plugin.TunnelSSH).WithSurface(sf),
			base: "https://127.0.0.1:54321"}
		verr := s.classifyTransport(err)
		if verr.Code != "keycloak.tls.forward" || !strings.Contains(verr.Hint, want) {
			t.Errorf("%s: %s %q, want %q in the hint", sf, verr.Code, verr.Hint, want)
		}
		if !strings.Contains(verr.Message, "ssh: forward is for a, b, c and 1 more") {
			t.Errorf("%s: %q does not list the certificate's names", sf, verr.Message)
		}
	}
}
