package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The whole database, for a person, as a file.
//
// **This is the capability that has no nameable blast radius, and it is
// therefore the one that refuses MCP outright rather than asking for a
// grant.** Every other control in rta bounds a call by what it names —
// Scope narrows a grant to one record, --limit bounds a result, a profile
// bounds an environment — and a full dump's single authorized use is
// "everything". A grant that could only ever mean that is not consent, it is
// a rubber stamp with an expiry date. keys.backup and kv.copy draw the same
// line for the same reason, and leaving NeedsGrant unset is deliberate and
// copied from keys.backup: a grant that can never be exercised over the one
// surface grants exist to gate would be a standing entry in `grant list`
// that means nothing.
//
// So the whole-database dump exists, and it belongs to whoever is at the
// keyboard.
//
// **It shells out to pg_dump rather than reimplementing it**, which is the
// most important decision here. A restorable dump has to get sequences,
// extensions, ownership, row-level security, large objects and COPY escaping
// right, and a half-written pg_dump that produces a file which will not
// restore is worse than no capability at all — a backup you cannot restore
// is not a backup, it is a belief about one. builtin/kv sets the precedent
// for depending on a tool that is simply present or simply not (`pbcopy`,
// `xclip`), including naming it when it is missing.

// dumpTools are tried in order. pg_dump is the only real answer; the list
// exists so the "not installed" refusal can name what it looked for.
var dumpTools = []string{"pg_dump"}

// humanOnly is the handler's half of the HumanOnly the dump and the restore
// declare. The declaration is what keeps them out of an agent's tool list; the
// check stays because a plugin binary travels to whichever rta is
// installed, and a host older than 0.11.0 does not read the flag.
// It comes first in the handler, before the connection is opened, so an
// agent's call never spends the operator's password on a question that was
// always going to be answered no. The hint is the caller's, because the
// dump and the restore refuse for mirrored reasons — everything leaving,
// everything arriving — and one blended hint would explain neither.
func humanOnly(req plugin.Request, id, hint string) *view.Error {
	if req.Surface() != plugin.SurfaceMCP {
		return nil
	}
	return view.Refusef("pg.human", "%s can only be run by a person at a terminal", id).
		WithHint(hint)
}

