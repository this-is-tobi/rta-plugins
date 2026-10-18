package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"

	"github.com/this-is-tobi/rta/pkg/view"
)

// nothingStored is a cluster that holds no key and grants no lease.
type nothingStored struct {
	etcdserverpb.UnimplementedKVServer
	etcdserverpb.UnimplementedLeaseServer
}

func (nothingStored) Range(context.Context, *etcdserverpb.RangeRequest) (*etcdserverpb.RangeResponse, error) {
	return &etcdserverpb.RangeResponse{Header: &etcdserverpb.ResponseHeader{}}, nil
}

func (nothingStored) LeaseLeases(context.Context, *etcdserverpb.LeaseLeasesRequest) (*etcdserverpb.LeaseLeasesResponse, error) {
	return &etcdserverpb.LeaseLeasesResponse{Header: &etcdserverpb.ResponseHeader{}}, nil
}

// An empty listing is still its table (docs: writing a plugin): a program reads
// zero rows under the columns it knows, and a person is told what the nothing
// is. Neither of these said anything: a bare grid on a terminal.
func TestAnEmptyListingIsItsTable(t *testing.T) {
	addr := serveGRPC(t, func(s *grpc.Server) {
		etcdserverpb.RegisterKVServer(s, nothingStored{})
		etcdserverpb.RegisterLeaseServer(s, nothingStored{})
	})
	c, err := clientv3.New(clientv3.Config{Endpoints: []string{addr}, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for name, tc := range map[string]struct {
		run      func() (view.View, error)
		sentence string
	}{
		"etcd.kv.list": {func() (view.View, error) {
			return kvListView(ctx, c, req(t, "etcd.kv.list", map[string]any{"endpoint": addr}))
		}, "no keys"},
		"etcd.kv.list under a prefix": {func() (view.View, error) {
			return kvListView(ctx, c, req(t, "etcd.kv.list", map[string]any{"endpoint": addr, "prefix": "/registry/"}))
		}, `no keys under "/registry/"`},
		"etcd.lease.list": {func() (view.View, error) {
			return leaseTable(ctx, c, req(t, "etcd.lease.list", map[string]any{"endpoint": addr}))
		}, "no leases"},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := tc.run()
			if err != nil {
				t.Fatal(err)
			}
			tbl, ok := v.(view.Table)
			if !ok {
				t.Fatalf("an empty answer is a %T, want the table", v)
			}
			if len(tbl.Rows) != 0 || tbl.Total != 0 || len(tbl.Columns) == 0 {
				t.Errorf("table = %d columns, %d rows, total %d, want its columns and no rows",
					len(tbl.Columns), len(tbl.Rows), tbl.Total)
			}
			if tbl.Empty != tc.sentence {
				t.Errorf("empty sentence = %q, want %q", tbl.Empty, tc.sentence)
			}
			raw, err := view.Marshal(view.Envelope{View: v})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"rows":[]`) || !strings.Contains(string(raw), `"type":"table"`) {
				t.Errorf("an agent reads %s, want a table with rows []", raw)
			}
		})
	}
}
