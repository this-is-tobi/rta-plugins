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
	"io"
	"math/big"
	stdnet "net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// These run the whole real path — connFields resolved, the driver's TLS set
// up, a handshake over a socket, the failure through classify — against a
// server that speaks just enough of the protocol to offer TLS, take a login
// and answer a ping. Not a test of crypto/tls: what is under test is which
// CA this plugin hands the driver, and what it says when that CA is wrong.

// privateCA is a CA nothing on this machine trusts, the shape of an
// operator-generated root or a cluster's own issuer.
type privateCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newPrivateCA(t *testing.T) privateCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "rta test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return privateCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// serverCert is a certificate this CA issued for 127.0.0.1, the address the
// tests dial, so the name check passes and the chain is the only question.
func (ca privateCA) serverCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []stdnet.IP{stdnet.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// writeFile puts content in a file of its own under the test's directory and
// names it.
func writeFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tlsServer listens on loopback and answers every connection as a server
// that requires TLS would: a handshake offering it, the switch, a login
// accepted without looking at it, and an OK to every command after. It
// returns the port.
func tlsServer(t *testing.T, cert tls.Certificate) int {
	t.Helper()
	return fakeServer(t, &cert)
}

// fakeServer is tlsServer, or with no certificate a server that offers no
// TLS at all, and hangs up on a client that wanted it.
func fakeServer(t *testing.T, cert *tls.Certificate) int {
	t.Helper()
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(c, cert)
		}
	}()
	return ln.Addr().(*stdnet.TCPAddr).Port
}

func serve(c stdnet.Conn, cert *tls.Certificate) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	// CLIENT_LONG_PASSWORD, LONG_FLAG, PROTOCOL_41, SSL, TRANSACTIONS,
	// SECURE_CONNECTION and PLUGIN_AUTH: what a server needs to offer for
	// the driver to ask for TLS and log in over it. SSL is left out by one
	// with no certificate.
	caps := 1 | 1<<2 | 1<<9 | 1<<11 | 1<<13 | 1<<15 | 1<<19
	if cert == nil {
		caps &^= 1 << 11
	}
	nul := func(s string) []byte { return append([]byte(s), 0) }
	hello := append([]byte{10}, nul("rta-fake")...)
	hello = append(hello, 1, 0, 0, 0)
	hello = append(hello, nul("12345678")...)
	hello = append(hello, byte(caps), byte(caps>>8), 0x21, 2, 0, byte(caps>>16), byte(caps>>24), 21)
	hello = append(hello, make([]byte, 10)...)
	hello = append(hello, nul("123456789012")...)
	hello = append(hello, nul("mysql_native_password")...)
	if writePacket(c, 0, hello) != nil || cert == nil {
		return
	}
	if _, _, err := readPacket(c); err != nil { // the request to switch to TLS
		return
	}
	tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12})
	if tc.Handshake() != nil {
		return
	}
	var rw io.ReadWriter = tc
	ok := []byte{0, 0, 0, 2, 0, 0, 0}
	for {
		payload, seq, err := readPacket(rw)
		if err != nil || (seq == 0 && len(payload) > 0 && payload[0] == 1) { // COM_QUIT
			return
		}
		if writePacket(rw, seq+1, ok) != nil {
			return
		}
	}
}

// writePacket frames payload as the protocol does: three bytes of length,
// little-endian, and the sequence number.
func writePacket(w io.Writer, seq byte, payload []byte) error {
	n := len(payload)
	_, err := w.Write(append([]byte{byte(n), byte(n >> 8), byte(n >> 16), seq}, payload...))
	return err
}

func readPacket(r io.Reader) ([]byte, byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, 0, err
	}
	payload := make([]byte, int(h[0])|int(h[1])<<8|int(h[2])<<16)
	_, err := io.ReadFull(r, payload)
	return payload, h[3], err
}

