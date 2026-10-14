package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	stdnet "net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// memberListServer answers the one call askMembers makes of the endpoint, with
// members that are whatever the test says.
type memberListServer struct {
	etcdserverpb.UnimplementedClusterServer
	members []*etcdserverpb.Member
}

func (s *memberListServer) MemberList(context.Context, *etcdserverpb.MemberListRequest) (*etcdserverpb.MemberListResponse, error) {
	return &etcdserverpb.MemberListResponse{Members: s.members}, nil
}

func serveGRPC(t *testing.T, register func(*grpc.Server), opts ...grpc.ServerOption) string {
	t.Helper()
	lis, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(opts...)
	register(srv)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// A member list is data the cluster supplies, and etcd's client dials an
// http:// URL in the clear whatever TLS the connection that fetched the list
// had, attaching the auth token to the call. A member that advertises one is
// not asked while the call carries a username, which is what kept the token
// of a secured cluster off the wire: measured against this server, which
// records the token any call brings.
func TestAMemberAdvertisingAPlaintextURLIsNotHandedTheToken(t *testing.T) {
	var mu sync.Mutex
	var tokens []string
	plain := serveGRPC(t, func(*grpc.Server) {}, grpc.UnknownServiceHandler(func(_ any, st grpc.ServerStream) error {
		md, _ := metadata.FromIncomingContext(st.Context())
		mu.Lock()
		tokens = append(tokens, md.Get("token")...)
		mu.Unlock()
		return status.Error(codes.Unavailable, "recorded")
	}))
	const selfID, otherID = 1, 2
	list := serveGRPC(t, func(s *grpc.Server) {
		etcdserverpb.RegisterClusterServer(s, &memberListServer{members: []*etcdserverpb.Member{
			{ID: selfID, Name: "eh1", ClientURLs: []string{"http://127.0.0.1:1"}},
			{ID: otherID, Name: "eh2", ClientURLs: []string{"http://" + plain}},
		}})
	})
	c, err := clientv3.New(clientv3.Config{Endpoints: []string{list}, DialTimeout: time.Second, Token: "SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	self := &clientv3.StatusResponse{Header: &etcdserverpb.ResponseHeader{MemberId: selfID}}
	rq := req(t, "etcd.overview", map[string]any{"username": "monitor", "endpoint": "https://etcd.internal:2379"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, verr := askMembers(ctx, c, rq, self)
	if verr != nil {
		t.Fatal(verr)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(tokens) != 0 {
		t.Errorf("the token reached a plaintext member URL: %v", tokens)
	}
	var other *memberRow
	for i := range rows {
		if rows[i].id == otherID {
			other = &rows[i]
		}
	}
	if other == nil || !other.notAsked || other.st != nil {
		t.Fatalf("the plaintext member = %+v, want withheld and not asked", other)
	}
	if got := memberHealth(*other, leaderView{}); got != "info — not asked: advertises http://"+plain+
		", which is not https://, so the credentials are not sent to it" {
		t.Errorf("health = %q", got)
	}
}

func TestTheTokenSafetyOfAMemberURL(t *testing.T) {
	for _, tc := range []struct {
		name     string
		username string
		url      string
		want     bool
	}{
		{"no username asks http", "", "http://eh2:2379", true},
		{"no username asks https", "", "https://eh2:2379", true},
		{"a username asks https", "monitor", "https://eh2:2379", true},
		{"a username asks https in any case", "monitor", "HTTPS://eh2:2379", true},
		{"a username does not ask http", "monitor", "http://eh2:2379", false},
		{"a username does not ask a bare address", "monitor", "eh2:2379", false},
	} {
		rq := req(t, "etcd.overview", map[string]any{"username": tc.username})
		if got := tokenSafeAt(rq, tc.url); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// etcd's client drops TLS for an http:// endpoint, so a CA, a certificate, a
// name or the switch given beside one is a setting that checks nothing, and the
// password goes in the clear in the first request while the call reads as
// verified. Refused before anything is dialled: measured against a listener
// that counts the connections it is offered, none is.
func TestATLSSettingBesideAnHTTPEndpointIsRefusedBeforeAnythingIsDialled(t *testing.T) {
	lis, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	var mu sync.Mutex
	dialled := 0
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			dialled++
			mu.Unlock()
			_ = conn.Close()
		}
	}()
	endpoint := "http://" + lis.Addr().String()
	// A CA that is there and valid: with the path of a file nobody wrote, a call
	// that was not refused for its scheme failed earlier, reading the file, and
	// the count of connections below proved nothing for the case it is there for.
	ca := writeCAFile(t)

	for _, tc := range []struct {
		name   string
		values map[string]any
		code   string
		naming string
	}{
		{"a CA", map[string]any{"ca-file": ca}, "etcd.tls.ca.unused", "--ca-file names a CA"},
		{"the switch", map[string]any{"tls": true}, "etcd.tls.plaintext", "--tls asks for TLS"},
		{"a client certificate", map[string]any{"cert-file": ca}, "etcd.tls.plaintext", "--cert-file asks for TLS"},
		{"a name", map[string]any{"tls-server-name": "etcd.internal"}, "etcd.tls.plaintext", "--tls-server-name asks for TLS"},
	} {
		values := map[string]any{"endpoint": endpoint, "username": "root", "password": "hunter2"}
		for k, v := range tc.values {
			values[k] = v
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, verr := connectWithin(ctx, req(t, "etcd.overview", values), 2*time.Second)
		cancel()
		if verr == nil || verr.Code != tc.code || !strings.Contains(verr.Message, tc.naming) ||
			!strings.Contains(verr.Message, endpoint) || !strings.Contains(verr.Hint, "an https:// endpoint") ||
			!strings.Contains(verr.Hint, "to call a plain server, clear "+strings.Fields(tc.naming)[0]) {
			t.Errorf("%s: %+v, want %s naming %q and the endpoint, and the way to TLS", tc.name, verr, tc.code, tc.naming)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if dialled != 0 {
		t.Errorf("%d connections reached the plain-HTTP endpoint before the refusal", dialled)
	}
}

// writeCAFile writes a self-signed CA certificate as PEM to a file in the
// test's own directory and returns its path: a CA that a call which was not
// refused would read and trust.
func writeCAFile(t *testing.T) string {
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
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A bare host:port is the way to TLS from the settings, and is not refused: it
// is the form a forward's address takes, and an https:// endpoint is TLS
// outright. The call goes on to read the CA file, which is not there.
func TestATLSSettingBesideABareOrHTTPSEndpointIsNotRefusedForItsScheme(t *testing.T) {
	for _, endpoint := range []string{"etcd.internal:2379", "https://etcd.internal:2379"} {
		rq := req(t, "etcd.overview", map[string]any{"endpoint": endpoint, "ca-file": "/nonexistent/ca.pem"})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, verr := connectWithin(ctx, rq, time.Second)
		cancel()
		if verr == nil || verr.Code != "etcd.tls.ca.unreadable" {
			t.Errorf("%s: %+v, want the call to go on to the CA file, etcd.tls.ca.unreadable", endpoint, verr)
		}
	}
}

// An http:// endpoint is a plaintext connection whatever tls says, since
// etcd's client drops TLS for that scheme, so a username over one is not over
// TLS and the other members are not asked.
func TestAnHTTPEndpointIsNotOverTLSWhateverTLSSays(t *testing.T) {
	rq := req(t, "etcd.overview", map[string]any{"username": "monitor", "tls": true, "endpoint": "http://etcd.internal:2379"})
	if overTLS(rq) {
		t.Error("an http:// endpoint with tls on was read as TLS")
	}
	if whyNotAsked(rq) == "" {
		t.Error("the members were asked over a plaintext endpoint")
	}
}
