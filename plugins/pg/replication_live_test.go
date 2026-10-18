//go:build livepg

package main

import (
	"context"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Against a real server, whichever role it plays: the same setup as the dump
// tests (the livepg tag, RTA_TEST_PG_PORT and RTA_TEST_PG_PASSWORD).
// What it holds is the part no fixture can — that the SQL is accepted by the
// server version it is pointed at and that every column scans into the shape
// the reader expects, which is where a version's differing catalogue shows.
func TestReplicationReadsARealServer(t *testing.T) {
	ctx := context.Background()
	req := reqFor(t, "pg.replication.status", liveValues(t, nil))
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

// The position line of pg.status and of the overview's glance against a real
// server, whichever role it plays, as the role the setup names and — on a
// primary, where a role can be made — as one with no membership at all, which
// is the claim the line's quiet degrading rests on: that the functions behind
// it are open to every role.
func TestThePositionLineReadsARealServerAsAnyRole(t *testing.T) {
	ctx := context.Background()
	primary := regexp.MustCompile(`^[0-9A-F]+/[0-9A-F]+, timeline [0-9]+, file [0-9A-F]{24}$`)
	standby := regexp.MustCompile(`^(received [0-9A-F]+/[0-9A-F]+, )?replayed [0-9A-F]+/[0-9A-F]+$`)

	positionAs := func(t *testing.T, id string, values map[string]any, run func(context.Context, querier, plugin.Request) (view.View, error)) string {
		t.Helper()
		req := reqFor(t, id, liveValues(t, values))
		v, err := withConn(ctx, req, func(ctx context.Context, conn *pgx.Conn) (view.View, error) {
			return run(ctx, conn, req)
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range v.(view.KeyValue).Pairs {
			if p.Key == "WAL position" {
				return p.Value
			}
		}
		t.Fatalf("%s has no WAL position line: %v", id, v)
		return ""
	}
	check := func(t *testing.T, role string, line string) {
		t.Helper()
		want := primary
		if role == "standby" {
			want = standby
		}
		if !want.MatchString(line) {
			t.Errorf("a %s's position line is %q, want it to match %s", role, line, want)
		}
	}

	conn, verr := connect(ctx, reqFor(t, "pg.status", liveValues(t, nil)))
	if verr != nil {
		t.Fatal(verr)
	}
	kind, err := roleOf(ctx, conn)
	_ = conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("as the role the setup names", func(t *testing.T) {
		check(t, kind, positionAs(t, "pg.status", nil, statusView))
		check(t, kind, positionAs(t, "pg.overview", nil, compactOverview))
	})
	t.Run("as a role with no membership", func(t *testing.T) {
		if kind != "primary" {
			t.Skip("a standby cannot create the role")
		}
		const plain = "rta_position_probe"
		admin(t, "postgres", "drop role if exists "+plain)
		admin(t, "postgres", "create role "+plain+" login password 'probe'")
		t.Cleanup(func() { admin(t, "postgres", "drop role if exists "+plain) })
		as := map[string]any{"user": plain, "password": "probe"}
		check(t, kind, positionAs(t, "pg.status", as, statusView))
		check(t, kind, positionAs(t, "pg.overview", as, compactOverview))
	})
}