// A server behind a CA of its own was reachable with tls=true only by
// giving that up for skip-verify, which checks nothing: no input named a CA
// to verify against. ca-file is that input, and the refusal it answers
// names it — not the mode that turns verification off.
func TestACAFileVerifiesAServerWithAPrivateCA(t *testing.T) {
	ca := newPrivateCA(t)
	port := tlsServer(t, ca.serverCert(t))
	conn := map[string]any{"host": "127.0.0.1", "port": port, "tls": "true"}

	_, verr := connect(context.Background(), req(t, "mysql.status", conn))
	if verr == nil {
		t.Fatal("a server with a CA nothing here trusts was verified with no ca-file")
	}
	if verr.Code != "mysql.tls.untrusted" {
		t.Fatalf("code = %s, want mysql.tls.untrusted: %s", verr.Code, verr.Message)
	}
	if !strings.Contains(verr.Hint, "that CA in --ca-file rather than --tls skip-verify") {
		t.Errorf("hint = %q, want the CA named in --ca-file, over turning verification off", verr.Hint)
	}

	conn["ca-file"] = writeFile(t, "ca.pem", ca.pem)
	db, verr := connect(context.Background(), req(t, "mysql.status", conn))
	if verr != nil {
		t.Fatalf("ca-file did not verify the server it issued for: %s: %s", verr.Code, verr.Message)
	}
	_ = db.Close()
}

// A server that offers no TLS, asked for it, answered; it did not go
// unreached, which is what the driver's own sentence read as. And one that
// insists on TLS refused a connection without it, which is not a query that
// failed.
func TestWhatTheServerSaysAboutTLSIsNamedAsThat(t *testing.T) {
	_, verr := connect(context.Background(), req(t, "mysql.status", map[string]any{
		"host": "127.0.0.1", "port": fakeServer(t, nil), "tls": "true",
	}))
	if verr == nil || verr.Code != "mysql.tls.unsupported" {
		t.Fatalf("err = %v, want mysql.tls.unsupported", verr)
	}
	if !strings.Contains(verr.Hint, "--tls false if that is expected") {
		t.Errorf("hint = %q, want --tls false named", verr.Hint)
	}

	required := classify(&mysql.MySQLError{Number: 3159, Message: "Connections using insecure transport are prohibited"},
		req(t, "mysql.status", map[string]any{"tls": "false"}))
	if required.Code != "mysql.tls.required" || !strings.Contains(required.Hint, "--tls true connects over it") {
		t.Errorf("3159 = %s: %s, want mysql.tls.required naming --tls true", required.Code, required.Hint)
	}
}

// A CA named that did not issue the server's certificate is said to be
// that, with the file named — not sent to put a CA in ca-file, which is
// where it already is.
func TestACAFileThatDidNotIssueTheCertificateIsNamed(t *testing.T) {
	port := tlsServer(t, newPrivateCA(t).serverCert(t))
	other := writeFile(t, "other-ca.pem", newPrivateCA(t).pem)
	_, verr := connect(context.Background(), req(t, "mysql.status", map[string]any{
		"host": "127.0.0.1", "port": port, "tls": "true", "ca-file": other,
	}))
	if verr == nil || verr.Code != "mysql.tls.untrusted" {
		t.Fatalf("err = %v, want mysql.tls.untrusted", verr)
	}
	if !strings.Contains(verr.Hint, other) || !strings.Contains(verr.Hint, "does not hold the CA that issued it") {
		t.Errorf("hint = %q, want %s named as not holding the issuer", verr.Hint, other)
	}
}

// A ca-file that cannot be used is refused before anything dials, saying
// what the file has to hold. A private key and a DER-encoded certificate are
// the two a reader most often has to hand instead.
func TestACAFileThatCannotBeUsedSaysWhatItMustHold(t *testing.T) {
	dir := t.TempDir()
	key := writeFile(t, "server.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a key")}))
	der := writeFile(t, "ca.der", newPrivateCA(t).cert.Raw)
	for _, tc := range []struct {
		name, path, code, hint string
	}{
		{"missing", filepath.Join(dir, "absent.pem"), "mysql.tls.ca.unreadable", "holding the CA's certificate in PEM"},
		{"a directory", dir, "mysql.tls.ca.unreadable", "holding the CA's certificate in PEM"},
		{"a private key", key, "mysql.tls.ca.invalid", "wants a PEM certificate"},
		{"DER", der, "mysql.tls.ca.invalid", "wants a PEM certificate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := connect(context.Background(), req(t, "mysql.status", map[string]any{
				"host": "127.0.0.1", "port": 1, "tls": "true", "ca-file": tc.path,
			}))
			if verr == nil || verr.Code != tc.code {
				t.Fatalf("err = %v, want %s", verr, tc.code)
			}
			if !strings.Contains(verr.Hint, "--ca-file") || !strings.Contains(verr.Hint, tc.hint) {
				t.Errorf("hint = %q, want --ca-file named and %q", verr.Hint, tc.hint)
			}
		})
	}
}

