package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	// Aliased: `format` is what this file calls a dump's own format, and the
	// package must not be shadowed where a count is spelled.
	rtaformat "github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The other half of pg.dump — the file back into a database.
//
// **It refuses MCP for the dump's reason run in reverse.** The dump refuses
// because everything would leave; a restore is everything arriving — whatever
// the file says, written into a live database, with --clean dropping objects
// on the way in. Neither direction has a blast radius a grant could name, so
// both belong to the person at the keyboard. Destructive besides, because
// that is what it is, and unlike the dump this one gets the --yes gate a
// person should have to type through.
//
// **The format is read from the bytes, never from the filename.** A custom
// archive named backup.sql handed to psql replays as garbage; a plain dump
// handed to pg_restore is refused with a message about text-format dumps
// that a person then searches for. A directory holding toc.dat is a
// directory-format dump, a file beginning PGDMP is a custom archive, and
// anything else is SQL for psql — which means the wrong-tool failure mode is
// not reachable from here.
//
// **A non-empty target is refused, which is the dump's O_EXCL pointing the
// other way.** The dump never writes over an existing file; the restore
// never lands on a database that already holds relations, unless --clean
// says that is the point. Without the check, restoring onto an existing
// schema is a flood of "already exists" errors at best and silently
// interleaved data at worst — and the worst case is the one that gets
// discovered months later.

type dumpFormat string

const (
	formatPlain     dumpFormat = "plain"
	formatCustom    dumpFormat = "custom"
	formatDirectory dumpFormat = "directory"
)

// customMagic opens every custom-format archive; pg_dump has written it
// since the format existed.
const customMagic = "PGDMP"

func runRestore(ctx context.Context, req plugin.Request) (view.View, error) {
	if verr := humanOnly(req, "pg.restore",
		"a restore writes a file's whole contents into a live database — a blast radius no "+
			"grant could name, in the direction that overwrites. The dump this file came from "+
			"was made by a person at a terminal, and it goes back the same way"); verr != nil {
		return nil, verr
	}

	path, err := expandHome(strings.TrimSpace(req.String("file")))
	if err != nil {
		return nil, view.Errorf("pg.restore.path", "resolving the dump path: %v", err)
	}
	format, verr := detectFormat(req.Surface(), path)
	if verr != nil {
		return nil, verr
	}
	if verr := checkRestoreFlags(req, format); verr != nil {
		return nil, verr
	}
	// runFullDump's reason: the dry run connects to nothing.
	if verr := checkRootCert(req); verr != nil {
		return nil, verr
	}
	tool, verr := lookupRestoreTool(format)
	if verr != nil {
		return nil, verr
	}

	args := restoreArgs(req, format, path)
	if req.DryRun {
		return view.Text{Body: fmt.Sprintf("would run %s %s\nrestoring a %s-format dump into %s on %s",
			filepath.Base(tool), strings.Join(args, " "), format,
			req.String("database"), address(req))}, nil
	}

	// Ask the server what it is before writing into it — the dump's
	// describeSource discipline, with two extra questions only a restore has
	// to ask: a standby cannot be written at all, and a database that already
	// holds relations is refused unless --clean names the intent.
	src, verr := checkTarget(ctx, req, format)
	if verr != nil {
		return nil, verr
	}

	started := time.Now()
	if verr := runRestoreTool(ctx, tool, args, req, format, path, src); verr != nil {
		return nil, verr
	}

	return view.KeyValue{Pairs: []view.Pair{
		{Key: "restored", Value: path},
		{Key: "into", Value: req.String("database") + " on " + address(req)},
		{Key: "format", Value: describeRestore(req, format)},
		{Key: "took", Value: time.Since(started).Round(time.Millisecond).String()},
		{Key: "guarantee", Value: restoreGuarantee(req)},
		{Key: "target", Value: src.describe(req.Surface())},
	}}, nil
}

