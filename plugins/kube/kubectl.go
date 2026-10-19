package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// This plugin shells out to `kubectl` rather than linking client-go, which is
// the same decision internal/tunnel took for port-forwards. Two reasons, and the second is the bigger one.
//
// Size: measured for the surface a resolver needs — clientcmd, spdy,
// portforward — client-go is +67 modules and +11 MB. This plugin needs
// strictly more of it (typed clients for pods, deployments, namespaces), so
// the number is worse here, not better.
//
// Authentication: OIDC refresh, `aws eks get-token`,
// `gke-gcloud-auth-plugin` and whatever exec credential plugin an
// organisation runs are all solved on the operator's machine already, by the
// binary they already keep working. Linking client-go means adopting that
// maintenance and getting it subtly wrong for somebody. Shelling out means a
// cluster that `kubectl` can reach is a cluster this can read, on the day
// they configure it, with no code here at all.

// kubectlBin is overridable in tests, which have no cluster and must not need
// one. Nothing outside this package writes to it.
var kubectlBin = "kubectl"

// timeout bounds one kubectl invocation.
//
// A cluster that is unreachable does not fail fast: the client retries a TLS
// handshake against an endpoint nobody answers for as long as it is allowed
// to. Over MCP the caller's own client has usually given up well before
// then, and a call nobody is waiting for is a subprocess holding a slot in
// the plugin host. `--request-timeout` is passed as well so kubectl gives up
// on its own terms first, and this is the backstop for the case where it
// does not.
const timeout = 20 * time.Second

// requestTimeout is what kubectl is told, comfortably inside timeout so its
// own error — which names the cluster and is actionable — is what an operator
// usually sees, rather than this package's blunter "did not answer".
const requestTimeout = "15s"

// nameRe is what may be passed to kubectl as a context, namespace or resource
// name.
//
// **This is argument-injection defence, not input validation for its own
// sake.** Every value below is interpolated into an argv slice, never into a
// shell string, so there is no shell to escape into — but a value beginning
// with `-` is still read by kubectl as a *flag*, and `--kubeconfig=/tmp/mine`
// arriving where a context name was expected would point the call at a
// different cluster entirely. The `--flag=value` form used throughout keeps
// the value in the same argv element as its flag, which already prevents
// that; this refuses it a second time and earlier, so a future caller that
// reaches for the two-element form cannot reintroduce it.
//
// The character set is Kubernetes' own for object names (RFC 1123 labels,
// plus the dots, underscores and slashes that appear in real context names —
// `arn:aws:eks:...` clusters and `kind-rta-lab` alike), and deliberately
// excludes whitespace, quotes and everything else.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,252}$`)

// checkName refuses a value kubectl would not read as the thing it was given
// as.
func checkName(kind, v string) *view.Error {
	if v == "" {
		return nil
	}
	if !nameRe.MatchString(v) {
		return view.Errorf("kube.name.invalid",
			"%q is not a usable %s name", v, kind).
			WithHint("names are letters, digits and ._:/@- and may not begin with a dash")
	}
	return nil
}

// selection is the connection half of every call: which cluster, which
// namespace.
//
// Held together rather than passed as two strings because they are read
// together on every path and because the *rule* about them differs — see
// connFields for why one of them a remote caller may never supply.
type selection struct {
	Context   string
	Namespace string
	AllNS     bool
	// sf is the surface the call came through, so a failure reading the
	// cluster names what to call next the way its reader calls it.
	sf plugin.Surface
	// reach is what a call named next gives to reach the same cluster again:
	// the profile whenever there was one, and the context beside it, which an
	// agent cannot give (Request.ReachArgs).
	reach []plugin.Arg
}

// kubectlLine is the kubectl command rest names, on the context s read, each
// value one shell word: for a message that hands a command over to be pasted,
// which bare asks the context of the shell it lands in.
func (s selection) kubectlLine(rest ...string) string {
	words := []string{"kubectl"}
	if s.Context != "" {
		words = append(words, plugin.ShellWord("--context="+s.Context))
	}
	for _, word := range rest {
		words = append(words, plugin.ShellWord(word))
	}
	return strings.Join(words, " ")
}

