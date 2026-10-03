package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// This plugin drives the `docker` CLI rather than linking the Engine SDK, for
// the reasons internal/tunnel records for kubectl, and
// plugins/kube repeats: the SDK is a large dependency for a small surface,
// and — the bigger half — connecting to a daemon is a solved problem on the
// operator's machine. Docker Desktop's socket, a rootless socket, a remote
// context over SSH, a TLS-secured DOCKER_HOST: the CLI already knows how to
// reach all of them, and every one of those is a thing this would otherwise
// have to reimplement and keep working.

// dockerBin is overridable in tests, which have no daemon and must not need
// one. Nothing outside this package writes to it.
var dockerBin = "docker"

// timeout bounds one docker invocation.
//
// The daemon is usually local and answers instantly; when it is not running,
// the CLI can sit waiting on a socket that will never answer. A call nobody
// is waiting for is a subprocess holding a slot in the plugin host.
const timeout = 20 * time.Second

// stopTimeout is how long `docker stop` gives a container to exit on its own
// before the daemon kills it, and it is deliberately not the CLI's default
// of ten seconds.
//
// A container that ignores SIGTERM is common and a container that needs
// longer than ten seconds to flush is not exotic — a database, a queue
// worker mid-batch. rta's own bound has to be past the daemon's so that what
// gives up first is the daemon, with its own reason, rather than this
// process abandoning a stop that was about to succeed.
const stopSeconds = 10

// nameRe is what may be passed to docker as a container, image or context
// name.
//
// **Argument-injection defence.** Values are interpolated into an argv slice
// and never into a shell string, so there is no shell to escape into — but a
// value beginning with `-` is read by docker as a *flag*, and something like
// `--host=tcp://elsewhere` arriving where a container name was expected would
// point the call at a different daemon. Every flag here is passed in the
// `--flag=value` form, which already keeps the value in its own argv element;
// this refuses it earlier and a second time, so a future caller reaching for
// the two-element form cannot reintroduce it.
//
// Docker's own rule for names is [a-zA-Z0-9][a-zA-Z0-9_.-]*; ids are hex,
// and image references add slashes, colons, at-signs and (for a digest) the
// sha256: prefix.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,252}$`)

func checkName(kind, v string) *view.Error {
	if v == "" {
		return nil
	}
	if !nameRe.MatchString(v) {
		return view.Errorf("docker.name.invalid", "%q is not a usable %s name", v, kind).
			WithHint("names are letters, digits and ._:/@- and may not begin with a dash")
	}
	return nil
}

// connection is which daemon a call is aimed at.
type connection struct {
	Host    string
	Context string
	// sf is the surface the request came through, so a failure names what
	// to call next the way its reader calls it.
	sf plugin.Surface
	// profile is the operator's profile the call came through
	// (Request.Profile), "" for none.
	profile string
	// reached is the daemon the call reached as its reader reaches it again
	// (Request.Reached), and "" for a call that named none: no host, no
	// context and no profile, which is the CLI's own default and names
	// nothing a refusal could add.
	reached string
	// reach is the arguments a call takes to reach the same daemon again
	// (Request.ReachArgs).
	reach []plugin.Arg
}

func connectionOf(req plugin.Request) (connection, *view.Error) {
	c := connection{
		Host:    strings.TrimSpace(req.String("host")),
		Context: strings.TrimSpace(req.String("context")),
		sf:      req.Surface(),
		profile: req.Profile(),
	}
	c.reached = reachedDaemon(req, c.Host, c.Context)
	c.reach = reachArgs(req, c.Host, c.Context)
	if verr := checkName("context", c.Context); verr != nil {
		return connection{}, verr
	}
	// A host is a URL rather than a name, so nameRe would refuse legitimate
	// values. What matters is the same thing: it must not be read as a flag.
	if strings.HasPrefix(c.Host, "-") {
		return connection{}, view.Errorf("docker.host.invalid",
			"%q is not a usable daemon address", c.Host)
	}
	return c, nil
}

// reachedDaemon names the daemon a call was aimed at: its host, else the
// context that picks one, beside the profile that supplied either. A profile
// with neither is the default daemon through a profile that sets nothing
// here, and is named as that. "" when the call named no daemon at all.
func reachedDaemon(req plugin.Request, host, context string) string {
	daemon := host
	if daemon == "" && context != "" {
		daemon = "context " + context
	}
	if daemon == "" {
		if req.Profile() == "" {
			return ""
		}
		daemon = "the default daemon"
	}
	return req.Reached(daemon)
}

// notFound is docker.notfound for a container name no daemon holds, with the
// listing that shows what is there carrying the daemon this call reached:
// pasted bare it listed the daemon the configuration where it was pasted
// names, and a container the profile's daemon lacks may well be there.
func (c connection) notFound(name string) *view.Error {
	message := fmt.Sprintf("no container named %q", name)
	if c.reached != "" {
		message += " on " + c.reached
	}
	return view.Errorf("docker.notfound", "%s", message).WithHint(c.listHint() + " shows what is there")
}

// startLine is the docker command that starts the container names again, on
// the daemon c reached, for a person to paste into a shell.
//
// **The daemon and every value as one word.** Bare, `docker start web` ran
// against the daemon of the shell it was pasted into, and a container of the
// same name there is another container; and a host such as tcp://[::1]:2375
// holds brackets a shell reads as a pattern. When a container has several
// names docker lists them joined at commas, and the first is the one to start.
func (c connection) startLine(names string) string {
	first, _, _ := strings.Cut(names, ",")
	return c.line("start", strings.TrimSpace(first))
}