// detectFormat reads what the artifact is rather than trusting its name.
func detectFormat(sf plugin.Surface, path string) (dumpFormat, *view.Error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", view.Errorf("pg.restore.missing", "no dump at %s", path).
			WithHint("`" + sf.Call("pg.dump", plugin.Arg{Name: "out", Value: "<path>"}) + "` writes one; this restores what that wrote")
	}
	if info.IsDir() {
		if _, err := os.Stat(filepath.Join(path, "toc.dat")); err != nil {
			return "", view.Errorf("pg.restore.notadump",
				"%s is a directory with no toc.dat, so it is not a directory-format dump", path).
				WithHint("a directory-format dump is what `" + sf.Call("pg.dump", plugin.Arg{Name: "format", Value: "directory"}) +
					"` writes — one toc.dat and one compressed file per table")
		}
		return formatDirectory, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return "", view.Errorf("pg.restore.unreadable", "opening %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, len(customMagic))
	n, err := io.ReadFull(f, magic)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", view.Errorf("pg.restore.unreadable", "reading %s: %v", path, err)
	}
	if n == 0 {
		// Restoring an empty file would report success while restoring
		// nothing, which is the same lie a truncated dump tells.
		return "", view.Errorf("pg.restore.empty", "%s is empty", path).
			WithHint("an empty file restores as nothing and reports success — if this was a " +
				"dump, it did not finish")
	}
	if string(magic[:n]) == customMagic {
		return formatCustom, nil
	}
	return formatPlain, nil
}

// checkRestoreFlags refuses, by name, the flags psql cannot honour.
//
// A plain dump is finished SQL: whether it drops objects first and who owns
// what were decided when pg_dump wrote it, and psql has no --jobs. Ignoring
// the flag instead would restore with a guarantee the caller did not ask
// for, silently.
func checkRestoreFlags(req plugin.Request, format dumpFormat) *view.Error {
	if format != formatPlain {
		return nil
	}
	sf := req.Surface()
	switch {
	case req.Int("jobs") > 1:
		return view.Errorf("pg.restore.plainflag",
			"%s needs a custom or directory dump, not plain SQL", sf.InputName("jobs")).
			WithHint("psql replays the file as written, one statement at a time — `" +
				sf.Call("pg.dump", plugin.Arg{Name: "out", Value: "<path>"},
					plugin.Arg{Name: "format", Value: "directory"}, plugin.Arg{Name: "jobs", Value: "<n>"}) +
				"` writes a dump that restores in parallel")
	case req.Bool("clean"):
		return view.Errorf("pg.restore.plainflag",
			"%s needs a custom or directory dump, not plain SQL", sf.InputName("clean")).
			WithHint("whether a plain dump drops objects first was decided when it was " +
				"written — restore into a fresh database instead")
	case req.Bool("no-owner"):
		return view.Errorf("pg.restore.plainflag",
			"%s needs a custom or directory dump, not plain SQL", sf.InputName("no-owner")).
			WithHint("ownership is baked into a plain dump's SQL — `pg_dump --no-owner` at " +
				"dump time is where that choice lives for this format")
	}
	return nil
}

// restoreTools maps a detected format to the tool that reads it. psql is the
// only reader of plain SQL and pg_restore the only reader of archives; the
// map exists so the "not installed" refusal names the one that was needed.
func lookupRestoreTool(format dumpFormat) (string, *view.Error) {
	name := "pg_restore"
	if format == formatPlain {
		name = "psql"
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", view.Errorf("pg.restore.missing", "no %s on $PATH", name).
			WithHint("rta does not reimplement it, for pg.dump's reason run in reverse: a " +
				"restore has to get COPY parsing, ownership and dependency order right. " +
				"Install the PostgreSQL client tools — `brew install libpq` or " +
				"`apt install postgresql-client`")
	}
	return p, nil
}