func selectionOf(req plugin.Request) (selection, *view.Error) {
	var named []string
	if ns := strings.TrimSpace(req.String("namespace")); ns != "" {
		named = []string{ns}
	}
	return selectionFor(req, named)
}

// selectionFor is selectionOf for a call that has already read the namespaces it
// names, as a capability that takes a list does: the selection carries a
// namespace only when the call named exactly one, and the refusal of a
// namespace beside every namespace is made for the list.
func selectionFor(req plugin.Request, named []string) (selection, *view.Error) {
	s := selection{
		Context: strings.TrimSpace(req.String("context")),
		AllNS:   req.Bool("all-namespaces"),
		sf:      req.Surface(),
	}
	if len(named) == 1 {
		s.Namespace = named[0]
	}
	if s.Context == "" || s.sf == plugin.SurfaceMCP {
		s.reach = req.ReachArgs()
	} else {
		s.reach = req.ReachArgs(plugin.Arg{Name: "context", Value: s.Context})
	}
	if verr := checkName("context", s.Context); verr != nil {
		return selection{}, verr
	}
	if verr := checkName("namespace", s.Namespace); verr != nil {
		return selection{}, verr
	}
	// **Refused rather than resolved, because a grant is scoped on one of
	// these two and not the other.** args() below picks --all-namespaces when
	// both are set, which reads as a harmless precedence rule and is not one:
	// every capability here declares Scope: "namespace", and internal/grant's
	// scopes() derives the scope a call is checked against from the value of
	// the `namespace` field alone — it has no idea `all-namespaces` exists. So
	// a caller holding a grant for one namespace could send that namespace
	// *and* all-namespaces together, satisfy the scope check on the first, and
	// have the request answered from every namespace in the cluster.
	//
	// Latent rather than live today, only because no *namespace-scoped* kube
	// capability sets NeedsGrant or is Destructive, so grant.Required() is
	// false for every capability that carries these two fields and the check
	// never runs. (kube.context.set and kube.serviceaccount.revoke do set
	// them. The first takes no namespace. The second does, and reads it
	// through here, but declares it Local — its grant is scoped on the name
	// alone, so no MCP caller may choose which namespace the delete reaches —
	// and declares no all-namespaces for one to be sent beside.) A profile
	// makes Required() true for everything, and so would adding NeedsGrant to
	// any of these later.
	// Closing it at the point where the two fields are read means the bypass
	// cannot come back by way of a decision somewhere else that looks
	// unrelated.
	//
	// Fail closed, not "narrowest wins": silently choosing the namespace would
	// answer a question nobody asked, and a caller that sent both does not
	// know what it wants.
	if s.AllNS && len(named) > 0 {
		// A caller that sent only the switch did not send the namespace: a
		// profile or the config filled it in, and "pass one or the other"
		// asked them for what they had already done. The hint says where the
		// other half may have come from and the way to lift it, which a fence
		// (`only:`) refuses on its own as a blank.
		return selection{}, view.Errorf("kube.namespace.ambiguous",
			"%s and %s ask for different things", s.sf.InputName("namespace"), s.sf.InputName("all-namespaces")).
			WithHint("pass one or the other — a namespace to read that namespace, " +
				s.sf.InputName("all-namespaces") + " to read every one. A namespace you did not give came from a " +
				"profile or the config, and " + s.sf.InputName("namespace") + " given empty lifts it, unless the profile " +
				"fences it (`only:`), when widening it is the operator's")
	}
	return s, nil
}