// preferred and skip-verify negotiate TLS and verify nothing, so a CA named
// beside either would read as a verified connection and be none. Refused,
// naming the mode that reads it. false negotiates nothing to verify, and is
// what a tunnel forces for the forward alone, so the CA a config names for
// direct connections goes unread there rather than refusing every call.
func TestACAFileIsRefusedBesideAModeThatNeverVerifies(t *testing.T) {
	ca := writeFile(t, "ca.pem", newPrivateCA(t).pem)
	for _, mode := range []string{"preferred", "skip-verify"} {
		_, verr := connect(context.Background(), req(t, "mysql.status", map[string]any{
			"host": "127.0.0.1", "port": 1, "tls": mode, "ca-file": ca,
		}))
		if verr == nil || verr.Code != "mysql.tls.ca.unused" {
			t.Fatalf("tls=%s: err = %v, want mysql.tls.ca.unused", mode, verr)
		}
		if !strings.Contains(verr.Hint, "--tls true verifies the server against it") {
			t.Errorf("tls=%s: hint = %q, want --tls true named", mode, verr.Hint)
		}
	}
	if cfg, verr := tlsConfig(req(t, "mysql.status", map[string]any{"tls": "false", "ca-file": ca})); cfg != nil || verr != nil {
		t.Errorf("tls=false: got %v, %v, want no TLS and no refusal", cfg, verr)
	}
}

// The dump and the restore verify the server against the CA the pre-flight
// connection did: the child is handed it as --ssl-ca, resolved as this
// process resolved it, and the restore line a dump's receipt prints carries
// it beside true, so the line does not refuse the server the dump verified.
func TestTheCAGoesWhereverTheConnectionDoes(t *testing.T) {
	home, _ := os.UserHomeDir()
	values := map[string]any{"host": "db.internal", "database": "app", "tls": "true", "ca-file": "~/ca.pem"}
	want := filepath.Join(home, "ca.pem")
	verifying := strings.Join(tlsArgs(req(t, "mysql.dump", map[string]any{"tls": "true"})), " ")
	for name, args := range map[string][]string{
		"dump":    dumpArgs(req(t, "mysql.dump", values)),
		"restore": restoreArgs(req(t, "mysql.restore", values)),
	} {
		if !strings.Contains(strings.Join(args, " "), verifying+" --ssl-ca="+want) {
			t.Errorf("%s: argv = %q, want --ssl-ca=%s beside %s", name, args, want, verifying)
		}
	}
	line := restoreCommand(req(t, "mysql.dump", values), "/backups/app.sql")
	if !strings.Contains(line, "--tls true --ca-file "+want) {
		t.Errorf("restore line = %q, want --ca-file %s beside --tls true", line, want)
	}
	values["ca-file"] = ""
	if line := restoreCommand(req(t, "mysql.dump", values), "/backups/app.sql"); strings.Contains(line, "--ca-file") {
		t.Errorf("restore line = %q names a CA nobody gave", line)
	}
}

// The dry run connects to nothing, so it would have described a child with
// an --ssl-ca the real run refuses. It refuses as the real run does.
func TestTheDryRunRefusesTheCAAsTheRunWould(t *testing.T) {
	ca := writeFile(t, "ca.pem", newPrivateCA(t).pem)
	dry := func(id string, values map[string]any) plugin.Request {
		return plugin.NewRequest(plugin.Resolve(capabilityByID(t, id), plugin.Inputs{Caller: values}), true, false)
	}
	dump := dry("mysql.dump", map[string]any{
		"database": "app", "out": filepath.Join(t.TempDir(), "app.sql"), "ca-file": ca,
	})
	restore := dry("mysql.restore", map[string]any{
		"database": "app", "file": writeFile(t, "app.sql", []byte("select 1;\n")), "ca-file": ca,
	})
	for name, run := range map[string]func() error{
		"dump":    func() error { _, err := runDump(context.Background(), dump); return err },
		"restore": func() error { _, err := runRestore(context.Background(), restore); return err },
	} {
		err := run()
		var verr *view.Error
		if !errors.As(err, &verr) || verr.Code != "mysql.tls.ca.unused" {
			t.Errorf("%s: err = %v, want mysql.tls.ca.unused", name, err)
		}
	}
}
