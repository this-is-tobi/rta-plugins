package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The forward turns sslmode off, and a CA or a name to check turns TLS back
// on over it, at verify-full: the one mode that checks a name, which a
// forward's end never is. Nothing else on the line says TLS, so without this
// a certificate behind a profile's forward could not be verified at all.
func TestAServerNameOrACATurnsTLSOnOverAForward(t *testing.T) {
	ca := testCA(t)
	forwarded := func(extra map[string]any) plugin.Request {
		values := map[string]any{"host": "127.0.0.1", "port": 54321, "sslmode": "disable"}
		for k, v := range extra {
			values[k] = v
		}
		return reqFor(t, "pg.status", values).WithProfile("prod", plugin.TunnelKube)
	}
	for _, tc := range []struct {
		name   string
		extra  map[string]any
		tunnel plugin.Tunnel
		want   string
	}{
		{"nothing asks for TLS", nil, plugin.TunnelKube, "sslmode='disable'"},
		{"a name", map[string]any{"tls-server-name": "db.internal"}, plugin.TunnelKube, "sslmode='verify-full'"},
		{"a CA", map[string]any{"sslrootcert": ca}, plugin.TunnelKube, "sslmode='verify-full'"},
		{"a name through an ssh forward", map[string]any{"tls-server-name": "db.internal"}, plugin.TunnelSSH,
			"sslmode='verify-full'"},
		{"a name, direct and disabled", map[string]any{"tls-server-name": "db.internal"}, plugin.TunnelNone,
			"sslmode='disable'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := forwarded(tc.extra).WithProfile("prod", tc.tunnel)
			if got := dsn(r); !strings.Contains(got, tc.want) {
				t.Errorf("dsn = %s, want %s", got, tc.want)
			}
			if env := childEnv(r); !slices.Contains(env, "PGSSLMODE="+strings.Trim(strings.TrimPrefix(tc.want, "sslmode="), "'")) {
				t.Errorf("the child's mode is not the connection's: %v", env)
			}
		})
	}
}

// A name beside a connection that would not check it is refused, since a
// name given is an operator expecting a check: beside disable the call goes
// in the clear, beside the modes below verify-full no name is checked, and
// libpq's children can be told a name only beside an address.
func TestAServerNameBesideAConnectionThatChecksNoNameIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]any
		tunnel plugin.Tunnel
		code   string
	}{
		{"disabled", map[string]any{"host": "10.0.0.5", "sslmode": "disable"}, plugin.TunnelNone, "pg.tls.name.plaintext"},
		{"prefer", map[string]any{"host": "10.0.0.5", "sslmode": "prefer"}, plugin.TunnelNone, "pg.tls.name.unchecked"},
		{"verify-ca", map[string]any{"host": "10.0.0.5", "sslmode": "verify-ca"}, plugin.TunnelNone, "pg.tls.name.unchecked"},
		{"a host that is a name", map[string]any{"host": "db.internal", "sslmode": "verify-full"}, plugin.TunnelNone,
			"pg.tls.name.host"},
		{"verify-full at an address", map[string]any{"host": "10.0.0.5", "sslmode": "verify-full"}, plugin.TunnelNone, ""},
		{"a forward", map[string]any{"host": "127.0.0.1", "sslmode": "disable"}, plugin.TunnelKube, ""},
		{"an IPv6 address", map[string]any{"host": "::1", "sslmode": "verify-full"}, plugin.TunnelNone, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.values["tls-server-name"] = "svc.example.internal"
			verr := checkServerName(reqFor(t, "pg.status", tc.values).WithProfile("prod", tc.tunnel))
			switch {
			case tc.code == "" && verr != nil:
				t.Errorf("refused: %s: %s", verr.Code, verr.Message)
			case tc.code != "" && (verr == nil || verr.Code != tc.code):
				t.Errorf("got %v, want %s", verr, tc.code)
			}
		})
	}
}

