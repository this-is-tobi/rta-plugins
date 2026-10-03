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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/view"
)

// req builds a resolved request the way the host would — defaults applied,
// caller values on top — so these test the values a handler actually sees
// rather than a hand-made map.
func req(t *testing.T, capID string, values map[string]any) plugin.Request {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == capID {
			return plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false)
		}
	}
	t.Fatalf("no capability %q", capID)
	return plugin.Request{}
}

// sdktest is the definition of "a correct plugin", and etcd gets no
// exemption from it — the spelling rule among the others, which holds what
// every capability declares to the SDK's speller.
//
// Every read here is NoPreview, so the suite runs none of them. What it
// drives is the two writes, under --dry-run. etcd.kv.get takes a key no
// default supplies, and without one the suite fails rather than report a
// pass for a capability it never ran. etcd.snapshot has no default for its
// file, and without one its dry run stops at the refusal asking for it,
// short of the description the rule is there to reach.
func TestConformance(t *testing.T) {
	sdktest.Check(t, Plugin(), sdktest.WithInputs(conformanceInputs), sdktest.WithSource("."))
}

// conformanceInputs points both writes at a port nothing listens on, so
// neither reaches a cluster. etcd.kv.get has no dry run of its own — it
// changes nothing, and reads under --dry-run as it does without — so left
// at its default endpoint it would read a key out of whatever etcd the
// machine running the tests has. The snapshot's file is inside dir, the
// directory the suite watches, so a dry run that wrote it would be caught
// where it landed.
//
// etcd.kv.get meets the closed port and is refused at once, as a port
// nothing listens on; so would a snapshot's dry run that stopped being dry.
func conformanceInputs(dir string) map[string]map[string]any {
	const endpoint = "127.0.0.1:1"
	return map[string]map[string]any{
		"etcd.kv.get":   {"endpoint": endpoint, "key": "/conformance"},
		"etcd.snapshot": {"endpoint": endpoint, "out": filepath.Join(dir, "etcd.snap")},
	}
}

// Every shared connection input must be Local. These fields together name
// which cluster a call reaches and as whom, and an MCP caller may not choose
// that — an agent that could would point rta at a cluster of its own and have
// the host supply the operator's credential beside it.
//
// Written against connFields() rather than a list of names, so an input added
// later is covered the day it is added.
func TestEveryConnectionInputIsLocal(t *testing.T) {
	for _, f := range connFields() {
		if !f.Local {
			t.Errorf("%s: connection input is not Local — an MCP caller could redirect this call", f.Name)
		}
	}
}

// Only a genuine credential opts into EnvFallback. A field that merely chooses
// a destination must not be fillable from an ambient variable the MCP server
// happened to inherit.
func TestOnlySecretsUseEnvFallback(t *testing.T) {
	for _, f := range connFields() {
		if f.EnvFallback && f.Type != plugin.Secret {
			t.Errorf("%s: non-secret input declares EnvFallback (%s); a destination must come from a caller or config",
				f.Name, f.Type)
		}
	}
}

// The three certificate paths are read off this machine's disk. An input
// naming a file that the host then opens is a file-read primitive if a caller
// can choose the path, so these being Local is load-bearing rather than
// consistent-looking.
func TestCertificatePathsCannotBeChosenByACaller(t *testing.T) {
	want := map[string]bool{"ca-file": false, "cert-file": false, "key-file": false}
	for _, f := range connFields() {
		if _, ok := want[f.Name]; ok {
			want[f.Name] = true
			if !f.Local {
				t.Errorf("%s: a caller-settable path the host then reads is a file-read primitive", f.Name)
			}
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("%s: no longer declared — this test is now guarding nothing", name)
		}
	}
}

// Every capability here reaches off the box, so cap must have forced
// NoPreview on all of them. That is what keeps the automatic dashboard from
// deciding, on its own, that the store a production cluster depends on is
// worth polling every few seconds.
func TestEveryCapabilityIsNoPreview(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if !c.NoPreview {
			t.Errorf("%s: NoPreview = false, want true — every capability here reaches off the box", c.ID)
		}
	}
}