// args puts the subcommand first and the selection flags after it.
//
// **That order is not cosmetic, and getting it wrong made every
// `--all-namespaces` call fail.** `--context` and `--namespace` are kubectl
// global flags and are accepted on either side of the verb; `--all-namespaces`
// is a flag of `get` and is not. kubectl reading an unrecognised flag before
// the verb concludes it is being asked for a *plugin* and answers "flags
// cannot be placed before plugin name: --request-timeout=15s" — naming the
// first flag it saw rather than the one it could not place, which is why this
// read as a timeout problem and was not one.
//
// Everything after the subcommand is the shape that works for all four flags,
// so the class is gone rather than the instance.
//
// `--flag=value` rather than `--flag value`, everywhere and without
// exception: it keeps the value inside its own argv element, so a value that
// begins with a dash is a malformed value rather than a new flag. checkName
// refuses those anyway; this is the second of the two.
func (s selection) args(rest ...string) []string {
	out := append([]string{}, rest...)
	out = append(out, "--request-timeout="+requestTimeout)
	if s.Context != "" {
		out = append(out, "--context="+s.Context)
	}
	switch {
	case s.AllNS:
		out = append(out, "--all-namespaces")
	case s.Namespace != "":
		out = append(out, "--namespace="+s.Namespace)
	}
	return out
}

// where names the cluster a message is about, for error text.
func (s selection) where() string {
	if s.Context == "" {
		return "the current context"
	}
	return "context " + s.Context
}

// scopeWords is where a listing looked, for the sentence that says it found
// nothing there. An empty list is most often a namespace nobody meant, so the
// namespace is the part that has to be in it.
func (s selection) scopeWords() string {
	switch {
	case s.AllNS:
		return "any namespace"
	case s.Namespace != "":
		return "namespace " + s.Namespace
	default:
		return "the current namespace"
	}
}

// withEmpty says what a table with no rows is, for the person reading it.
func withEmpty(t view.Table, sentence string) view.Table {
	if len(t.Rows) == 0 {
		t.Empty = sentence
	}
	return t
}

// run executes kubectl and returns its stdout.
//
// stderr is captured separately and never merged into stdout: stdout is
// parsed as JSON, and a warning kubectl decides to print — a deprecated API
// version, a missing auth plugin's advice — would otherwise turn a good
// answer into a parse error.
//
// sf is the surface the call came through: a failure names the call to make
// next, and only the surface knows how its reader makes one.
func run(ctx context.Context, sf plugin.Surface, args ...string) ([]byte, *view.Error) {
	return runStdin(ctx, sf, nil, args...)
}

// runStdin is run with one difference: stdin is a byte slice this package
// built itself (a manifest for `create -f -`), never the plugin host's own
// stdin. That distinction is the reason run doesn't just set cmd.Stdin to
// the caller's os.Stdin-equivalent unconditionally — inheriting it would
// hand a kubectl subprocess the plugin host's own gRPC channel; a manifest
// this package assembled and immediately hands to a subprocess it also
// waits on has none of that risk.
func runStdin(ctx context.Context, sf plugin.Surface, stdin []byte, args ...string) ([]byte, *view.Error) {
	g := gateOf(ctx)
	if verr := g.enter(ctx); verr != nil {
		return nil, verr
	}
	defer g.leave()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	verr := classify(ctx, err, errBuf.String(), args, sf)
	g.note(verr)
	return nil, verr
}

// gate bounds how many kubectl calls one answer has running, for the answer that
// makes thirty of them (kube.overview reads each namespace of a list with calls
// of its own): subprocesses started all at once are a laptop's fans and an API
// server's throttle, for a read that takes a minute to be asked again.
//
// And it keeps a cluster that does not answer from being asked thirty times. A
// call that fails because the cluster cannot be reached, or does not accept the
// credential, or there is nothing to reach it with, has said the same of every
// call to come, and each of those waits for kubectl's own fifteen seconds: at six
// at a time, thirty calls would keep a tile waiting for over a minute to be told
// what the first six already said. The calls still waiting for a place are given
// that answer instead. A refusal of one list (kube.denied) says nothing of the
// others, and is not that.
//
// It travels in the context and is nil outside the one caller that makes it, so
// every other call here runs as it always did.
type gate struct {
	slots chan struct{}
	mu    sync.Mutex
	down  *view.Error
}

