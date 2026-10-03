package main

import (
	"context"
	stdnet "net"
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
