package main

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	stdnet "net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A refusal names a capability and an input the way the surface reading it
// gives them: at a terminal as it always read, and to an agent as a tool and
// the connection setting the operator holds, never as a flag or an `rta`
// command line it has no terminal for.
func TestARefusalNamesWhatItsSurfaceGives(t *testing.T) {
	for _, tc := range []struct {
		name, cli, mcp string
		refuse         func(sf plugin.Surface) *view.Error
	}{
		{
			name: "a certificate without its key",
			cli:  "--cert-file given without --key-file",
			mcp:  "the operator's `cert-file` setting given without the operator's `key-file` setting",
			refuse: func(sf plugin.Surface) *view.Error {
				r := req(t, "redis.overview", map[string]any{"cert-file": "/tmp/client.pem"})
				_, verr := tlsConfig(r.WithSurface(sf))
				return verr
			},
		},
		{
			// What the file must hold, never a guess that it held the client
			// certificate: one in PEM would have been read as a certificate,
			// and only a file with none in it, a private key among them, gets
			// here.
			name: "a CA file with no PEM certificate in it",
			cli:  "--ca-file wants a PEM certificate — the CA's, or a self-signed server's own — and a private key, which belongs in --key-file,",
			mcp: "the operator's `ca-file` setting wants a PEM certificate — the CA's, or a self-signed server's own — " +
				"and a private key, which belongs in the operator's `key-file` setting,",
			refuse: func(sf plugin.Surface) *view.Error {
				der := filepath.Join(t.TempDir(), "server.der")
				if err := os.WriteFile(der, []byte{0x30, 0x03, 0x02, 0x01, 0x01}, 0o600); err != nil {
					t.Fatal(err)
				}
				_, verr := tlsConfig(req(t, "redis.overview", map[string]any{"ca-file": der}).WithSurface(sf))
				return verr
			},
		},
		{
			name: "a key on another cluster node",
			cli:  "this is a cluster and that key lives on another node — `rta redis cluster` lists them; point --address at the one named",
			mcp:  "the `redis_cluster` tool lists them; point the operator's `address` setting at the one named",
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(&serverError{msg: "MOVED 3999 10.0.0.2:6379"}, "10.0.0.1:6379", "10.0.0.1:6379", sf)
			},
		},
		{
			name: "a name DNS does not know",
			cli:  "no address for \"cache.internal\"\n`rta net dns cache.internal` shows what DNS returns",
			mcp:  "`net_dns {\"name\":\"cache.internal\"}` shows what DNS returns",
			refuse: func(sf plugin.Surface) *view.Error {
				err := &stdnet.OpError{Op: "dial", Net: "tcp", Err: &stdnet.DNSError{Err: "no such host", Name: "cache.internal"}}
				return classify(err, "cache.internal:6379", "cache.internal:6379", sf)
			},
		},
		{
			name: "a server that wants a password",
			cli:  "the password belongs in $RTA_REDIS_PASSWORD or --password",
			mcp:  "the password belongs in $RTA_REDIS_PASSWORD or the operator's `password` setting",
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(&serverError{msg: "NOAUTH Authentication required."}, "10.0.0.1:6379", "10.0.0.1:6379", sf)
			},
		},
		{
			name: "a certificate nothing here trusts",
			cli:  "the CA that issued it belongs in --ca-file (a self-signed certificate is its own CA)",
			mcp:  "the CA that issued it belongs in the operator's `ca-file` setting (a self-signed",
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(x509.UnknownAuthorityError{}, "10.0.0.1:6379", "10.0.0.1:6379", sf)
			},
		},
		{
			// The switch turned on, as a command line takes it, and to an
			// agent the operator's setting with the value it needs.
			name: "a TLS server that hung up on plaintext",
			cli:  "a TLS server answers a plaintext client by hanging up — try --tls",
			mcp:  "try the operator's `tls` set to true",
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(io.EOF, "10.0.0.1:6379", "10.0.0.1:6379", sf)
			},
		},
		{
			name: "anything else",
			cli: "`rta explain redis.overview` lists every input and which of the command line, the rta config, " +
				"a profile and the environment can set it",
			mcp: "ask the operator to run `rta explain redis.overview`, which lists every setting and which of " +
				"the rta config, a profile and the environment the operator can set it in",
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(errors.New("handshake went sideways"), "10.0.0.1:6379", "10.0.0.1:6379", sf)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := tc.refuse(plugin.SurfaceCLI)
			if said := cli.Message + "\n" + cli.Hint; !strings.Contains(said, tc.cli) {
				t.Errorf("the CLI reads %q, want %q in it", said, tc.cli)
			}
			mcp := tc.refuse(plugin.SurfaceMCP)
			said := mcp.Message + "\n" + mcp.Hint
			if !strings.Contains(said, tc.mcp) {
				t.Errorf("an agent reads %q, want %q in it", said, tc.mcp)
			}
			if strings.Contains(said, tc.cli) {
				t.Errorf("an agent reads the CLI's %q", tc.cli)
			}
			// A connection input is Local, and the bridge drops one an
			// agent gives: told to pass one, it passes an argument that is
			// thrown away and reads the same refusal again.
			if strings.Contains(said, "pass ") {
				t.Errorf("an agent is told to pass a setting it cannot: %q", said)
			}
		})
	}
}

