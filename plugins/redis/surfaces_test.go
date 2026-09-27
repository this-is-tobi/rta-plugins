package main

import (
	"errors"
	stdnet "net"
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
			mcp:  "`cert-file` given without `key-file`",
			refuse: func(sf plugin.Surface) *view.Error {
				r := req(t, "redis.overview", map[string]any{"cert-file": "/tmp/client.pem"})
				_, verr := tlsConfig(r.WithSurface(sf))
				return verr
			},
		},
		{
			name: "a key on another cluster node",
			cli:  "this is a cluster and that key lives on another node — `rta redis cluster` lists them; point --address at the one named",
			mcp:  "the `redis_cluster` tool lists them; point `address` at the one named",
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(&serverError{msg: "MOVED 3999 10.0.0.2:6379"}, "10.0.0.1:6379", sf)
			},
		},
		{
			name: "a name DNS does not know",
			cli:  "`rta net dns cache.internal` shows what DNS returns",
			mcp:  "`net_dns {\"name\":\"cache.internal\"}` shows what DNS returns",
			refuse: func(sf plugin.Surface) *view.Error {
				err := &stdnet.OpError{Op: "dial", Net: "tcp", Err: &stdnet.DNSError{Err: "no such host", Name: "cache.internal"}}
				return classify(err, "cache.internal:6379", sf)
			},
		},
		{
			name: "anything else",
			cli:  "`rta explain redis.overview` lists every input and where each one can come from",
			mcp:  "ask the operator to run `rta explain redis.overview`, which lists every input",
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(errors.New("handshake went sideways"), "10.0.0.1:6379", sf)
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
		})
	}
}