func runFullDump(ctx context.Context, req plugin.Request) (view.View, error) {
	if verr := humanOnly(req, "pg.dump",
		"a whole-database dump has no blast radius a grant could name — its one "+
			"authorized use is everything. Ask for the table you need with "+
			req.Surface().CapabilityName("pg.table.dump")+", which takes a grant naming that table"); verr != nil {
		return nil, verr
	}

	out := strings.TrimSpace(req.String("out"))
	if out == "" {
		return nil, view.Errorf("pg.dump.nooutput", "say where the dump should be written").
			WithHint(req.Surface().SettingTo("out", "./"+req.String("database")+backupSuffix(req.String("format"))) +
				" — a whole database is a file, not something to read in a terminal")
	}
	path, err := expandHome(out)
	if err != nil {
		return nil, view.Errorf("pg.dump.path", "resolving %s: %v", req.Surface().InputName("out"), err)
	}
	// Here and not only in connect, so the dry run, which connects to
	// nothing, refuses the pair the real run would, rather than describing a
	// child that would carry it.
	if verr := checkTransport(req); verr != nil {
		return nil, verr
	}

	tool, err := lookupDumpTool()
	if err != nil {
		return nil, view.Errorf("pg.dump.missing", "no %s on $PATH",
			strings.Join(dumpTools, " or ")).
			WithHint("rta does not reimplement it: a dump has to get sequences, extensions, " +
				"ownership and COPY escaping right, and one that will not restore is worse " +
				"than none. Install the PostgreSQL client tools — `brew install libpq` or " +
				"`apt install postgresql-client`")
	}

	if verr := checkParallel(req); verr != nil {
		return nil, verr
	}
	// A friendly early refusal, before anything opens a connection to say the
	// same thing more slowly. It is not the guarantee — O_EXCL in writeDump
	// is, and that still catches the race this stat cannot.
	if _, err := os.Stat(path); err == nil {
		return nil, alreadyThere(path)
	}

	args := dumpArgs(req)
	if req.String("format") == "directory" {
		// Directory format is the only one pg_dump writes itself, so it takes
		// the destination rather than handing bytes back through a pipe. The
		// path is not a secret, so argv is fine for it — unlike the password,
		// which is why that rule is stated where the password is.
		args = append(args, "--file="+path)
	}
	if req.DryRun {
		return view.Text{Body: fmt.Sprintf("would run %s %s\nand write %s",
			filepath.Base(tool), strings.Join(args, " "), path)}, nil
	}

	// **Ask the server what it is before dumping it.** Two things come back
	// that the receipt cannot honestly leave out — whether this is a primary
	// or a replica, and what version it is — and it fails here, in a
	// classified error, rather than after a file has been created and a child
	// process has started.
	src, verr := describeSource(ctx, req)
	if verr != nil {
		return nil, verr
	}

	started := time.Now()
	written, verr := writeDump(ctx, tool, args, req, path)
	if verr != nil {
		return nil, verr
	}

	pairs := []view.Pair{
		{Key: "wrote", Value: path},
		{Key: "size", Value: format.Bytes(written)},
		{Key: "took", Value: time.Since(started).Round(time.Millisecond).String()},
		{Key: "contents", Value: contentsOf(req)},
		{Key: "source", Value: src.describe(req.Surface())},
		{Key: "consistency", Value: consistencyOf(req)},
		// Named on the answer rather than left in the docs. The file is every
		// row in the database in the clear, and the moment to say so is while
		// somebody is looking at where it landed.
		{Key: "at rest", Value: "unencrypted, mode 0600 — `rta kv` or `age` if it is going anywhere"},
		// **The half of the restore that is not in this file.** pg_dump omits
		// globals on purpose — roles and tablespaces belong to the cluster
		// rather than to this database — so a restore onto a fresh server
		// fails on the first ownership line and every one after it. pg.restore
		// already names the symptom when it happens; this names the cause
		// while there is still time to take the other half.
		{Key: "does not carry", Value: "roles or tablespaces — pg_dump leaves the cluster's " +
			"globals out, and a restore onto a fresh server fails on every ownership line " +
			"until `pg_dumpall --globals-only` has been replayed first"},
		{Key: "restore with", Value: restoreCommand(req, path)},
	}
	if pair, ok := restoresInto(toolMajor(ctx, tool), src.version/10000); ok {
		pairs = append(pairs, pair)
	}
	return view.KeyValue{Pairs: pairs}, nil
}

// restoresInto is the receipt's note when the pg_dump that wrote the file is
// a newer major than the server it read, and nothing when it is not.
//
// **pg_dump writes for its own version, and a restore reads what it wrote.**
// Its output loads into a server as new as itself or newer; an older one can
// refuse a setting it sets. pg_dump 17 and later set transaction_timeout,
// which PostgreSQL 16 and older do not have, so a pg_dump 18 dump of a 16
// server does not go back into that server: the plain SQL stops at the SET,
// and an archive is either replayed by a pg_restore 17 or later, which sets
// it too, or refused by 16's own pg_restore, which cannot read the newer
// archive. Found the way somebody would, by running the restore this receipt
// had just named, and said here, while the server that can still be dumped
// the right way is the one on the other end.
//
// The way out is the server's own major: its pg_dump writes a file that
// restores into it and into anything newer. A version either side could not
// read (0) says nothing, rather than a guess.
func restoresInto(client, server int) (view.Pair, bool) {
	if client == 0 || server == 0 || client <= server {
		return view.Pair{}, false
	}
	why := fmt.Sprintf("pg_dump %d wrote it, newer than this PostgreSQL %d server, and a server "+
		"older than the pg_dump that wrote a dump can refuse a setting its restore sets", client, server)
	if server < 17 {
		why = fmt.Sprintf("pg_dump %d wrote it, and its restore sets transaction_timeout, which "+
			"PostgreSQL %d does not have — a restore into this server stops at that line", client, server)
	}
	return view.Pair{Key: "restores into", Value: fmt.Sprintf("PostgreSQL %d or newer, reliably: %s. "+
		"PostgreSQL %d's own pg_dump takes one that restores here: put it first on $PATH "+
		"(`brew install postgresql@%d`, `apt install postgresql-client-%d`) and dump again",
		client, why, server, server, server)}, true
}

