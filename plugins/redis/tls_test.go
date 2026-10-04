package main

import (
	"bufio"
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
	stdnet "net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// testCA is a private CA: its certificate as PEM, and what it signs with.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rta test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// serverCert is a certificate the CA issued for names, the way a server in a
// cluster carries its service's names and not the address a forward dials.
func (ca testCA) serverCert(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, name := range names {
		if ip := stdnet.ParseIP(name); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, name)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// tlsServer answers PING over TLS with cert, on a port of this machine, and
// is gone when the test is.
func tlsServer(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					if _, err := readCommand(r); err != nil {
						return
					}
					if _, err := conn.Write([]byte("+PONG\r\n")); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A certificate is answered with the CA to name only when it is an unknown
// issuer's: Go's own verdicts for a chain that reaches no root, and none for
// any other reason Go's verifier refuses it. The verdicts macOS's own
// verifier gives untyped, which are answered by what they say, are in
// verdict_test.go.
func TestOnlyAnUnknownIssuerIsAnsweredWithTheCA(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"Go's verifier, an issuer in no pool", x509.UnknownAuthorityError{}, "redis.tls.untrusted"},
		{"no pool to read at all", x509.SystemRootsError{}, "redis.tls.untrusted"},
		{"a signature algorithm Go's verifier refuses", &tls.CertificateVerificationError{
			Err: x509.InsecureAlgorithmError(x509.SHA1WithRSA)}, "redis.conn.failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verr := classify(tc.err, "10.0.0.1:6379", req(t, "redis.overview", nil).WithSurface(plugin.SurfaceCLI))
			if verr.Code != tc.code {
				t.Fatalf("code = %q, want %q", verr.Code, tc.code)
			}
			if tc.code != "redis.tls.untrusted" {
				if !strings.Contains(verr.Message, tc.err.Error()) {
					t.Errorf("message = %q, want the verifier's own words in it", verr.Message)
				}
				if strings.Contains(verr.Hint, "ca-file") {
					t.Errorf("hint = %q sends the reader to a CA for a reason no CA cures", verr.Hint)
				}
				return
			}
			if !strings.Contains(verr.Hint, "--ca-file") || !strings.Contains(verr.Hint, "replaces the system's") {
				t.Errorf("hint = %q, want the CA file named and what naming one replaces", verr.Hint)
			}
		})
	}
}

// A ca-file named that did not issue the server's certificate is said to be
// that, with the file named — not sent to put the CA in ca-file, which is
// where one already is.
func TestACAFileThatDidNotIssueTheCertificateIsNamed(t *testing.T) {
	addr := tlsServer(t, newTestCA(t).serverCert(t, "127.0.0.1"))
	other := writeFile(t, "other-ca.pem", newTestCA(t).pem)
	_, verr := connect(context.Background(), req(t, "redis.overview", map[string]any{
		"address": addr, "tls": true, "ca-file": other,
	}))
	if verr == nil || verr.Code != "redis.tls.untrusted" {
		t.Fatalf("err = %v, want redis.tls.untrusted", verr)
	}
	if !strings.Contains(verr.Hint, other+", which --ca-file names, does not hold the CA that issued it") {
		t.Errorf("hint = %q, want %s named as not holding the issuer", verr.Hint, other)
	}
}