// **The line this plugin draws, pinned.**
//
// Nothing here mutates a cluster. The two writes are writes for what they
// disclose, and they are opposite ends of the same scale — which is why only
// one of them takes a grant.
//
// etcd.kv.get returns one stored value, so a person can consent to it by name
// and the grant is worth having. etcd.snapshot returns every stored value, so
// there is no name to put in a grant: it refuses MCP outright instead, which
// is keys.backup's line and pg.dump's, and leaving NeedsGrant off is part of
// that decision rather than an oversight — a grant that can never be exercised
// over the one surface grants gate is an entry in `grant list` meaning nothing.
//
// It matters more here than in most places: a Kubernetes cluster keeps its
// Secrets in etcd base64-encoded rather than encrypted, unless encryption at
// rest was turned on.
//
// The table fails in both directions, so a new capability that is not
// accounted for fails, and an entry naming one that no longer exists fails too.
func TestTheWriteTierIsDisclosureAndOnlyOneHalfIsGrantable(t *testing.T) {
	want := map[string]struct {
		safety plugin.Safety
		grant  bool
	}{
		"etcd.overview":    {plugin.Read, false},
		"etcd.member.list": {plugin.Read, false},
		"etcd.lease.list":  {plugin.Read, false},
		"etcd.kv.list":     {plugin.Read, false},
		"etcd.kv.tree":     {plugin.Read, false},
		"etcd.kv.get":      {plugin.Write, true},
		"etcd.snapshot":    {plugin.Write, false},
	}
	seen := map[string]bool{}
	for _, c := range Plugin().Capabilities {
		seen[c.ID] = true
		expect, ok := want[c.ID]
		if !ok {
			t.Errorf("%s: not accounted for in this test's table", c.ID)
			continue
		}
		if c.Safety != expect.safety {
			t.Errorf("%s: Safety = %s, want %s", c.ID, c.Safety, expect.safety)
		}
		if c.NeedsGrant != expect.grant {
			t.Errorf("%s: NeedsGrant = %v, want %v", c.ID, c.NeedsGrant, expect.grant)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("%s: declared in this test's table but not in Plugin()", id)
		}
	}
}

// The value is the whole point of etcd.kv.get and must still not land in a
// terminal scrollback or a log by accident. Redacted is what makes every
// renderer mask it unless somebody asked for it.
func TestTheValueIsDeclaredRedacted(t *testing.T) {
	v := kvGetResult("/registry/secrets/default/api-token", []byte("s3cret"), 1, 1, 1, 0)
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("want KeyValue, got %s", view.TypeOf(v))
	}
	if !slices.Contains(kv.Redacted, "value") {
		t.Errorf("etcd.kv.get does not declare `value` redacted: %v", kv.Redacted)
	}
	// The key is not redacted, and should not be: knowing a secret exists is
	// the read tier's job and is already available from etcd.kv.list.
	if slices.Contains(kv.Redacted, "key") {
		t.Error("the key is redacted — that hides which secret was read from the record")
	}
}

// What the description says about the value is what the result does with it.
// rta masks every field a plugin marks redacted, on every surface and for a
// caller holding a grant too, and the description promised "the value stored
// at one key" without saying so: an agent granted the call got `••••••`. If
// the value is ever returned, this fails, and the description has to follow it.
func TestTheDescriptionSaysTheValueComesBackMasked(t *testing.T) {
	declared := slices.Contains(kvGetResult("/k", []byte("v"), 1, 1, 1, 0).(view.KeyValue).Redacted, "value")
	for _, c := range Plugin().Capabilities {
		if c.ID != "etcd.kv.get" {
			continue
		}
		if said := strings.Contains(c.Description, "comes back masked"); said != declared {
			t.Errorf("the value is redacted = %v, and the description says it comes back masked = %v", declared, said)
		}
		return
	}
	t.Fatal("etcd.kv.get is not declared")
}