// restoreArgs builds the child's argv — never a shell string, and never the
// password, which travels through childEnv exactly as it does for the dump.
func restoreArgs(req plugin.Request, format dumpFormat, path string) []string {
	args := []string{
		"--host=" + req.String("host"),
		"--port=" + strconv.Itoa(req.Int("port")),
		"--username=" + req.String("user"),
		"--dbname=" + req.String("database"),
		// The dump's rule: fail now rather than hang at a prompt the plugin
		// does not own.
		"--no-password",
	}
	if format == formatPlain {
		// ON_ERROR_STOP is what makes --single-transaction mean something:
		// without it psql notes each error and keeps going, then exits zero,
		// and the transaction commits whatever half worked.
		return append(args,
			"--single-transaction",
			"--set=ON_ERROR_STOP=1",
			"--quiet",
			"--file="+path,
		)
	}
	if n := req.Int("jobs"); n > 1 {
		// Parallel workers cannot share one transaction — the same reason a
		// parallel dump needs pg_export_snapshot. --exit-on-error is the
		// closest guarantee left: stop at the first failure rather than
		// counting errors to the end and calling the survivor a restore.
		args = append(args, "--jobs="+strconv.Itoa(n), "--exit-on-error")
	} else {
		args = append(args, "--single-transaction")
	}
	if req.Bool("clean") {
		// --if-exists rides along so a dump restored into a database missing
		// some of its objects does not fail on the DROP of a thing that was
		// never there — which inside --single-transaction would abort the lot.
		args = append(args, "--clean", "--if-exists")
	}
	if req.Bool("no-owner") {
		args = append(args, "--no-owner")
	}
	return append(args, path)
}

// createdbHint is the command, as a code span, that makes the database a
// restore found missing, on the server the restore reached and as the role it
// connected as, and what to do after it. It once named the host alone, and
// createdb fills the rest from its own defaults, port 5432 and the operating
// system's user: against a server on any other port, the hint made the
// database somewhere else, or nowhere, and the restore went on missing it.
//
// **The address only when the restore reached it directly.** Through a kube:
// or ssh: profile the host and port were the local end of a forward that
// closed when the restore did, and createdb, which has no profile to open
// one again, was sent to a port nothing listens on. The server is named by
// the profile instead, which createdb has to reach by a way of its own.
func createdbHint(req plugin.Request) string {
	if req.Tunnel() == plugin.TunnelNone {
		return "`createdb --host=" + req.String("host") + " --port=" + strconv.Itoa(req.Int("port")) +
			" --username=" + req.String("user") + " " + req.String("database") + "` makes it, then restore again"
	}
	return "`createdb --username=" + req.String("user") + " " + req.String("database") + "` makes it, run " +
		"against the server profile " + req.Profile() + " reaches — the " + string(req.Tunnel()) + ": forward " +
		"this restore went through closed when it did, so createdb needs a way there of its own — then " +
		"restore again"
}