// A server behind a kube: or ssh: forward is dialled at the forward's end,
// 127.0.0.1, and its certificate is for the name it answers as. Through a
// forward, a certificate for that name alone is refused as one checked for
// the forward's end, with the name it is for and the setting that checks
// that name instead — never a tls value, which given by the caller opens no
// forward; given the name, the call is answered, and the name is what TLS
// checked; given another, the certificate is refused for it. Reached
// directly, the address is what the reader chose, and its refusal names it;
// a certificate that names the forward's end passes as it always did.
func TestThroughAForwardTheCertificateIsCheckedForTheNameGiven(t *testing.T) {
	ca := newTestCA(t)
	caFile := writeFile(t, "ca.pem", ca.pem)
	service := tlsServer(t, ca.serverCert(t, "cache.internal", "cache.prod.svc"))
	through := func(values map[string]any, tunnel plugin.Tunnel) plugin.Request {
		values["address"], values["ca-file"] = service, caFile
		// What the host forces over a forward, and what ca-file and
		// tls-server-name each turn back on.
		values["tls"] = false
		return req(t, "redis.overview", values).WithProfile("prod", tunnel)
	}

	_, verr := connect(context.Background(), through(map[string]any{}, plugin.TunnelKube))
	if verr == nil || verr.Code != "redis.tls.forward" {
		t.Fatalf("through a forward with no name: %v, want redis.tls.forward", verr)
	}
	if want := "the certificate behind profile prod (through its kube: forward) is for cache.internal, cache.prod.svc, " +
		"not for 127.0.0.1, where the forward ends"; !strings.Contains(verr.Message, want) {
		t.Errorf("message = %q, want %q in it", verr.Message, want)
	}
	if !strings.Contains(verr.Hint, "--tls-server-name, which the profile can hold beside its forward") {
		t.Errorf("hint = %q does not name the setting", verr.Hint)
	}
	if strings.Contains(verr.Hint, "--tls ") || strings.Contains(verr.Hint, "--tls=") {
		t.Errorf("hint = %q offers a tls value, which given by the caller skips the forward", verr.Hint)
	}

	c, verr := connect(context.Background(), through(map[string]any{"tls-server-name": "cache.prod.svc"}, plugin.TunnelKube))
	if verr != nil {
		t.Fatalf("the name the certificate is for was refused: %s: %s", verr.Code, verr.Message)
	}
	c.Close()

	_, verr = connect(context.Background(), through(map[string]any{"tls-server-name": "other.internal"}, plugin.TunnelKube))
	if verr == nil || verr.Code != "redis.tls.name" || !strings.Contains(verr.Message, "not other.internal") ||
		!strings.Contains(verr.Hint, "--tls-server-name is the name the certificate is checked against") {
		t.Errorf("a name the certificate is not for: %v, want redis.tls.name naming the setting", verr)
	}

	_, verr = connect(context.Background(), through(map[string]any{}, plugin.TunnelNone))
	if verr == nil || verr.Code != "redis.tls.name" || !strings.Contains(verr.Hint, "--address is the name") {
		t.Errorf("a name refused on a direct connection: %v, want redis.tls.name naming the address", verr)
	}

	probes := tlsServer(t, ca.serverCert(t, "cache.internal", "127.0.0.1"))
	c, verr = connect(context.Background(), req(t, "redis.overview", map[string]any{"address": probes, "ca-file": caFile}).
		WithProfile("prod", plugin.TunnelKube))
	if verr != nil {
		t.Fatalf("a certificate naming the forward's address, through the forward: %s: %s", verr.Code, verr.Message)
	}
	c.Close()
}

// A name given turns TLS on by itself, as ca-file does: through a forward the
// host has turned tls off, and a name given in the profile beside it would
// otherwise have been a plaintext call to a TLS port, with nothing checked.
func TestATLSServerNameTurnsTLSOn(t *testing.T) {
	ca := newTestCA(t)
	service := tlsServer(t, ca.serverCert(t, "cache.prod.svc"))
	_, verr := connect(context.Background(), req(t, "redis.overview", map[string]any{
		"address": service, "tls": false, "tls-server-name": "cache.prod.svc",
	}).WithProfile("prod", plugin.TunnelKube))
	// No ca-file: the handshake happened, and the certificate's issuer is one
	// nothing here trusts — plaintext would have been a hang-up instead.
	if verr == nil || verr.Code != "redis.tls.untrusted" {
		t.Fatalf("err = %v, want redis.tls.untrusted from a TLS handshake", verr)
	}
}