// Every capability must be reachable and describable, and namespaced by the
// plugin's own name.
func TestEveryCapabilityIsRunnableAndDescribed(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.Run == nil {
			t.Errorf("%s: no Run", c.ID)
		}
		if strings.TrimSpace(c.Description) == "" {
			t.Errorf("%s: no Description — `rta explain` has nothing to print", c.ID)
		}
		if !strings.HasPrefix(c.ID, Plugin().Name+".") {
			t.Errorf("%s: capability IDs must be namespaced by %q", c.ID, Plugin().Name)
		}
	}
}

// The certificate paths resolve a leading ~ as every other path a plugin
// reads does. Opened as typed, ~/ca.pem was a path under a directory named
// ~, and a CA sitting in the operator's home was answered as no such file.
func TestTheCertificatePathsResolveTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rta test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"cert.pem": {Type: "CERTIFICATE", Bytes: der},
		"key.pem":  {Type: "PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(filepath.Join(home, name), pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, verr := tlsConfig(req(t, "etcd.overview", map[string]any{
		"ca-file": "~/cert.pem", "cert-file": "~/cert.pem", "key-file": "~/key.pem",
	}))
	if verr != nil {
		t.Fatalf("paths under ~ were refused: %s: %s", verr.Code, verr.Message)
	}
	if cfg.RootCAs == nil || len(cfg.Certificates) != 1 {
		t.Errorf("the CA or the client pair under ~ was not loaded: %+v", cfg)
	}
}

// Half of an mTLS pair is not a partial configuration — it is a connection
// that fails at handshake with an error naming neither file.
func TestHalfAnMTLSPairIsRefusedWithTheMissingHalfNamed(t *testing.T) {
	_, verr := tlsConfig(req(t, "etcd.overview", map[string]any{"cert-file": "/tmp/c.pem"}))
	if verr == nil || !strings.Contains(verr.Code, "key.missing") {
		t.Errorf("a certificate with no key was accepted: %v", verr)
	}
	_, verr = tlsConfig(req(t, "etcd.overview", map[string]any{"key-file": "/tmp/k.pem"}))
	if verr == nil || !strings.Contains(verr.Code, "cert.missing") {
		t.Errorf("a key with no certificate was accepted: %v", verr)
	}
	// Neither is a working configuration too, but it is a valid one: a cluster
	// with server-only TLS needs no client pair at all.
	if _, verr := tlsConfig(req(t, "etcd.overview", map[string]any{})); verr != nil {
		t.Errorf("server-only TLS was refused: %v", verr)
	}
}

// etcd speaks gRPC, so most failures arrive as a status code rather than a
// typed error, and the codes are the stable part. Each must produce a distinct
// code and a hint naming the next step.
func TestGRPCFailuresAreClassifiedByCode(t *testing.T) {
	r := req(t, "etcd.overview", map[string]any{"endpoint": "etcd-0.internal:2379"})
	cases := []struct {
		code codes.Code
		want string
	}{
		{codes.Unauthenticated, "etcd.auth.failed"},
		{codes.PermissionDenied, "etcd.denied"},
		{codes.Unavailable, "etcd.unavailable"},
		{codes.DeadlineExceeded, "etcd.timeout"},
	}
	for _, c := range cases {
		got := classify(status.Error(c.code, "server text"), r)
		if got.Code != c.want {
			t.Errorf("%s classified as %q, want %q", c.code, got.Code, c.want)
		}
		if strings.TrimSpace(got.Hint) == "" {
			t.Errorf("%s has no hint — the code alone does not say what to do next", c.code)
		}
	}
}

