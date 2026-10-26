package main

import (
	"context"
	"database/sql/driver"
	"errors"
	stdnet "net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// refusal is the code of err and the input it names, held to the declaration
// of the capability that answered it: a name the capability does not declare
// is one no surface acts on (plugin.Capability.ErrorInput), so it fails here
// rather than passing for a stamp that does nothing.
func refusal(t *testing.T, id string, err error) (code, input string) {
	t.Helper()
	verr := view.AsError(err, "mysql.test")
	if verr == nil {
		t.Fatalf("%s answered no error", id)
	}
	if _, declared := capabilityByID(t, id).ErrorInput(verr); verr.Input != "" && !declared {
		t.Errorf("%s: %s names the input %q, which %s does not declare", id, verr.Code, verr.Input, id)
	}
	return verr.Code, verr.Input
}

// errOf is a helper's refusal as the error a handler returns, nil staying nil.
func errOf(verr *view.Error) error {
	if verr == nil {
		return nil
	}
	return verr
}

// onPath puts a stand-in for each named client on $PATH, and nothing else
// there, so a run gets past the lookup on a machine with no client tools. None
// is ever started: every refusal it is used for comes before the child.
func onPath(t *testing.T, names ...string) {
	t.Helper()
	bin := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
}

// refusingServer listens on loopback as a server that answers every login
// with the error number given — a database it does not have, an account it
// does not know — and returns the port. It offers no TLS, which the default
// tls, preferred, takes as it is.
func refusingServer(t *testing.T, number uint16, message string) int {
	t.Helper()
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go refuseLogin(c, number, message)
		}
	}()
	return ln.Addr().(*stdnet.TCPAddr).Port
}

// refuseLogin greets c as fakeServer's server does when it has no
// certificate, reads the login and refuses it.
func refuseLogin(c stdnet.Conn, number uint16, message string) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	caps := 1 | 1<<2 | 1<<9 | 1<<13 | 1<<15 | 1<<19
	nul := func(s string) []byte { return append([]byte(s), 0) }
	hello := append([]byte{10}, nul("rta-fake")...)
	hello = append(hello, 1, 0, 0, 0)
	hello = append(hello, nul("12345678")...)
	hello = append(hello, byte(caps), byte(caps>>8), 0x21, 2, 0, byte(caps>>16), byte(caps>>24), 21)
	hello = append(hello, make([]byte, 10)...)
	hello = append(hello, nul("123456789012")...)
	hello = append(hello, nul("mysql_native_password")...)
	if writePacket(c, 0, hello) != nil {
		return
	}
	_, seq, err := readPacket(c)
	if err != nil {
		return
	}
	_ = writePacket(c, seq+1, append([]byte{0xff, byte(number), byte(number >> 8)}, "#42000"+message...))
}