// toolMajor is the major version the PostgreSQL client at path reports —
// "pg_dump (PostgreSQL) 18.6 (Homebrew)" is 18 — or 0 when it says nothing
// that reads as one. It runs with PATH and the C locale alone: asking a
// client its version needs no credential, so it is handed none.
func toolMajor(ctx context.Context, path string) int {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	return majorOf(string(out), "(PostgreSQL) ")
}

// majorOf reads the major version that follows label in text — the digits up
// to the first that are not — or 0 when there is none.
func majorOf(text, label string) int {
	_, rest, ok := strings.Cut(text, label)
	if !ok {
		return 0
	}
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(rest)
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

// source is what the server said it is when asked, just before the dump.
type source struct {
	role    string // "primary" or "standby"
	version int    // server_version_num, e.g. 170011
}

func (s source) standby() bool { return s.role == "standby" }

// describe says what the server is, and for a standby where its lag is
// read — named for sf, the surface reading the receipt.
func (s source) describe(sf plugin.Surface) string {
	where := fmt.Sprintf("%s, PostgreSQL %d.%d", s.role, s.version/10000, s.version%10000)
	if s.standby() {
		// Said on the receipt because it changes what the dump means and what
		// can go wrong with it, and the moment to say so is while somebody is
		// looking at the backup they just took.
		where += " — a replica is as current as its replay lag, which " + sf.CapabilityName("pg.overview") + " reports"
	}
	return where
}

// describeSource asks the server what it is before anything is dumped from
// it, so a failure to reach it is a classified error rather than a child
// process exiting oddly after a file has already been created.
func describeSource(ctx context.Context, req plugin.Request) (source, *view.Error) {
	conn, verr := connect(ctx, req)
	if verr != nil {
		return source{}, verr
	}
	defer func() { _ = conn.Close(ctx) }()

	var s source
	role, err := roleOf(ctx, conn)
	if err != nil {
		return source{}, classify(err, req)
	}
	s.role = role
	if err := conn.QueryRow(ctx,
		`select current_setting('server_version_num')::int`).Scan(&s.version); err != nil {
		return source{}, classify(err, req)
	}
	return s, nil
}

// consistencyOf states the guarantee the dump actually carries.
//
// **pg_dump is consistent by default and it is worth saying how**, because
// the mechanism is what decides whether the parallel form is equally safe. A
// serial dump runs in one REPEATABLE READ transaction, so every table is
// read from a single snapshot no concurrent writer can move. A parallel dump
// cannot share one transaction across workers, so the leader exports its
// snapshot with pg_export_snapshot() and every worker joins it — the same
// point in time, from N connections.
//
// Which is exactly why **--no-synchronized-snapshots is never passed here**,
// not as a fallback and not on older servers. It is the one flag that turns a
// parallel dump into a set of unrelated reads at different times, producing a
// file that restores without complaint and holds a database state that never
// existed. If a server cannot export a snapshot, the right answer is the
// serial dump, and pg_dump saying so is a better outcome than rta silently
// dropping the guarantee to keep a flag working.
func consistencyOf(req plugin.Request) string {
	if n := req.Int("jobs"); n > 1 {
		return fmt.Sprintf("one snapshot shared by %d workers (pg_export_snapshot)", n)
	}
	return "one REPEATABLE READ snapshot"
}

// checkParallel refuses --jobs anywhere it would not work, by name.
//
// pg_dump parallelises by opening one connection per worker and having each
// dump a different table, which needs an output the workers can write
// independently — so it is directory format or nothing. pg_dump says
// "parallel backup only supported by the directory format" and exits; saying
// it here means the message names rta's own flags and arrives before a
// connection is opened.
//
// Refused rather than silently switching the format, which would hand
// somebody a directory where they asked for a file and change the restore
// command under them.
func checkParallel(req plugin.Request) *view.Error {
	if req.Int("jobs") <= 1 || req.String("format") == "directory" {
		return nil
	}
	sf := req.Surface()
	return view.Errorf("pg.dump.notparallel",
		"%s needs %s, not %s", sf.InputName("jobs"), sf.InputTo("format", "directory"), req.String("format")).
		WithHint("pg_dump parallelises by giving each worker its own connection and its own " +
			"file, so there has to be a directory to put them in — " + sf.InputTo("format", "directory") +
			" keeps " + sf.InputTo("jobs", req.Int("jobs")) + ", restored with `pg_restore --jobs`")
}

// writeDump creates the destination, runs the tool, and reports how much
// landed — cleaning up whatever it made if the run fails.
//
// **The exclusive create is the no-overwrite guarantee**, one syscall rather
// than a stat followed by a create, which is a race a backup should not have.
// keys.restore refuses to write over an existing key for the same reason.
// 0600 for a file and 0700 for a directory, set at creation rather than
// chmod'd afterwards, so there is no instant where the dump is both complete
// and readable by everyone: this is every row in the database.
func writeDump(ctx context.Context, tool string, args []string,
	req plugin.Request, path string) (int64, *view.Error) {
	if req.String("format") == "directory" {
		// pg_dump writes the files, and accepts an existing directory only if
		// it is empty — so creating it here first both reserves the name
		// exclusively and hands the tool something it will take.
		//
		// Parents first, so `--out ./backups/2026-08-29` works without
		// pre-creating `backups`; then the leaf exclusively, which is where
		// the no-overwrite guarantee lives. MkdirAll on the leaf would accept
		// an existing directory and lose whatever was in it.
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return 0, view.Errorf("pg.dump.create", "creating %s: %v", filepath.Dir(path), err)
		}
		if err := os.Mkdir(path, 0o700); errors.Is(err, os.ErrExist) {
			return 0, alreadyThere(path)
		} else if err != nil {
			return 0, view.Errorf("pg.dump.create", "creating %s: %v", path, err)
		}
		if verr := runDumpTool(ctx, tool, args, req, nil); verr != nil {
			_ = os.RemoveAll(path)
			return 0, verr
		}
		return sizeOnDisk(path), nil
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return 0, alreadyThere(path)
	}
	if err != nil {
		return 0, view.Errorf("pg.dump.create", "creating %s: %v", path, err)
	}
	verr := runDumpTool(ctx, tool, args, req, f)
	closeErr := f.Close()
	switch {
	case verr != nil:
		// A partial dump left on disk is the failure that gets restored six
		// months later, so the half-written file goes rather than staying to
		// be mistaken for a good one.
		_ = os.Remove(path)
		return 0, verr
	case closeErr != nil:
		_ = os.Remove(path)
		return 0, view.Errorf("pg.dump.write", "finishing %s: %v", path, closeErr)
	}
	return sizeOnDisk(path), nil
}