// checkTarget asks the server what it is before anything writes into it, on
// one connection: a standby is refused because it cannot be written, and a
// database already holding relations is refused unless --clean names the
// intent.
//
// The emptiness check is the friendly early refusal, not the guarantee —
// like the dump's stat before O_EXCL, a concurrent writer can still land
// between this count and the child process, and what catches that race is
// the tool's own "already exists" errors, classified below.
func checkTarget(ctx context.Context, req plugin.Request, format dumpFormat) (source, *view.Error) {
	conn, verr := connect(ctx, req)
	if verr != nil {
		// The driver has already classified an absent database (SQLSTATE
		// 3D000), but its hint points at `pg database list`, which is the
		// right next step for a typo in pg.status and the wrong one here:
		// half the time the missing database is the fresh target somebody
		// has not created yet. Same fact, restore's advice.
		if verr.Code == "pg.database.missing" {
			return source{}, view.Errorf("pg.restore.nodatabase", "%s", verr.Message).
				WithHint("rta does not create databases on its own — a typo'd name becoming " +
					"a new database is worse than this refusal. " + createdbHint(req))
		}
		return source{}, verr
	}
	defer func() { _ = conn.Close(ctx) }()

	var s source
	role, err := roleOf(ctx, conn)
	if err != nil {
		return source{}, classify(err, req)
	}
	s.role = role
	if s.standby() {
		return source{}, view.Errorf("pg.restore.standby",
			"%s is a replica, and a replica cannot be written", address(req)).
			WithHint("restore on the primary — the replica replays it from there, which is " +
				"the only path that keeps the two the same database")
	}
	if err := conn.QueryRow(ctx,
		`select current_setting('server_version_num')::int`).Scan(&s.version); err != nil {
		return source{}, classify(err, req)
	}

	if req.Bool("clean") {
		return s, nil
	}
	var relations int
	// The relkinds a dump recreates. System schemas are excluded the way
	// resolveRelation excludes them: what template1 installed is not the
	// caller's data, and refusing a genuinely fresh database for its
	// extension catalogue would make the guard fire on exactly the target it
	// exists to steer people toward.
	if err := conn.QueryRow(ctx, `
		select count(*) from pg_class c join pg_namespace n on n.oid = c.relnamespace
		where c.relkind in ('r', 'p', 'v', 'm', 'S', 'f')
		  and n.nspname not like 'pg\_%' and n.nspname <> 'information_schema'`).
		Scan(&relations); err != nil {
		return source{}, classify(err, req)
	}
	if relations > 0 {
		hint := "restore into a fresh database — `createdb` is one command, and rta will not " +
			"invent a database on its own"
		if format != formatPlain {
			hint = req.Surface().InputName("clean") + " drops what is there and recreates what the dump carries, or " + hint
		}
		return source{}, view.Errorf("pg.restore.notempty",
			"%s already holds %s", req.String("database"), rtaformat.CountOf(relations, "relation")).
			WithHint(hint)
	}
	return s, nil
}

// runRestoreTool runs the child. Stdout is discarded — psql's command tags
// are output for a terminal, and this handler's answer is the receipt — and
// stderr is kept for classification, in the C locale for the dump's reason:
// rta reads this text.
func runRestoreTool(ctx context.Context, tool string, args []string,
	req plugin.Request, format dumpFormat, path string, target source) *view.Error {
	cmd := exec.CommandContext(ctx, tool, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Env = childEnv(req)

	if err := cmd.Run(); err != nil {
		var v versions
		if strings.Contains(stderr.String(), unknownParameter) {
			v = restoreVersions(ctx, tool, format, path, target)
		}
		return classifyRestore(err, stderr.String(), req, format, v)
	}
	return nil
}

// unknownParameter is how a server refuses a SET for a parameter it does not
// have. The words are the server's, in its lc_messages, the same limit every
// text match in classifyRestore lives with.
const unknownParameter = "unrecognized configuration parameter"

// versions is what a restore refused a parameter needs to say whose it was:
// the target's major, the major of the pg_dump that wrote the file, and for
// an archive the major of the pg_restore replaying it, which writes the
// session's settings itself rather than reading them from the file. 0 is a
// version not read.
type versions struct {
	server, dump, restore int
}

// skewed reports whether the parameter the server refused was set by
// something newer than it: the pg_dump that wrote a plain file, or the
// pg_restore replaying an archive. Only then is the refusal the version
// skew; a parameter that came from anywhere else, a function's own SET
// clause among them, is the tool's words passed through.
func (v versions) skewed(format dumpFormat) bool {
	writer := v.dump
	if format != formatPlain {
		writer = v.restore
	}
	return v.server > 0 && writer > v.server
}

// restoreVersions reads the versions on either side of a refused parameter,
// once a restore has failed on one — never before, since a restore that
// works has no use for them.
func restoreVersions(ctx context.Context, tool string, format dumpFormat, path string, target source) versions {
	v := versions{server: target.version / 10000}
	if format == formatPlain {
		v.dump = plainDumpedBy(path)
		return v
	}
	v.restore = toolMajor(ctx, tool)
	v.dump = archiveDumpedBy(ctx, tool, path)
	return v
}

// plainDumpedBy is the major of the pg_dump that wrote a plain dump, read off
// the header it opens every file with — "-- Dumped by pg_dump version 18.6"
// — or 0 when the head of the file holds none.
func plainDumpedBy(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 4096)
	n, _ := io.ReadFull(f, head)
	return majorOf(string(head[:n]), "-- Dumped by pg_dump version ")
}