// The server sends gRPC statuses and the client hands back its own error type
// for them: rpctypes.Error is what it does on the way out. Classified as the
// status the server sent, a refused password and a role without permission
// were both "could not reach" the endpoint, with a hint about connection
// settings, against a cluster that had answered.
func TestWhatTheClientRaisesIsClassifiedByItsCode(t *testing.T) {
	r := req(t, "etcd.overview", map[string]any{"endpoint": "etcd-0.internal:2379"})
	for _, tc := range []struct {
		raised error
		want   string
	}{
		{rpctypes.ErrGRPCAuthFailed, "etcd.auth.failed"},
		{rpctypes.ErrGRPCUserEmpty, "etcd.auth.failed"},
		{rpctypes.ErrGRPCPermissionDenied, "etcd.denied"},
		{rpctypes.ErrGRPCNoLeader, "etcd.unavailable"},
	} {
		got := classify(rpctypes.Error(tc.raised), r)
		if got.Code != tc.want {
			t.Errorf("%v classified as %q, want %q", tc.raised, got.Code, tc.want)
		}
	}
	denied := classify(rpctypes.Error(rpctypes.ErrGRPCPermissionDenied), r)
	if !strings.Contains(denied.Message, "etcdserver: permission denied") || !strings.Contains(denied.Hint, "root role") {
		t.Errorf("denied = %q / %q", denied.Message, denied.Hint)
	}
}

// The failure people actually hit: pointing this at 2380, which is the peer
// port and will never answer a client. The hint has to say so, because nothing
// about the error does.
func TestTheWrongPortIsNamedInTheHint(t *testing.T) {
	r := req(t, "etcd.overview", map[string]any{"endpoint": "etcd-0.internal:2380"})
	got := classify(clientv3.ErrNoAvailableEndpoints, r)
	if got.Code != "etcd.unreachable" {
		t.Errorf("code = %q, want etcd.unreachable", got.Code)
	}
	if !strings.Contains(got.Hint, "2380") {
		t.Errorf("the hint does not mention the peer port: %q", got.Hint)
	}
}

// A cluster that has lost quorum accepts connections and answers nothing, so a
// timeout must not be reported as if the network were down.
func TestATimeoutPointsAtQuorumRatherThanTheNetwork(t *testing.T) {
	r := req(t, "etcd.overview", map[string]any{})
	got := classify(context.DeadlineExceeded, r)
	if got.Code != "etcd.timeout" {
		t.Errorf("code = %q, want etcd.timeout", got.Code)
	}
	if !strings.Contains(got.Hint, "quorum") {
		t.Errorf("the hint does not mention quorum: %q", got.Hint)
	}
}

// closedPort is an address nothing listens on: one the kernel handed out a
// moment ago and took back.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// listener serves each connection with serve, for as long as the test runs.
func listener(t *testing.T, serve func(stdnet.Conn)) string {
	t.Helper()
	l, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go serve(conn)
		}
	}()
	return l.Addr().String()
}

// connectRefusal connects with context.Background(), the context a CLI call
// has — cancelled by nothing but a signal — so an unbounded wait shows up as
// this test hanging rather than as a pass.
func connectRefusal(t *testing.T, values map[string]any, within time.Duration) (*view.Error, time.Duration) {
	t.Helper()
	started := time.Now()
	c, verr := connectWithin(context.Background(), req(t, "etcd.overview", values), within)
	took := time.Since(started)
	if verr == nil {
		_ = c.Close()
		t.Fatalf("connected to %v", values["endpoint"])
	}
	return verr, took
}

// A port nothing listens on is answered at once, as one: the client's own
// connection keeps its reason to itself, and waiting it out to report a
// timeout would name a cluster that answers nothing, not a port nobody is on.
func TestAClosedPortIsRefusedAtOnceAndNamed(t *testing.T) {
	endpoint := closedPort(t)
	verr, took := connectRefusal(t, map[string]any{"endpoint": endpoint}, 10*time.Second)
	if verr.Code != "etcd.conn.refused" || !strings.Contains(verr.Message, endpoint) {
		t.Errorf("got %s %q, want etcd.conn.refused naming %s", verr.Code, verr.Message, endpoint)
	}
	if took > 5*time.Second {
		t.Errorf("took %s to say nothing listens", took)
	}
}

