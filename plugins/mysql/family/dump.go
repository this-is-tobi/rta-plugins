package family

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The whole database, for a person, as a file — pg.dump's posture, with the
// differences each server itself imposes rather than ones this plugin
// invented. Engine holds them: the client's names, the flags only one of the
// two clients has, and the words for what each file can promise.
//
// **It shells out to the server's own dump client rather than reimplementing
// it**, pg.dump's most important decision repeated: a restorable dump has to
// get character sets, generated columns, triggers, routines and quoting
// right, and a file that will not restore is worse than no capability at all.
// builtin/kv sets the precedent for depending on a tool that is simply
// present or simply not.
//
// **There is one format and no --jobs.** The client writes SQL and nothing
// else — pg's custom/directory formats and parallel workers have no
// equivalent here, so the flags do not exist rather than existing and being
// refused.
//
// **The consistency guarantee is real for transactional tables and absent
// for the rest**, and the receipt says which is which. --single-transaction
// opens one REPEATABLE READ snapshot, so every InnoDB table is read from a
// single instant — but MyISAM, and on MariaDB Aria, have no transactions to
// join and are read live, mid-write if a write is happening. Claiming "one
// snapshot" over a database holding either would be the working-but-wrong
// receipt this family refuses to print, so the source is asked first and the
// caveat is counted, not assumed either way.
//
// It refuses MCP outright for pg.dump's reason: a whole database has no
// blast radius a grant could name. NeedsGrant stays unset — keys.backup's
// rule, that a grant which can never be exercised is an entry in
// `grant list` meaning nothing.

// humanOnly is the handler's half of the HumanOnly the dump and the restore
// declare. The declaration is what keeps them out of an agent's tool list; the
// check stays because a plugin binary travels to whichever rta is
// installed, and a host older than 0.11.0 does not read the flag.
// It comes first, before a connection is opened, so an agent's call never
// spends the operator's password on a question that was always going to be
// answered no. The hint is the caller's, because the dump and the restore
// refuse for mirrored reasons.
func humanOnly(req plugin.Request, id, hint string) *view.Error {
	if req.Surface() != plugin.SurfaceMCP {
		return nil
	}
	return view.Refusef(engine.NS+".human", "%s can only be run by a person at a terminal", id).
		WithHint(hint)
}

func dumpCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:        engine.NS + ".dump",
		HumanOnly: true,
		Summary:   "Back up one database to a SQL file, for a person at a terminal",
		Examples: []plugin.Example{
			{Title: "the schema alone, without the rows", Inputs: map[string]any{"include": "schema"}},
		},
		Keywords: []string{"backup", "mysqldump", "export", "snapshot"},
		Safety:   plugin.Write,
		// Running it twice at the same --out refuses rather than overwriting.
		Idempotent: false,
		Description: "The whole database as a SQL file you can restore. **Refuses MCP outright " +
			"rather than asking for a grant** — pg.dump's line: a full dump's one authorized use " +
			"is everything, and an agent that needs rows asks for " + engine.NS + ".query, which is bounded " +
			"per call.\n\n" +
			"Runs `" + engine.dumpTools[0] + "` rather than reimplementing it: a restorable dump has to get " +
			"character sets, triggers, routines and quoting right, and a file that will not " +
			"restore is worse than no capability at all. Routines, events and triggers are " +
			"included — " + engine.dumpTools[0] + " omits routines and events by default, which is how dumps " +
			"quietly stop round-tripping. The password reaches the child through its " +
			"environment, never argv; option files are ignored (`" + engine.dumpTools[0] + " --no-defaults`), so an ambient " +
			"~/.my.cnf credential is never silently spent.\n\n" +
			engine.dumpConsistency +
			"Created with O_EXCL at 0600, never over an existing file; a failed run takes its " +
			"half-written file with it. The receipt names the restore command.",
		Run: runDump,
	},
		// Local for the usual reason — a destination is a destination — and
		// so a caller can never choose which file on the host is written.
		// Belt and braces beside the MCP refusal; the two protect against
		// different mistakes.
		plugin.Field{Name: "out", Type: plugin.Path, Local: true,
			Help: "file to write; refused if it already exists"},
		plugin.Field{Name: "include", Type: plugin.String, Config: "dump.include", Default: "all",
			Options: []string{"all", "schema", "data"},
			Help:    "what to put in the file"})
}

