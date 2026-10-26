package main

import (
	"context"
	stdnet "net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func capabilityOf(t *testing.T, id string) plugin.Capability {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no capability %s", id)
	return plugin.Capability{}
}

// fileHolding writes body to a file nothing else uses and returns its path.
func fileHolding(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// snapshotMember is a member that answers its status and streams a snapshot of
// one small blob: all a snapshot needs to get as far as the file it is written
// to.
type snapshotMember struct{ statusServer }

func (snapshotMember) Snapshot(_ *etcdserverpb.SnapshotRequest, st grpc.ServerStreamingServer[etcdserverpb.SnapshotResponse]) error {
	return st.Send(&etcdserverpb.SnapshotResponse{
		Header: &etcdserverpb.ResponseHeader{}, Blob: []byte("not a database"), Version: "3.6.0",
	})
}

// refusedInput runs capability id with values, resolved as the host resolves
// them, as a call through sf, and returns the refusal it answered with. It
// fails the test when the refusal is not code, or when the input it names is
// one id does not declare: every surface ignores such a name, so a misspelt one
// would pass for no name at all.
func refusedInput(t *testing.T, id string, sf plugin.Surface, values map[string]any, code string) *view.Error {
	t.Helper()
	c := capabilityOf(t, id)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := c.Run(ctx, req(t, id, values).WithSurface(sf))
	if err == nil {
		t.Fatalf("%s %v: answered, want %s", id, values, code)
	}
	ve := view.AsError(err, "etcd.test")
	if ve.Code != code {
		t.Fatalf("%s %v: %s (%s), want %s", id, values, ve.Code, ve.Message, code)
	}
	if _, ok := c.ErrorInput(ve); ve.Input != "" && !ok {
		t.Fatalf("%s: %s names %q, which it does not declare", id, code, ve.Input)
	}
	return ve
}

// A refusal of the value one input was given names that input, by the name the
// capability that refused it declares, so the form the call came from opens
// again on that box and an agent knows which argument to send again: a CA file
// that is not there, is a directory or holds no certificate, a client
// certificate or key that is not there, a key that is not stored, and a
// snapshot's file left out, there already, or in a directory that is not.
//
// Beside them, the refusals that name none on purpose: half of a client pair,
// two files read that make no pair, a TLS setting beside a plain HTTP endpoint
// (two inputs at odds), a port nothing listens on, and a snapshot asked for by
// an agent.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	for _, c := range []struct {
		why    string
		id     string
		sf     plugin.Surface
		values func(t *testing.T) map[string]any
		code   string
		input  string
	}{
		{why: "a CA file that is not there", id: "etcd.overview", code: "etcd.tls.ca.unreadable", input: "ca-file",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": closedPort(t), "ca-file": filepath.Join(t.TempDir(), "absent.pem")}
			}},
		{why: "a CA file that is a directory", id: "etcd.kv.list", code: "etcd.tls.ca.unreadable", input: "ca-file",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": closedPort(t), "ca-file": t.TempDir()}
			}},
		{why: "a CA file that holds no certificate", id: "etcd.overview", code: "etcd.tls.ca.invalid", input: "ca-file",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": closedPort(t), "ca-file": fileHolding(t, "ca.pem", "hello\n")}
			}},
		{why: "a client certificate that is not there", id: "etcd.overview", code: "etcd.tls.pair.invalid", input: "cert-file",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": closedPort(t), "cert-file": filepath.Join(t.TempDir(), "absent.crt"),
					"key-file": fileHolding(t, "client.key", "not a key\n")}
			}},
		{why: "a client key that is not there", id: "etcd.member.list", code: "etcd.tls.pair.invalid", input: "key-file",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": closedPort(t), "cert-file": fileHolding(t, "client.crt", "not a certificate\n"),
					"key-file": filepath.Join(t.TempDir(), "absent.key")}
			}},
		{why: "a certificate and a key that make no pair", id: "etcd.overview", code: "etcd.tls.pair.invalid", input: "",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": closedPort(t), "cert-file": fileHolding(t, "client.crt", "not a certificate\n"),
					"key-file": fileHolding(t, "client.key", "not a key\n")}
			}},
		{why: "a client certificate without its key", id: "etcd.overview", code: "etcd.tls.key.missing", input: "",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": closedPort(t), "cert-file": fileHolding(t, "client.crt", "not a certificate\n")}
			}},
		// With tls on: a key-file alone asks for no TLS (tlsRequested), so the
		// pair is never read and the refusal is not reached.
		{why: "a client key without its certificate", id: "etcd.overview", code: "etcd.tls.cert.missing", input: "",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": closedPort(t), "tls": true, "key-file": fileHolding(t, "client.key", "not a key\n")}
			}},
		{why: "a CA beside a plain HTTP endpoint", id: "etcd.overview", code: "etcd.tls.ca.unused", input: "",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": "http://" + closedPort(t), "ca-file": writeCAFile(t)}
			}},
		{why: "TLS beside a plain HTTP endpoint", id: "etcd.overview", code: "etcd.tls.plaintext", input: "",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": "http://" + closedPort(t), "tls": true}
			}},
		{why: "a port nothing listens on", id: "etcd.overview", code: "etcd.conn.refused", input: "",
			values: func(t *testing.T) map[string]any { return map[string]any{"address": closedPort(t)} }},

		{why: "a key that is not stored", id: "etcd.kv.get", code: "etcd.key.notfound", input: "key",
			values: func(t *testing.T) map[string]any {
				addr := serveGRPC(t, func(s *grpc.Server) { etcdserverpb.RegisterKVServer(s, nothingStored{}) })
				return map[string]any{"address": addr, "key": "/config/absent"}
			}},

		{why: "no file to write to", id: "etcd.snapshot", code: "etcd.snapshot.nooutput", input: "out",
			values: func(t *testing.T) map[string]any { return map[string]any{"address": closedPort(t)} }},
		{why: "a file that is there already", id: "etcd.snapshot", code: "etcd.snapshot.exists", input: "out",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": closedPort(t), "out": fileHolding(t, "etcd.snap", "an older backup")}
			}},
		{why: "a file in a directory that is not there", id: "etcd.snapshot", code: "etcd.snapshot.create", input: "out",
			values: func(t *testing.T) map[string]any {
				addr := serveGRPC(t, func(s *grpc.Server) { etcdserverpb.RegisterMaintenanceServer(s, snapshotMember{}) })
				return map[string]any{"address": addr, "out": filepath.Join(t.TempDir(), "absent", "etcd.snap")}
			}},
		{why: "a call from an agent", id: "etcd.snapshot", sf: plugin.SurfaceMCP, code: "etcd.human", input: "",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"address": closedPort(t), "out": filepath.Join(t.TempDir(), "etcd.snap")}
			}},
	} {
		t.Run(c.id+": "+c.why, func(t *testing.T) {
			sf := c.sf
			if sf == "" {
				sf = plugin.SurfaceCLI
			}
			if ve := refusedInput(t, c.id, sf, c.values(t), c.code); ve.Input != c.input {
				t.Errorf("%s names input %q, want %q: %s", ve.Code, ve.Input, c.input, ve.Message)
			}
		})
	}
}

