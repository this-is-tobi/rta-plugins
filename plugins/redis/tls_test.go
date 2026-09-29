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
	"runtime"
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
// issuer's. macOS's own verifier, asked whenever no ca-file is named, gives
// most of its verdicts untyped, and a revoked certificate named by one of
// them is no issuer a CA file would cure: naming one turns the system's
// revocation check off. Such a verdict keeps the system's words, and the one
// hint that does send the reader to a CA file says what naming one costs.
func TestOnlyAnUnknownIssuerIsAnsweredWithTheCA(t *testing.T) {
	open, closing := string(rune(0x201c)), string(rune(0x201d))
	verdict := func(words string) error {
		return &tls.CertificateVerificationError{
			Err: errors.New("x509: " + open + "cache.internal" + closing + " " + words)}
	}
	// The system's words for a chain to no anchor it holds are read as
	// untrusted where the system gave them, and are nobody's words elsewhere.
	notTrusted := "redis.conn.failed"
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		notTrusted = "redis.tls.untrusted"
	}
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"Go's verifier, an issuer in no pool", x509.UnknownAuthorityError{}, "redis.tls.untrusted"},
		{"no pool to read at all", x509.SystemRootsError{}, "redis.tls.untrusted"},
		{"macOS, a chain to no anchor it holds", verdict("certificate is not trusted"), notTrusted},
		{"macOS, a revoked certificate", verdict("certificate is revoked"), "redis.conn.failed"},
		{"macOS, a policy it will not pass", verdict("certificate is not standards compliant"), "redis.conn.failed"},
		{"a signature algorithm Go's verifier refuses", &tls.CertificateVerificationError{
			Err: x509.InsecureAlgorithmError(x509.SHA1WithRSA)}, "redis.conn.failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verr := classify(tc.err, "10.0.0.1:6379", plugin.SurfaceCLI)
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

// Through a kube: or ssh: forward connect dials 127.0.0.1, and TLS checks the
// certificate against that. The forward turns tls off, and ca-file turns it
// back on, so a profile naming a CA beside a coordinate failed on every call
// as "could not reach" a server that had answered. It is the forward's doing,
// said as that with what does get through; a certificate that names the
// address still passes, as it always did, and a name refused on a direct
// connection is the certificate's, not the forward's.
func TestANameCheckedThroughAForwardIsNamedAsTheForwards(t *testing.T) {
	ca := newTestCA(t)
	caFile := writeFile(t, "ca.pem", ca.pem)
	values := func(addr string) map[string]any { return map[string]any{"address": addr, "ca-file": caFile} }
	service := tlsServer(t, ca.serverCert(t, "cache.internal", "cache.prod.svc"))

	_, verr := connect(context.Background(), req(t, "redis.overview", values(service)).WithProfile("prod", plugin.TunnelKube))
	if verr == nil || verr.Code != "redis.tls.forward" {
		t.Fatalf("err = %v, want redis.tls.forward", verr)
	}
	for _, want := range []string{"a certificate for cache.internal, cache.prod.svc; the TLS --ca-file turns on",
		"checked it against 127.0.0.1, the local end of the kube: forward profile prod opened"} {
		if !strings.Contains(verr.Message, want) {
			t.Errorf("message = %q, want %q in it", verr.Message, want)
		}
	}
	for _, want := range []string{"without --ca-file and --cert-file, which turn TLS on though the forward turns it off",
		"inside the API server's TLS", "a certificate that names 127.0.0.1 too"} {
		if !strings.Contains(verr.Hint, want) {
			t.Errorf("hint = %q, want %q in it", verr.Hint, want)
		}
	}
	// --tls=false given by the caller opens no forward, and the call goes to
	// the config's address instead: the hint never offers it.
	if strings.Contains(verr.Hint, "--tls") {
		t.Errorf("hint = %q offers a tls value, which given by the caller skips the forward", verr.Hint)
	}

	_, verr = connect(context.Background(), req(t, "redis.overview", values(service)).WithProfile("prod", plugin.TunnelNone))
	if verr == nil || verr.Code != "redis.tls.name" {
		t.Errorf("a name refused on a direct connection: %v, want redis.tls.name", verr)
	}

	probes := tlsServer(t, ca.serverCert(t, "cache.internal", "127.0.0.1"))
	c, verr := connect(context.Background(), req(t, "redis.overview", values(probes)).WithProfile("prod", plugin.TunnelKube))
	if verr != nil {
		t.Fatalf("a certificate naming the forward's address, through the forward: %s: %s", verr.Code, verr.Message)
	}
	c.Close()
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
		if got := classify(tc.err, "cache.internal:6379", plugin.SurfaceCLI); got.Code != tc.code {
			t.Errorf("%s: %s %q, want %s", tc.name, got.Code, got.Message, tc.code)
		}
	}
}