func runDump(ctx context.Context, req plugin.Request) (view.View, error) {
	if verr := humanOnly(req, engine.NS+".dump",
		"a whole-database dump has no blast radius a grant could name — its one authorized "+
			"use is everything. Ask for the rows you need with "+req.Surface().CapabilityName(engine.NS+".query")+
			", which is bounded per call"); verr != nil {
		return nil, verr
	}

	database := req.String("database")
	if database == "" {
		return nil, view.Errorf(engine.NS+".dump.nodatabase", "say which database to dump").
			WithHint(req.Surface().SettingTo("database", plugin.Placeholder("name")) + " — " +
				nextCall(req, engine.NS+".database.list") + " shows what is there").WithInput("database")
	}
	out := strings.TrimSpace(req.String("out"))
	if out == "" {
		return nil, view.Errorf(engine.NS+".dump.nooutput", "say where the dump should be written").
			WithHint(req.Surface().SettingTo("out", "./"+database+".sql") + " — a whole database is a file, not something " +
				"to read in a terminal").WithInput("out")
	}
	path, err := expandHome(out)
	if err != nil {
		return nil, view.Errorf(engine.NS+".dump.path", "resolving %s: %v", req.Surface().InputName("out"), err).
			WithInput("out")
	}
	// Here and not only in connect, so the dry run, which connects to
	// nothing, refuses the CA the real run would, rather than describing a
	// child with an --ssl-ca that was never going to be read.
	if _, verr := tlsConfig(req); verr != nil {
		return nil, verr
	}
	if verr := checkClientTLS(req); verr != nil {
		return nil, verr
	}

	tool, err := lookupTool(engine.dumpTools)
	if err != nil {
		return nil, view.Errorf(engine.NS+".dump.missing", "no %s on $PATH",
			strings.Join(engine.dumpTools, " or ")).
			WithHint("rta does not reimplement it: a dump has to get character sets, triggers, " +
				"routines and quoting right, and one that will not restore is worse than none. " +
				"Install " + engine.clientInstall)
	}
	// A friendly early refusal, before anything opens a connection to say the
	// same thing more slowly. Not the guarantee — O_EXCL in writeDump is, and
	// that still catches the race this stat cannot.
	if _, err := os.Stat(path); err == nil {
		return nil, alreadyThere(path)
	}

	args := dumpArgs(req)
	pinning := engine.pinsTLS && req.String("tls") == "verify-ca" && takesPin(ctx, tool)
	if req.DryRun {
		if pinning {
			args = pinned(args, pinPending)
		}
		return view.Text{Body: fmt.Sprintf("would run %s %s\nand write %s",
			filepath.Base(tool), strings.Join(args, " "), path)}, nil
	}

	// **Ask the server what it is before dumping it** — pg.dump's
	// describeSource discipline. What comes back decides what the receipt may
	// honestly claim: the version, whether this is a read-only server, and
	// how many tables sit outside the snapshot guarantee.
	src, verr := describeSource(ctx, req, database)
	if verr != nil {
		return nil, verr
	}
	if pinning {
		args = pinned(args, src.pin)
	}

	started := time.Now()
	written, verr := writeDump(ctx, tool, args, req, path)
	if verr != nil {
		return nil, verr
	}

	return view.KeyValue{Pairs: []view.Pair{
		{Key: "wrote", Value: path},
		{Key: "size", Value: format.Bytes(written)},
		{Key: "took", Value: time.Since(started).Round(time.Millisecond).String()},
		{Key: "contents", Value: contentsOf(req)},
		{Key: "source", Value: src.describe()},
		{Key: "consistency", Value: src.consistency()},
		// Named on the answer rather than left in the docs. The file is every
		// row in the database in the clear, and the moment to say so is while
		// somebody is looking at where it landed.
		{Key: "at rest", Value: "unencrypted, mode 0600 — `rta kv` or `age` if it is going anywhere"},
		// **The half of the restore that is not in this file.** This dumps one
		// database, and users and grants live in another — the server's own
		// `mysql` database — so a restore onto a fresh server lands the data
		// and nobody who can reach it. The other half is spelled differently
		// per fork (Engine.otherHalf).
		{Key: "does not carry", Value: "users or grants — those are rows in the server's " +
			"own `mysql` database, not in this one, so a restore elsewhere arrives with the " +
			"data and no account able to read it. " + engine.otherHalf},
		{Key: "restore with", Value: restoreCommand(req, path)},
	}}, nil
}

