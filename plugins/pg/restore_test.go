package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// needsRestoreTools skips a test whose subject sits past the tool lookup,
// needsPgDump's reason applied to the other direction.
func needsRestoreTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"psql", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on $PATH, and this test's subject is only reachable past the lookup", tool)
		}
	}
}

// plainFixture writes a small SQL file and returns its path.
func plainFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.sql")
	if err := os.WriteFile(path, []byte("create table t (id int);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// **The format comes from the bytes, never the filename.** A custom archive
// named backup.sql handed to psql replays as garbage, and a plain dump
// handed to pg_restore fails with a message about text-format dumps that a
// person then has to search for — so the misleading extension is the exact
// case worth pinning.
func TestTheFormatComesFromTheBytesNotTheName(t *testing.T) {
	dir := t.TempDir()

	misleadingSQL := filepath.Join(dir, "actually-custom.sql")
	if err := os.WriteFile(misleadingSQL, []byte("PGDMP\x01"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, verr := detectFormat(plugin.SurfaceCLI, misleadingSQL); verr != nil || got != formatCustom {
		t.Errorf("a PGDMP file named .sql = %q (%v), want custom", got, verr)
	}

	misleadingDump := filepath.Join(dir, "actually-plain.dump")
	if err := os.WriteFile(misleadingDump, []byte("-- pg_dump\ncreate table t (id int);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, verr := detectFormat(plugin.SurfaceCLI, misleadingDump); verr != nil || got != formatPlain {
		t.Errorf("a SQL file named .dump = %q (%v), want plain", got, verr)
	}

	dirDump := filepath.Join(dir, "dirdump")
	if err := os.MkdirAll(dirDump, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirDump, "toc.dat"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, verr := detectFormat(plugin.SurfaceCLI, dirDump); verr != nil || got != formatDirectory {
		t.Errorf("a directory with toc.dat = %q (%v), want directory", got, verr)
	}
}

// The artifacts that are not dumps are refused with a code each, rather than
// handed to a tool whose error message names none of them.
func TestWhatIsNotADumpIsRefusedByName(t *testing.T) {
	dir := t.TempDir()

	if _, verr := detectFormat(plugin.SurfaceCLI, filepath.Join(dir, "nope")); verr == nil || verr.Code != "pg.restore.missing" {
		t.Errorf("missing file: %v, want pg.restore.missing", verr)
	}

	if _, verr := detectFormat(plugin.SurfaceCLI, dir); verr == nil || verr.Code != "pg.restore.notadump" {
		t.Errorf("directory without toc.dat: %v, want pg.restore.notadump", verr)
	}

	empty := filepath.Join(dir, "empty.sql")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// An empty file would restore as nothing and report success, which is
	// the same lie a truncated dump tells.
	if _, verr := detectFormat(plugin.SurfaceCLI, empty); verr == nil || verr.Code != "pg.restore.empty" {
		t.Errorf("empty file: %v, want pg.restore.empty", verr)
	}
}

// The flags psql cannot honour are refused by name rather than ignored:
// silently dropping --clean would restore with a guarantee the caller did
// not ask for.
func TestPlainDumpsRefuseTheArchiveOnlyFlags(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]any
	}{
		{"jobs", map[string]any{"jobs": 4}},
		{"clean", map[string]any{"clean": true}},
		{"no-owner", map[string]any{"no-owner": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verr := checkRestoreFlags(reqFor(t, "pg.restore", tc.values), formatPlain)
			if verr == nil || verr.Code != "pg.restore.plainflag" {
				t.Fatalf("verr = %v, want pg.restore.plainflag", verr)
			}
			if !strings.Contains(verr.Message, "--"+tc.name) {
				t.Errorf("message = %q, want it to name --%s", verr.Message, tc.name)
			}
		})
	}
	// And the same flags pass for an archive, or the refusal would be a ban.
	if verr := checkRestoreFlags(reqFor(t, "pg.restore",
		map[string]any{"jobs": 4, "clean": true, "no-owner": true}), formatCustom); verr != nil {
		t.Errorf("archive flags refused: %v", verr)
	}
}

// argv, both shapes. The password stays out of it — it travels through the
// child's environment exactly as the dump's does, and `ps` shows argv to
// every account on the machine.
func TestRestoreArgvIsRightForEachFormatAndCarriesNoPassword(t *testing.T) {
	r := reqFor(t, "pg.restore", map[string]any{
		"host": "db.internal", "port": 6543, "user": "app",
		"database": "prod", "password": "hunter2",
	})

	plain := strings.Join(restoreArgs(r, formatPlain, "/b/app.sql"), " ")
	for _, want := range []string{"--host=db.internal", "--port=6543", "--username=app",
		"--dbname=prod", "--no-password", "--single-transaction",
		"--set=ON_ERROR_STOP=1", "--file=/b/app.sql"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain argv missing %q: %s", want, plain)
		}
	}

	custom := strings.Join(restoreArgs(r, formatCustom, "/b/app.dump"), " ")
	if !strings.Contains(custom, "--single-transaction") {
		t.Errorf("serial archive restore is not single-transaction: %s", custom)
	}
	if !strings.HasSuffix(custom, " /b/app.dump") {
		t.Errorf("archive path is not the final argument: %s", custom)
	}

	for _, args := range [][]string{
		restoreArgs(r, formatPlain, "/b/app.sql"),
		restoreArgs(r, formatCustom, "/b/app.dump"),
	} {
		if strings.Contains(strings.Join(args, " "), "hunter2") {
			t.Fatalf("the password is in argv: %v", args)
		}
	}
}

// **Parallel workers cannot share one transaction**, so --jobs swaps the
// all-or-nothing guarantee for --exit-on-error — and the swap must be total:
// keeping --single-transaction alongside --jobs makes pg_restore refuse the
// combination at runtime.
func TestParallelSwapsTheTransactionForExitOnError(t *testing.T) {
	r := reqFor(t, "pg.restore", map[string]any{"jobs": 4, "clean": true, "no-owner": true})
	got := strings.Join(restoreArgs(r, formatDirectory, "/b/dir"), " ")
	for _, want := range []string{"--jobs=4", "--exit-on-error", "--clean", "--if-exists", "--no-owner"} {
		if !strings.Contains(got, want) {
			t.Errorf("argv missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "--single-transaction") {
		t.Errorf("--jobs and --single-transaction together: %s", got)
	}
}

// pg.restore leaves the agent surface entirely, pg.dump's reason run in
// reverse — and it refuses before touching the filesystem or opening a
// connection, so the check uses a file that does not exist and a host
// nothing listens on: reaching either would change the error.
func TestRestoreRefusesMCPBeforeTouchingAnything(t *testing.T) {
	_, err := runRestore(context.Background(), mcpReqFor(t, "pg.restore", map[string]any{
		"file": "/nonexistent/backup.sql", "host": "127.0.0.1", "port": 1,
	}))
	if err == nil {
		t.Fatal("an MCP caller was allowed to restore into the database")
	}
	var verr *view.Error
	if !errors.As(err, &verr) || verr.Code != "pg.human" || !verr.Refusal {
		t.Fatalf("err = %v, want pg.human marked a refusal", err)
	}
}

// --dry-run names the tool, the argv and the target, runs nothing, and
// connects to nothing — the host and port name a server that is not there,
// so a dry run that stopped being dry would fail as a refused connection.
func TestRestoreDryRunRunsNothingAndConnectsToNothing(t *testing.T) {
	needsRestoreTools(t)
	path := plainFixture(t)
	v, err := runRestore(context.Background(), dryRunReqFor(t, "pg.restore", map[string]any{
		"file": path, "database": "prod", "host": "127.0.0.1", "port": 1,
	}))
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	text, ok := v.(view.Text)
	if !ok {
		t.Fatalf("want Text, got %s", view.TypeOf(v))
	}
	for _, want := range []string{"psql", path, "prod", "plain"} {
		if !strings.Contains(text.Body, want) {
			t.Errorf("the dry run does not mention %q: %q", want, text.Body)
		}
	}
}

// The classifier turns the failures particular to this direction into acting
// instructions. Matched on stderr text the way classifyDump matches — pinned
// to C-locale tool output, best-effort on the server's own lines.
func TestRestoreFailuresAreClassified(t *testing.T) {
	r := reqFor(t, "pg.restore", map[string]any{"database": "prod", "host": "db.internal"})
	exit := errors.New("exit status 1")
	for _, tc := range []struct {
		stderr, code, hint string
	}{
		{`connection to server failed: FATAL:  database "prod" does not exist`,
			"pg.restore.nodatabase", "createdb"},
		{`pg_restore: error: could not execute query: ERROR:  role "app_owner" does not exist`,
			"pg.restore.owner", "--no-owner"},
		{`pg_restore: error: could not execute query: ERROR:  relation "t" already exists`,
			"pg.restore.collision", "--clean"},
		{`psql: error: ERROR:  cannot execute CREATE TABLE in a read-only transaction`,
			"pg.restore.standby", "primary"},
		{`pg_restore: error: unsupported version (1.16) in file header`,
			"pg.restore.version", "client"},
		{`connection to server failed: fe_sendauth: no password supplied`,
			"pg.auth.failed", "RTA_PG_PASSWORD"},
	} {
		verr := classifyRestore(exit, tc.stderr, r, formatCustom, versions{})
		if verr.Code != tc.code {
			t.Errorf("stderr %q -> %s, want %s", tc.stderr, verr.Code, tc.code)
			continue
		}
		if !strings.Contains(verr.Hint, tc.hint) {
			t.Errorf("%s hint = %q, want it to mention %q", tc.code, verr.Hint, tc.hint)
		}
	}
}

// An interrupted single-transaction restore rolled back; an interrupted
// parallel one may not have. The hint is the one thing the operator reads
// next, so the two cases must not share it.
// A restore into a server older than what wrote it fails on a SET the server
// does not have, and is named as that, with the way to a file that goes in:
// the words alone said the tool reported it, and nothing about which side
// was too new. The fix depends on who set the parameter — the file, for
// plain SQL, or pg_restore itself, for an archive — and on whether an older
// pg_restore can read the archive at all.
func TestARestoreIntoAnOlderServerNamesTheSkewAndTheWayThrough(t *testing.T) {
	const refused = `ERROR:  unrecognized configuration parameter "transaction_timeout"`
	r := reqFor(t, "pg.restore", nil)
	exit := errors.New("exit status 3")
	for _, tc := range []struct {
		name   string
		format dumpFormat
		stderr string
		v      versions
		want   []string
	}{
		{
			name: "a plain file a newer pg_dump wrote", format: formatPlain,
			stderr: "psql:/backups/app.sql:13: " + refused + "\n",
			v:      versions{server: 16, dump: 18},
			want: []string{
				"pg_dump 18 wrote this file for PostgreSQL 18 and newer",
				"PostgreSQL 16's own pg_dump writes one that restores here",
				"`brew install postgresql@16`", "`apt install postgresql-client-16`",
				"a copy of this file without its SET transaction_timeout line",
			},
		},
		{
			name: "an archive an older pg_restore can read", format: formatCustom,
			stderr: "pg_restore: error: could not execute query: " + refused + "\n",
			v:      versions{server: 16, dump: 16, restore: 18},
			want: []string{
				"pg_restore 18 sets a parameter PostgreSQL 16 does not have",
				"PostgreSQL 16's own pg_restore reads this archive",
			},
		},
		{
			name: "an archive only a newer pg_restore reads", format: formatCustom,
			stderr: "pg_restore: error: could not execute query: " + refused + "\n",
			v:      versions{server: 16, dump: 18, restore: 18},
			want: []string{
				"cannot read an archive a newer pg_dump wrote",
				"PostgreSQL 16's own pg_dump takes one it can",
				"`pg_restore --file`",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verr := classifyRestore(exit, tc.stderr, r, tc.format, tc.v)
			if verr.Code != "pg.restore.skew" || !strings.Contains(verr.Message, "transaction_timeout") {
				t.Fatalf("got %s %q, want pg.restore.skew with the tool's line", verr.Code, verr.Message)
			}
			for _, want := range tc.want {
				if !strings.Contains(verr.Hint, want) {
					t.Errorf("hint = %q, want %q in it", verr.Hint, want)
				}
			}
		})
	}

	// A parameter nothing newer set — a function's own SET clause, a file
	// from this very version — is not the skew, and is passed through.
	same := classifyRestore(exit, "psql:/backups/app.sql:40: "+refused+"\n", r, formatPlain,
		versions{server: 17, dump: 17})
	if same.Code != "pg.restore.failed" {
		t.Errorf("a parameter from a file of the server's own version = %s, want pg.restore.failed", same.Code)
	}
}

// What wrote a dump is read off the dump: the header a plain file opens with,
// and the listing pg_restore prints of an archive, whose own header is binary.
func TestTheDumpsWriterIsReadOffTheDump(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "app.sql")
	header := "--\n-- PostgreSQL database dump\n--\n\n-- Dumped from database version 16.15\n" +
		"-- Dumped by pg_dump version 18.6 (Homebrew)\n\nSET statement_timeout = 0;\n"
	if err := os.WriteFile(plain, []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := plainDumpedBy(plain); got != 18 {
		t.Errorf("a plain dump by pg_dump 18.6 read as %d", got)
	}
	if got := plainDumpedBy(filepath.Join(dir, "absent.sql")); got != 0 {
		t.Errorf("a file that is not there read as %d, want 0", got)
	}

	if runtime.GOOS == "windows" {
		t.Skip("the fake pg_restore is a shell script")
	}
	fake := filepath.Join(dir, "pg_restore")
	listing := "echo ';     Dumped from database version: 16.15'\n" +
		"echo ';     Dumped by pg_dump version: 18.6 (Homebrew)'"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+listing+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := archiveDumpedBy(context.Background(), fake, filepath.Join(dir, "app.dump")); got != 18 {
		t.Errorf("an archive listed as by pg_dump 18.6 read as %d", got)
	}
}

func TestAnInterruptedRestoreSaysWhatItLeftBehind(t *testing.T) {
	serial := classifyRestore(context.Canceled, "", reqFor(t, "pg.restore", nil), formatCustom, versions{})
	if serial.Code != "pg.restore.cancelled" || !strings.Contains(serial.Hint, "holds what it held") {
		t.Errorf("serial cancel = %s %q, want the rolled-back reassurance", serial.Code, serial.Hint)
	}
	parallel := classifyRestore(context.Canceled, "",
		reqFor(t, "pg.restore", map[string]any{"jobs": 4}), formatDirectory, versions{})
	if parallel.Code != "pg.restore.cancelled" || !strings.Contains(parallel.Hint, "partial") {
		t.Errorf("parallel cancel = %s %q, want the partial-restore warning", parallel.Code, parallel.Hint)
	}
}

// The receipt's guarantee line matches the argv the run actually had.
func TestTheReceiptGuaranteeMatchesTheRun(t *testing.T) {
	serial := restoreGuarantee(reqFor(t, "pg.restore", nil))
	if !strings.Contains(serial, "one transaction") {
		t.Errorf("serial guarantee = %q", serial)
	}
	parallel := restoreGuarantee(reqFor(t, "pg.restore", map[string]any{"jobs": 8}))
	if !strings.Contains(parallel, "8 workers") || !strings.Contains(parallel, "partial") {
		t.Errorf("parallel guarantee = %q", parallel)
	}
}
