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
	"runtime"
	"slices"
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
	return ca.certFor(t, "127.0.0.1", []stdnet.IP{stdnet.ParseIP("127.0.0.1")})
}

// certFor is a certificate this CA issued under common name cn for the
// given DNS names and addresses; with neither, the shape of the one a server
// generates for itself, which names itself in a CN no verifier reads.
func (ca privateCA) certFor(t *testing.T, cn string, ips []stdnet.IP, dnsNames ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		IPAddresses:  ips,
		DNSNames:     dnsNames,
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

	_, verr := connect(context.Background(), req(t, "mariadb.status", conn))
	if verr == nil {
		t.Fatal("a server with a CA nothing here trusts was verified with no ca-file")
	}
	if verr.Code != "mariadb.tls.untrusted" {
		t.Fatalf("code = %s, want mariadb.tls.untrusted: %s", verr.Code, verr.Message)
	}
	if !strings.Contains(verr.Hint, "rather than --tls skip-verify, which turns verification off — "+
		"the CA that issued it belongs in --ca-file") || !strings.Contains(verr.Hint, "replaces the system's") {
		t.Errorf("hint = %q, want the CA named in --ca-file, what naming one replaces, and skip-verify "+
			"named as what it is not", verr.Hint)
	}

	conn["ca-file"] = writeFile(t, "ca.pem", ca.pem)
	db, verr := connect(context.Background(), req(t, "mariadb.status", conn))
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
	_, verr := connect(context.Background(), req(t, "mariadb.status", map[string]any{
		"host": "127.0.0.1", "port": fakeServer(t, nil), "tls": "true",
	}))
	if verr == nil || verr.Code != "mariadb.tls.unsupported" {
		t.Fatalf("err = %v, want mariadb.tls.unsupported", verr)
	}
	if !strings.Contains(verr.Hint, "--tls false if that is expected") {
		t.Errorf("hint = %q, want --tls false named", verr.Hint)
	}

	required := classify(&mysql.MySQLError{Number: 3159, Message: "Connections using insecure transport are prohibited"},
		req(t, "mariadb.status", map[string]any{"tls": "false"}))
	if required.Code != "mariadb.tls.required" || !strings.Contains(required.Hint, "--tls true connects over it") {
		t.Errorf("3159 = %s: %s, want mariadb.tls.required naming --tls true", required.Code, required.Hint)
	}

	// Through a forward, tls true given by the caller opens no forward, and
	// the call goes to the default host instead: the forward is named as
	// what carries no TLS, and the way on is a direct connection.
	forwarded := classify(&mysql.MySQLError{Number: 3159, Message: "Connections using insecure transport are prohibited"},
		req(t, "mariadb.status", map[string]any{"host": "127.0.0.1", "port": 54321, "tls": "false"}).
			WithProfile("prod", plugin.TunnelKube))
	if forwarded.Code != "mariadb.tls.required" ||
		!strings.Contains(forwarded.Message, "profile prod (through its kube: forward) carries none") {
		t.Errorf("3159 through a forward = %s: %s, want the forward named", forwarded.Code, forwarded.Message)
	}
	if !strings.Contains(forwarded.Hint, "by a profile with no kube: or ssh: coordinate") ||
		strings.Contains(forwarded.Hint, "--tls") {
		t.Errorf("hint = %q, want a direct connection named and no tls to give", forwarded.Hint)
	}
}

// A CA named that did not issue the server's certificate is said to be
// that, with the file named — not sent to put a CA in ca-file, which is
// where it already is.
func TestACAFileThatDidNotIssueTheCertificateIsNamed(t *testing.T) {
	port := tlsServer(t, newPrivateCA(t).serverCert(t))
	other := writeFile(t, "other-ca.pem", newPrivateCA(t).pem)
	_, verr := connect(context.Background(), req(t, "mariadb.status", map[string]any{
		"host": "127.0.0.1", "port": port, "tls": "true", "ca-file": other,
	}))
	if verr == nil || verr.Code != "mariadb.tls.untrusted" {
		t.Fatalf("err = %v, want mariadb.tls.untrusted", verr)
	}
	if !strings.Contains(verr.Hint, other) || !strings.Contains(verr.Hint, "does not hold the CA that issued it") {
		t.Errorf("hint = %q, want %s named as not holding the issuer", verr.Hint, other)
	}
}

