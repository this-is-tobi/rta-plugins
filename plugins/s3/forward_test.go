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
	"errors"
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

// A server behind a kube: or ssh: forward is dialled at the forward's end,
// 127.0.0.1, and its certificate is for the name it answers as, which it
// rarely is. These run the real client against a server with a certificate
// for svc.example.internal alone, as a cluster service's is, dialled at
// 127.0.0.1 the way a forward's end is.

// serverBehindAForward answers every bucket listing with none, over TLS on
// 127.0.0.1 with a certificate for svc.example.internal, issued by a CA
// written to a file whose path it returns beside the endpoint.
func serverBehindAForward(t *testing.T) (endpoint, caFile string) {
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
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<ListAllMyBucketsResult><Owner><ID>o</ID></Owner><Buckets></Buckets></ListAllMyBucketsResult>`))
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

// Through a forward, a certificate for the server's own name is refused as
// one checked for the forward's end, with the name it is for and the setting
// that checks that name instead; given the name, the call is answered; given
// another, the certificate is refused for it. Reached directly, the endpoint
// is what the reader chose, and its refusal names it.
func TestThroughAForwardTheCertificateIsCheckedForTheNameGiven(t *testing.T) {
	endpoint, ca := serverBehindAForward(t)
	list := func(values map[string]any, tunnel plugin.Tunnel) *view.Error {
		values["endpoint"], values["ca-file"] = endpoint, ca
		values["access-key"], values["secret-key"] = "k", "s"
		// What the host forces over a forward, and what ca-file and
		// tls-server-name each turn back on.
		values["tls"] = false
		_, err := runBucketList(context.Background(), req(t, "s3.bucket.list", values).WithProfile("lab", tunnel))
		var verr *view.Error
		if err != nil && !errors.As(err, &verr) {
			t.Fatalf("err = %v, want a refusal", err)
		}
		return verr
	}

	// In parallel, since minio-go retries a failed handshake for seconds.
	t.Run("through a forward with no name", func(t *testing.T) {
		t.Parallel()
		verr := list(map[string]any{}, plugin.TunnelKube)
		if verr == nil || verr.Code != "s3.tls.forward" {
			t.Fatalf("%+v, want s3.tls.forward", verr)
		}
		for _, want := range []string{"profile lab (through its kube: forward)", "svc.example.internal", "127.0.0.1"} {
			if !strings.Contains(verr.Message, want) {
				t.Errorf("message %q does not name %q", verr.Message, want)
			}
		}
		if !strings.Contains(verr.Hint, "--tls-server-name") {
			t.Errorf("hint %q does not name the setting", verr.Hint)
		}
	})
	t.Run("the name the certificate is for", func(t *testing.T) {
		t.Parallel()
		if verr := list(map[string]any{"tls-server-name": "svc.example.internal"}, plugin.TunnelKube); verr != nil {
			t.Fatalf("refused: %s: %s", verr.Code, verr.Message)
		}
	})
	t.Run("a name the certificate is not for", func(t *testing.T) {
		t.Parallel()
		verr := list(map[string]any{"tls-server-name": "other.example.internal"}, plugin.TunnelKube)
		if verr == nil || verr.Code != "s3.tls.rejected" || !strings.Contains(verr.Hint, "the name in --tls-server-name") {
			t.Errorf("%+v, want s3.tls.rejected naming the setting", verr)
		}
	})
	t.Run("reached directly", func(t *testing.T) {
		t.Parallel()
		verr := list(map[string]any{}, plugin.TunnelNone)
		if verr == nil || verr.Code != "s3.tls.rejected" || !strings.Contains(verr.Hint, "the host in --endpoint") {
			t.Errorf("%+v, want s3.tls.rejected naming the endpoint", verr)
		}
	})
}

// The refusal names the setting as its reader changes it: an agent is told
// it is the operator's, never an argument it could pass.
func TestAForwardRefusalNamesTheSettingForItsSurface(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://127.0.0.1:54321/", Err: &tls.CertificateVerificationError{
		Err: x509.HostnameError{Certificate: &x509.Certificate{DNSNames: []string{"a", "b", "c", "d"}}, Host: "127.0.0.1"}}}
	for sf, want := range map[plugin.Surface]string{
		plugin.SurfaceCLI: "--tls-server-name",
		plugin.SurfaceTUI: "the tls-server-name box",
		plugin.SurfaceMCP: "the operator's `tls-server-name` setting",
	} {
		r := req(t, "s3.overview", nil).WithProfile("lab", plugin.TunnelSSH).WithSurface(sf)
		verr := classify(err, r)
		if verr.Code != "s3.tls.forward" || !strings.Contains(verr.Hint, want) {
			t.Errorf("%s: %s %q, want %q in the hint", sf, verr.Code, verr.Hint, want)
		}
		if !strings.Contains(verr.Message, "(through its ssh: forward) is for a, b, c and 1 more") {
			t.Errorf("%s: %q does not list the certificate's names", sf, verr.Message)
		}
		if !strings.Contains(verr.Hint, "the name the server answers as") {
			t.Errorf("%s: hint %q does not say what answers at the name", sf, verr.Hint)
		}
	}
}
