package main

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The listing a refusal offers is a call, and it reaches the server the
// refused call reached: through a profile's forward the host and port were
// 127.0.0.1 and a port that closed with the call, so the profile is what names
// the server, and reached directly the address, the role and the database stay
// beside it. Spelled bare, it listed whatever the configuration where it was
// pasted names, and an agent was given a call with an argument it cannot give.
func TestTheListingARefusalOffersReachesTheServerItCameFrom(t *testing.T) {
	missing := &pgconn.PgError{Code: "3D000"}
	for _, tc := range []struct {
		name    string
		values  map[string]any
		tunnel  plugin.Tunnel
		profile string
		surface plugin.Surface
		want    string
		unwant  string
	}{
		{"a terminal, no profile", map[string]any{"host": "db.internal", "port": 6432, "user": "app", "database": "shop"},
			plugin.TunnelNone, "", plugin.SurfaceCLI,
			"`rta pg database list --host db.internal --port 6432 --user app --database shop` shows what is there", "--profile"},
		{"a terminal, through a forward", map[string]any{"host": "127.0.0.1", "port": 54321, "user": "app", "database": "shop"},
			plugin.TunnelKube, "prod", plugin.SurfaceCLI,
			"`rta pg database list --profile prod --user app --database shop` shows what is there", "--host"},
		{"a terminal, a profile reached directly", map[string]any{"host": "db.internal", "user": "app", "database": "shop"},
			plugin.TunnelNone, "prod", plugin.SurfaceCLI,
			"`rta pg database list --profile prod --host db.internal --port 5432 --user app --database shop`", ""},
		{"an agent, through a profile", map[string]any{"host": "127.0.0.1"},
			plugin.TunnelKube, "prod", plugin.SurfaceMCP,
			"`pg_database_list {\"profile\":\"prod\"}` shows what is there", "host"},
		{"an agent, no profile", map[string]any{},
			plugin.TunnelNone, "", plugin.SurfaceMCP, "the `pg_database_list` tool shows what is there", "{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := reqFor(t, "pg.status", tc.values).WithProfile(tc.profile, tc.tunnel).WithSurface(tc.surface)
			verr := classify(missing, r)
			if !strings.Contains(verr.Hint, tc.want) || (tc.unwant != "" && strings.Contains(verr.Hint, tc.unwant)) {
				t.Errorf("hint = %q, want %q and not %q", verr.Hint, tc.want, tc.unwant)
			}
		})
	}
}

// The TLS the call used travels with the listing, since it is what the server
// may insist on: over a forward what turned TLS on there, directly the mode
// that insists on it and the files by the settings that named them.
func TestTheListingARefusalOffersCarriesTheTLSTheCallUsed(t *testing.T) {
	ca := testCA(t)
	missing := &pgconn.PgError{Code: "3D000"}
	direct := classify(missing, reqFor(t, "pg.status", map[string]any{"host": "10.0.0.5", "sslmode": "verify-full",
		"sslrootcert": ca, "tls-server-name": "db.internal"}))
	for _, want := range []string{"--sslmode verify-full", "--sslrootcert " + ca, "--tls-server-name db.internal"} {
		if !strings.Contains(direct.Hint, want) {
			t.Errorf("direct: hint = %q, want %q in it", direct.Hint, want)
		}
	}
	forwarded := classify(missing, reqFor(t, "pg.status", map[string]any{"host": "127.0.0.1", "sslrootcert": ca,
		"tls-server-name": "db.internal"}).WithProfile("prod", plugin.TunnelKube))
	for _, want := range []string{"--profile prod", "--sslrootcert " + ca, "--tls-server-name db.internal"} {
		if !strings.Contains(forwarded.Hint, want) {
			t.Errorf("forwarded: hint = %q, want %q in it", forwarded.Hint, want)
		}
	}
	if strings.Contains(forwarded.Hint, "--sslmode") {
		t.Errorf("forwarded: hint = %q spells an sslmode the host would refuse beside the profile", forwarded.Hint)
	}
}