type gateKey struct{}

func withGate(ctx context.Context, width int) context.Context {
	return context.WithValue(ctx, gateKey{}, &gate{slots: make(chan struct{}, width)})
}

func gateOf(ctx context.Context) *gate {
	g, _ := ctx.Value(gateKey{}).(*gate)
	return g
}

// enter waits for a place, and gives the answer a cluster that is down has
// already given rather than asking it again.
func (g *gate) enter(ctx context.Context) *view.Error {
	if g == nil {
		return nil
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return view.Errorf("kube.cancelled", "the call was cancelled")
	}
	g.mu.Lock()
	down := g.down
	g.mu.Unlock()
	if down != nil {
		<-g.slots
		return down
	}
	return nil
}

func (g *gate) leave() {
	if g != nil {
		<-g.slots
	}
}

// note keeps the first failure that is the cluster's as a whole, before the
// place it was running in is given up, so that the call waiting for it already
// sees it.
func (g *gate) note(e *view.Error) {
	if g == nil || !clusterWide(e.Code) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.down == nil {
		g.down = e
	}
}

// clusterWide is whether a failure says of every call, and not of the one that
// hit it, that it will fail: nothing answered, or nothing was accepted, or
// there was nothing to ask with.
func clusterWide(code string) bool {
	switch code {
	case "kube.unreachable", "kube.auth.failed", "kube.login", "kube.credential.missing",
		"kube.noconfig", "kube.context.unknown", "kube.context.none", "kube.kubectl.missing":
		return true
	}
	return false
}

// rejected is the first of errs that is the API server answering 401, which a
// call that sets a read's failure aside (a row, a warning, a page) still ends
// on: a dashboard stops asking a tile whose credential was refused twice, and
// reads that off the error a call returns (view.Error.CredentialRefused), so a
// refusal folded into a page is one it never sees and the token goes out again
// at every refresh.
//
// Only kube.auth.failed. A 403 (kube.denied) is one read's answer and says
// nothing of the next, which is why a restricted identity gets the page of what
// it may read; and the credential plugin that gave nothing (kube.login,
// kube.credential.missing) reached no API server, so nothing was refused.
func rejected(errs ...*view.Error) *view.Error {
	for _, e := range errs {
		if e != nil && e.Code == "kube.auth.failed" {
			return e
		}
	}
	return nil
}