// archiveDumpedBy is the same for an archive, whose own header is binary:
// read off the listing pg_restore prints of it — ";     Dumped by pg_dump
// version: 18.6". Listing reads the table of contents and nothing else, and
// reaches no server, so it runs with PATH and the C locale alone.
func archiveDumpedBy(ctx context.Context, tool, path string) int {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, "--list", path)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	return majorOf(string(out), "Dumped by pg_dump version: ")
}

// skewHint names what set the parameter the target does not have, and the
// way to a file that restores there.
//
// **A plain file carries its own SETs**, written by the pg_dump that made it,
// so the way out is that server's own pg_dump — or, when the source is gone,
// the file without the line, if the line is the transaction_timeout that
// pg_dump 17 and later add: it only turns off a timeout the older server does
// not have. **An archive carries none**: pg_restore writes the session's
// settings itself, for its own version, so an older pg_restore is the way
// out — unless a newer pg_dump wrote the archive, which an older pg_restore
// cannot read, and then it is the dump that has to be taken again.
func skewHint(format dumpFormat, v versions, stderr string) string {
	n := v.server
	install := fmt.Sprintf("put it first on $PATH (`brew install postgresql@%d`, `apt install postgresql-client-%d`)", n, n)
	timeout := strings.Contains(stderr, `"transaction_timeout"`)
	if format == formatPlain {
		hint := fmt.Sprintf("pg_dump %d wrote this file for PostgreSQL %d and newer, and it sets a parameter "+
			"PostgreSQL %d does not have. PostgreSQL %d's own pg_dump writes one that restores here: %s and "+
			"dump the source again", v.dump, v.dump, n, n, install)
		if timeout {
			hint += " — or restore a copy of this file without its SET transaction_timeout line, which only " +
				"turns off a timeout this server does not have"
		}
		return hint
	}
	hint := fmt.Sprintf("pg_restore %d sets a parameter PostgreSQL %d does not have, before anything in the "+
		"archive runs", v.restore, n)
	if v.dump > 0 && v.dump <= n {
		return hint + fmt.Sprintf(". PostgreSQL %d's own pg_restore reads this archive and sets no such thing: "+
			"%s and restore again", n, install)
	}
	hint += fmt.Sprintf(", and PostgreSQL %d's own pg_restore cannot read an archive a newer pg_dump wrote. "+
		"PostgreSQL %d's own pg_dump takes one it can: %s and dump the source again", n, n, install)
	if timeout {
		hint += " — or write this archive out as SQL with `pg_restore --file`, and restore that without its " +
			"SET transaction_timeout line"
	}
	return hint
}