// The name is the operator's to say, as everything a connection is checked
// by is: an agent cannot set it, and a profile or the config can.
func TestTLSServerNameIsTheOperators(t *testing.T) {
	for _, f := range connFields() {
		if f.Name != "tls-server-name" {
			continue
		}
		if !f.Local || f.Config != "tls-server-name" {
			t.Fatalf("tls-server-name: Local %v, Config %q; want Local and configurable", f.Local, f.Config)
		}
		return
	}
	t.Fatal("no tls-server-name input")
}

// A certificate for another name than the one dialled was "could not reach"
// a server that had answered, with the page of every input for a hint. It is
// named as what it is, with the names the certificate does carry and the
// setting the name came from; one that names no host at all is said to be
// that.
func TestACertificateForAnotherNameIsNamedAsThat(t *testing.T) {
	ca := newTestCA(t)
	server := tlsServer(t, ca.serverCert(t, "cache.internal", "10.0.0.7"))
	values := map[string]any{"address": server, "ca-file": writeFile(t, "ca.pem", ca.pem)}

	_, verr := connect(context.Background(), req(t, "redis.overview", values))
	if verr == nil || verr.Code != "redis.tls.name" {
		t.Fatalf("err = %v, want redis.tls.name", verr)
	}
	if want := "presented a certificate for cache.internal, 10.0.0.7, not 127.0.0.1"; !strings.Contains(verr.Message, want) {
		t.Errorf("message = %q, want %q in it", verr.Message, want)
	}
	if want := "--address is the name the certificate is checked against"; !strings.Contains(verr.Hint, want) {
		t.Errorf("hint = %q, want %q in it", verr.Hint, want)
	}
	mcp := classifyDial(&tls.CertificateVerificationError{Err: x509.HostnameError{
		Certificate: &x509.Certificate{DNSNames: []string{"cache.internal"}}, Host: "127.0.0.1"}},
		"127.0.0.1:6379", req(t, "redis.overview", nil).WithSurface(plugin.SurfaceMCP))
	if want := "the operator's `address` setting is the name"; !strings.Contains(mcp.Hint, want) {
		t.Errorf("MCP hint = %q, want %q in it", mcp.Hint, want)
	}

	none := classifyDial(&tls.CertificateVerificationError{Err: x509.HostnameError{
		Certificate: &x509.Certificate{}, Host: "127.0.0.1"}}, "127.0.0.1:6379", req(t, "redis.overview", nil))
	if none.Code != "redis.tls.name" || !strings.Contains(none.Message, "names no host, 127.0.0.1 or any other") {
		t.Errorf("a certificate with no names: %s %q, want redis.tls.name saying it names none", none.Code,
			none.Message)
	}
}

// A TLS server hangs up on a plaintext client, and "try --tls" is the way on
// for a direct connection. Through a forward it was a way off it: tls given
// by the caller opens no forward, and the call went to the default address
// instead. There ca-file and tls-server-name are what turn TLS on, and the
// forward stays open.
func TestAHangUpThroughAForwardIsNotSentToTLS(t *testing.T) {
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Read(make([]byte, 64))
			_ = conn.Close()
		}
	}()
	values := map[string]any{"address": ln.Addr().String()}

	_, verr := connect(context.Background(), req(t, "redis.overview", values))
	if verr == nil || verr.Code != "redis.conn.closed" || !strings.Contains(verr.Hint, "try --tls") {
		t.Fatalf("a direct connection: %v, want redis.conn.closed with --tls", verr)
	}
	_, verr = connect(context.Background(), req(t, "redis.overview", values).WithProfile("prod", plugin.TunnelKube))
	if verr == nil || verr.Code != "redis.conn.closed" {
		t.Fatalf("through a forward: %v, want redis.conn.closed", verr)
	}
	if !strings.Contains(verr.Message, "profile prod (through its kube: forward) closed the connection") {
		t.Errorf("message = %q, want the profile named", verr.Message)
	}
	for _, want := range []string{"this call asks for — --ca-file and " +
		"--tls-server-name each turn TLS on over the forward", "since the forward ends at 127.0.0.1"} {
		if !strings.Contains(verr.Hint, want) {
			t.Errorf("hint = %q, want %q in it", verr.Hint, want)
		}
	}
	if strings.Contains(verr.Hint, "--tls ") || strings.Contains(verr.Hint, "--tls=") {
		t.Errorf("hint = %q offers --tls, which given by the caller opens no forward", verr.Hint)
	}
}