// classify turns a kubectl failure into something an operator can act on.
//
// The same job plugins/pg and plugins/vault do for their own clients. What
// makes it worth the length here is that kubectl reports very different
// problems through the same exit code, and "exit status 1" is the least
// useful thing this plugin could say.
func classify(ctx context.Context, err error, stderr string, args []string, sf plugin.Surface) *view.Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return view.Errorf("kube.unreachable",
			"kubectl did not answer within %s", timeout).
			WithHint("the cluster may be unreachable, or its endpoint may be behind a VPN that is not up; `" +
				clusterInfoLine(args) + "` is the same question without this plugin in the way")
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return view.Errorf("kube.cancelled", "the call was cancelled")
	}
	var notFound *exec.Error
	if errors.As(err, &notFound) {
		return view.Errorf("kube.kubectl.missing",
			"kubectl is not on this machine's PATH").
			WithHint("this plugin drives kubectl rather than linking a Kubernetes client, so it " +
				"needs the binary the operator already uses — install it, or put it on PATH")
	}
	// Ahead of the first-line switch, because the first line is then not
	// kubectl's. A context whose user is an exec credential plugin — tsh,
	// aws, gcloud, kubelogin — hands that plugin kubectl's own stderr, so a
	// session that lapsed reads, in order: the plugin's word (`ERROR: Not
	// logged in.`), a few lines of client-go's retry noise, and only then
	// kubectl's diagnosis, `getting credentials: exec: executable
	// /opt/homebrew/bin/tsh failed with exit code 1`. The switch below took
	// the plugin's line, matched none of its words, and reported
	// `kube.failed ERROR: Not logged in.` — no code a caller could act on,
	// the word ERROR twice, and no hint, for the failure a person on
	// Teleport hits every morning.
	if exe, said, ok := credentialPluginRefused(stderr); ok {
		return view.Errorf("kube.login",
			"%s, the context's credential plugin, refused: %s", exe, said).
			WithHint(loginHint(exe))
	}
	// The same mechanism's other failure: the kubeconfig names a plugin
	// this machine does not have — a kubeconfig copied from a colleague,
	// or a laptop set up before the plugin was. Its first line is client-go's
	// own log line, which carries the word credentials, so the switch below
	// read it as an expired credential and told the person to sign in with
	// a binary they do not have.
	if exe, ok := credentialPluginMissing(stderr); ok {
		return view.Errorf("kube.credential.missing",
			"%s, the context's credential plugin, is not installed", exe).
			WithHint("the kubeconfig's exec block for this context names it — install it, or put it on PATH")
	}
	// The rest of what a credential helper's failure says, which the two
	// patterns above do not read: a plugin that could not be run, one that is
	// configured wrongly or that answered with nothing, and a kubeconfig that
	// names an auth provider this kubectl no longer has. Each is a failure before
	// there was a request, so none is the API server refusing a credential and
	// none ends the way kube.auth.failed does; they are read ahead of the 401
	// below because client-go's retry noise around a plugin's failure can carry
	// the API server's words as well.
	whole := strings.ToLower(stderr)
	if strings.Contains(whole, "no auth provider found") {
		return view.Errorf("kube.credential.missing", "%s", firstLine(stderr)).
			WithHint("the kubeconfig names an auth provider this kubectl does not have — kubectl dropped " +
				"the built-in gcp and azure providers, and the context needs an exec credential plugin in their place")
	}
	if strings.Contains(whole, "getting credentials") || strings.Contains(whole, "exec plugin") ||
		strings.Contains(whole, "credential plugin") {
		return view.Errorf("kube.login", "this context's credential plugin gave no credential: %s", firstLine(stderr)).
			WithHint("nothing was refused — the request never reached the API server. Whatever issues this " +
				"context's credential (a login, an SSO session, an exec credential plugin) has to run again, " +
				"and the kubeconfig's exec block is where a plugin that cannot run is set up")
	}
	msg := firstLine(stderr)
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "no configuration has been provided"),
		strings.Contains(low, "no such file or directory") && strings.Contains(low, "kube"):
		return view.Errorf("kube.noconfig", "there is no kubeconfig on this machine").
			WithHint("`kubectl config get-contexts` is the same question; this reads what it reads")
	case strings.Contains(low, "current-context is not set"):
		// A kubeconfig that exists and names no current context: the one
		// failure here that fell to kube.failed with the cluster's own words
		// and no hint, when the two calls that fix it are this plugin's own.
		return view.Errorf("kube.context.none", "no context is current in this machine's kubeconfig").
			WithHint(contextHint(sf, askWhichContext, sf.CapabilityName("kube.context.list")+" shows the contexts this machine has, and "+
				sf.CapabilityName("kube.context.set")+" makes one current"))
	case strings.Contains(low, "context") && strings.Contains(low, "does not exist"),
		strings.Contains(low, "context was not found"):
		return view.Errorf("kube.context.unknown", "%s", msg).
			WithHint(contextHint(sf, askFixContext, sf.CapabilityName("kube.context.list")+" shows the contexts this machine has"))
	case strings.Contains(low, "forbidden"), strings.Contains(low, "cannot list"),
		strings.Contains(low, "is not allowed"):
		// Left as the API server phrased it. An RBAC refusal names the verb,
		// the resource and the identity, and every one of those is what the
		// operator needs to fix it.
		//
		// This code and the next end the way they do on purpose: a dashboard
		// stops asking a tile whose credential was refused twice, and it reads
		// that off the code's `.denied` and `.auth.failed` endings
		// (view.Error.CredentialRefused). kube.login and kube.credential.missing
		// are not that — nothing reached the API server to be refused — and
		// have neither ending.
		return view.Errorf("kube.denied", "%s", msg).
			WithHint("the credential this context uses does not have that permission")
	case strings.Contains(low, "unauthorized"), strings.Contains(low, "you must be logged in to the server"),
		strings.Contains(low, "the server has asked for the client to provide credentials"):
		// Only the API server's own 401 words. The words a credential helper's
		// failure comes in ("credentials", "exec plugin") used to be here, and
		// a plugin that never ran ended in a code a tile reads as the cluster
		// turning the credential down; they are kube.login above.
		return view.Errorf("kube.auth.failed", "%s", msg).
			WithHint("the API server answered 401: the credential this context sends is expired, revoked or " +
				"not valid — whatever refreshes it (a login, an SSO session, an exec credential plugin) has to run first")
	case strings.Contains(low, "connection refused"), strings.Contains(low, "no route to host"),
		strings.Contains(low, "i/o timeout"), strings.Contains(low, "dial tcp"),
		strings.Contains(low, "server could not find"), strings.Contains(low, "connect:"),
		transportFailed(stderr):
		return view.Errorf("kube.unreachable", "%s", msg).
			WithHint("the API server did not answer — check the VPN, the context, and that the cluster is up; `" +
				clusterInfoLine(args) + "` is the same question without this plugin in the way")
	case strings.Contains(low, "not found"):
		return view.Errorf("kube.notfound", "%s", msg)
	case msg != "":
		return view.Errorf("kube.failed", "%s", msg)
	}
	return view.Errorf("kube.failed", "kubectl %s failed: %v", strings.Join(args, " "), err)
}

