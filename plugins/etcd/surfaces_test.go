package main

import (
	"context"
	"errors"
	stdnet "net"
	"strings"
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A refusal names a capability and an input the way the surface reading it
// gives them: at a terminal as it always read, and elsewhere as a tool, a
// form's box, or the connection setting the operator holds — never as a flag
// or an `rta` command line its reader has no terminal for.
func TestARefusalNamesWhatItsSurfaceGives(t *testing.T) {
	for _, tc := range []struct {
		name, cli, other string
		surface          plugin.Surface
		refuse           func(sf plugin.Surface) *view.Error
	}{
		{
			name:    "a certificate without its key",
			cli:     "--cert-file given without --key-file",
			other:   "`cert-file` given without `key-file`",
			surface: plugin.SurfaceMCP,
			refuse: func(sf plugin.Surface) *view.Error {
				_, verr := tlsConfig(req(t, "etcd.overview", map[string]any{"cert-file": "/tmp/client.pem"}).WithSurface(sf))
				return verr
			},
		},
		{
			name:    "a cluster that answers nothing",
			cli:     "`rta etcd overview` shows whether the members can see each other",
			other:   "the `etcd_overview` tool shows whether the members can see each other",
			surface: plugin.SurfaceMCP,
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(context.DeadlineExceeded, req(t, "etcd.overview", nil).WithSurface(sf))
			},
		},
		{
			name:    "no endpoint answering",
			cli:     "is the cluster up, and is --endpoint right?",
			other:   "is the cluster up, and is `endpoint` right?",
			surface: plugin.SurfaceMCP,
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(clientv3.ErrNoAvailableEndpoints, req(t, "etcd.overview", nil).WithSurface(sf))
			},
		},
		{
			// A dial's lookup failure arrives inside the dial's own error, and
			// what DNS was asked is the host alone: the port beside it is
			// nothing a lookup was made for.
			name:    "a name DNS does not know",
			cli:     "no address for \"etcd-0.internal\"\n`rta net dns etcd-0.internal` shows what DNS returns",
			other:   "no address for \"etcd-0.internal\"\n`net_dns {\"name\":\"etcd-0.internal\"}` shows what DNS returns",
			surface: plugin.SurfaceMCP,
			refuse: func(sf plugin.Surface) *view.Error {
				err := &stdnet.OpError{Op: "dial", Net: "tcp", Err: &stdnet.DNSError{Err: "no such host", Name: "etcd-0.internal"}}
				return classify(err, req(t, "etcd.overview", map[string]any{"endpoint": "etcd-0.internal:2379"}).WithSurface(sf))
			},
		},
		{
			name:    "anything else",
			cli:     "`rta explain etcd.overview` lists every input and where each one can come from",
			other:   "ask the operator to run `rta explain etcd.overview`, which lists every input",
			surface: plugin.SurfaceMCP,
			refuse: func(sf plugin.Surface) *view.Error {
				return classify(errors.New("handshake went sideways"), req(t, "etcd.overview", nil).WithSurface(sf))
			},
		},
		{
			name:    "a snapshot with nowhere to go",
			cli:     "--out ./etcd.snap — a whole keyspace is a file",
			other:   "the out box set to ./etcd.snap — a whole keyspace is a file",
			surface: plugin.SurfaceTUI,
			refuse: func(sf plugin.Surface) *view.Error {
				_, err := runSnapshot(context.Background(), req(t, "etcd.snapshot", nil).WithSurface(sf))
				var verr *view.Error
				if !errors.As(err, &verr) {
					t.Fatalf("err = %v, want a refusal", err)
				}
				return verr
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := tc.refuse(plugin.SurfaceCLI)
			if said := cli.Message + "\n" + cli.Hint; !strings.Contains(said, tc.cli) {
				t.Errorf("the CLI reads %q, want %q in it", said, tc.cli)
			}
			other := tc.refuse(tc.surface)
			said := other.Message + "\n" + other.Hint
			if !strings.Contains(said, tc.other) {
				t.Errorf("%s reads %q, want %q in it", tc.surface, said, tc.other)
			}
			if strings.Contains(said, tc.cli) {
				t.Errorf("%s reads the CLI's %q", tc.surface, tc.cli)
			}
		})
	}
}

// What DNS was asked is the host, whatever form the endpoint takes: a URL,
// which etcd's client reads as readily as host:port, names the same host.
func TestADNSRefusalNamesTheHostOfEveryEndpointForm(t *testing.T) {
	err := &stdnet.OpError{Op: "dial", Net: "tcp", Err: &stdnet.DNSError{Err: "no such host", Name: "etcd-0.internal"}}
	for _, endpoint := range []string{
		"etcd-0.internal:2379", "https://etcd-0.internal:2379", "http://etcd-0.internal:2379/", "etcd-0.internal",
	} {
		got := classify(err, req(t, "etcd.overview", map[string]any{"endpoint": endpoint}))
		if want := `no address for "etcd-0.internal"`; got.Message != want {
			t.Errorf("%s: %q, want %q", endpoint, got.Message, want)
		}
		if want := "`rta net dns etcd-0.internal`"; !strings.Contains(got.Hint, want) {
			t.Errorf("%s: hint %q, want %q in it", endpoint, got.Hint, want)
		}
	}
}