// The CA right and the name wrong came out as "could not reach", with the
// page of every input for a hint, and the untrusted hint had just sent the
// reader to ca-file, which cannot cure it. The certificate a server generates
// for itself names no host at all, and is the one met most: said as that, the
// reader stops looking for a better CA — and said so with no CA named yet,
// rather than after the detour the untrusted hint would send them on.
func TestACertificateForAnotherNameIsNamedAsThat(t *testing.T) {
	ca := newPrivateCA(t)
	caFile := writeFile(t, "ca.pem", ca.pem)
	for _, tc := range []struct {
		name, message, hint, ca string
		cert                    tls.Certificate
	}{
		{"another name", "a certificate for db.internal, not 127.0.0.1", "reach the server by one it carries",
			caFile, ca.certFor(t, "db.internal", nil, "db.internal")},
		{"no name", "a certificate that names no host, 127.0.0.1 or any other",
			"--tls true reaches the server once its certificate is reissued with 127.0.0.1 among them",
			caFile, ca.certFor(t, "MariaDB_Server_Auto_Generated_Server_Certificate", nil)},
		{"no name, and no CA named for it", "a certificate that names no host", "reissued with 127.0.0.1",
			"", ca.certFor(t, "MariaDB_Server_Auto_Generated_Server_Certificate", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := connect(context.Background(), req(t, "mariadb.status", map[string]any{
				"host": "127.0.0.1", "port": tlsServer(t, tc.cert), "tls": "true", "ca-file": tc.ca,
			}))
			if verr == nil || verr.Code != "mariadb.tls.name" {
				t.Fatalf("err = %v, want mariadb.tls.name", verr)
			}
			if !strings.Contains(verr.Message, tc.message) || !strings.Contains(verr.Hint, tc.hint) {
				t.Errorf("got %q (hint %q), want %q with %q", verr.Message, verr.Hint, tc.message, tc.hint)
			}
		})
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
		{"missing", filepath.Join(dir, "absent.pem"), "mariadb.tls.ca.unreadable", "holding the CA's certificate in PEM"},
		{"a directory", dir, "mariadb.tls.ca.unreadable", "holding the CA's certificate in PEM"},
		{"a private key", key, "mariadb.tls.ca.invalid", "wants a PEM certificate"},
		{"DER", der, "mariadb.tls.ca.invalid", "wants a PEM certificate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := connect(context.Background(), req(t, "mariadb.status", map[string]any{
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
		_, verr := connect(context.Background(), req(t, "mariadb.status", map[string]any{
			"host": "127.0.0.1", "port": 1, "tls": mode, "ca-file": ca,
		}))
		if verr == nil || verr.Code != "mariadb.tls.ca.unused" {
			t.Fatalf("tls=%s: err = %v, want mariadb.tls.ca.unused", mode, verr)
		}
		if !strings.Contains(verr.Hint, "--tls true verifies the server against it") {
			t.Errorf("tls=%s: hint = %q, want --tls true named", mode, verr.Hint)
		}
	}
	if cfg, verr := tlsConfig(req(t, "mariadb.status", map[string]any{"tls": "false", "ca-file": ca})); cfg != nil || verr != nil {
		t.Errorf("tls=false: got %v, %v, want no TLS and no refusal", cfg, verr)
	}
}

// verify-ca checks the chain against ca-file and not the name: the one
// verified way to the certificate a server generates for itself, which names
// no host for true to check, and which only skip-verify reached before, by
// checking nothing. The chain still counts: a CA that did not issue the
// certificate is refused as untrusted, the file named.
func TestVerifyCAChecksTheChainAndNotTheName(t *testing.T) {
	ca := newPrivateCA(t)
	caFile := writeFile(t, "ca.pem", ca.pem)
	generated := ca.certFor(t, "MariaDB_Server_Auto_Generated_Server_Certificate", nil)
	for name, cert := range map[string]tls.Certificate{
		"no name":      generated,
		"another name": ca.certFor(t, "db.internal", nil, "db.internal"),
	} {
		db, verr := connect(context.Background(), req(t, "mariadb.status", map[string]any{
			"host": "127.0.0.1", "port": tlsServer(t, cert), "tls": "verify-ca", "ca-file": caFile,
		}))
		if verr != nil {
			t.Errorf("%s: the chain verified and the connection was refused: %s: %s", name, verr.Code, verr.Message)
			continue
		}
		_ = db.Close()
	}
	other := writeFile(t, "other-ca.pem", newPrivateCA(t).pem)
	_, verr := connect(context.Background(), req(t, "mariadb.status", map[string]any{
		"host": "127.0.0.1", "port": tlsServer(t, generated), "tls": "verify-ca", "ca-file": other,
	}))
	if verr == nil || verr.Code != "mariadb.tls.untrusted" || !strings.Contains(verr.Hint, other) {
		t.Errorf("a CA that did not issue it: %v, want mariadb.tls.untrusted naming %s", verr, other)
	}
}

// verify-ca with no ca-file is refused rather than checked against this
// machine's own store, where a chain that ends at any public CA and a name
// nobody checks would pass for any server. Before anything dials, and before
// a dry run describes a child that could not be told what to verify against.
func TestVerifyCAWithoutACAIsRefused(t *testing.T) {
	_, verr := connect(context.Background(), req(t, "mariadb.status", map[string]any{
		"host": "127.0.0.1", "port": 1, "tls": "verify-ca",
	}))
	if verr == nil || verr.Code != "mariadb.tls.ca.missing" || !strings.Contains(verr.Hint, "--ca-file names it") {
		t.Fatalf("err = %v, want mariadb.tls.ca.missing naming --ca-file", verr)
	}
	dry := func(id string, values map[string]any) plugin.Request {
		values["database"], values["tls"] = "app", "verify-ca"
		return plugin.NewRequest(plugin.Resolve(capabilityByID(t, id), plugin.Inputs{Caller: values}), true, false)
	}
	dump := dry("mariadb.dump", map[string]any{"out": filepath.Join(t.TempDir(), "app.sql")})
	restore := dry("mariadb.restore", map[string]any{"file": writeFile(t, "app.sql", []byte("select 1;\n"))})
	for name, run := range map[string]func() error{
		"dump":    func() error { _, err := runDump(context.Background(), dump); return err },
		"restore": func() error { _, err := runRestore(context.Background(), restore); return err },
	} {
		var verr *view.Error
		if err := run(); !errors.As(err, &verr) || verr.Code != "mariadb.tls.ca.missing" {
			t.Errorf("%s dry run: err = %v, want mariadb.tls.ca.missing", name, err)
		}
	}
}

// The dump and the restore verify the server against the CA the pre-flight
// connection did: the child is handed it as --ssl-ca, resolved as this
// process resolved it, and the restore line a dump's receipt prints carries
// it beside true, so the line does not refuse the server the dump verified.
func TestTheCAGoesWhereverTheConnectionDoes(t *testing.T) {
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "ca.pem")
	for _, mode := range []string{"true", "verify-ca"} {
		values := map[string]any{"host": "db.internal", "database": "app", "tls": mode, "ca-file": "~/ca.pem"}
		for name, args := range map[string][]string{
			"dump":    dumpArgs(req(t, "mariadb.dump", values)),
			"restore": restoreArgs(req(t, "mariadb.restore", values)),
		} {
			if !slices.Contains(args, "--ssl-ca="+want) {
				t.Errorf("tls %s, %s: argv = %q, want --ssl-ca=%s", mode, name, args, want)
			}
		}
		line := restoreCommand(req(t, "mariadb.dump", values), "/backups/app.sql")
		if !strings.Contains(line, "--tls "+mode+" --ca-file "+want) {
			t.Errorf("restore line = %q, want --ca-file %s beside --tls %s", line, want, mode)
		}
	}
	values := map[string]any{"host": "db.internal", "database": "app", "tls": "true", "ca-file": ""}
	if line := restoreCommand(req(t, "mariadb.dump", values), "/backups/app.sql"); strings.Contains(line, "--ca-file") {
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
	dump := dry("mariadb.dump", map[string]any{
		"database": "app", "out": filepath.Join(t.TempDir(), "app.sql"), "ca-file": ca,
	})
	restore := dry("mariadb.restore", map[string]any{
		"database": "app", "file": writeFile(t, "app.sql", []byte("select 1;\n")), "ca-file": ca,
	})
	for name, run := range map[string]func() error{
		"dump":    func() error { _, err := runDump(context.Background(), dump); return err },
		"restore": func() error { _, err := runRestore(context.Background(), restore); return err },
	} {
		err := run()
		var verr *view.Error
		if !errors.As(err, &verr) || verr.Code != "mariadb.tls.ca.unused" {
			t.Errorf("%s: err = %v, want mariadb.tls.ca.unused", name, err)
		}
	}
}

// A certificate is answered with the CA to name only when it is an unknown
// issuer's. macOS's own verifier, asked whenever no ca-file is named, gives
// most of its verdicts untyped, and every one was read as untrusted: a
// revoked certificate was answered with the CA file that turns the system's
// revocation check off. Such a verdict keeps the system's words, and the one
// hint that does send the reader to a CA file says what naming one costs.
func TestOnlyAnUnknownIssuerIsAnsweredWithTheCA(t *testing.T) {
	r := req(t, "mariadb.overview", map[string]any{"host": "db.internal", "tls": "true"})
	open, closing := string(rune(0x201c)), string(rune(0x201d))
	verdict := func(words string) error {
		return &tls.CertificateVerificationError{
			Err: errors.New("x509: " + open + "db.internal" + closing + " " + words)}
	}
	// The system's words for a chain to no anchor it holds are read as
	// untrusted where the system gave them, and are nobody's words elsewhere.
	notTrusted := "mariadb.tls.rejected"
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		notTrusted = "mariadb.tls.untrusted"
	}
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"Go's verifier, an issuer in no pool", x509.UnknownAuthorityError{}, "mariadb.tls.untrusted"},
		{"no pool to read at all", x509.SystemRootsError{}, "mariadb.tls.untrusted"},
		{"macOS, a chain to no anchor it holds", verdict("certificate is not trusted"), notTrusted},
		{"macOS, a revoked certificate", verdict("certificate is revoked"), "mariadb.tls.rejected"},
		{"macOS, a policy it will not pass", verdict("certificate is not standards compliant"), "mariadb.tls.rejected"},
		{"a revoked certificate named to look untrusted",
			verdict("certificate is not trusted" + closing + " certificate is revoked"), "mariadb.tls.rejected"},
		{"a signature algorithm Go's verifier refuses", &tls.CertificateVerificationError{
			Err: x509.InsecureAlgorithmError(x509.SHA1WithRSA)}, "mariadb.tls.rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verr := classify(tc.err, r)
			if verr.Code != tc.code {
				t.Fatalf("code = %q, want %q", verr.Code, tc.code)
			}
			if tc.code != "mariadb.tls.untrusted" {
				words := tc.err.Error()
				var handshake *tls.CertificateVerificationError
				if errors.As(tc.err, &handshake) {
					words = handshake.Err.Error()
				}
				if !strings.Contains(verr.Message, words) {
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
