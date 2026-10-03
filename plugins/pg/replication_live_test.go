//go:build livepg

package main

import (
	"context"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/this-is-tobi/rta/pkg/view"
)

// Against a real server, whichever role it plays: the same setup as the dump
// tests (the livepg tag, RTA_TEST_PG_PORT and RTA_TEST_PG_PASSWORD).
// What it holds is the part no fixture can — that the SQL is accepted by the
// server version it is pointed at and that every column scans into the shape
// the reader expects, which is where a version's differing catalogue shows.
func TestReplicationReadsARealServer(t *testing.T) {
	ctx := context.Background()
	req := reqFor(t, "pg.replication", liveValues(t, nil))
	v, err := withConn(ctx, req, func(ctx context.Context, conn *pgx.Conn) (view.View, error) {
		return replicationView(ctx, conn, req)
	})
	if err != nil {
		t.Fatal(err)
	}
	page, ok := v.(view.Sections)
	if !ok || len(page.Items) == 0 {
		t.Fatalf("view = %#v, want a page", v)
	}
	server, ok := page.Items[0].View.(view.KeyValue)
	if !ok {
		t.Fatalf("first section = %#v, want key/value", page.Items[0].View)
	}
	got := map[string]string{}
	for _, p := range server.Pairs {
		got[p.Key] = p.Value
	}
	lsn := regexp.MustCompile(`^[0-9A-F]+/[0-9A-F]+$`)
	switch got["role"] {
	case "primary":
		if !lsn.MatchString(got["WAL position"]) || !regexp.MustCompile(`^[0-9A-F]{24}$`).MatchString(got["WAL file"]) {
			t.Errorf("primary position = %q in %q", got["WAL position"], got["WAL file"])
		}
	case "standby":
		if got["received"] != "-" && !lsn.MatchString(got["received"]) {
			t.Errorf("standby received = %q", got["received"])
		}
	default:
		t.Errorf("role = %q, want primary or standby", got["role"])
	}
	if got["replication"] == "" {
		t.Error("no replication line")
	}
}