// source is what the server said it is when asked, just before the dump.
type source struct {
	version  string
	readOnly bool
	// liveTables is how many BASE TABLEs in this database are not
	// transactional — MyISAM and friends — and therefore read live, outside
	// the snapshot --single-transaction opens.
	liveTables int
	// note is what the engine adds about the source (dialect.sourceNote).
	note string
	// pin is the SHA-256 fingerprint of the certificate a verify-ca
	// connection verified against ca-file, which pin.go hands a child that
	// takes one.
	pin string
}

func (s source) describe() string {
	where := s.version
	if s.readOnly {
		// A read-only server is usually a replica, and a replica's dump is as
		// current as its replication lag — said on the receipt because it
		// changes what the backup means.
		where += " — read-only, likely a replica: the dump is as current as its replication lag"
	}
	return where + s.note
}

func (s source) consistency() string {
	base := "one REPEATABLE READ snapshot (`" + engine.dumpTools[0] + " --single-transaction`) for transactional tables"
	if s.liveTables > 0 {
		return fmt.Sprintf("%s — but %s %s read live, outside it",
			base, format.CountOf(s.liveTables, "non-transactional table"), format.Plural(s.liveTables, "was", "were"))
	}
	return base
}

func describeSource(ctx context.Context, req plugin.Request, database string) (source, *view.Error) {
	db, pin, verr := connectPinned(ctx, req)
	if verr != nil {
		return source{}, verr
	}
	defer func() { _ = db.Close() }()

	s := source{pin: pin}
	if err := db.QueryRowContext(ctx, "select version()").Scan(&s.version); err != nil {
		return source{}, classify(err, req)
	}
	var ro int
	if err := db.QueryRowContext(ctx, "select @@read_only").Scan(&ro); err != nil {
		return source{}, classify(err, req)
	}
	s.readOnly = ro != 0
	// Only InnoDB sits inside --single-transaction's snapshot. Aria is
	// MariaDB's own crash-safe engine and the default for system tables, but
	// crash-safe is not transactional, so an Aria table is read live and
	// outside the snapshot exactly as MyISAM is — and excluding InnoDB alone,
	// rather than listing the engines that are not it, is what counts Aria
	// without naming it, on the one server that has it.
	if err := db.QueryRowContext(ctx, `
		select count(*) from information_schema.tables
		where table_schema = ? and table_type = 'BASE TABLE'
		  and engine is not null and engine not in ('InnoDB')`,
		database).Scan(&s.liveTables); err != nil {
		return source{}, classify(err, req)
	}
	s.note = engine.sourceNote(ctx, db, req.Surface())
	return s, nil
}

