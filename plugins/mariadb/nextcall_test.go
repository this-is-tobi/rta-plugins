package main

import (
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A refusal the server gave names the server as the reader reaches it again.
// Through a forward the address the driver dialled is 127.0.0.1 and a port
// that closed with the call, and "127.0.0.1:54321 rejected user" named nothing
// the reader could change.
func TestARefusalTheServerGaveNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	r := plugin.NewRequest(map[string]any{"host": "127.0.0.1", "port": 54321, "user": "app", "database": "shop"},
		false, false).WithProfile("prod", plugin.TunnelKube)
	for name, number := range map[string]uint16{"credentials": 1045, "no database": 1049, "host": 1130} {
		t.Run(name, func(t *testing.T) {
			got := classify(&mysql.MySQLError{Number: number, Message: "server text"}, r)
			if !strings.Contains(got.Message, "profile prod (through its kube: forward)") ||
				strings.Contains(got.Message, "54321") {
				t.Errorf("message = %q, want the profile and its forward, not the forward's end", got.Message)
			}
		})
	}
}

// The listing a refusal offers is a call, and it reaches the server the
// refused call reached: through a profile's forward the host and port were
// 127.0.0.1 and a port that closed with the call, so the profile is what names
// the server, and reached directly the address and the account stay beside it.
// Spelled bare, it listed whatever the configuration where it was pasted names,
// and an agent was given a call that named no profile at all.
func TestTheListingARefusalOffersReachesTheServerItCameFrom(t *testing.T) {
	missing := &mysql.MySQLError{Number: 1049, Message: "Unknown database"}
	for _, tc := range []struct {
		name    string
		values  map[string]any
		tunnel  plugin.Tunnel
		profile string
		surface plugin.Surface
		want    string
		unwant  string
	}{
		{"a terminal, no profile", map[string]any{"host": "db.internal", "port": 3307, "user": "app", "database": "shop"},
			plugin.TunnelNone, "", plugin.SurfaceCLI,
			"`rta mariadb database list --host db.internal --port 3307 --user app --database shop` shows what is there", "--profile"},
		{"a terminal, through a forward", map[string]any{"host": "127.0.0.1", "port": 54321, "user": "app", "database": "shop"},
			plugin.TunnelKube, "prod", plugin.SurfaceCLI,
			"`rta mariadb database list --profile prod --user app --database shop` shows what is there", "--host"},
		{"a terminal, with TLS the server insists on", map[string]any{"host": "db.internal", "user": "app", "tls": "verify-ca",
			"ca-file": "/etc/mariadb/ca.pem"},
			plugin.TunnelNone, "", plugin.SurfaceCLI, "--tls verify-ca --ca-file /etc/mariadb/ca.pem", ""},
		{"an agent, through a profile", map[string]any{"host": "127.0.0.1"},
			plugin.TunnelKube, "prod", plugin.SurfaceMCP,
			"`mariadb_database_list {\"profile\":\"prod\"}` shows what is there", "host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t, "mariadb.overview", tc.values).WithProfile(tc.profile, tc.tunnel).WithSurface(tc.surface)
			verr := classify(missing, r)
			if !strings.Contains(verr.Hint, tc.want) || (tc.unwant != "" && strings.Contains(verr.Hint, tc.unwant)) {
				t.Errorf("hint = %q, want %q and not %q", verr.Hint, tc.want, tc.unwant)
			}
		})
	}
}