// The listing a missing key offers runs against the server the lookup
// reached, never whatever the configuration where it is pasted points at:
// through a profile it names the profile, whose password the lookup may have
// used; reached directly, the address too, which may be one typed over the
// profile's, and how and as whom it was reached, and the database the key was
// looked for in; through a forward the host opened, the profile alone, since
// the address was the forward's end on 127.0.0.1. An agent gives the profile
// and nothing Local.
func TestTheListingAMissingKeyOffersReachesTheServerTheLookupReached(t *testing.T) {
	srv := newFakeServer(t, map[string]string{"TYPE nope": "+none\r\n", "TTL nope": ":-2\r\n",
		"SELECT 3": "+OK\r\n"})
	for _, tc := range []struct {
		name    string
		values  map[string]any
		profile string
		tunnel  plugin.Tunnel
		sf      plugin.Surface
		want    string
	}{
		{"no profile", map[string]any{}, "", plugin.TunnelNone, plugin.SurfaceCLI,
			"rta redis key list <pattern> --address " + srv.addr()},
		{"a profile reached directly", map[string]any{}, "prod", plugin.TunnelNone, plugin.SurfaceCLI,
			"rta redis key list <pattern> --profile prod --address " + srv.addr()},
		{"a profile through a forward", map[string]any{}, "prod", plugin.TunnelKube, plugin.SurfaceCLI,
			"rta redis key list <pattern> --profile prod`"},
		{"as a user, in a database", map[string]any{"username": "reader", "db": 3}, "", plugin.TunnelNone,
			plugin.SurfaceCLI, "rta redis key list <pattern> --address " + srv.addr() + " --username reader --db 3"},
		{"an agent through a profile", map[string]any{"db": 3}, "prod", plugin.TunnelNone, plugin.SurfaceMCP,
			`redis_key_list {"pattern":"<pattern>","profile":"prod"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := tc.values
			values["address"], values["key"] = srv.addr(), "nope"
			r := req(t, "redis.key.get", values).WithProfile(tc.profile, tc.tunnel).WithSurface(tc.sf)
			c, verr := connect(context.Background(), r)
			if verr != nil {
				t.Fatal(verr)
			}
			defer c.Close()
			_, err := keyGetView(context.Background(), c, r)
			if ve := view.AsError(err, "x"); ve.Code != "redis.key.notfound" || !strings.Contains(ve.Hint, tc.want) {
				t.Errorf("%s %q, want redis.key.notfound offering %q", ve.Code, ve.Hint, tc.want)
			}
		})
	}
}
