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
	stdnet "net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A member behind a kube: or ssh: forward is dialled at the forward's end,
// 127.0.0.1, and its certificate is for the name it answers as, which it
// rarely is. These run the real client against a member that serves etcd's KV
// over TLS with a certificate for svc.example.internal alone, as a cluster
// service's is, dialled at 127.0.0.1 the way a forward's end is.

// memberBehindAForward serves etcd's KV over TLS on 127.0.0.1 with a
// certificate for svc.example.internal, issued by a CA written to a file whose
// path it returns beside the address.
func memberBehindAForward(t *testing.T) (addr, caFile string) {
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

	l, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: key}},
	})))
	etcdserverpb.RegisterKVServer(srv, emptyKV{})
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(srv.Stop)
	return l.Addr().String(), caFile
}

// emptyKV answers every range with nothing, which is all a Get needs.
type emptyKV struct {
	etcdserverpb.UnimplementedKVServer
}

func (emptyKV) Range(context.Context, *etcdserverpb.RangeRequest) (*etcdserverpb.RangeResponse, error) {
	return &etcdserverpb.RangeResponse{Header: &etcdserverpb.ResponseHeader{}}, nil
}

// Through a forward, a certificate for the member's own name is refused as
// one checked for the forward's end, with the name it is for and the setting
// that checks that name instead; given the name, the real client connects and
// reads; given another, the certificate is refused for it. Reached directly,
// the endpoint is what the reader chose, and its refusal names it.
func TestThroughAForwardTheCertificateIsCheckedForTheNameGiven(t *testing.T) {
	addr, ca := memberBehindAForward(t)
	through := func(values map[string]any, tunnel plugin.Tunnel) plugin.Request {
		values["endpoint"], values["ca-file"] = addr, ca
		// What the host forces over a forward, and what ca-file and
		// tls-server-name each turn back on.
		values["tls"] = false
		return req(t, "etcd.overview", values).WithProfile("lab", tunnel)
	}

	_, verr := connectWithin(context.Background(), through(map[string]any{}, plugin.TunnelKube), 5*time.Second)
	if verr == nil || verr.Code != "etcd.tls.forward" {
		t.Fatalf("through a forward with no name: %+v, want etcd.tls.forward", verr)
	}
	for _, want := range []string{"profile lab (through its kube: forward)", "svc.example.internal", "127.0.0.1"} {
		if !strings.Contains(verr.Message, want) {
			t.Errorf("message %q does not name %q", verr.Message, want)
		}
	}
	if !strings.Contains(verr.Hint, "--tls-server-name") {
		t.Errorf("hint %q does not name the setting", verr.Hint)
	}

	c, verr := connectWithin(context.Background(),
		through(map[string]any{"tls-server-name": "svc.example.internal"}, plugin.TunnelKube), 5*time.Second)
	if verr != nil {
		t.Fatalf("the name the certificate is for was refused: %s: %s", verr.Code, verr.Message)
	}
	_, err := c.Get(context.Background(), "k")
	_ = c.Close()
	if err != nil {
		t.Fatalf("connected and could not read: %v", err)
	}

	_, verr = connectWithin(context.Background(),
		through(map[string]any{"tls-server-name": "other.example.internal"}, plugin.TunnelKube), 5*time.Second)
	if verr == nil || verr.Code != "etcd.tls.rejected" || !strings.Contains(verr.Hint, "the name in --tls-server-name") {
		t.Errorf("a name the certificate is not for: %+v, want etcd.tls.rejected naming the setting", verr)
	}

	_, verr = connectWithin(context.Background(), through(map[string]any{}, plugin.TunnelNone), 5*time.Second)
	if verr == nil || verr.Code != "etcd.tls.rejected" || !strings.Contains(verr.Hint, "the host in --endpoint") {
		t.Errorf("reached directly: %+v, want etcd.tls.rejected naming the endpoint", verr)
	}
}

// The refusal names the setting as its reader changes it: an agent is told
// it is the operator's, never an argument it could pass.
func TestAForwardRefusalNamesTheSettingForItsSurface(t *testing.T) {
	hostErr := x509.HostnameError{Certificate: &x509.Certificate{DNSNames: []string{"a", "b", "c", "d"}}, Host: "127.0.0.1"}
	for sf, want := range map[plugin.Surface]string{
		plugin.SurfaceCLI: "--tls-server-name",
		plugin.SurfaceTUI: "the tls-server-name box",
		plugin.SurfaceMCP: "the operator's `tls-server-name` setting",
	} {
		r := req(t, "etcd.overview", nil).WithProfile("lab", plugin.TunnelSSH).WithSurface(sf)
		verr := classify(handshakeError{&tls.CertificateVerificationError{Err: hostErr}}, r)
		if verr.Code != "etcd.tls.forward" || !strings.Contains(verr.Hint, want) {
			t.Errorf("%s: %s %q, want %q in the hint", sf, verr.Code, verr.Hint, want)
		}
		// What answers is etcd's member, which is the one word this plugin gives
		// the SDK's refusal besides its code and where the call reached.
		if !strings.Contains(verr.Hint, "the name the member answers as") {
			t.Errorf("%s: hint %q does not say it is the member's name", sf, verr.Hint)
		}
		if !strings.Contains(verr.Message, "(through its ssh: forward) is for a, b, c and 1 more") {
			t.Errorf("%s: %q does not list the certificate's names", sf, verr.Message)
		}
	}
}

// tls-server-name turns TLS on as ca-file does, since it means nothing
// without it: over a forward the host turns tls off, and a name given in the
// profile would otherwise have been a plaintext call to a TLS port.
func TestTheServerNameTurnsTLSOn(t *testing.T) {
	addr, _ := memberBehindAForward(t)
	verr, _ := connectRefusal(t, map[string]any{"endpoint": addr, "tls": false,
		"tls-server-name": "svc.example.internal"}, 5*time.Second)
	if verr.Code != "etcd.tls.untrusted" {
		t.Errorf("got %s %q, want the handshake's own verdict", verr.Code, verr.Message)
	}
}

// A port that takes the connection and answers nothing etcd's client reads
// is, among other things, a client port serving TLS to a plaintext client.
// Through a forward the way on is not tls, which the host turns off for the
// forward and refuses beside one, but what turns TLS on over it: a CA file,
// or the name the certificate is checked for.
func TestThroughAForwardAPortThatAnswersNothingNamesWhatTurnsTLSOn(t *testing.T) {
	plain := target{addr: "127.0.0.1:2379", reachable: true}
	for _, c := range []struct {
		tunnel       plugin.Tunnel
		want, unwant string
	}{
		{plugin.TunnelKube, "to the plaintext profile lab (through its kube: forward) asks for: --ca-file", "without --tls"},
		{plugin.TunnelNone, "without --tls on", "--ca-file"},
	} {
		r := req(t, "etcd.overview", nil).WithProfile("lab", c.tunnel)
		verr := wrongPort(r, plain)
		if !strings.Contains(verr.Hint, c.want) || strings.Contains(verr.Hint, c.unwant) {
			t.Errorf("%q: hint %q, want %q and not %q", c.tunnel, verr.Hint, c.want, c.unwant)
		}
	}
}