func alreadyThere(path string) *view.Error {
	return view.Errorf("pg.dump.exists", "%s already exists", path).
		WithHint("a dump is never written over an existing file — name a new one, or " +
			"move that one aside")
}

// sizeOnDisk measures what was written — one file, or every file under a
// directory-format dump.
//
// **It asks the filesystem how long the file is, rather than asking the
// descriptor where it got to**, and that distinction was a live bug: the
// first version read `f.Seek(0, io.SeekCurrent)` after the child exited, on
// the reasoning that the child inherits this exact descriptor and therefore
// shares its offset. It does. What that misses is that **pg_dump seeks** —
// custom format writes the archive and then goes back to patch the
// table-of-contents offsets, so the shared offset ends up near the start of
// the file. A 219 MB dump reported itself as 6.6 KiB, which is exactly the
// kind of wrong that looks plausible on a receipt nobody re-measures.
//
// Best effort: a size that could not be read is worth less than the dump it
// describes, and the dump is already safely on disk by the time this runs.
func sizeOnDisk(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return info.Size()
	}
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // a file that cannot be stat'd is not a failed backup
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// dumpArgs builds pg_dump's argv.
//
// **Never a shell string, and never the password.** argv is world-readable
// through `ps` on every platform this runs on, so the credential goes to the
// child through its environment and nowhere else; the same reason rta builds
// its own DSN rather than a URL is the reason this builds a slice rather
// than a command line.
func dumpArgs(req plugin.Request) []string {
	host, _ := childHost(req)
	args := []string{
		"--host=" + host,
		"--port=" + strconv.Itoa(req.Int("port")),
		"--username=" + req.String("user"),
		"--dbname=" + req.String("database"),
		// Without this, a missing password makes pg_dump prompt on a
		// terminal the plugin does not own, and the call hangs until
		// somebody kills it. Failing immediately is the behaviour a wrapper
		// owes its caller.
		"--no-password",
	}
	switch f := req.String("format"); f {
	case "custom", "directory":
		args = append(args, "--format="+f)
	default:
		args = append(args, "--format=plain")
	}
	// **The one flag that changes the transfer rate rather than the output.**
	// pg_dump with --jobs N opens N connections and dumps N tables at once,
	// which turns a serial walk of a big database into work the server and
	// the disk can overlap. checkParallel has already refused it anywhere it
	// would not apply, so reaching here means directory format.
	if n := req.Int("jobs"); n > 1 {
		args = append(args, "--jobs="+strconv.Itoa(n))
	}
	switch req.String("include") {
	case "schema":
		args = append(args, "--schema-only")
	case "data":
		args = append(args, "--data-only")
	}
	return args
}