// dumpArgs builds the dump client's argv.
//
// **Never a shell string, and never the password.** argv is world-readable
// through `ps`, so the credential goes to the child through MYSQL_PWD and
// nowhere else. --no-defaults comes first because that is where the client
// requires it, and it is the argv half of the ambient-credential rule: the
// operator's ~/.my.cnf is not silently read, the same closure childEnv's
// PGPASSFILE line makes for pg.
func dumpArgs(req plugin.Request) []string {
	args := []string{
		"--no-defaults",
		"--host=" + req.String("host"),
		"--port=" + strconv.Itoa(req.Int("port")),
		// TCP, said outright, because the client does not assume it: given
		// localhost, this plugin's default host, it may take the name for
		// its Unix socket and leave --port unread. The pre-flight connection
		// dialled host:port over TCP, and the socket can be another server —
		// a local mysqld, where the one checked was a container's port — so
		// the receipt would describe a server this file never came from.
		"--protocol=TCP",
		// No --connect-timeout, unlike the restore's client: the dump client
		// has none, and refuses one as an unknown variable. What bounds its
		// connect is the operating system's own connect timeout, a minute or
		// more, and what keeps that rare is the pre-flight describeSource
		// makes a moment before, under connectTimeout: only a server gone in
		// between waits it out. A caller who stops waiting ends the child
		// with the call, and a connect that ran out is named as a timeout.
		"--user=" + req.String("user"),
		// One snapshot for everything transactional; describeSource counts
		// what falls outside it for the receipt.
		"--single-transaction",
	}
	args = append(args, engine.dumpFlags...)
	args = append(args, engine.tlsArgs(req)...)
	switch req.String("include") {
	case "schema":
		args = append(args, "--no-data", "--routines", "--events", "--triggers")
	case "data":
		// Rows only: the schema half of triggers/routines/events was asked to
		// be left out, so they all are.
		args = append(args, "--no-create-info", "--skip-triggers")
	default:
		// Routines and events are opt-in flags upstream, and leaving them out
		// is how a dump quietly stops round-tripping: the schema restores,
		// the stored procedures the application calls do not exist.
		args = append(args, "--routines", "--events", "--triggers")
	}
	return append(args, req.String("database"))
}

// writeDump creates the destination, runs the tool with its stdout handed
// the destination descriptor directly — no pipe, no copy through this
// process — and reports how much landed, removing whatever it made if the
// run fails: a partial dump left on disk is the one that gets restored six
// months later.
func writeDump(ctx context.Context, tool string, args []string,
	req plugin.Request, path string) (int64, *view.Error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return 0, alreadyThere(path)
	}
	if err != nil {
		return 0, view.Errorf(engine.NS+".dump.create", "creating %s: %v", path, err).WithInput("out")
	}

	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Stdout = f
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Env = childEnv(req)

	runErr := cmd.Run()
	closeErr := f.Close()
	switch {
	case runErr != nil:
		_ = os.Remove(path)
		return 0, classifyDump(runErr, stderr.String(), req)
	case closeErr != nil:
		_ = os.Remove(path)
		return 0, view.Errorf(engine.NS+".dump.write", "finishing %s: %v", path, closeErr).WithInput("out")
	}

	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	return size, nil
}

// childEnv is exactly what the child needs and nothing else — an unrelated
// token in the operator's shell is not something the client could ever have
// printed.
func childEnv(req plugin.Request) []string {
	// LC_ALL=C because rta reads the child's stderr to classify it, and a
	// classifier that works in one locale and silently degrades to
	// "unrecognised failure" in another is worse than one that never worked.
	env := []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	if pw := req.String("password"); pw != "" {
		// The documented-as-insecure warning on MYSQL_PWD is about multi-user
		// machines of an earlier era reading /proc environments; on every
		// platform this runs on, a process's environment is readable only by
		// its own uid — while argv is readable by everyone through `ps`.
		// Between the two channels the child actually offers, this is the
		// closed one.
		env = append(env, "MYSQL_PWD="+pw)
	}
	return env
}