// With a username, the client asks for a token before it hands itself back,
// and that request ends as a bare deadline when the connection never came up.
// The endpoint is still what is named, and why.
func TestAClosedPortWithCredentialsIsStillRefusedAsOne(t *testing.T) {
	endpoint := closedPort(t)
	verr, _ := connectRefusal(t, map[string]any{
		"endpoint": endpoint, "username": "root", "password": "hunter2",
	}, 300*time.Millisecond)
	if verr.Code != "etcd.conn.refused" || !strings.Contains(verr.Message, endpoint) {
		t.Errorf("got %s %q, want etcd.conn.refused naming %s", verr.Code, verr.Message, endpoint)
	}
}

// Something that takes the connection and never speaks is the case the bound
// exists for: nothing fails, so nothing but the bound ends the wait, and it
// ends it as the plugin's timeout naming the endpoint.
func TestAnEndpointThatNeverSpeaksIsATimeoutNamingIt(t *testing.T) {
	endpoint := listener(t, func(conn stdnet.Conn) {
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(io.Discard, conn)
	})
	verr, took := connectRefusal(t, map[string]any{"endpoint": endpoint}, 300*time.Millisecond)
	if verr.Code != "etcd.timeout" || !strings.Contains(verr.Message, endpoint) {
		t.Errorf("got %s %q, want etcd.timeout naming %s", verr.Code, verr.Message, endpoint)
	}
	if took > 5*time.Second {
		t.Errorf("a 300ms bound took %s", took)
	}
}

// A port that takes the connection and drops it, attempt after attempt, is
// something listening that does not speak etcd's client protocol — the peer
// port, or TLS the client is not using — and is named as that at the
// client's second attempt, not at the end of the bound as a firewall.
func TestAPortThatHangsUpIsNamedAsTheWrongOne(t *testing.T) {
	endpoint := listener(t, func(conn stdnet.Conn) { _ = conn.Close() })
	verr, took := connectRefusal(t, map[string]any{"endpoint": endpoint}, 10*time.Second)
	if verr.Code != "etcd.conn.protocol" || !strings.Contains(verr.Message, endpoint) {
		t.Errorf("got %s %q, want etcd.conn.protocol naming %s", verr.Code, verr.Message, endpoint)
	}
	if !strings.Contains(verr.Hint, "2380") || !strings.Contains(verr.Hint, "--tls") {
		t.Errorf("hint = %q, want the peer port and --tls named", verr.Hint)
	}
	if took > 5*time.Second {
		t.Errorf("took %s to name a port that hangs up", took)
	}
}

// TLS to a port serving plaintext is hung up on mid-handshake, which arrives
// as the same error a port nobody is on gives; it is named as the handshake
// it is, with what turns TLS on.
func TestTLSToAPlaintextPortIsNamedAsTheHandshake(t *testing.T) {
	endpoint := listener(t, func(conn stdnet.Conn) { _ = conn.Close() })
	verr, _ := connectRefusal(t, map[string]any{"endpoint": endpoint, "tls": true}, 10*time.Second)
	if verr.Code != "etcd.tls.failed" || !strings.Contains(verr.Message, endpoint) {
		t.Errorf("got %s %q, want etcd.tls.failed naming %s", verr.Code, verr.Message, endpoint)
	}
	if !strings.Contains(verr.Hint, "an https:// endpoint turns it on, as do --tls, --ca-file, --cert-file and --tls-server-name") {
		t.Errorf("hint = %q, want every setting that turns TLS on named", verr.Hint)
	}
}

// A certificate nothing here trusts fails the client's handshake over and
// over, silently, and was a wait that never ended. It is named as what it is,
// at once.
func TestAnUntrustedCertificateIsNamedRatherThanWaitedOn(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	endpoint := srv.Listener.Addr().String()
	verr, took := connectRefusal(t, map[string]any{"endpoint": endpoint, "tls": true}, 10*time.Second)
	if verr.Code != "etcd.tls.untrusted" || !strings.Contains(verr.Message, endpoint) {
		t.Errorf("got %s %q, want etcd.tls.untrusted naming %s", verr.Code, verr.Message, endpoint)
	}
	if took > 5*time.Second {
		t.Errorf("took %s to name the certificate", took)
	}
}