// classifyRestore turns the child's exit into something an operator can act
// on — classifyDump's job, for the failures that only happen in this
// direction.
func classifyRestore(err error, stderr string, req plugin.Request, format dumpFormat, v versions) *view.Error {
	msg := func(needles ...string) string {
		if line := lineMatching(stderr, needles...); line != "" {
			return line
		}
		if line := lastLine(stderr); line != "" {
			return line
		}
		return err.Error()
	}

	switch {
	// **A target older than what wrote the restore.** It reads like a broken
	// file and is a version skew: the tool's words said only that the tool
	// reported it, and the one fix, a client of the target's own major, is
	// nothing a person guesses from "unrecognized configuration parameter".
	case strings.Contains(stderr, unknownParameter) && v.skewed(format):
		return view.Errorf("pg.restore.skew", "%s", msg(unknownParameter)).
			WithHint(skewHint(format, v, stderr))
	case strings.Contains(stderr, "database") && strings.Contains(stderr, "does not exist"):
		return view.Errorf("pg.restore.nodatabase", "%s", msg("does not exist")).
			WithHint("rta does not create databases on its own — a typo'd name becoming a new " +
				"database is worse than this refusal. " + createdbHint(req))
	case strings.Contains(stderr, "role") &&
		(strings.Contains(stderr, "does not exist") || strings.Contains(stderr, "must be member of")):
		hint := "the dump sets ownership to the roles that existed at dump time — recreate " +
			"that role, or restore as it"
		if format != formatPlain {
			hint = req.Surface().InputName("no-owner") + " skips the ownership changes so everything belongs to the " +
				"connecting role, or " + hint
		}
		return view.Errorf("pg.restore.owner", "%s", msg("role")).WithHint(hint)
	case strings.Contains(stderr, "already exists"):
		// The race past checkTarget's count, or a concurrent writer.
		hint := "the target stopped being empty between the check and the restore — a fresh " +
			"database is the safe way through"
		if format != formatPlain {
			hint = req.Surface().InputName("clean") + " drops before recreating, or " + hint
		}
		return view.Errorf("pg.restore.collision", "%s", msg("already exists")).WithHint(hint)
	case strings.Contains(stderr, "read-only"):
		return view.Errorf("pg.restore.standby", "%s", msg("read-only")).
			WithHint("the target became read-only after the pre-flight check — a promoted " +
				"replica, or default_transaction_read_only. Restore on the primary")
	case strings.Contains(stderr, "unsupported version"):
		return view.Errorf("pg.restore.version", "%s", msg("unsupported version")).
			WithHint("pg_restore refuses an archive written by a newer pg_dump — install a " +
				"client at least as new as the one that wrote this file")
	case strings.Contains(stderr, "no password supplied"),
		strings.Contains(stderr, "password authentication failed"):
		return view.Errorf("pg.auth.failed", "%s", msg("password")).
			WithHint("set $" + plugin.LocalEnvVar("pg.restore", "password") +
				" — the child runs as `psql --no-password` or `pg_restore --no-password`, so it " +
				"fails here instead of waiting at a prompt nothing can answer")
	case strings.Contains(stderr, "timeout expired"):
		return view.Errorf("pg.conn.timeout", "%s", msg("timeout expired")).WithHint(childTimeoutHint)
	case strings.Contains(stderr, "permission denied"):
		return view.Errorf("pg.denied", "%s", msg("permission denied")).
			WithHint("recreating every object needs a role that may create every object — " +
				"this is the database refusing, not rta")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		hint := "it ran in one transaction, so the target holds what it held before"
		if req.Int("jobs") > 1 {
			hint = "it ran with " + req.Surface().InputName("jobs") + ", so the target may hold a partial restore — a fresh " +
				"database and a fresh run is the clean way back"
		}
		return view.Errorf("pg.restore.cancelled", "the restore was interrupted").WithHint(hint)
	}
	return view.Errorf("pg.restore.failed", "%s", msg("error:", "FATAL")).
		WithHint("the tool reported this; rta passed it through unchanged")
}

// restoreGuarantee states what a failure would have meant — the receipt's
// version of consistencyOf, for the direction where the stakes are the
// target rather than the file.
func restoreGuarantee(req plugin.Request) string {
	if n := req.Int("jobs"); n > 1 {
		return fmt.Sprintf("%d workers, stopping at the first error — a failure can leave a "+
			"partial restore", n)
	}
	return "one transaction — a failure would have rolled back everything"
}

func describeRestore(req plugin.Request, format dumpFormat) string {
	what := string(format)
	if n := req.Int("jobs"); n > 1 {
		what += fmt.Sprintf(", %d workers", n)
	}
	if req.Bool("clean") {
		what += ", existing objects dropped first"
	}
	return what
}