// transportWords are the ways kubectl words a request that never got an answer
// from the API server and that the cases above do not already read. The first
// two are kubectl's own sentences for any failure of the connection, whatever
// the transport said: its refusal has no "connection refused" in it, and its
// handshake and deadline failures have no "dial tcp" or "connect:". The rest are
// what the transport says when kubectl passes it through bare.
var transportWords = []string{
	"unable to connect to the server", "was refused - did you specify",
	"tls handshake timeout", "client.timeout exceeded", "context deadline exceeded",
	"connection reset by peer",
}

// transportFailed is whether stderr says the request did not get an answer, as
// against the API server answering with a status of its own. The difference is
// what the whole of a page is told: a cluster that cannot be reached cannot be
// reached by any call that follows (clusterWide), where a status is about the one
// thing asked. The same words come back inside a status as well, from an
// admission webhook that timed out, which kubectl prints as "Error from server
// (InternalError): ... context deadline exceeded" and which says nothing of the
// next read, so a line that starts as a status is never one of these.
func transportFailed(stderr string) bool {
	line := strings.ToLower(strings.TrimSpace(stderr))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if strings.HasPrefix(line, "error from server") {
		return false
	}
	for _, word := range transportWords {
		if strings.Contains(line, word) {
			return true
		}
	}
	return false
}

// clusterInfoLine is the kubectl command that asks whether the API server
// answers at all, with this plugin out of the way, on the context the call that
// failed was run against: read from the arguments it ran with, since a bare
// `kubectl cluster-info` asks the context of the shell it is pasted into, which
// is not always the one this call was about. The same line plugins/cnpg gives
// its own unreachable cluster.
func clusterInfoLine(args []string) string {
	var s selection
	for _, arg := range args {
		if name, ok := strings.CutPrefix(arg, "--context="); ok {
			s.Context = name
		}
	}
	return s.kubectlLine("cluster-info")
}