// macOS verifies against the system's trust store itself and reports a chain
// it cannot anchor as a bare error, never as x509.UnknownAuthorityError: that
// is still a certificate nothing here trusts. Every other verdict it gives
// untyped is its own reason and is quoted in its words: a revoked certificate
// answered with the CA file to name was answered with the way around the
// revocation check. Elsewhere an untyped verdict is never a question of
// trust. A name or a date that fails is a refusal of its own too, and none is
// a port serving plaintext.
func TestACertificateThatFailsVerificationIsNamedForWhy(t *testing.T) {
	r := req(t, "etcd.overview", map[string]any{"endpoint": "etcd-0.internal:2379"})
	open, closing := string(rune(0x201c)), string(rune(0x201d))
	onMac := "etcd.tls.rejected"
	if runtime.GOOS == "darwin" {
		onMac = "etcd.tls.untrusted"
	}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"Go's own verifier", x509.UnknownAuthorityError{}, "etcd.tls.untrusted"},
		{"no roots to read", x509.SystemRootsError{}, "etcd.tls.untrusted"},
		{"the system's verifier, a chain it cannot anchor",
			errors.New("x509: " + open + "etcd-0" + closing + " certificate is not trusted"), onMac},
		{"the system's verifier, a revoked certificate",
			errors.New("x509: " + open + "etcd-0" + closing + " certificate is revoked"), "etcd.tls.rejected"},
		{"the system's verifier, a name that ends like the untrusted verdict",
			errors.New("x509: " + open + "a" + closing + " certificate is not trusted" + closing +
				" certificate is revoked"), "etcd.tls.rejected"},
		{"the system's verifier, a policy it holds the certificate to",
			errors.New("x509: " + open + "etcd-0" + closing + " certificate is not standards compliant"), "etcd.tls.rejected"},
		{"a name it is not for", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "etcd-0.internal"}, "etcd.tls.rejected"},
		{"a date it is not valid on", x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired}, "etcd.tls.rejected"},
		// Go's verifier types each reason of its own, and no CA cures one.
		{"a signature algorithm it refuses", x509.InsecureAlgorithmError(x509.SHA1WithRSA), "etcd.tls.rejected"},
		{"a critical extension it does not handle", x509.UnhandledCriticalExtension{}, "etcd.tls.rejected"},
	} {
		err := handshakeError{&tls.CertificateVerificationError{Err: tc.err}}
		got := classify(err, r)
		if got.Code != tc.want {
			t.Errorf("%s: classified %s, want %s", tc.name, got.Code, tc.want)
		}
		if got.Code == "etcd.tls.rejected" && !strings.Contains(got.Message, tc.err.Error()) {
			t.Errorf("%s: %q does not quote the verdict", tc.name, got.Message)
		}
	}
}

// The hint for a certificate nothing here trusts says what naming a CA file
// costs, since the cure is the one that runs another check.
func TestAnUntrustedCertificateSaysWhatACAFileReplaces(t *testing.T) {
	got := classify(x509.UnknownAuthorityError{}, req(t, "etcd.overview", nil))
	if !strings.Contains(got.Hint, "--ca-file (a self-signed certificate is its own CA)") ||
		!strings.Contains(got.Hint, "a CA file replaces the system's") {
		t.Errorf("hint = %q, want the CA file named and what it replaces", got.Hint)
	}
}

// A dial that found no way to the host reached nothing that could refuse it,
// and is not a port nobody is on; one the host refused still is.
func TestAHostNoRouteReachesIsNotAPortNobodyIsOn(t *testing.T) {
	r := req(t, "etcd.overview", map[string]any{"endpoint": "10.0.0.9:2379"})
	dial := func(errno syscall.Errno) error {
		return &stdnet.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
	}
	for errno, want := range map[syscall.Errno]string{
		syscall.ENETUNREACH:  "etcd.unreachable",
		syscall.EHOSTUNREACH: "etcd.unreachable",
		syscall.EHOSTDOWN:    "etcd.unreachable",
		syscall.ECONNREFUSED: "etcd.conn.refused",
	} {
		got := classify(dial(errno), r)
		if got.Code != want || !strings.Contains(got.Message, "10.0.0.9:2379") {
			t.Errorf("%v: %s %q, want %s naming the endpoint", errno, got.Code, got.Message, want)
		}
		if want == "etcd.unreachable" && !strings.Contains(got.Message, errno.Error()) {
			t.Errorf("%v: %q does not say why", errno, got.Message)
		}
	}
}