// A refusal of the value one input was given names that input
// (view.Error.Input), by the name the capability that refused it declares, so
// the form the call came from opens again on that box and an agent knows
// which argument to send again: a database, a table, a dump or a CA file that
// is not there, a destination already taken, an input the call cannot go
// without left empty, and a statement the server will not run as written.
//
// Beside them, the refusals that name none on purpose: the account and the
// connection refused, a resolver that did not answer, two settings at odds,
// no database named anywhere, and more rows than the limit, which the
// statement or the limit may cure. No one box holds the fix to those, and a
// form reopened on one of them would point at the wrong half.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent")
	dump := writeFile(t, "app.sql", []byte("CREATE TABLE t (id int);\n"))
	empty := writeFile(t, "empty.sql", nil)
	taken := writeFile(t, "taken.sql", []byte("the backup that already existed"))
	notPEM := writeFile(t, "ca.pem", []byte("not a certificate\n"))
	missing := refusingServer(t, 1049, "Unknown database 'nope'")
	denied := refusingServer(t, 1045, "Access denied for user 'app'@'localhost'")

	// handler runs the capability's own Run with values resolved as the host
	// resolves them.
	handler := func(values map[string]any) func(*testing.T, string) error {
		return func(t *testing.T, id string) error {
			_, err := capabilityByID(t, id).Run(ctx, req(t, id, values))
			return err
		}
	}
	// reaching is values pointed at the server listening on port.
	reaching := func(port int, values map[string]any) map[string]any {
		values["host"], values["port"] = "127.0.0.1", port
		return values
	}
	// withClient is run with a stand-in for the client on $PATH.
	withClient := func(client string, run func(*testing.T, string) error) func(*testing.T, string) error {
		return func(t *testing.T, id string) error {
			onPath(t, client)
			return run(t, id)
		}
	}
	// statement is a statement the server refused with fail, or answered with
	// rows, through the read-only transaction and the fake driver.
	statement := func(fail error, rows ...[]driver.Value) func(*testing.T, string) error {
		return func(t *testing.T, id string) error {
			db := fakeDB(t, []string{"n"}, rows)
			fakeAnswer.err = fail
			t.Cleanup(func() { fakeAnswer.err = nil })
			_, err := queryView(ctx, db, req(t, id, map[string]any{"sql": "select n from orders", "limit": 2}))
			return err
		}
	}
	serverSays := func(number uint16) error { return &mysql.MySQLError{Number: number, Message: "server text"} }
	// shape is the schema asked about a database the fake driver holds no
	// table of, and listing the table list asked the same, of a database the
	// server says it does not hold.
	shape := func(values map[string]any) func(*testing.T, string) error {
		return func(t *testing.T, id string) error {
			_, err := schemaTree(ctx, fakeDB(t, nil, nil), req(t, id, values))
			return err
		}
	}
	listing := func(values map[string]any) func(*testing.T, string) error {
		return func(t *testing.T, id string) error {
			db := fakeDB(t, nil, nil)
			fakeRoutes = append(fakeRoutes, route("SCHEMATA", []string{"n"}, [][]driver.Value{{int64(0)}}))
			t.Cleanup(func() { fakeRoutes = nil })
			_, err := tableTable(ctx, db, req(t, id, values))
			return err
		}
	}
	resolved := func(answer *stdnet.DNSError) func(*testing.T, string) error {
		return func(t *testing.T, id string) error {
			dial := &stdnet.OpError{Op: "dial", Net: "tcp", Err: answer}
			return errOf(classify(dial, req(t, id, map[string]any{"host": "db.internal"})))
		}
	}
	// child is what a client's stderr is read as.
	child := func(read func(error, string, plugin.Request) *view.Error, stderr string) func(*testing.T, string) error {
		return func(t *testing.T, id string) error {
			return errOf(read(errors.New("exit status 1"), stderr, req(t, id, map[string]any{"database": "app"})))
		}
	}
	row := []driver.Value{int64(1)}
	wide := []driver.Value{[]byte(strings.Repeat("x", maxBytes/2+1))}

	for _, c := range []struct {
		name, id    string
		run         func(*testing.T, string) error
		code, input string
	}{
		// What a call is refused before it reaches anything.
		{"no database to dump", "mysql.dump",
			handler(map[string]any{"out": filepath.Join(dir, "a.sql")}), "mysql.dump.nodatabase", "database"},
		{"nowhere to write the dump", "mysql.dump",
			handler(map[string]any{"database": "app"}), "mysql.dump.nooutput", "out"},
		{"a dump onto a file that is there", "mysql.dump",
			withClient(dumpTools[0], handler(map[string]any{"database": "app", "out": taken})), "mysql.dump.exists", "out"},
		{"a CA file that is not there", "mysql.status",
			handler(map[string]any{"tls": "true", "ca-file": absent}), "mysql.tls.ca.unreadable", "ca-file"},
		{"a directory for a CA file", "mysql.dump",
			handler(map[string]any{"database": "app", "out": filepath.Join(dir, "b.sql"), "tls": "verify-ca", "ca-file": dir}),
			"mysql.tls.ca.unreadable", "ca-file"},
		{"a CA file that holds no certificate", "mysql.restore",
			handler(map[string]any{"database": "app", "file": dump, "tls": "true", "ca-file": notPEM}),
			"mysql.tls.ca.invalid", "ca-file"},
		{"no database to restore into", "mysql.restore",
			handler(map[string]any{"file": dump}), "mysql.restore.nodatabase", "database"},
		{"a dump that is not there", "mysql.restore",
			handler(map[string]any{"database": "app", "file": absent}), "mysql.restore.nofile", "file"},
		{"a dump under a file", "mysql.restore",
			handler(map[string]any{"database": "app", "file": filepath.Join(dump, "app.sql")}), "mysql.restore.nofile", "file"},
		{"a directory for a dump", "mysql.restore",
			handler(map[string]any{"database": "app", "file": dir}), "mysql.restore.notafile", "file"},
		{"an empty dump", "mysql.restore",
			handler(map[string]any{"database": "app", "file": empty}), "mysql.restore.empty", "file"},

		// What a server answers the login with.
		{"a database the server does not have", "mysql.status",
			handler(reaching(missing, map[string]any{"database": "nope"})), "mysql.database.notfound", "database"},
		{"a dump of a database the server does not have", "mysql.dump",
			withClient(dumpTools[0], handler(reaching(missing, map[string]any{"database": "nope", "out": filepath.Join(dir, "c.sql")}))),
			"mysql.database.notfound", "database"},
		{"a restore into a database the server does not have", "mysql.restore",
			withClient(restoreTools[0], handler(reaching(missing, map[string]any{"database": "nope", "file": dump}))),
			"mysql.restore.notarget", "database"},
		{"an account the server refuses", "mysql.status",
			handler(reaching(denied, map[string]any{"database": "app"})), "mysql.auth.failed", ""},

		// What the server says of the caller's own statement.
		{"a statement the server cannot parse", "mysql.query", statement(serverSays(1064)), "mysql.query.failed", "sql"},
		{"a statement that would write", "mysql.query", statement(serverSays(1792)), "mysql.query.readonly", "sql"},
		{"a table the statement names that is not there", "mysql.query", statement(serverSays(1146)), "mysql.table.notfound", "sql"},
		{"a database the statement names that is not there", "mysql.query", statement(serverSays(1049)), "mysql.database.notfound", "sql"},
		{"a table the statement leaves unqualified", "mysql.query", statement(serverSays(1046)), "mysql.schema.unset", "sql"},
		{"a result past the size bound", "mysql.query", statement(nil, wide, wide), "mysql.query.toolarge", "sql"},
		{"a table the account may not read", "mysql.query", statement(serverSays(1142)), "mysql.denied", ""},
		{"more rows than the limit", "mysql.query", statement(nil, row, row, row), "mysql.query.toomany", ""},

		// What the shape of a database and its listing find missing.
		{"a table the database does not have", "mysql.schema",
			shape(map[string]any{"schema": "shop", "table": "nope"}), "mysql.table.notfound", "table"},
		{"a database with no table to describe", "mysql.schema",
			shape(map[string]any{"schema": "nope"}), "mysql.database.empty", "schema"},
		{"the connection's database with no table to describe", "mysql.schema",
			shape(map[string]any{"database": "nope"}), "mysql.database.empty", "database"},
		{"no database named anywhere", "mysql.schema", shape(map[string]any{}), "mysql.schema.unset", ""},
		{"a database the server does not hold", "mysql.table.list",
			listing(map[string]any{"schema": "nope"}), "mysql.database.notfound", "schema"},

		// What the resolver answered, and what the clients a dump and a
		// restore run said.
		{"a host the resolver says does not exist", "mysql.overview",
			resolved(&stdnet.DNSError{Err: "no such host", Name: "db.internal", IsNotFound: true}), "mysql.host.unknown", "host"},
		{"a host the resolver could not be asked about", "mysql.overview",
			resolved(&stdnet.DNSError{Err: "i/o timeout", Name: "db.internal", IsTimeout: true}), "mysql.host.unknown", ""},
		{"a database the dump's client does not find", "mysql.dump",
			child(classifyDump, "mysqldump: Got error: 1049: Unknown database 'app'"), "mysql.database.notfound", "database"},
		{"a database the restore's client does not find", "mysql.restore",
			child(classifyRestore, "ERROR 1049 (42000): Unknown database 'app'"), "mysql.restore.notarget", "database"},
		{"a host the dump's client could not resolve after the check before it did", "mysql.dump",
			child(classifyDump, "mysqldump: Got error: 2005: Unknown MySQL server host 'db.internal'"), "mysql.host.unknown", ""},

		// Where the dump is written, and the file a restore hands its client.
		{"a dump into a directory that is not there", "mysql.dump", func(t *testing.T, id string) error {
			_, verr := writeDump(ctx, absent, nil, req(t, id, map[string]any{}), filepath.Join(absent, "app.sql"))
			return errOf(verr)
		}, "mysql.dump.create", "out"},
		{"a dump onto a file that got there first", "mysql.dump", func(t *testing.T, id string) error {
			_, verr := writeDump(ctx, absent, nil, req(t, id, map[string]any{}), taken)
			return errOf(verr)
		}, "mysql.dump.exists", "out"},
		{"a dump gone before the client was handed it", "mysql.restore", func(t *testing.T, id string) error {
			return errOf(runRestoreTool(ctx, absent, nil, req(t, id, map[string]any{}), absent))
		}, "mysql.restore.unreadable", "file"},

		// Two settings at odds.
		{"verify-ca with no CA to verify against", "mysql.status",
			handler(map[string]any{"tls": "verify-ca"}), "mysql.tls.ca.missing", ""},
		{"a CA beside a mode that never reads one", "mysql.status",
			handler(map[string]any{"tls": "preferred", "ca-file": notPEM}), "mysql.tls.ca.unused", ""},
		{"a name to check over a connection without TLS", "mysql.status",
			handler(map[string]any{"tls": "false", "tls-server-name": "db.internal"}), "mysql.tls.name.plaintext", ""},
		{"a name to check, for a client that checks none", "mysql.dump",
			handler(map[string]any{"database": "app", "out": filepath.Join(dir, "d.sql"), "tls": "true", "tls-server-name": "db.internal"}),
			"mysql.tls.client.name", ""},
	} {
		t.Run(c.id+": "+c.name, func(t *testing.T) {
			code, input := refusal(t, c.id, c.run(t, c.id))
			if code != c.code || input != c.input {
				t.Errorf("%s naming %q, want %s naming %q", code, input, c.code, c.input)
			}
		})
	}
}

// A file that is there and that this process may not read names no input: the
// path is right, and the fix is a chmod, not the box. Read as a path that
// leads nowhere, it would reopen a form on a box that needs no change.
func TestAFileThatCannotBeReadNamesNoInput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("mode bits do not stop this reader")
	}
	ctx := context.Background()
	shut := writeFile(t, "shut", []byte("CREATE TABLE t (id int);\n"))
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := capabilityByID(t, "mysql.status").Run(ctx,
		req(t, "mysql.status", map[string]any{"tls": "true", "ca-file": shut}))
	if code, input := refusal(t, "mysql.status", err); code != "mysql.tls.ca.unreadable" || input != "" {
		t.Errorf("a CA file this process may not read: %s naming %q, want mysql.tls.ca.unreadable naming none", code, input)
	}
	err = errOf(runRestoreTool(ctx, shut, nil, req(t, "mysql.restore", map[string]any{}), shut))
	if code, input := refusal(t, "mysql.restore", err); code != "mysql.restore.unreadable" || input != "" {
		t.Errorf("a dump this process may not read: %s naming %q, want mysql.restore.unreadable naming none", code, input)
	}
}
