package main

import (
	"strings"
	"testing"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A listing that stopped has to be continuable. It named the last key it
// showed in a bare cursor and declared no input to pass it to, so an agent
// that read "there is more" had no call that reached it: a prefix is a range
// of its own, never a starting point inside one.
func TestAStoppedListingCanBeContinued(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.ID != "etcd.kv.list" {
			continue
		}
		for _, f := range c.Inputs {
			if f.Name == "after" && f.Type == plugin.String && !f.Local && !f.Required {
				return
			}
		}
		t.Fatalf("etcd.kv.list declares no optional `after` input to continue from: %+v", c.Inputs)
	}
	t.Fatal("etcd.kv.list is not declared")
}

// The next page starts at the first key past `after` and stays inside the
// prefix: a range that ran to the end of the keyspace would return every
// sibling prefix's keys as if they were under this one.
func TestTheNextPageStartsPastTheLastKeyAndStaysInThePrefix(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, after string
		key                 string
		fromKey, prefixed   bool
		end                 string
	}{
		{"first page of everything", "", "", "\x00", true, false, ""},
		{"first page of a prefix", "/registry/", "", "/registry/", false, true, ""},
		{"next page of everything", "", "/a", "/a\x00", true, false, ""},
		{"next page of a prefix", "/registry/", "/registry/svc/k1", "/registry/svc/k1\x00", false, false, "/registry0"},
		{"after before the prefix is the prefix", "/registry/", "/a", "/registry/", false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, opts := keyFetchOptions(tc.prefix, tc.after, 10)
			op := clientv3.OpGet(key, opts...)
			if key != tc.key {
				t.Errorf("key = %q, want %q", key, tc.key)
			}
			if op.IsOptsWithFromKey() != tc.fromKey || op.IsOptsWithPrefix() != tc.prefixed {
				t.Errorf("from-key = %v, prefix = %v, want %v and %v", op.IsOptsWithFromKey(), op.IsOptsWithPrefix(),
					tc.fromKey, tc.prefixed)
			}
			if tc.end != "" && string(op.RangeBytes()) != tc.end {
				t.Errorf("range end = %q, want %q", op.RangeBytes(), tc.end)
			}
			if !op.IsKeysOnly() || op.Limit() != 11 {
				t.Errorf("keys-only = %v, limit = %d: a continued page must stay names-only and one past the bound",
					op.IsKeysOnly(), op.Limit())
			}
		})
	}
}

// A page that stopped says so where a reader of the table looks, and names the
// input that continues it the way the surface reading it gives one.
func TestAPageThatStoppedNamesTheCallThatContinuesIt(t *testing.T) {
	kvs := []*mvccpb.KeyValue{{Key: []byte("/a"), Version: 1}, {Key: []byte("/b"), Version: 1}, {Key: []byte("/c"), Version: 1}}
	tbl := kvListTable(kvs, 2, plugin.SurfaceMCP)
	if len(tbl.Rows) != 2 || tbl.Page == nil || tbl.Page.Next != "/b" {
		t.Fatalf("rows = %d, page = %+v, want two rows and a cursor at /b", len(tbl.Rows), tbl.Page)
	}
	if len(tbl.Warnings) != 1 || tbl.Warnings[0].Code != "etcd.kv.list.partial" {
		t.Fatalf("warnings = %+v, want the one that says it stopped", tbl.Warnings)
	}
	if hint := tbl.Warnings[0].Hint; !strings.Contains(hint, `the "after" argument set to "/b"`) {
		t.Errorf("hint = %q, want the argument that continues it", hint)
	}

	whole := kvListTable(kvs, 3, plugin.SurfaceMCP)
	if whole.Page != nil || len(whole.Warnings) != 0 || len(whole.Rows) != 3 {
		t.Errorf("a page that ends on the boundary reported more: %+v", whole)
	}
}