// infoLine is the docker command that asks the daemon c reached whether it
// answers, for a person to paste: bare, `docker info` asks the daemon of the
// shell it is pasted into, which is not the one that did not answer.
func (c connection) infoLine() string { return c.line("info") }

// line is the docker command rest names, on the daemon c reached, each value
// one shell word.
func (c connection) line(rest ...string) string {
	args := c.args(rest...)
	words := make([]string, 0, len(args)+1)
	words = append(words, "docker")
	for _, arg := range args {
		words = append(words, plugin.ShellWord(arg))
	}
	return strings.Join(words, " ")
}

// listHint is the call that lists every container, stopped ones included, on
// the daemon c reached.
func (c connection) listHint() string {
	return "`" + c.sf.Call("docker.container.list",
		append([]plugin.Arg{{Name: "all", Value: true}}, c.callArgs()...)...) + "`"
}

func (c connection) args(rest ...string) []string {
	out := []string{}
	if c.Host != "" {
		out = append(out, "--host="+c.Host)
	}
	if c.Context != "" {
		out = append(out, "--context="+c.Context)
	}
	return append(out, rest...)
}

// callArgs points a call a message hands its reader at the daemon c reached:
// the host and the context it was given, and the profile the call came
// through, whenever there was one — the only one of the three an agent can
// give, the other two being Local, which the bridge drops.
//
// Without them the call ran against the daemon the configuration where it
// was pasted names, and a container there may well share the name: a removal
// through --host or a profile, refused because its container was running,
// offered a stop that stopped another machine's.
func (c connection) callArgs() []plugin.Arg { return c.reach }

// reachArgs is the profile a call came through, whenever there was one, and
// the host and the context it was given beside it: over MCP the profile
// alone, since the other two are Local and the bridge drops what an agent
// sends. docker opens no forward, so Request.ReachArgs always gives them.
func reachArgs(req plugin.Request, host, context string) []plugin.Arg {
	if req.Surface() == plugin.SurfaceMCP {
		return req.ReachArgs()
	}
	var named []plugin.Arg
	if host != "" {
		named = append(named, plugin.Arg{Name: "host", Value: host})
	}
	if context != "" {
		named = append(named, plugin.Arg{Name: "context", Value: context})
	}
	return req.ReachArgs(named...)
}

// run executes docker and returns its stdout.
//
// stderr is captured separately and never merged: stdout is parsed, and the
// CLI writes warnings and progress to stderr, so merging them would turn a
// good answer into a parse error.
func run(ctx context.Context, c connection, args ...string) ([]byte, *view.Error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, dockerBin, c.args(args...)...)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	// No stdin: nothing here prompts, and a subprocess that inherited this
	// process's stdin would be reading the plugin host's gRPC channel.
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	return nil, classify(ctx, err, errBuf.String(), args, c)
}

// classify turns a docker failure into something an operator can act on.
func classify(ctx context.Context, err error, stderr string, args []string, c connection) *view.Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return view.Errorf("docker.unreachable", "docker did not answer within %s", timeout).
			WithHint("the daemon may not be running — `" + c.infoLine() + "` is the same question")
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return view.Errorf("docker.cancelled", "the call was cancelled")
	}
	var notFound *exec.Error
	if errors.As(err, &notFound) {
		return view.Errorf("docker.cli.missing", "docker is not on this machine's PATH").
			WithHint("this plugin drives the docker CLI rather than linking the Engine SDK, so it " +
				"needs the binary the operator already uses — install it, or put it on PATH")
	}
	msg := firstLine(stderr)
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "cannot connect to the docker daemon"),
		strings.Contains(low, "is the docker daemon running"),
		strings.Contains(low, "connection refused"):
		// The daemon a profile names is where its host and context point,
		// and "the host input" sent the reader to a setting of this call
		// when the call's was the profile's.
		if c.profile != "" {
			return view.Errorf("docker.unreachable", "%s", msg).
				WithHint("start Docker, or fix where profile " + c.profile + " points: the host and context it " +
					"sets choose the daemon")
		}
		return view.Errorf("docker.unreachable", "%s", msg).
			WithHint("start Docker, or point this at the right daemon with the host input")
	case strings.Contains(low, "permission denied"):
		return view.Errorf("docker.denied", "%s", msg).
			WithHint("this account cannot reach the daemon's socket")
	case strings.Contains(low, "no such container"), strings.Contains(low, "no such object"),
		strings.Contains(low, "no such image"):
		if c.reached != "" {
			msg += " on " + c.reached
		}
		return view.Errorf("docker.notfound", "%s", msg).
			WithHint(c.listHint() + " shows what is there, stopped ones included")
	case strings.Contains(low, "context") && strings.Contains(low, "not found"):
		return view.Errorf("docker.context.unknown", "%s", msg)
	case msg != "":
		return view.Errorf("docker.failed", "%s", msg)
	}
	return view.Errorf("docker.failed", "docker %s failed: %v", strings.Join(args, " "), err)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.TrimPrefix(s, "Error response from daemon:"))
}

// jsonLines decodes the CLI's `--format json` output, which is one JSON
// object per line rather than a JSON array.
func jsonLines[T any](raw []byte) ([]T, *view.Error) {
	var out []T
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, view.Errorf("docker.unreadable", "docker's answer could not be read: %v", err)
		}
		out = append(out, item)
	}
	return out, nil
}

// short trims an id to the twelve characters docker itself displays.
func short(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// plural is one word or two.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// count renders "1 container" / "3 containers".
func count(n int, one, many string) string {
	return fmt.Sprintf("%d %s", n, plural(n, one, many))
}