// classifyDump turns the child's exit into something an operator can act on
// — the same job classify does for the driver, for the failures that only
// happen out here.
func classifyDump(err error, stderr string, req plugin.Request) *view.Error {
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
	case strings.Contains(stderr, "Access denied") && strings.Contains(stderr, "PROCESS"):
		return view.Errorf(engine.NS+".denied", "%s", msg("PROCESS")).
			WithHint(engine.processHint)
	case strings.Contains(stderr, "Access denied"):
		return view.Errorf(engine.NS+".auth.failed", "%s", msg("Access denied")).
			WithHint("set $" + plugin.LocalEnvVar(engine.NS+".dump", "password") + ", or check " + req.Surface().SettingName("user"))
	case strings.Contains(stderr, "Unknown database"):
		return view.Errorf(engine.NS+".database.notfound", "%s", msg("Unknown database")).
			WithHint(nextCall(req, engine.NS+".database.list") + " shows what is there").WithInput("database")
	case strings.Contains(stderr, "Unknown MySQL server host"):
		return view.Errorf(engine.NS+".host.unknown", "%s", msg("Unknown MySQL server host")).
			WithHint(req.Surface().DNSHint(req.String("host")))
	// Each client's own refusal of the certificate it was told to check:
	// MySQL's for want of a CA, MariaDB's for one other than it was pinned to.
	case strings.Contains(stderr, "CA certificate is required"):
		return noCA(req.Surface(), msg("CA certificate is required"))
	case strings.Contains(stderr, "Fingerprint validation"):
		return pinRefusal(req.Surface(), msg("Fingerprint validation"))
	case strings.Contains(stderr, "Can't connect"):
		return unreached(req.Surface(), msg("Can't connect"))
	case strings.Contains(stderr, "unknown variable") || strings.Contains(stderr, "unknown option"):
		// The version-skew failure: a flag this plugin passes that the
		// installed client does not know — the other fork's client
		// answering to this one's name.
		return view.Errorf(engine.NS+".dump.toolskew", "%s", msg("unknown")).
			WithHint(engine.toolSkewHint)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return view.Errorf(engine.NS+".dump.cancelled", "the dump was interrupted").
			WithHint("the partial file has been removed")
	}
	return view.Errorf(engine.NS+".dump.failed", "%s", msg("error:", "Error:")).
		WithHint("`" + filepath.Base(engine.dumpTools[0]) + "` reported this; rta passed it through unchanged")
}

// unreached answers a child that could not connect, from the client's own
// line for it: a connect that ran out of time, its bound or the operating
// system's, is not a port nobody is on, and rta's own connection reached the
// server a moment before, so what is in question is what changed in between.
//
// The client gives the operating system's error as its number, in
// parentheses — "(110) when trying to connect" — and it ran on this
// machine, so the number is this platform's own errno: ETIMEDOUT's for a
// timeout. No route is read by it too, with plugin.DialUnroutable as
// the pre-flight's own dial is: a route that went down in between, (113)
// from the MySQL client on Linux, was "is the server up, and are the host
// and port right?" about a port no packet reached.
//
// Every other number is the refusal it always was, and not the system's
// words for it: the MariaDB client gives EINPROGRESS, the state of its own
// non-blocking connect, whatever failed it — a port refused and a host
// with no route alike, measured against 11.4 — and in its words, a
// connection "in progress" would have been the answer to both.
func unreached(sf plugin.Surface, line string) *view.Error {
	errno, numbered := childErrno(line)
	switch {
	case numbered && errno == syscall.ETIMEDOUT:
		return view.Errorf(engine.NS+".timeout", "%s", line).
			WithHint("rta's own connection reached the server a moment before — a failover, a port-forward " +
				"that exited, or a firewall that began dropping rather than refusing looks exactly like this")
	case numbered && plugin.DialUnroutable(errno):
		return view.Errorf(engine.NS+".conn.unreachable", "%s", line).
			WithHint("rta's own connection reached the server a moment before, and now no route leads " +
				"there — a VPN or tunnel that went down in between looks exactly like this")
	}
	return view.Errorf(engine.NS+".conn.refused", "%s", line).WithHint(reachHint(sf))
}

// childErrno is the errno a client's line gives in parentheses, the last
// number so given.
func childErrno(line string) (syscall.Errno, bool) {
	found := errnoInLine.FindAllStringSubmatch(line, -1)
	if len(found) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(found[len(found)-1][1])
	if err != nil {
		return 0, false
	}
	return syscall.Errno(n), true
}

var errnoInLine = regexp.MustCompile(`\((\d+)\)`)

