package main

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/this-is-tobi/rta/pkg/view"
)

// refusedBy is err as the refusal it is, failing the test when there is none,
// or when the input it names is one capability id does not declare: every
// surface ignores such a name (plugin.Capability.ErrorInput), so a misspelt
// one would pass here for no name at all.
func refusedBy(t *testing.T, id string, err error) *view.Error {
	t.Helper()
	verr := view.AsError(err, "pg.test.none")
	if verr == nil {
		t.Fatalf("%s answered, want a refusal", id)
	}
	if _, declared := findCapability(t, id).ErrorInput(verr); verr.Input != "" && !declared {
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
	_, err := findCapability(t, id).Run(context.Background(), reqFor(t, id, values))
	return err
}

// at is values with the connection pointed at a loopback port, in the clear:
// a fake server's, or one nothing listens on for a call refused before it
// dials.
func at(port int, values map[string]any) map[string]any {
	out := map[string]any{"host": "127.0.0.1", "port": port, "user": "rta", "database": "app", "sslmode": "disable"}
	for k, v := range values {
		out[k] = v
	}
	return out
}

// fileOf writes body to name under dir, with mode, and returns its path.
func fileOf(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// serveStatements is a server that takes every connection and answers the
// statement a call sends as PostgreSQL does over the extended protocol: refused
// with the SQLSTATE code when there is one, or answered with rows, one text
// column each. The transaction around it, which pgx begins and rolls back as
// plain queries, is answered as begun and rolled back. It returns the port.
func serveStatements(t *testing.T, code string, rows ...string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go answerStatements(conn, code, rows)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func answerStatements(conn net.Conn, code string, rows []string) {
	defer func() { _ = conn.Close() }()
	b := pgproto3.NewBackend(conn, conn)
	if _, err := b.ReceiveStartupMessage(); err != nil {
		return
	}
	b.Send(&pgproto3.AuthenticationOk{})
	b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	column := &pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{
		{Name: []byte("v"), DataTypeOID: 25, DataTypeSize: -1, TypeModifier: -1}}}
	status, refused := byte('I'), false
	for {
		if err := b.Flush(); err != nil {
			return
		}
		msg, err := b.Receive()
		if err != nil {
			return
		}
		switch m := msg.(type) {
		case *pgproto3.Query:
			tag := "SELECT"
			if words := strings.Fields(m.String); len(words) > 0 {
				tag = strings.ToUpper(words[0])
			}
			status = 'I'
			if tag == "BEGIN" {
				status = 'T'
			}
			b.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
			b.Send(&pgproto3.ReadyForQuery{TxStatus: status})
		case *pgproto3.Parse:
			if code != "" {
				b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: code, Message: "refused as scripted"})
				refused = true
				continue
			}
			b.Send(&pgproto3.ParseComplete{})
		case *pgproto3.Describe:
			if refused {
				continue
			}
			if m.ObjectType == 'S' {
				b.Send(&pgproto3.ParameterDescription{})
			}
			b.Send(column)
		case *pgproto3.Bind:
			if !refused {
				b.Send(&pgproto3.BindComplete{})
			}
		case *pgproto3.Execute:
			if refused {
				continue
			}
			for _, r := range rows {
				b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(r)}})
			}
			b.Send(&pgproto3.CommandComplete{CommandTag: []byte(fmt.Sprintf("SELECT %d", len(rows)))})
		case *pgproto3.Sync:
			if refused {
				status = 'E'
			}
			refused = false
			b.Send(&pgproto3.ReadyForQuery{TxStatus: status})
		case *pgproto3.Terminate:
			return
		}
	}
}

