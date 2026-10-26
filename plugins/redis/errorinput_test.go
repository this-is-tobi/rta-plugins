package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	stdnet "net"
	"os"
	"path/filepath"
	"testing"

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
	t.Fatalf("redis declares no %s", id)
	return plugin.Capability{}
}

// refusedBy is err as the refusal it is, failing the test when there is none,
// or when the input it names is one capability id does not declare: every
// surface ignores such a name (plugin.Capability.ErrorInput), so a misspelt
// one would pass here for no name at all.
func refusedBy(t *testing.T, id string, err error) *view.Error {
	t.Helper()
	verr := view.AsError(err, "redis.test.none")
	if verr == nil {
		t.Fatalf("%s answered, want a refusal", id)
	}
	if _, declared := capabilityOf(t, id).ErrorInput(verr); verr.Input != "" && !declared {
		t.Errorf("%s: %s names the input %q, which %s does not declare", id, verr.Code, verr.Input, id)
	}
	return verr
}

// refusal is verr as an error, and nil — not a nil *view.Error inside an
// error — when there is none.
func refusal(verr *view.Error) error {
	if verr == nil {
		return nil
	}
	return verr
}

// runOn runs capability id with values, resolved as the host resolves them.
func runOn(t *testing.T, id string, values map[string]any) error {
	t.Helper()
	_, err := capabilityOf(t, id).Run(context.Background(), req(t, id, values))
	return err
}