// A port on this machine with nothing on it is a forward that exited far more
// often than a server that is down. Through the host's forward it is said to
// be that forward's end, gone before the call reached it; reached directly,
// a loopback address is a server not running here or a port-forward the
// operator runs; elsewhere the refusal is the server's port.
func TestARefusalOnThisMachineIsAForwardThatExited(t *testing.T) {
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()
	values := map[string]any{"address": closed}

	_, verr := connect(context.Background(), req(t, "redis.overview", values).WithProfile("prod", plugin.TunnelKube))
	if verr == nil || verr.Code != "redis.conn.refused" {
		t.Fatalf("through a forward: %v, want redis.conn.refused", verr)
	}
	if want := "the end of profile prod (through its kube: forward)"; !strings.Contains(verr.Message, want) {
		t.Errorf("message = %q, want %q in it", verr.Message, want)
	}
	if want := "a port-forward that exited — this one was opened for this call"; !strings.Contains(verr.Hint, want) {
		t.Errorf("hint = %q, want %q in it", verr.Hint, want)
	}

	_, verr = connect(context.Background(), req(t, "redis.overview", values))
	if verr == nil || verr.Code != "redis.conn.refused" || !strings.Contains(verr.Hint, "a port-forward that exited — "+
		"check the terminal running it") {
		t.Errorf("a loopback address reached directly: %v, want the forward the operator runs named", verr)
	}

	far := classifyDial(&stdnet.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)},
		"10.0.0.1:6379", req(t, "redis.overview", nil))
	if far.Code != "redis.conn.refused" || strings.Contains(far.Hint, "port-forward") {
		t.Errorf("another machine's refusal: %s %q, want no forward in it", far.Code, far.Hint)
	}
}

// A failed dial is read by the operating system's own error, never by the
// *net.OpError around it, which every broken socket call is: a server behind
// a VPN that was down, and one that reset the connection, were each "nothing
// is listening". Text flattened by a layer between is read by the words the
// error had, and a name nothing resolved stays that, whatever the resolver's
// own failed exchange with its server said.
func TestADialIsReadByItsOwnErrorNotByTheWrapper(t *testing.T) {
	dial := func(errno syscall.Errno) error {
		return &stdnet.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
	}
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"refused", dial(syscall.ECONNREFUSED), "redis.conn.refused"},
		{"no route to the host", dial(syscall.EHOSTUNREACH), "redis.conn.unreachable"},
		{"no way onto the network", dial(syscall.ENETUNREACH), "redis.conn.unreachable"},
		{"the system's connect timeout", dial(syscall.ETIMEDOUT), "redis.timeout"},
		{"a connection the server reset", &stdnet.OpError{Op: "read", Net: "tcp",
			Err: os.NewSyscallError("read", syscall.ECONNRESET)}, "redis.conn.failed"},
		{"a refusal flattened to text", errors.New("dial tcp 10.0.0.1:6379: connect: connection refused"),
			"redis.conn.refused"},
		{"no route flattened to text", errors.New("dial tcp 10.0.0.1:6379: connect: no route to host"),
			"redis.conn.unreachable"},
		{"a name whose DNS server refused the resolver", &stdnet.OpError{Op: "dial", Net: "tcp",
			Err: &stdnet.DNSError{Err: "dial udp 10.0.0.53:53: connect: connection refused", Name: "cache.internal"}},
			"redis.host.unknown"},
	} {
		if got := classify(tc.err, "cache.internal:6379", req(t, "redis.overview", nil).WithSurface(plugin.SurfaceCLI)); got.Code != tc.code {
			t.Errorf("%s: %s %q, want %s", tc.name, got.Code, got.Message, tc.code)
		}
	}
}