// A refusal of the one input a call got wrong names it (view.Error.Input), by
// the name the capability that refused it declares: the TUI opens the form the
// call came from again on that box, and an agent is told which argument to
// send again — a database, a table, a column or a schema that is not there, a
// host the resolver says does not exist, a file a path names that is not there
// or is no certificate, key or dump, a statement the server refused for what
// it says, and the destination a dump cannot be written to.
//
// Beside them, the refusals that name none on purpose: the connection, the
// credential, a resolver that did not answer, a file ssl-home found, two inputs
// at odds, and a bound two inputs can each answer. No one box holds the fix to
// those, and a form reopened on one of them would point at the wrong half.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent")
	written := fileOf(t, dir, "written.sql", "select 1;\n", 0o600)
	notACert := fileOf(t, dir, "not-a-cert.pem", "not a certificate\n", 0o600)
	emptyDump := fileOf(t, dir, "empty.sql", "", 0o600)
	locked := fileOf(t, dir, "locked.key",
		string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("x")})), 0o600)
	cert, key := pairIn(t, dir, "client")
	_, otherKey := pairIn(t, dir, "other")
	noTOC := t.TempDir()
	missingDatabase := serveStartupError(t, "3D000", `database "nope" does not exist`)
	wrongPassword := serveStartupError(t, "28P01", `password authentication failed for user "rta"`)
	statement := func(code string, values map[string]any) map[string]any {
		values["sql"] = "select 1"
		return at(serveStatements(t, code), values)
	}
	wide := strings.Repeat("x", 400<<10)
	relations := func(rows ...[]any) script {
		return script{{match: "from pg_class c join pg_namespace n", cols: []string{"oid", "nspname", "relname"}, rows: rows}}
	}
	orders := relation{oid: 42, schema: "public", name: "orders"}
	ordersRows := script{
		{match: "pg_index"},
		{match: "pg_attribute", cols: []string{"attname"}, rows: [][]any{{"id"}}},
		{match: `from "public"."orders"`, cols: []string{"id"}, rows: [][]any{{int64(1)}, {int64(2)}, {int64(3)}}},
	}
	schemas := script{
		{match: "from pg_stat_user_tables", cols: []string{"schemaname", "relname", "n_live_tup", "size"}},
		{match: "select nspname from pg_namespace", cols: []string{"nspname"}, rows: [][]any{{"public"}}},
	}
	columns := make([][]any, 50000)
	for i := range columns {
		columns[i] = []any{"big", fmt.Sprintf("column_%06d", i), "text", false, false}
	}
	bigSchema := script{
		{match: "select nspname from pg_namespace", cols: []string{"nspname"}, rows: [][]any{{"public"}}},
		{match: "select c.relname, c.relkind = 'p'", cols: []string{"relname", "partitioned"}, rows: [][]any{{"big", false}}},
		{match: "format_type(a.atttypid", rows: columns},
		{match: "pg_get_constraintdef"},
		{match: "pg_get_indexdef"},
		{match: "from pg_proc p", rows: [][]any{{int64(0), int64(0), int64(0), int64(0)}}},
	}
	snapshot := "pg_dump: error: could not obtain lock on relation\n" +
		"pg_dump: detail: Error message from server: ERROR:  pg_export_snapshot is not supported\n"

	for _, c := range []struct {
		name  string
		id    string
		run   func(t *testing.T) error
		code  string
		input string
	}{
		{name: "a database the server does not have", id: "pg.status", code: "pg.database.missing", input: "database",
			run: func(t *testing.T) error {
				return runOn(t, "pg.status", at(missingDatabase, map[string]any{"database": "nope"}))
			}},
		{name: "a target database the restore cannot find", id: "pg.restore", code: "pg.restore.nodatabase", input: "database",
			run: func(t *testing.T) error {
				_, verr := checkTarget(ctx, reqFor(t, "pg.restore",
					at(missingDatabase, map[string]any{"database": "nope", "file": written})), formatPlain)
				return refusal(verr)
			}},
		{name: "a host the resolver says does not exist", id: "pg.status", code: "pg.host.unknown", input: "host",
			run: func(t *testing.T) error {
				notFound := &net.DNSError{Err: "no such host", Name: "db.invalid", IsNotFound: true}
				return refusal(classify(fmt.Errorf("hostname resolving error: %w", errors.Join(notFound)),
					reqFor(t, "pg.status", map[string]any{"host": "db.invalid"})))
			}},
		{name: "a CA file that is not there", id: "pg.status", code: "pg.tls.ca.unreadable", input: "sslrootcert",
			run: func(t *testing.T) error {
				return runOn(t, "pg.status", at(1, map[string]any{"sslmode": "verify-full", "sslrootcert": absent}))
			}},
		{name: "a CA file that is a directory", id: "pg.status", code: "pg.tls.ca.unreadable", input: "sslrootcert",
			run: func(t *testing.T) error {
				return runOn(t, "pg.status", at(1, map[string]any{"sslmode": "verify-full", "sslrootcert": dir}))
			}},
		{name: "a CA file that holds no certificate", id: "pg.status", code: "pg.tls.ca.invalid", input: "sslrootcert",
			run: func(t *testing.T) error {
				return runOn(t, "pg.status", at(1, map[string]any{"sslmode": "verify-full", "sslrootcert": notACert}))
			}},
		{name: "a client certificate that is not there", id: "pg.status", code: "pg.tls.client.unreadable", input: "sslcert",
			run: func(t *testing.T) error {
				return runOn(t, "pg.status", at(1, map[string]any{"sslmode": "require", "sslcert": absent, "sslkey": key}))
			}},
		{name: "a client key that is not there", id: "pg.status", code: "pg.tls.client.unreadable", input: "sslkey",
			run: func(t *testing.T) error {
				return runOn(t, "pg.status", at(1, map[string]any{"sslmode": "require", "sslcert": cert, "sslkey": absent}))
			}},
		{name: "a client key under a passphrase", id: "pg.status", code: "pg.tls.client.key.encrypted", input: "sslkey",
			run: func(t *testing.T) error {
				return runOn(t, "pg.status", at(1, map[string]any{"sslmode": "require", "sslcert": cert, "sslkey": locked}))
			}},
		{name: "a table the database does not have", id: "pg.table.rows", code: "pg.table.missing", input: "table",
			run: func(t *testing.T) error {
				_, verr := resolveRelation(ctx, relations(), reqFor(t, "pg.table.rows", map[string]any{"table": "public.nope"}))
				return refusal(verr)
			}},
		{name: "a bare name two schemas hold", id: "pg.table.rows", code: "pg.table.ambiguous", input: "table",
			run: func(t *testing.T) error {
				_, verr := resolveRelation(ctx, relations([]any{uint32(1), "archive", "orders"}, []any{uint32(2), "public", "orders"}),
					reqFor(t, "pg.table.rows", map[string]any{"table": "orders"}))
				return refusal(verr)
			}},
		{name: "a bare name, from an agent", id: "pg.table.rows", code: "pg.table.unqualified", input: "table",
			run: func(t *testing.T) error {
				_, verr := resolveRelation(ctx, relations(), mcpReqFor(t, "pg.table.rows", map[string]any{"table": "orders"}))
				return refusal(verr)
			}},
		{name: "a column the table does not have", id: "pg.table.rows", code: "pg.column.missing", input: "columns",
			run: func(t *testing.T) error {
				_, verr := readRows(ctx, ordersRows, reqFor(t, "pg.table.rows",
					map[string]any{"table": "public.orders", "columns": []string{"absent"}}), orders)
				return refusal(verr)
			}},
		{name: "a schema the database does not have", id: "pg.schema", code: "pg.schema.missing", input: "schema",
			run: func(t *testing.T) error {
				_, err := schemaDDL(ctx, schemas, reqFor(t, "pg.schema", map[string]any{"schema": "nope"}))
				return err
			}},
		{name: "that schema, in a table listing", id: "pg.table.list", code: "pg.schema.missing", input: "schema",
			run: func(t *testing.T) error {
				_, err := tableListView(ctx, schemas, reqFor(t, "pg.table.list", map[string]any{"schema": "nope"}), true)
				return err
			}},
		{name: "a schema too large to describe in one answer", id: "pg.schema", code: "pg.schema.toolarge", input: "limit",
			run: func(t *testing.T) error {
				_, err := schemaDDL(ctx, bigSchema, reqFor(t, "pg.schema", map[string]any{"schema": "public"}))
				return err
			}},
		{name: "a statement that does not parse", id: "pg.query", code: "pg.query.failed", input: "sql",
			run: func(t *testing.T) error { return runOn(t, "pg.query", statement("42601", map[string]any{})) }},
		{name: "a statement naming a table that is not there", id: "pg.query", code: "pg.query.failed", input: "sql",
			run: func(t *testing.T) error { return runOn(t, "pg.query", statement("42P01", map[string]any{})) }},
		{name: "a statement dividing by zero", id: "pg.query", code: "pg.query.failed", input: "sql",
			run: func(t *testing.T) error { return runOn(t, "pg.query", statement("22012", map[string]any{})) }},
		{name: "a reference to another database", id: "pg.query", code: "pg.query.failed", input: "sql",
			run: func(t *testing.T) error { return runOn(t, "pg.query", statement("0A000", map[string]any{})) }},
		{name: "a subquery that answers more than one row", id: "pg.query", code: "pg.query.failed", input: "sql",
			run: func(t *testing.T) error { return runOn(t, "pg.query", statement("21000", map[string]any{})) }},
		// A statement's 3D000 is about a database the statement names, while
		// the connection's own is there: the statement's box, not the database's.
		{name: "a statement naming a database that is not there", id: "pg.query", code: "pg.query.failed", input: "sql",
			run: func(t *testing.T) error { return runOn(t, "pg.query", statement("3D000", map[string]any{})) }},
		{name: "a write the READ ONLY transaction refuses", id: "pg.query", code: "pg.query.failed", input: "sql",
			run: func(t *testing.T) error { return runOn(t, "pg.query", statement("25006", map[string]any{})) }},
		{name: "rows over the size bound", id: "pg.query", code: "pg.query.toolarge", input: "sql",
			run: func(t *testing.T) error {
				return runOn(t, "pg.query", at(serveStatements(t, "", wide, wide, wide), map[string]any{"sql": "select v"}))
			}},
		{name: "no file to write the dump to", id: "pg.dump", code: "pg.dump.nooutput", input: "out",
			run: func(t *testing.T) error {
				_, err := runFullDump(ctx, reqFor(t, "pg.dump", map[string]any{"out": " "}))
				return err
			}},
		{name: "a dump file that is there already", id: "pg.dump", code: "pg.dump.exists", input: "out",
			run: func(t *testing.T) error {
				_, verr := writeDump(ctx, absent, nil, reqFor(t, "pg.dump", map[string]any{"out": written}), written)
				return refusal(verr)
			}},
		{name: "a dump directory that is there already", id: "pg.dump", code: "pg.dump.exists", input: "out",
			run: func(t *testing.T) error {
				_, verr := writeDump(ctx, absent, nil,
					reqFor(t, "pg.dump", map[string]any{"out": noTOC, "format": "directory"}), noTOC)
				return refusal(verr)
			}},
		{name: "a dump in a directory that is not there", id: "pg.dump", code: "pg.dump.create", input: "out",
			run: func(t *testing.T) error {
				out := filepath.Join(absent, "app.sql")
				_, verr := writeDump(ctx, absent, nil, reqFor(t, "pg.dump", map[string]any{"out": out}), out)
				return refusal(verr)
			}},
		{name: "a directory dump under a file", id: "pg.dump", code: "pg.dump.create", input: "out",
			run: func(t *testing.T) error {
				out := filepath.Join(written, "dumps", "app")
				_, verr := writeDump(ctx, absent, nil, reqFor(t, "pg.dump", map[string]any{"out": out, "format": "directory"}), out)
				return refusal(verr)
			}},
		{name: "parallel workers a server cannot give one snapshot", id: "pg.dump", code: "pg.dump.nosnapshot", input: "jobs",
			run: func(t *testing.T) error {
				return refusal(classifyDump(errStub{}, snapshot,
					reqFor(t, "pg.dump", map[string]any{"format": "directory", "jobs": 4})))
			}},
		{name: "no dump at the path", id: "pg.restore", code: "pg.restore.nofile", input: "file",
			run: func(t *testing.T) error {
				_, err := runRestore(ctx, reqFor(t, "pg.restore", map[string]any{"file": absent}))
				return err
			}},
		{name: "a directory that is no dump", id: "pg.restore", code: "pg.restore.notadump", input: "file",
			run: func(t *testing.T) error {
				_, err := runRestore(ctx, reqFor(t, "pg.restore", map[string]any{"file": noTOC}))
				return err
			}},
		{name: "an empty file", id: "pg.restore", code: "pg.restore.empty", input: "file",
			run: func(t *testing.T) error {
				_, err := runRestore(ctx, reqFor(t, "pg.restore", map[string]any{"file": emptyDump}))
				return err
			}},

		{name: "a port nothing listens on", id: "pg.status", code: "pg.conn.refused",
			run: func(t *testing.T) error { return runOn(t, "pg.status", at(1, map[string]any{})) }},
		{name: "a password the server refused", id: "pg.status", code: "pg.auth.failed",
			run: func(t *testing.T) error {
				return runOn(t, "pg.status", at(wrongPassword, map[string]any{"password": "wrong"}))
			}},
		{name: "a resolver that did not answer", id: "pg.status", code: "pg.host.unknown",
			run: func(t *testing.T) error {
				timedOut := &net.DNSError{Err: "i/o timeout", Name: "db.internal", IsTimeout: true}
				return refusal(classify(timedOut, reqFor(t, "pg.status", map[string]any{"host": "db.internal"})))
			}},
		{name: "a CA ssl-home found that holds no certificate", id: "pg.status", code: "pg.tls.ca.invalid",
			run: func(t *testing.T) error {
				home := t.TempDir()
				t.Setenv("HOME", home)
				if err := os.MkdirAll(filepath.Join(home, ".postgresql"), 0o700); err != nil {
					t.Fatal(err)
				}
				fileOf(t, filepath.Join(home, ".postgresql"), "root.crt", "not a certificate\n", 0o644)
				return runOn(t, "pg.status", at(1, map[string]any{"sslmode": "verify-full", "ssl-home": true}))
			}},
		{name: "verify-ca and no CA named", id: "pg.status", code: "pg.tls.ca.missing",
			run: func(t *testing.T) error { return runOn(t, "pg.status", at(1, map[string]any{"sslmode": "verify-ca"})) }},
		{name: "a certificate and a key that are not a pair", id: "pg.status", code: "pg.tls.client.invalid",
			run: func(t *testing.T) error {
				return runOn(t, "pg.status", at(1, map[string]any{"sslmode": "require", "sslcert": cert, "sslkey": otherKey}))
			}},
		{name: "a statement the server cancelled", id: "pg.query", code: "pg.query.failed",
			run: func(t *testing.T) error { return runOn(t, "pg.query", statement("57014", map[string]any{})) }},
		{name: "a statement the role may not run", id: "pg.query", code: "pg.denied",
			run: func(t *testing.T) error { return runOn(t, "pg.query", statement("42501", map[string]any{})) }},
		{name: "more rows than the query's limit", id: "pg.query", code: "pg.query.toomany",
			run: func(t *testing.T) error {
				return runOn(t, "pg.query", at(serveStatements(t, "", "a", "b", "c"), map[string]any{"sql": "select v", "limit": 2}))
			}},
		{name: "more rows than the table read's limit", id: "pg.table.rows", code: "pg.table.rows.toomany",
			run: func(t *testing.T) error {
				_, verr := readRows(ctx, ordersRows, reqFor(t, "pg.table.rows",
					map[string]any{"table": "public.orders", "limit": 2}), orders)
				return refusal(verr)
			}},
		{name: "jobs beside a format that is not directory", id: "pg.dump", code: "pg.dump.notparallel",
			run: func(t *testing.T) error {
				return refusal(checkParallel(reqFor(t, "pg.dump", map[string]any{"jobs": 4, "format": "plain"})))
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

// A file that is there and that this process may not read names no input: the
// path is right, and the fix is a chmod, not the box.
func TestAFileThatIsThereAndCannotBeReadNamesNoInput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("mode bits do not stop this reader")
	}
	dir := t.TempDir()
	cert, key := pairIn(t, dir, "client")
	shut := func(path string) string {
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		return path
	}
	caFile := shut(fileOf(t, dir, "ca.pem", "not read\n", 0o600))
	shutCert := shut(fileOf(t, dir, "shut.crt", string(mustRead(t, cert)), 0o600))
	shutKey := shut(fileOf(t, dir, "shut.key", string(mustRead(t, key)), 0o600))
	dump := shut(fileOf(t, dir, "app.sql", "select 1;\n", 0o600))

	for _, c := range []struct {
		name, id, code string
		values         map[string]any
	}{
		{"the CA", "pg.status", "pg.tls.ca.unreadable", at(1, map[string]any{"sslmode": "verify-full", "sslrootcert": caFile})},
		{"the client certificate", "pg.status", "pg.tls.client.unreadable",
			at(1, map[string]any{"sslmode": "require", "sslcert": shutCert, "sslkey": key})},
		{"the client key", "pg.status", "pg.tls.client.unreadable",
			at(1, map[string]any{"sslmode": "require", "sslcert": cert, "sslkey": shutKey})},
		{"the dump", "pg.restore", "pg.restore.unreadable", map[string]any{"file": dump}},
	} {
		verr := refusedBy(t, c.id, runOn(t, c.id, c.values))
		if verr.Code != c.code || verr.Input != "" {
			t.Errorf("%s: %s naming %q, want %s naming none", c.name, verr.Code, verr.Input, c.code)
		}
	}
}

// The schema a detailed overview's largest tables are narrowed to is pg.overview's
// own input, so the warning that says it is not there names it there too, as a
// declared input of the call that answered.
func TestTheOverviewsWarningOfASchemaThatIsNotThereNamesItsSchema(t *testing.T) {
	q := script{
		{match: "select version()", rows: [][]any{{"PostgreSQL 17.2", "app", "rta", "8 MB"}}},
		{match: "from pg_stat_user_tables", cols: []string{"schemaname", "relname", "n_live_tup", "size"}},
		{match: "select nspname from pg_namespace", cols: []string{"nspname"}, rows: [][]any{{"public"}}},
	}
	v, err := detailedOverview(context.Background(), q,
		reqFor(t, "pg.overview", map[string]any{"detail": true, "schema": "nope"}))
	if err != nil {
		t.Fatal(err)
	}
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("the detail is a %T, want sections", v)
	}
	for _, w := range page.Warnings {
		if w.Code != "pg.schema.missing" {
			continue
		}
		if _, declared := findCapability(t, "pg.overview").ErrorInput(&w); w.Input != "schema" || !declared {
			t.Errorf("the warning names %q, want pg.overview's schema", w.Input)
		}
		return
	}
	t.Fatalf("no pg.schema.missing among the warnings: %+v", page.Warnings)
}