// pairFiles writes ca's certificate and its key, PEM, as the two files a
// client pair is named by, and returns their paths.
func pairFiles(t *testing.T, ca testCA) (cert, key string) {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, "client.crt", ca.pem), writeFile(t, "client.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// A refusal of the one input a call got wrong names it (view.Error.Input), by
// the name the capability that refused it declares: the TUI opens the form the
// call came from again on that box, and an agent is told which argument to
// send again — a key or a database index the server does not have, a host the
// resolver says does not exist, and a certificate file a path names that is not
// there or holds no certificate.
//
// Beside them, the refusals that name none on purpose: the connection, the
// credential however the server refused it, a resolver that failed, a key that
// lives on another node or holds a type this does not render, and a pair of
// files that are not a pair or are half of one. No one box holds the fix to
// those, and a form reopened on one of them would point at the wrong half.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent")
	notACert := writeFile(t, "not-a-cert.pem", []byte("not a certificate\n"))
	cert, key := pairFiles(t, newTestCA(t))
	_, otherKey := pairFiles(t, newTestCA(t))
	srv := newFakeServer(t, map[string]string{
		"SELECT 20":   "-ERR DB index is out of range\r\n",
		"TYPE absent": "+none\r\n", "TTL absent": ":-2\r\n",
		"TYPE events": "+stream\r\n", "TTL events": ":-1\r\n",
		"TYPE elsewhere": "-MOVED 3999 10.0.0.2:6379\r\n",
	})
	answering := func(answers map[string]string) string { return newFakeServer(t, answers).addr() }
	const nowhere = "127.0.0.1:1"
	dialFailed := func(dnsErr *stdnet.DNSError) error {
		return &stdnet.OpError{Op: "dial", Net: "tcp", Err: dnsErr}
	}

	for _, c := range []struct {
		name  string
		id    string
		run   func(t *testing.T) error
		code  string
		input string
	}{
		{name: "a CA file that is not there", id: "redis.overview", code: "redis.tls.ca.unreadable", input: "ca-file",
			run: func(t *testing.T) error {
				return runOn(t, "redis.overview", map[string]any{"address": nowhere, "ca-file": absent})
			}},
		{name: "a CA file that is a directory", id: "redis.overview", code: "redis.tls.ca.unreadable", input: "ca-file",
			run: func(t *testing.T) error {
				return runOn(t, "redis.overview", map[string]any{"address": nowhere, "ca-file": dir})
			}},
		{name: "a CA file that holds no certificate", id: "redis.overview", code: "redis.tls.ca.invalid", input: "ca-file",
			run: func(t *testing.T) error {
				return runOn(t, "redis.overview", map[string]any{"address": nowhere, "ca-file": notACert})
			}},
		{name: "a client certificate that is not there", id: "redis.overview", code: "redis.tls.pair.invalid", input: "cert-file",
			run: func(t *testing.T) error {
				return runOn(t, "redis.overview", map[string]any{"address": nowhere, "cert-file": absent, "key-file": key})
			}},
		{name: "a client key that is not there", id: "redis.overview", code: "redis.tls.pair.invalid", input: "key-file",
			run: func(t *testing.T) error {
				return runOn(t, "redis.overview", map[string]any{"address": nowhere, "cert-file": cert, "key-file": absent})
			}},
		{name: "a database index the server does not have", id: "redis.overview", code: "redis.db.range", input: "db",
			run: func(t *testing.T) error {
				_, err := run(t, "redis.overview", srv, map[string]any{"db": 20})
				return err
			}},
		{name: "an address the resolver says does not exist", id: "redis.overview", code: "redis.host.unknown", input: "address",
			run: func(t *testing.T) error {
				notFound := &stdnet.DNSError{Err: "no such host", Name: "cache.invalid", IsNotFound: true}
				return refusal(classify(dialFailed(notFound), "cache.invalid:6379",
					req(t, "redis.overview", map[string]any{"address": "cache.invalid"})))
			}},
		{name: "a key the server does not hold", id: "redis.key.get", code: "redis.key.notfound", input: "key",
			run: func(t *testing.T) error {
				_, err := run(t, "redis.key.get", srv, map[string]any{"key": "absent"})
				return err
			}},

		{name: "a port nothing listens on", id: "redis.overview", code: "redis.conn.refused",
			run: func(t *testing.T) error { return runOn(t, "redis.overview", map[string]any{"address": nowhere}) }},
		{name: "a resolver that failed", id: "redis.overview", code: "redis.host.unknown",
			run: func(t *testing.T) error {
				failed := &stdnet.DNSError{Err: "server misbehaving", Name: "cache.internal", IsTemporary: true}
				return refusal(classify(dialFailed(failed), "cache.internal:6379",
					req(t, "redis.overview", map[string]any{"address": "cache.internal"})))
			}},
		{name: "a password the server refused", id: "redis.overview", code: "redis.auth.failed",
			run: func(t *testing.T) error {
				return runOn(t, "redis.overview", map[string]any{"password": "wrong",
					"address": answering(map[string]string{"AUTH wrong": "-WRONGPASS invalid username-password pair\r\n"})})
			}},
		{name: "a server that wants a password, given none", id: "redis.overview", code: "redis.auth.required",
			run: func(t *testing.T) error {
				return runOn(t, "redis.overview", map[string]any{
					"address": answering(map[string]string{"PING": "-NOAUTH Authentication required.\r\n"})})
			}},
		{name: "a password, to a server that has none", id: "redis.overview", code: "redis.auth.unneeded",
			run: func(t *testing.T) error {
				return runOn(t, "redis.overview", map[string]any{"password": "pw",
					"address": answering(map[string]string{"AUTH pw": "-ERR Client sent AUTH, but no password is set\r\n"})})
			}},
		{name: "a key that lives on another node", id: "redis.key.get", code: "redis.cluster.redirect",
			run: func(t *testing.T) error {
				_, err := run(t, "redis.key.get", srv, map[string]any{"key": "elsewhere"})
				return err
			}},
		{name: "a key of a type this does not render", id: "redis.key.get", code: "redis.key.type",
			run: func(t *testing.T) error {
				_, err := run(t, "redis.key.get", srv, map[string]any{"key": "events"})
				return err
			}},
		{name: "a certificate and a key that are not a pair", id: "redis.overview", code: "redis.tls.pair.invalid",
			run: func(t *testing.T) error {
				return runOn(t, "redis.overview", map[string]any{"address": nowhere, "cert-file": cert, "key-file": otherKey})
			}},
		{name: "a certificate without its key", id: "redis.overview", code: "redis.tls.key.missing",
			run: func(t *testing.T) error {
				return runOn(t, "redis.overview", map[string]any{"address": nowhere, "cert-file": cert})
			}},
	} {
		t.Run(c.id+": "+c.name, func(t *testing.T) {
			verr := refusedBy(t, c.id, c.run(t))
			if verr.Code != c.code || verr.Input != c.input {
				t.Errorf("%s naming %q, want %s naming %q: %s", verr.Code, verr.Input, c.code, c.input, verr.Message)
			}
		})
	}
}

// A certificate file that is there and that this process may not read names
// no input: the path is right, and the fix is a chmod, not the box.
func TestACertificateFileThatCannotBeReadNamesNoInput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("mode bits do not stop this reader")
	}
	cert, key := pairFiles(t, newTestCA(t))
	shut := writeFile(t, "shut.pem", []byte("not read\n"))
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, code string
		values     map[string]any
	}{
		{"the CA", "redis.tls.ca.unreadable", map[string]any{"ca-file": shut}},
		{"the client certificate", "redis.tls.pair.invalid", map[string]any{"cert-file": shut, "key-file": key}},
		{"the client key", "redis.tls.pair.invalid", map[string]any{"cert-file": cert, "key-file": shut}},
	} {
		c.values["address"] = "127.0.0.1:1"
		verr := refusedBy(t, "redis.overview", runOn(t, "redis.overview", c.values))
		if verr.Code != c.code || verr.Input != "" {
			t.Errorf("%s: %s naming %q, want %s naming none", c.name, verr.Code, verr.Input, c.code)
		}
	}
}