// restoreCommand names the other half. A backup capability that does not say
// how to restore is the shape of every backup that turned out not to be one.
//
// **tls travels when the dump insisted on it** — true and verify-ca, which
// verify, and skip-verify, which at least never falls back to plaintext. Left
// out, the line connected however the config where it was pasted said,
// preferred on a machine with none: a dump taken over a verified connection
// printed a restore that sent the password to a server nothing had verified,
// or in the clear to one that offered no TLS. A looser tls stays off the line —
// the default is at least as protected, a stricter config there still wins,
// and false is what a tunnel forces for the forward alone, which a line that
// spelled it would carry to a restore with no tunnel. Never the password.
//
// ca-file travels beside true and verify-ca, the modes that read it. Left
// off, the line would verify against whatever the machine it is pasted on
// trusts, and refuse the server this dump verified against its own CA.
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
	args := append([]plugin.Arg{{Name: "file", Value: path, Positional: true}}, reachArgs(req)...)
	return req.Surface().Call(engine.NS+".restore", args...)
}

// reachArgs points a call this one hands its reader at the server it reached
// (Request.ReachArgs): the profile whenever there was one, the host and the
// port only when no forward was open, then the account and the database, which
// decide what the call may see, and the TLS it insisted on, that being what the
// server may insist on in turn. To an agent the profile alone: the rest is
// Local, and the bridge drops what an agent sends. Never the password.
//
// **Without them, the call named reached another server.** Pasted, it ran
// against whatever the configuration there names, and a database "not found"
// was looked for again somewhere it was never going to be.
func reachArgs(req plugin.Request) []plugin.Arg {
	if req.Surface() == plugin.SurfaceMCP {
		return req.ReachArgs()
	}
	args := req.ReachArgs(plugin.Arg{Name: "host", Value: req.String("host")},
		plugin.Arg{Name: "port", Value: req.Int("port")})
	args = append(args, plugin.Arg{Name: "user", Value: req.String("user")})
	if database := req.String("database"); database != "" {
		args = append(args, plugin.Arg{Name: "database", Value: database})
	}
	switch mode := req.String("tls"); {
	case req.Tunnel() != plugin.TunnelNone:
		if ca := caFile(req); ca != "" {
			args = append(args, plugin.Arg{Name: "ca-file", Value: ca})
		}
	case mode == "true" || mode == "verify-ca" || mode == "skip-verify":
		args = append(args, plugin.Arg{Name: "tls", Value: mode})
		if ca := caFile(req); mode != "skip-verify" && ca != "" {
			args = append(args, plugin.Arg{Name: "ca-file", Value: ca})
		}
	}
	if name := serverName(req); name != "" {
		args = append(args, plugin.Arg{Name: "tls-server-name", Value: name})
	}
	return args
}

// nextCall names capability id called with args and reachArgs, for a hint
// that sends its reader to it next, quoted for the sentence around it — or by
// its name alone when there is nothing to give, which reads better to an
// agent than a tool beside an empty object.
func nextCall(req plugin.Request, id string, args ...plugin.Arg) string {
	sf := req.Surface()
	args = append(args, reachArgs(req)...)
	if len(args) == 0 {
		return sf.CapabilityName(id)
	}
	return "`" + sf.Call(id, args...) + "`"
}

func alreadyThere(path string) *view.Error {
	return view.Errorf(engine.NS+".dump.exists", "%s already exists", path).
		WithHint("a dump is never written over an existing file — name a new one, or move " +
			"that one aside").WithInput("out")
}

func lookupTool(names []string) (string, error) {
	var err error
	for _, name := range names {
		p, e := exec.LookPath(name)
		if e == nil {
			return p, nil
		}
		err = e
	}
	return "", err
}

func contentsOf(req plugin.Request) string {
	switch req.String("include") {
	case "schema":
		return "schema only — tables, routines, events and triggers, no rows"
	case "data":
		return "rows only, no schema"
	}
	return "schema and rows, with routines, events and triggers"
}

// lineMatching returns the first stderr line containing any of the needles —
// the child reports failures across several lines, and the useful one is
// whichever mentioned the thing that was matched on.
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