// libpq has no setting for the name a certificate is checked for. A child
// that is given the name as its host and the address as hostaddr dials the
// address and checks the name, which is what the driver does with the name on
// its TLS configuration; both are handed the same one.
func TestTheChildrenCheckTheNameTheDriverChecks(t *testing.T) {
	values := map[string]any{"host": "127.0.0.1", "port": 54321, "sslmode": "disable",
		"tls-server-name": "svc.example.internal"}
	r := reqFor(t, "pg.dump", values).WithProfile("prod", plugin.TunnelKube)
	if args := dumpArgs(r); !slices.Contains(args, "--host=svc.example.internal") {
		t.Errorf("dump argv = %q, want the name as the host", args)
	}
	if args := restoreArgs(r, formatCustom, "/tmp/x.dump"); !slices.Contains(args, "--host=svc.example.internal") {
		t.Errorf("restore argv = %q, want the name as the host", args)
	}
	env := childEnv(r)
	for _, want := range []string{"PGHOSTADDR=127.0.0.1", "PGSSLMODE=verify-full"} {
		if !slices.Contains(env, want) {
			t.Errorf("the child's environment lacks %s: %v", want, env)
		}
	}

	plain := reqFor(t, "pg.dump", map[string]any{"host": "127.0.0.1", "port": 54321})
	if args := dumpArgs(plain); !slices.Contains(args, "--host=127.0.0.1") {
		t.Errorf("dump argv = %q, want the host", args)
	}
	for _, kv := range childEnv(plain) {
		if strings.HasPrefix(kv, "PGHOSTADDR=") {
			t.Errorf("a hostaddr with no name to check: %s", kv)
		}
	}
}

// What a dump's restore line carries to reach the same server again through
// a forward: the profile, and what turned TLS on there, never an sslmode the
// host would refuse beside the profile.
func TestTheRestoreLineCarriesWhatTurnedTLSOnOverAForward(t *testing.T) {
	ca := testCA(t)
	r := reqFor(t, "pg.dump", map[string]any{"host": "127.0.0.1", "port": 54321, "sslmode": "disable",
		"sslrootcert": ca, "tls-server-name": "svc.example.internal"}).WithProfile("prod", plugin.TunnelKube)
	line := restoreCommand(r, "/backups/app.dump")
	for _, want := range []string{"--profile prod", "--sslrootcert " + ca, "--tls-server-name svc.example.internal"} {
		if !strings.Contains(line, want) {
			t.Errorf("restore line = %q, want %q in it", line, want)
		}
	}
	for _, unwanted := range []string{"--sslmode", "--host", "--port"} {
		if strings.Contains(line, unwanted) {
			t.Errorf("restore line = %q names %s, which the profile's forward fills or forces", line, unwanted)
		}
	}
	direct := restoreCommand(reqFor(t, "pg.dump", map[string]any{"host": "10.0.0.5", "sslmode": "verify-full",
		"sslrootcert": ca, "tls-server-name": "svc.example.internal"}), "/backups/app.dump")
	for _, want := range []string{"--sslmode verify-full", "--sslrootcert " + ca, "--tls-server-name svc.example.internal"} {
		if !strings.Contains(direct, want) {
			t.Errorf("direct restore line = %q, want %q in it", direct, want)
		}
	}
}

// A certificate for another name than the one checked is named as that, with
// the names it carries and the setting that moves the check. Through a forward
// the host dialled is 127.0.0.1, which a service's certificate names only by
// luck, and the cure is tls-server-name and never anything that checks less.
func TestACertificateForAnotherNameIsNamedAsThat(t *testing.T) {
	cert := &x509.Certificate{DNSNames: []string{"svc.example.internal", "b", "c", "d"}}
	refuse := func(values map[string]any, tunnel plugin.Tunnel, sf plugin.Surface) (code, message, hint string) {
		r := reqFor(t, "pg.status", values).WithProfile("prod", tunnel).WithSurface(sf)
		verr := classify(&tls.CertificateVerificationError{
			Err: x509.HostnameError{Certificate: cert, Host: "127.0.0.1"}}, r)
		return verr.Code, verr.Message, verr.Hint
	}

	code, message, hint := refuse(map[string]any{"host": "127.0.0.1", "port": 54321}, plugin.TunnelKube, plugin.SurfaceCLI)
	if code != "pg.tls.forward" ||
		!strings.Contains(message, "the certificate behind profile prod (through its kube: forward) is for "+
			"svc.example.internal, b, c and 1 more, not for 127.0.0.1") ||
		!strings.Contains(hint, "--tls-server-name") || strings.Contains(hint, "--sslmode") {
		t.Errorf("through a forward: %s %q %q", code, message, hint)
	}

	for sf, want := range map[plugin.Surface]string{
		plugin.SurfaceTUI: "the tls-server-name box",
		plugin.SurfaceMCP: "the operator's `tls-server-name` setting",
	} {
		if _, _, hint := refuse(map[string]any{"host": "127.0.0.1"}, plugin.TunnelSSH, sf); !strings.Contains(hint, want) {
			t.Errorf("%s: hint = %q, want %q", sf, hint, want)
		}
	}

	code, _, hint = refuse(map[string]any{"host": "10.0.0.5", "sslmode": "verify-full"}, plugin.TunnelNone, plugin.SurfaceCLI)
	if code != "pg.tls.name" || !strings.Contains(hint, "--tls-server-name checks the certificate for a name it carries") {
		t.Errorf("an address, directly: %s %q, want the name setting offered", code, hint)
	}
	code, _, hint = refuse(map[string]any{"host": "db.internal", "sslmode": "verify-full"}, plugin.TunnelNone, plugin.SurfaceCLI)
	if code != "pg.tls.name" || !strings.Contains(hint, "--host is the name the certificate is checked against") ||
		strings.Contains(hint, "tls-server-name") {
		t.Errorf("a name, directly: %s %q, want the host named and no name setting", code, hint)
	}
	code, message, hint = refuse(map[string]any{"host": "127.0.0.1", "sslmode": "disable", "tls-server-name": "other"},
		plugin.TunnelKube, plugin.SurfaceCLI)
	if code != "pg.tls.name" || !strings.Contains(message, "not 127.0.0.1") ||
		!strings.Contains(hint, "--tls-server-name is the name the certificate is checked against") {
		t.Errorf("a name given and not matched: %s %q %q", code, message, hint)
	}
}