// runDumpTool runs the child, writing to f when there is one.
//
// **When f is an *os.File, os/exec hands its descriptor to the child
// directly** — no pipe, no copying goroutine, no buffer in this process at
// all. That is the whole transfer path for a plain or custom dump: pg_dump
// writes to the destination fd and rta is not on the hot path. It is worth
// naming because the obvious alternative — StdoutPipe and io.Copy — would put
// every byte of a hundred-gigabyte database through a 32 KiB buffer in a Go
// process that has no reason to see any of it.
//
// Directory format passes nil: the tool writes its own files, which is what
// makes --jobs possible.
func runDumpTool(ctx context.Context, tool string, args []string,
	req plugin.Request, f *os.File) *view.Error {
	cmd := exec.CommandContext(ctx, tool, args...)
	if f != nil {
		cmd.Stdout = f
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Env = childEnv(req)

	if err := cmd.Run(); err != nil {
		return classifyDump(err, stderr.String(), req)
	}
	return nil
}

// childEnv is exactly what pg_dump needs and nothing else.
//
// A child inherits the whole environment otherwise, and this one is handed a
// database password: it gets what it needs to connect and no more, so an
// unrelated token in the operator's shell is not something pg_dump could ever
// have printed.
func childEnv(req plugin.Request) []string {
	// **LC_ALL=C because rta reads this output to classify it.** pg_dump is
	// translated, and the first live run of the replica-conflict path came
	// back as `pg_dump: détail : La commande était : COPY public.t1 ...` —
	// which matched only by luck, because the half that matched was the
	// server's message rather than pg_dump's own label. A classifier that
	// works in one locale and silently degrades to "unrecognised failure" in
	// another is worse than one that never worked, so the child's messages
	// are pinned to the language this code is written in. It does not change
	// the dump: format and encoding come from COPY and client_encoding, not
	// from LC_ALL.
	//
	// The server's own messages still arrive in the server's lc_messages,
	// which nothing here controls — the reason `classify` matches SQLSTATE
	// codes for the driver and only this path matches text at all.
	env := []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	if pw := req.String("password"); pw != "" {
		env = append(env, "PGPASSWORD="+pw)
	}
	t := transportOf(req)
	if t.mode != "" {
		env = append(env, "PGSSLMODE="+t.mode)
	}
	// The name the certificate is checked for, given the way libpq takes one:
	// the child's host is that name (childHost) and this is the address it
	// connects to.
	if _, hostaddr := childHost(req); hostaddr != "" {
		env = append(env, "PGHOSTADDR="+hostaddr)
	}
	// **The same files dsn() hands the driver, and a path that is not there
	// for each one nobody named.** The same keywords, carried the only way a
	// subprocess reads them — pg_dump has no connection-string argument for a
	// single value like these, only PG* environment variables and its own
	// -h/-p/-U/-d. libpq reads an empty value as unset and falls back to the
	// file under ~/.postgresql (the passfile's lesson, below), so a file no
	// setting named is spelled as a path that does not exist: a missing client
	// certificate is no certificate, and a missing root certificate means no
	// verification below verify-ca, which is what the driver does with an
	// empty one. The revocation list is closed the same way, since pgx has no
	// way to read one and a libpq that did would refuse a connection the
	// pre-flight had made.
	//
	// Left out under disable, for dsn()'s reason and libpq's own: it refuses
	// system beside disable outright. And system for verify-full with no CA
	// named, which is what the driver does there: it checks this machine's
	// own store, and libpq 16 and later takes the word for the same thing.
	root, cert, key := t.rootCert, t.clientCert, t.clientKey
	if t.mode == "disable" {
		root, cert, key = "", "", ""
	}
	if root == "" && t.mode == "verify-full" {
		root = "system"
	}
	env = append(env, "PGSSLROOTCERT="+orUnreadable(root), "PGSSLCERT="+orUnreadable(cert),
		"PGSSLKEY="+orUnreadable(key), "PGSSLCRL="+unreadable)
	// The bound dsn() gives the in-process connect, given to the child the
	// way libpq reads it, in the same seconds. pgx connecting first proves
	// the server was there a moment ago, and nothing about the child's own
	// connection a moment later: a server gone in between — a failover, a
	// port-forward that exited, a firewall that began dropping — left
	// pg_dump, psql or pg_restore waiting on the operating system's connect
	// timeout, more than a minute, with nothing on the terminal to say why.
	env = append(env, "PGCONNECT_TIMEOUT="+strconv.Itoa(int(connectTimeout/time.Second)))
	// The same ambient credential dsn() closes for the in-process driver,
	// closed here for the subprocess — a separate fix, because libpq and
	// pgconn disagree about what an empty value means. `PGPASSFILE=` is read
	// by libpq as "unset, use the default", so it reads ~/.pgpass anyway;
	// only naming a path that is not there fails closed. /dev/null works too
	// and is worse: libpq prints `WARNING: password file "/dev/null" is not a
	// plain file` onto the stderr classifyDump parses.
	//
	// HOME is not handed over, and that would not have done it either: libpq
	// falls back to getpwuid when HOME is unset, so it finds the operator's
	// ~/.pgpass either way — verified against a real server before this line
	// was written, because the obvious version of this fix silently does
	// nothing. The TLS files above are closed by the same means, a path that
	// is not there, for the same reason; ssl-home reaches the child as the
	// paths it resolved, never as a home directory to search.
	env = append(env, "PGPASSFILE=/nonexistent/rta-refuses-ambient-credentials")
	return env
}

// childTimeoutHint answers a child whose connect ran out the bound childEnv
// gives it, libpq's "timeout expired". rta's own connection reached the
// server a moment before, so what is in question is what changed in between,
// not the address, and the hint the in-process timeout gives would not say so.
const childTimeoutHint = "rta's own connection reached the server a moment before — a failover, " +
	"a port-forward that exited, or a firewall that began dropping rather than refusing looks exactly like this"

// classifyDump turns pg_dump's exit into something an operator can act on —
// the same job classify does for the driver, for the failures that only
// happen out here.
func classifyDump(err error, stderr string, req plugin.Request) *view.Error {
	// **Report the line that explains it, not the last one.** pg_dump ends a
	// failure with `detail: The command was: COPY ...`, so the last line is
	// reliably the least informative — the replica-conflict path first
	// surfaced as a COPY statement with no hint of why it stopped. Each branch
	// below names what it matched on and gets that line back.
	msg := func(needles ...string) string {
		if line := lineMatching(stderr, needles...); line != "" {
			return line
		}
		if line := lastLine(stderr); line != "" {
			return line
		}
		return err.Error()
	}

	// The TLS files and the server's verdict on the certificate the child
	// presented, ahead of the rest: a connection that failed there reached
	// nothing the cases below read.
	if verr := childTLS(stderr, req); verr != nil {
		return verr
	}

	switch {
	// **The failure that only happens on a replica**, and the one nobody
	// diagnoses from the message. A dump on a hot standby holds a snapshot for
	// as long as it runs; when the primary vacuums away row versions that
	// snapshot still needs, replay would have to overwrite them, and the
	// standby resolves the conflict by cancelling the reader. So a dump that
	// works on a small database fails on a big one, intermittently, with a
	// message about "recovery" that names neither the dump nor the setting
	// that fixes it.
	case strings.Contains(stderr, "conflict with recovery"),
		strings.Contains(stderr, "canceling statement due to conflict"):
		return view.Errorf("pg.dump.replicaconflict",
			"the replica cancelled the dump to catch up with its primary: %s",
			msg("conflict with recovery", "canceling statement")).
			WithHint("a dump holds one snapshot for its whole run, and the standby killed it " +
				"rather than fall further behind. Set hot_standby_feedback = on so the primary " +
				"keeps what this snapshot needs, or raise max_standby_streaming_delay — or dump " +
				"from the primary, where nothing can cancel it")
	case strings.Contains(stderr, "pg_export_snapshot"),
		strings.Contains(stderr, "synchronized snapshot"):
		// Never answered by dropping to --no-synchronized-snapshots, which is
		// the flag that would make this "work": it turns one parallel dump into
		// N unrelated reads at different times, and produces a file that
		// restores cleanly into a state the database was never in.
		return view.Errorf("pg.dump.nosnapshot",
			"this server cannot share one snapshot across parallel workers: %s",
			msg("pg_export_snapshot", "synchronized snapshot")).
			WithHint("run it serially with " + req.Surface().InputTo("jobs", 1) + ", which uses a single " +
				"transaction. rta will not run `pg_dump --no-synchronized-snapshots` to make " +
				req.Surface().InputName("jobs") + " work here: that drops the " +
				"guarantee that every table came from the same instant, and a dump without it " +
				"restores without complaint into a state that never existed")
	case strings.Contains(stderr, "server version") && strings.Contains(stderr, "aborting"):
		// The one failure nobody guesses from the message, because it reads
		// like a server problem and is a client one.
		return view.Errorf("pg.dump.version", "%s", msg("server version")).
			WithHint("pg_dump refuses a server newer than itself — install a client at least " +
				"as new as the server " + req.Surface().CapabilityName("pg.status") + " reports")
	case strings.Contains(stderr, "no password supplied"),
		strings.Contains(stderr, "password authentication failed"):
		return view.Errorf("pg.auth.failed", "%s", msg("password")).
			WithHint("set $" + plugin.LocalEnvVar("pg.dump", "password") +
				" — it runs as `pg_dump --no-password`, so it fails here instead of " +
				"waiting at a prompt nothing can answer")
	case strings.Contains(stderr, "timeout expired"):
		return view.Errorf("pg.conn.timeout", "%s", msg("timeout expired")).WithHint(childTimeoutHint)
	case strings.Contains(stderr, "permission denied"):
		return view.Errorf("pg.denied", "%s", msg("permission denied")).
			WithHint("dumping every table needs a role that can read every table — this is " +
				"the database refusing, not rta")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return view.Errorf("pg.dump.cancelled", "the dump was interrupted").
			WithHint("the partial file has been removed")
	}
	return view.Errorf("pg.dump.failed", "%s", msg("error:")).
		WithHint("`" + filepath.Base(dumpTools[0]) + "` reported this; rta passed it through unchanged")
}

// lineMatching returns the first stderr line containing any of the needles.
//
// pg_dump reports a failure across several lines — an "error:" line, a
// "detail:" line carrying the server's own message, and a "detail: The
// command was:" line last. The useful one is whichever mentioned the thing
// that was matched on, which is never reliably the first or the last.
func lineMatching(stderr string, needles ...string) string {
	for _, line := range strings.Split(stderr, "\n") {
		for _, needle := range needles {
			if strings.Contains(line, needle) {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

func lookupDumpTool() (string, error) {
	var err error
	for _, name := range dumpTools {
		p, e := exec.LookPath(name)
		if e == nil {
			return p, nil
		}
		err = e
	}
	return "", err
}

// contentsOf says what is in the file, in the words the flags used.
func contentsOf(req plugin.Request) string {
	what := "schema and rows"
	switch req.String("include") {
	case "schema":
		what = "schema only, no rows"
	case "data":
		what = "rows only, no schema"
	}
	what += ", " + req.String("format") + " format"
	if n := req.Int("jobs"); n > 1 {
		what += fmt.Sprintf(", %d workers", n)
	}
	return what
}

// restoreCommand names the other half. A backup capability that does not say
// how to restore is the shape of every backup that turned out not to be one.
//
// It names `rta pg restore` rather than the raw psql/pg_restore invocation it
// printed before that capability existed: rta reads the format off the bytes,
// so the psql-versus-pg_restore decision this line used to make for the
// reader is made again, correctly, at restore time — and the raw command
// carried none of the guardrails (the non-empty refusal, the standby
// refusal, ON_ERROR_STOP) that are the reason the capability exists.
//
// **--jobs still carries over**, which is the half people forget: a dump
// written by eight workers restores serially unless you ask, and the restore
// is usually the slower direction because it rebuilds every index. The
// connection flags are spelled out so the line works on a machine whose rta
// config does not already point at this server.
//
// **So is the transport's protection, when the dump had any.** sslmode
// travels when it is stricter than prefer, and sslrootcert beside it, which
// only verify-ca and verify-full can carry — checkRootCert refuses it beside
// require before any dump runs. Left out, the line connected however the
// config where it was pasted said, prefer on a machine with none: a dump
// taken over verify-full printed a restore that sent the password to a
// server nothing had verified. A looser sslmode is left out on purpose — the
// default is at least as protected, a stricter config there still wins, and
// disable is what a tunnel forces for the forward alone, which a line that
// spelled it would carry to a restore with no tunnel. Never the password.
//
// Spelled by the request's surface, like every call this plugin names: the
// command line at a terminal, with the path quoted when a shell would split
// it, and the capability with its boxes filled in the TUI.
//
// **The profile the dump came through, whenever there was one, and the
// address only when the dump reached it directly.** Through a kube: or ssh:
// profile the host and port the dump was handed were the local end of a
// forward that closed when the dump did, and the line named 127.0.0.1 and a
// port nothing listens on any more — the profile is what opens the forward
// again. Named beside a direct address too, since the credentials may be the
// profile's and no other layer holds them; and the address beside it then,
// since it may be one the caller typed over the profile's (Request.Profile).
func restoreCommand(req plugin.Request, path string) string {
	args := append([]plugin.Arg{{Name: "file", Value: path, Positional: true}},
		req.ReachArgs(plugin.Arg{Name: "host", Value: req.String("host")},
			plugin.Arg{Name: "port", Value: req.Int("port")})...)
	args = append(args, plugin.Arg{Name: "user", Value: req.String("user")},
		plugin.Arg{Name: "database", Value: req.String("database")})
	// Over a forward the mode is the host's, forced, and a line spelling it
	// would be refused by the host beside the profile: what travels is what
	// turned TLS on there, the CA and the name.
	t := transportOf(req)
	namedRoot := t.rootCert != "" && !t.fromHome.root
	switch {
	case req.Tunnel() != plugin.TunnelNone:
		if namedRoot {
			args = append(args, plugin.Arg{Name: "sslrootcert", Value: t.rootCert})
		}
	case verifiesOrRequires(t.mode):
		args = append(args, plugin.Arg{Name: "sslmode", Value: t.mode})
		if namedRoot {
			args = append(args, plugin.Arg{Name: "sslrootcert", Value: t.rootCert})
		}
	}
	if t.serverName != "" {
		args = append(args, plugin.Arg{Name: "tls-server-name", Value: t.serverName})
	}
	// The client pair the dump presented, by the settings that named it, and
	// the switch that found it when ssl-home did: a restore that connects
	// without the certificate the server asked this dump for is refused, and
	// one that looks under the home of the shell it is pasted into presents
	// whatever it finds there.
	if t.mode != "disable" {
		if t.clientCert != "" && !t.fromHome.client {
			args = append(args, plugin.Arg{Name: "sslcert", Value: t.clientCert},
				plugin.Arg{Name: "sslkey", Value: t.clientKey})
		}
		if req.Bool("ssl-home") {
			args = append(args, plugin.Arg{Name: "ssl-home", Value: true})
		}
	}
	if n := req.Int("jobs"); n > 1 {
		args = append(args, plugin.Arg{Name: "jobs", Value: n})
	}
	return req.Surface().Call("pg.restore", args...)
}

// verifiesOrRequires reports whether sslmode insists on TLS: require and the
// two that verify, the modes stricter than the prefer every call defaults to.
func verifiesOrRequires(sslmode string) bool {
	switch sslmode {
	case "require", "verify-ca", "verify-full":
		return true
	}
	return false
}

func backupSuffix(f string) string {
	switch f {
	case "custom":
		return ".dump"
	case "directory":
		// A directory, so no extension — but a name that says what it is,
		// since `--out ./app` giving back a directory surprises people.
		return "-backup"
	}
	return ".sql"
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// expandHome makes a Local path input absolute, with a leading ~ resolved.
//
// The tilde rule is plugin.ExpandHome now. This was a copy of it, as were seven
// others across these plugins, because the host's own lives in an internal
// package no plugin can import — and copies of this exact rule had already
// drifted twice inside rta itself, handling "~/x" and not a bare "~", which is
// what made the SDK export it in v0.26.0.
//
// filepath.Abs stays this function's own work, and is why the call site does not
// simply call the SDK: the resolved path is stat'd before anything connects, and
// it is what the dry run and the receipt name back to the operator, where a
// relative "." answers nothing.
func expandHome(p string) (string, error) {
	return filepath.Abs(plugin.ExpandHome(p))
}