// A CA file that is there and that this process may not read names no input:
// the path is right, and the fix is a chmod, not the box.
func TestACAFileThatCannotBeReadNamesNoInput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("mode bits do not stop this reader")
	}
	shut := writeCAFile(t)
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Fatal(err)
	}
	ve := refusedInput(t, "etcd.overview", plugin.SurfaceCLI,
		map[string]any{"address": closedPort(t), "ca-file": shut}, "etcd.tls.ca.unreadable")
	if ve.Input != "" {
		t.Errorf("a CA file that cannot be read names %q, want none", ve.Input)
	}
}

// A name the resolver answered does not exist is the address's fault — a
// mistyped host — and its refusal names it. A resolver that did not answer, or
// failed, says nothing of the name, which may well be right, and names none.
func TestOnlyANameThatDoesNotExistNamesTheAddress(t *testing.T) {
	overview := capabilityOf(t, "etcd.overview")
	r := req(t, "etcd.overview", map[string]any{"address": "etcd-0.internal:2379"})
	dial := func(dns *stdnet.DNSError) error { return &stdnet.OpError{Op: "dial", Net: "tcp", Err: dns} }
	for why, c := range map[string]struct {
		err   error
		input string
	}{
		"a name that does not exist": {dial(&stdnet.DNSError{Err: "no such host", Name: "etcd-0.internal", IsNotFound: true}),
			"address"},
		"a resolver that did not answer": {dial(&stdnet.DNSError{Err: "i/o timeout", Name: "etcd-0.internal", IsTimeout: true}),
			""},
		"a resolver that refused": {dial(&stdnet.DNSError{Name: "etcd-0.internal",
			Err: "dial udp 10.0.0.53:53: connect: " + syscall.ECONNREFUSED.Error()}), ""},
	} {
		verr := classify(c.err, r)
		if verr.Code != "etcd.host.unknown" {
			t.Fatalf("%s: classified %s %q, want etcd.host.unknown", why, verr.Code, verr.Message)
		}
		if verr.Input != c.input {
			t.Errorf("%s: names input %q, want %q", why, verr.Input, c.input)
		}
		if _, ok := overview.ErrorInput(verr); verr.Input != "" && !ok {
			t.Errorf("%s: names %q, which etcd.overview does not declare", why, verr.Input)
		}
	}
}