// A failed dial is read by the operating system's error it carries, not by the
// *net.OpError every failed dial is: one that timed out or was reset reached
// no port that refused it. A driver's flattened text is read by the words the
// errno has on this machine, and a name that did not resolve stays that,
// whatever its resolver's own failed exchange said.
func TestADialIsReadByTheErrorItCarries(t *testing.T) {
	r := req(t, "etcd.overview", map[string]any{"endpoint": "10.0.0.9:2379"})
	dial := func(errno syscall.Errno) error {
		return &stdnet.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
	}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"a dial that timed out", dial(syscall.ETIMEDOUT), "etcd.conn.failed"},
		{"a connection reset", dial(syscall.ECONNRESET), "etcd.conn.failed"},
		{"a refusal flattened into text", errors.New(`connection error: desc = "transport: Error while dialing: ` +
			`dial tcp 10.0.0.9:2379: connect: ` + syscall.ECONNREFUSED.Error() + `"`), "etcd.conn.refused"},
		{"no route flattened into text", errors.New("dial tcp 10.0.0.9:2379: connect: " +
			syscall.EHOSTUNREACH.Error()), "etcd.unreachable"},
		{"a name whose resolver refused", &stdnet.OpError{Op: "dial", Net: "tcp", Err: &stdnet.DNSError{
			Err: "dial udp 10.0.0.53:53: connect: " + syscall.ECONNREFUSED.Error(), Name: "etcd-0.internal"}},
			"etcd.host.unknown"},
	} {
		if got := classify(tc.err, r); got.Code != tc.want {
			t.Errorf("%s: classified %s %q, want %s", tc.name, got.Code, got.Message, tc.want)
		}
	}
}

// A classified error must not be re-wrapped, or the specific answer is buried
// under a generic one.
func TestClassifyReturnsAlreadyClassifiedErrorsUnchanged(t *testing.T) {
	original := view.Errorf("etcd.something.specific", "a precise message")
	got := classify(errors.New("wrapper: "+original.Error()), req(t, "etcd.overview", map[string]any{}))
	if got.Code == "etcd.something.specific" {
		t.Fatal("the fixture does not actually wrap — this test proves nothing")
	}
	got = classify(original, req(t, "etcd.overview", map[string]any{}))
	if got.Code != "etcd.something.specific" {
		t.Errorf("a classified error was re-wrapped as %q", got.Code)
	}
}

// The client port is what a bare host means, as the input's help says: etcd's
// client has none of its own, and dials a bare host as it stands.
func TestAnEndpointWithNoPortIsTheClientPort(t *testing.T) {
	for given, want := range map[string]string{
		"127.0.0.1:2379":               "127.0.0.1:2379",
		"127.0.0.1":                    "127.0.0.1:2379",
		"etcd-0.internal":              "etcd-0.internal:2379",
		"::1":                          "[::1]:2379",
		"[::1]":                        "[::1]:2379",
		"[::1]:2380":                   "[::1]:2380",
		"https://etcd-0.internal":      "https://etcd-0.internal:2379",
		"http://etcd-0.internal:4001/": "http://etcd-0.internal:4001/",
		"https://[::1]/":               "https://[::1]:2379/",
		"unix:///run/etcd.sock":        "unix:///run/etcd.sock",
		"unix:etcd.sock":               "unix:etcd.sock",
		"[::1":                         "[::1",
	} {
		if got := endpointOf(req(t, "etcd.overview", map[string]any{"endpoint": given})); got != want {
			t.Errorf("%q became %q, want %q", given, got, want)
		}
	}
}
