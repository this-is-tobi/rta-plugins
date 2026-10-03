package main

import (
	"context"
	stdnet "net"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
)

// recordingCluster answers the member list and keeps what the request asked
// for, which is the only place the consistency a read wants is visible.
type recordingCluster struct {
	etcdserverpb.UnimplementedClusterServer
	linearizable chan bool
}

func (r recordingCluster) MemberList(_ context.Context, req *etcdserverpb.MemberListRequest) (*etcdserverpb.MemberListResponse, error) {
	r.linearizable <- req.GetLinearizable()
	return &etcdserverpb.MemberListResponse{Header: &etcdserverpb.ResponseHeader{}}, nil
}

// A linearizable member list needs a quorum, which is the one thing a cluster
// being looked at because it lost it does not have: the overview waited out
// its whole context there, and printed nothing. The list is asked of the
// endpoint's own copy.
func TestTheMemberListIsReadWithoutAskingTheClusterToAgree(t *testing.T) {
	l, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	asked := make(chan bool, 1)
	srv := grpc.NewServer()
	etcdserverpb.RegisterClusterServer(srv, recordingCluster{linearizable: asked})
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(srv.Stop)

	c, err := clientv3.New(clientv3.Config{Endpoints: []string{l.Addr().String()}, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := memberList(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got := <-asked; got {
		t.Error("the member list was requested linearizable, so it waits for a quorum")
	}
}
