package main

import (
	"context"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

type statusServer struct {
	etcdserverpb.UnimplementedMaintenanceServer
}

func (statusServer) Status(context.Context, *etcdserverpb.StatusRequest) (*etcdserverpb.StatusResponse, error) {
	return &etcdserverpb.StatusResponse{
		Header:  &etcdserverpb.ResponseHeader{MemberId: 1, Revision: 3},
		Version: "3.5.21", Leader: 1, RaftTerm: 2, RaftIndex: 9, RaftAppliedIndex: 9,
		DbSize: 4096, DbSizeInUse: 4096,
	}, nil
}

// The endpoint row is what a page kept or pasted into a ticket says about the
// server, and through a forward the address is 127.0.0.1 and a port that closed
// with the call. It names the server the way its reader reaches it again.
func TestTheOverviewNamesTheProfileNotTheEndOfItsForward(t *testing.T) {
	addr := serveGRPC(t, func(s *grpc.Server) {
		etcdserverpb.RegisterClusterServer(s, &memberListServer{members: []*etcdserverpb.Member{
			{ID: 1, Name: "eh1", ClientURLs: []string{"http://eh1:2379"}},
		}})
		etcdserverpb.RegisterMaintenanceServer(s, statusServer{})
	})
	c, err := clientv3.New(clientv3.Config{Endpoints: []string{addr}, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for name, tc := range map[string]struct {
		profile string
		tunnel  plugin.Tunnel
		want    string
	}{
		"direct":    {"", plugin.TunnelNone, addr},
		"a profile": {"prod", plugin.TunnelNone, addr + " (profile prod)"},
		"a forward": {"prod", plugin.TunnelKube, "profile prod (through its kube: forward)"},
	} {
		t.Run(name, func(t *testing.T) {
			r := req(t, "etcd.overview", map[string]any{"endpoint": addr})
			if tc.profile != "" {
				r = r.WithProfile(tc.profile, tc.tunnel)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			v, err := overviewView(ctx, c, r)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			for _, s := range v.(view.Sections).Items {
				if s.Key() != "status" {
					continue
				}
				for _, p := range s.View.(view.KeyValue).Pairs {
					if p.Key == "endpoint" {
						got = p.Value
					}
				}
			}
			if got != tc.want {
				t.Errorf("endpoint row = %q, want %q", got, tc.want)
			}
		})
	}
}