// The whole path, with no PostgreSQL behind it: the driver is pointed at a
// listener that answers the SSL request and speaks TLS with a certificate for
// svc.example.internal alone, at the address a forward's end would be. Given
// the name, the handshake verifies and the failure is the server hanging up
// after it; without it the certificate is refused for 127.0.0.1, and named.
func TestTheDriverChecksTheCertificateForTheNameGiven(t *testing.T) {
	addr, ca := pgBehindAForward(t)
	host, port, _ := net.SplitHostPort(addr)
	portNumber, _ := strconv.Atoi(port)
	through := func(extra map[string]any) plugin.Request {
		values := map[string]any{"host": host, "port": portNumber, "sslmode": "disable", "sslrootcert": ca}
		for k, v := range extra {
			values[k] = v
		}
		return reqFor(t, "pg.status", values).WithProfile("lab", plugin.TunnelKube)
	}

	_, verr := connect(context.Background(), through(nil))
	if verr == nil || verr.Code != "pg.tls.forward" {
		t.Fatalf("through a forward with no name: %v, want pg.tls.forward", verr)
	}

	_, verr = connect(context.Background(), through(map[string]any{"tls-server-name": "svc.example.internal"}))
	if verr == nil {
		t.Fatal("a listener that is no PostgreSQL connected")
	}
	if strings.HasPrefix(verr.Code, "pg.tls.") {
		t.Errorf("the name the certificate is for was refused: %s: %s", verr.Code, verr.Message)
	}

	_, verr = connect(context.Background(), through(map[string]any{"tls-server-name": "other.example.internal"}))
	if verr == nil || verr.Code != "pg.tls.name" || !strings.Contains(verr.Hint, "--tls-server-name is the name") {
		t.Errorf("a name the certificate is not for: %v, want pg.tls.name naming the setting", verr)
	}
}

type issuer struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCA(t *testing.T) (issuer, []byte) {
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
	return issuer{cert, key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// pgBehindAForward serves what PostgreSQL's TLS does and no more: it answers
// the SSL request with S, speaks TLS with a certificate for
// svc.example.internal issued by a CA written to a file whose path it returns
// beside the address, and hangs up when the handshake is over.
func pgBehindAForward(t *testing.T) (addr, caFile string) {
	t.Helper()
	ca, caPEM := newCA(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "svc.example.internal"},
		DNSNames:  []string{"svc.example.internal"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	caFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	config := &tls.Config{MinVersion: tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{leaf}, PrivateKey: key}}}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				var request [8]byte
				if _, err := io.ReadFull(conn, request[:]); err != nil ||
					binary.BigEndian.Uint32(request[4:]) != 80877103 {
					return
				}
				if _, err := conn.Write([]byte{'S'}); err != nil {
					return
				}
				_ = tls.Server(conn, config).Handshake()
			}()
		}
	}()
	return l.Addr().String(), caFile
}