// execPluginFailed is kubectl's own line for an exec credential plugin that
// exited non-zero, anywhere in stderr: the plugin's words come first.
var execPluginFailed = regexp.MustCompile(`getting credentials: exec: executable (\S+) failed with exit code (\d+)`)

// credentialPluginRefused reports whether stderr is an exec credential
// plugin refusing, and if so which plugin — by its base name, since the
// kubeconfig holds its full path — and what it said: its first line, less
// the `ERROR:` tsh prefixes, or its exit code when it said nothing.
func credentialPluginRefused(stderr string) (exe, said string, ok bool) {
	m := execPluginFailed.FindStringSubmatch(stderr)
	if m == nil {
		return "", "", false
	}
	said = strings.TrimSpace(strings.TrimPrefix(firstLine(stderr), "ERROR:"))
	if said == "" || strings.Contains(said, "getting credentials: exec:") {
		said = "exit code " + m[2] + ", with nothing said"
	}
	return filepath.Base(m[1]), said, true
}

// execPluginMissing is kubectl's line for an exec credential plugin the
// kubeconfig names and this machine does not have: client-go's own words
// for a bare name it found nowhere on PATH, and the operating system's for
// a path that is not there.
var execPluginMissing = regexp.MustCompile(
	`getting credentials: exec: (?:executable (\S+) not found|fork/exec (\S+): no such file or directory)`)

// credentialPluginMissing reports whether stderr is kubectl failing to
// start the context's exec credential plugin at all, and which plugin, by
// its base name.
func credentialPluginMissing(stderr string) (exe string, ok bool) {
	m := execPluginMissing.FindStringSubmatch(stderr)
	if m == nil {
		return "", false
	}
	exe = m[1]
	if exe == "" {
		exe = m[2]
	}
	return filepath.Base(exe), true
}

// loginHint is the command that refreshes a credential plugin's session,
// for the ones a kubeconfig commonly names. The plugin's own words above
// usually say as much; the hint is for when they do not — `Not logged in.`
// names no command — and it names the plugin either way, because a person
// with three clusters behind three sign-ins wants to know which one lapsed.
func loginHint(exe string) string {
	switch exe {
	case "tsh":
		return "the Teleport session has lapsed — run `tsh login` in a terminal, then try again"
	case "aws":
		return "the AWS session has lapsed — run `aws sso login` for the profile this context uses, then try again"
	case "gcloud":
		return "the Google Cloud session has lapsed — run `gcloud auth login`, then try again"
	case "az":
		return "the Azure session has lapsed — run `az login`, then try again"
	case "kubelogin", "kubectl-oidc_login":
		return "the OIDC session has lapsed — run `kubectl` once in a terminal so " + exe + " can open the sign-in, then try again"
	}
	return "sign in with " + exe + " again — whatever refreshes its session — then try again"
}

// firstLine trims kubectl's stderr to the sentence worth showing.
//
// kubectl prefixes many errors with "error: " and prints multi-line advice
// after them; the first line is the diagnosis and the rest is a tutorial.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "error:"))
	s = strings.TrimSpace(strings.TrimPrefix(s, "Error from server"))
	return strings.TrimSpace(strings.TrimPrefix(s, ":"))
}

// getJSON runs a `kubectl get -o json` and decodes the list it returns.
func getJSON(ctx context.Context, s selection, kind string, out any) *view.Error {
	raw, verr := run(ctx, s.sf, s.args("get", kind, "-o", "json")...)
	if verr != nil {
		return verr
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return view.Errorf("kube.unreadable",
			"kubectl's answer for %s could not be read: %v", kind, err)
	}
	return nil
}

// list is the shape every `kubectl get -o json` returns.
type list[T any] struct {
	Items []T `json:"items"`
}

// meta is the part of every object this plugin reads.
type meta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	CreationTimestamp time.Time         `json:"creationTimestamp"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
}

// age renders a creation timestamp the way kubectl does.
func age(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
