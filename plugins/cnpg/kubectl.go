package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Shelling out to `kubectl`, the same decision plugins/kube and internal/tunnel
// both took, and for a reason this plugin makes sharper rather than weaker.
//
// A CloudNativePG cluster's whole state is one custom resource. Reading it
// needs no typed client, no scheme registration and no informer — it is a GET
// on one object, and `kubectl get -o json` is a GET on one object. What
// linking client-go would add is +67 modules and +11 MB for the privilege of
// reimplementing authentication that already works on the operator's machine.
//
// **And that authentication is the whole point here.** This plugin exists
// because `kubectl cnpg status` does not work through every proxy people put
// in front of a cluster, while a plain API read does — so the one thing it
// must not do is stop using the credential path the operator already has
// working. Whatever kubectl can reach, including through an exec credential
// plugin, this can read.
//
// The wrapper below is deliberately not shared with plugins/kube, which has a
// larger one. Two callers is the repo's recorded bar for *considering* a seam
// and not for cutting one — builtin/keys says of an earlier duplication "two
// built-ins, ten lines, no third caller yet to justify the seam" — and these
// two need different slices: that one switches contexts, lists workloads and
// completes namespaces, this one does a single typed GET. A third operator
// plugin is what would make `pkg/kubectl` worth freezing as public API.

// kubectlBin is overridable in tests, which have no cluster and must not need
// one.
var kubectlBin = "kubectl"

// timeout bounds one kubectl invocation, and requestTimeout is what kubectl
// is told so its own error — which names the cluster and is actionable — is
// what an operator usually sees rather than this package's blunter one.
const (
	timeout        = 20 * time.Second
	requestTimeout = "15s"
)

// clusterCRD is the fully-qualified resource name, never the `cluster` short
// form. Short names are resolved through the cluster's discovery document and
// are ambiguous the moment anything else registers one; the group-qualified
// name means exactly one thing on every cluster.
const clusterCRD = "clusters.postgresql.cnpg.io"

// nameRe is what may be passed to kubectl as a context, namespace or object
// name.
//
// **Argument-injection defence, not validation for its own sake.** These
// values are interpolated into an argv slice and never into a shell string,
// so there is no shell to escape into — but a value beginning with `-` is
// still read by kubectl as a *flag*, and `--kubeconfig=/tmp/mine` arriving
// where a context name was expected would point the call at a different
// cluster entirely. The `--flag=value` form used throughout already prevents
// that by keeping the value in its flag's own argv element; this refuses it a
// second time and earlier, so a future caller reaching for the two-element
// form cannot reintroduce it. Copied deliberately from plugins/kube, which
// records the same reasoning: it is the half of that wrapper that must not
// drift, so it drifts by being identical rather than by being imported.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,252}$`)

func checkName(kind, v string) *view.Error {
	if v == "" {
		return nil
	}
	if !nameRe.MatchString(v) {
		return view.Errorf("cnpg.name.invalid", "%q is not a usable %s name", v, kind).
			WithHint("Kubernetes names are letters, digits, dots and dashes; a leading " +
				"dash would be read as a flag")
	}
	return nil
}

// selection is which cluster and which namespace a call reads.
type selection struct {
	context   string
	namespace string
	// namespaces is the several a listing reads when it is given two or more, each
	// with a call of its own (scopes), sorted and once each. Only listingOf sets
	// it, and a listing given one namespace holds it in namespace and leaves this
	// empty, so a list of one reads, and words everything it says, as the single
	// name always did.
	namespaces   []string
	allNamespace bool
	// sf is the surface the call came through, so a failure reading the
	// cluster names what to call next the way its reader calls it.
	sf plugin.Surface
	// profile is the operator's profile the call came through (Request.Profile),
	// "" for none.
	profile string
	// reach is what a call named next gives to reach the same cluster again
	// (callArgs): the profile, and the context at a terminal, as
	// Request.ReachArgs spells them. An agent is given the profile alone, the
	// context being Local.
	reach []plugin.Arg
}

// connectionOf is the half of a selection every capability has alike: the
// context, and who is asking.
func connectionOf(req plugin.Request) (selection, *view.Error) {
	s := selection{
		context: strings.TrimSpace(req.String("context")),
		sf:      req.Surface(),
		profile: req.Profile(),
	}
	switch {
	case s.sf == plugin.SurfaceMCP || s.context == "":
		s.reach = req.ReachArgs()
	default:
		s.reach = req.ReachArgs(plugin.Arg{Name: "context", Value: s.context})
	}
	if verr := checkName("context", s.context); verr != nil {
		return selection{}, verr.WithInput("context")
	}
	return s, nil
}

func selectionOf(req plugin.Request) (selection, *view.Error) {
	s, verr := connectionOf(req)
	if verr != nil {
		return selection{}, verr
	}
	s.namespace = strings.TrimSpace(req.String("namespace"))
	s.allNamespace = req.Bool("all-namespaces")
	if verr := checkName("namespace", s.namespace); verr != nil {
		return selection{}, verr.WithInput("namespace")
	}
	if verr := s.refuseBoth(); verr != nil {
		return selection{}, verr
	}
	return s, nil
}

// refuseBoth is the refusal of a namespace given beside every namespace.
//
// **Refused here rather than resolved in args(), which prefers
// --all-namespaces when both are set.** That preference is safe only for
// as long as these capabilities declare no Scope: rta derives the scope a
// call is checked against from the value of the `namespace` field, and
// knows nothing about `all-namespaces`. Give either capability below
// `Scope: "namespace"` — the natural thing to do, since both already take
// a namespace — and a caller granted one namespace could send it together
// with --all-namespaces, pass the scope check on the first, and be
// answered from every namespace in the cluster.
//
// So this is not fixing a live bug here; it is refusing to leave a trap
// that springs on whoever adds the scope later and has no reason to look
// at this function. plugins/kube carries the same refusal for the same
// reason, where the scope *is* declared and the bypass was real.
func (s selection) refuseBoth() *view.Error {
	if !s.allNamespace || (s.namespace == "" && len(s.namespaces) == 0) {
		return nil
	}
	// A caller that sent only the switch did not send the namespace: a
	// profile or the config filled it in, and "pass one or the other"
	// asked them for what they had already done. The hint says where the
	// other half may have come from and the way to lift it, which a fence
	// (`only:`) refuses on its own as a blank.
	return view.Errorf("cnpg.namespace.ambiguous",
		"%s and %s ask for different things", s.sf.InputName("namespace"), s.sf.InputName("all-namespaces")).
		WithHint("pass one or the other — a namespace to read that namespace, " +
			s.sf.InputName("all-namespaces") + " to read every one. A namespace you did not give came from a " +
			"profile or the config, and " + s.sf.InputName("namespace") + " given empty lifts it, unless the profile " +
			"fences it (`only:`), when widening it is the operator's")
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
// `--flag=value` rather than `--flag value`, everywhere and without exception:
// it keeps the value inside its own argv element, so a value that begins with
// a dash is a malformed value rather than a new flag. checkName refuses those
// anyway; this is the second of the two.
func (s selection) args(rest ...string) []string {
	out := append([]string{}, rest...)
	out = append(out, "--request-timeout="+requestTimeout)
	if s.context != "" {
		out = append(out, "--context="+s.context)
	}
	switch {
	case s.allNamespace:
		out = append(out, "--all-namespaces")
	case s.namespace != "":
		out = append(out, "--namespace="+s.namespace)
	}
	return out
}

// callArgs points a call a message names at the cluster s read: in namespace,
// the one it was found in, and in s's context when s had one. Without them
// the call reads the context's own namespace in the current context, and a
// cluster found anywhere else is not there — the hint sends its reader to a
// "not found". Over MCP the context is left out: it is Local, so an agent
// cannot give one, and its call reads the operator's own, the one s read.
//
// **And the profile s came through, on every surface, whenever there was
// one.** The operator's own context is the one s read only when no profile
// named another: an agent's call through a profile was handed a call without
// it, which read the current context — another cluster, where the backup it
// was told to watch is not. The profile is what reaches the same one again,
// as Request.Profile says of every line naming a connection once a call is
// over; at a terminal it stands beside the context, which names the same
// cluster.
func (s selection) callArgs(namespace string) []plugin.Arg {
	var out []plugin.Arg
	if namespace != "" {
		out = append(out, plugin.Arg{Name: "namespace", Value: namespace})
	}
	return append(out, s.reach...)
}

// caller is who a kubectl call is made for: the surface that reads what a
// failure says, and what a call it names gives to reach the same cluster again
// (callArgs). Held together because a failure that names the next call to make
// needs both, and completion makes calls for neither.
type caller struct {
	sf    plugin.Surface
	reach []plugin.Arg
	// context is the kubectl context the call read, for a kubectl command a
	// message hands over to be pasted: bare it asks the context of the shell it
	// lands in.
	context string
}

func (s selection) caller() caller { return caller{sf: s.sf, reach: s.reach, context: s.context} }

// kubectlLine is the kubectl command rest names, on the context c read, each
// value one shell word.
func (c caller) kubectlLine(rest ...string) string {
	words := []string{"kubectl"}
	if c.context != "" {
		words = append(words, plugin.ShellWord("--context="+c.context))
	}
	for _, word := range rest {
		words = append(words, plugin.ShellWord(word))
	}
	return strings.Join(words, " ")
}

// listEverywhere is the listing of every cluster in every namespace, as a
// call: on the cluster this one reached when there is a profile or a context
// to say which, and as the capability and its flag otherwise, which is how an
// agent with nothing to give is told.
func (c caller) listEverywhere() string {
	if len(c.reach) == 0 {
		return c.sf.CapabilityWith("cnpg.list", "all-namespaces")
	}
	return "`" + c.sf.Call("cnpg.list", append([]plugin.Arg{{Name: "all-namespaces", Value: true}}, c.reach...)...) + "`"
}

// where names what was read, for a message that has to say which cluster — and
// which part of it. A call that names no namespace read the context's own,
// which is not the cluster, and a sentence that said "in the current context"
// of it told somebody with clusters in another namespace that there were none.
//
// Several namespaces are named while that is short and counted when it is not
// (maxNamed): the names are in the call and in the warning for the ones that
// could not be read, and a sentence that carried thirty of them would be the page.
func (s selection) where() string {
	var scope string
	switch {
	case s.allNamespace:
		scope = "every namespace"
	case len(s.namespaces) > maxNamed:
		scope = "the " + strconv.Itoa(len(s.namespaces)) + " namespaces read"
	case len(s.namespaces) > 1:
		scope = "namespaces " + strings.Join(s.namespaces, ", ")
	case s.namespace != "":
		scope = "namespace " + s.namespace
	default:
		scope = "the context's own namespace"
	}
	if s.context != "" {
		return "context " + s.context + ", " + scope
	}
	return scope
}

// sf is the surface the call came through: a failure names the call to make
// next, and only the surface knows how its reader makes one.
func run(ctx context.Context, by caller, args ...string) ([]byte, *view.Error) {
	if verr := refusesSecrets(args); verr != nil {
		return nil, verr
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, kubectlBin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, classify(cctx, err, stderr.String(), args, by)
	}
	return out, nil
}

// refusesSecrets is the one thing this plugin never asks a cluster for.
//
// **A Secret is the one resource whose read is the leak.** Every object this
// plugin decodes that references a credential — an ObjectStore, a Backup's
// status, a cluster's stanza — states the Secret's name where it needs to, and
// the name is all that was ever wanted; the value behind it is the thing a
// boundary exists to keep from an agent. Nothing calls for one, so nothing may:
// a get that names the type, in any of the spellings kubectl accepts
// (`secret`, `secrets`, `secrets.v1`, `secret/name`, or one among
// `pods,secrets`) fails here, before kubectl is run, and so does one a later
// change introduces by slip.
//
// Checked on the resource word of a get and nowhere else: an object that is
// merely *called* secrets is not one.
func refusesSecrets(args []string) *view.Error {
	if len(args) < 2 || args[0] != "get" {
		return nil
	}
	for _, part := range strings.Split(args[1], ",") {
		kind, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(part)), "/")
		kind, _, _ = strings.Cut(kind, ".")
		if kind == "secret" || kind == "secrets" {
			return view.Errorf("cnpg.secrets.refused", "this plugin does not read Secrets").
				WithHint("what it needs of a credential is the name of the Secret an object references, " +
					"which the object itself states")
		}
	}
	return nil
}

// classify turns kubectl's stderr into something actionable.
//
// **Authentication and permission are told apart**, which is the distinction
// internal/tunnel had wrong until recently and for the same reason it matters
// more here than anywhere: this plugin's whole purpose is to be usable behind
// a proxy that hands out short-lived credentials, so "your login expired" is
// the ordinary failure and reporting it as an RBAC problem sends people to
// argue with the wrong team.
func classify(ctx context.Context, err error, stderr string, args []string, by caller) *view.Error {
	s := strings.TrimSpace(stderr)
	// A timeout has the code of a refused connection (below), and a refusal by
	// the cluster is cnpg.denied: plugins/kube gives the same two
	// conditions kube.unreachable and kube.denied. A hung cluster read
	// cnpg.timeout here and kube.unreachable beside it, for the same kubectl
	// waiting on the same API server.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return view.Errorf("cnpg.unreachable", "kubectl did not answer within %s", timeout).
			WithHint("the cluster may be unreachable — `" + by.kubectlLine("cluster-info") + "` is the same " +
				"question without this plugin in the way")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		if errors.Is(err, exec.ErrNotFound) {
			return view.Errorf("cnpg.kubectl.missing", "kubectl is not on $PATH").
				WithHint("this plugin shells out to kubectl so your existing cluster " +
					"credentials keep working — install kubectl")
		}
		return view.Errorf("cnpg.kubectl.failed", "running kubectl: %v", err)
	}
	// The codes below that a refused credential gets end `.auth.failed` and `.denied`
	// on purpose: a dashboard stops asking a tile whose credential was refused twice
	// and reads that off those endings (view.Error.CredentialRefused). cnpg.login has
	// neither — the credential plugin failed before there was a request to refuse, so
	// a lapsed session or a plugin that cannot reach its own service is not a
	// credential the cluster turned down, and a tile that stopped asking after it
	// would be hiding an outage. plugins/kube draws the same line with kube.login.
	switch {
	case credentialUnavailable(s):
		return view.Errorf("cnpg.login", "kubectl could not get a credential for this context").
			WithHint("nothing was refused — the request never got that far. Whatever " +
				"issues your cluster credentials (`tsh kube login`, `aws eks get-token`, " +
				"`gcloud`, an OIDC refresh) needs running again")
	case credentialNotAccepted(s):
		return view.Errorf("cnpg.auth.failed", "this cluster does not know who you are").
			WithHint("the API server answered 401: the credential this context sends is missing, " +
				"expired or revoked. Whatever issues your cluster credentials (`tsh kube login`, " +
				"`aws eks get-token`, `gcloud`, an OIDC refresh) needs running again")
	case strings.Contains(s, "forbidden"):
		return denied(by, askedOf(s, args), args)
	case strings.Contains(s, "server doesn't have a resource type"):
		return crdMissing(by, askedOf(s, args))
	case strings.Contains(s, "the server could not find the requested resource"):
		return notServed(by, askedOf(s, args))
	// The failures plugins/kube places before it falls back to kubectl's own
	// words, and which this plugin left to the fallback below: a cluster that
	// does not answer and a kubeconfig with nothing to answer from. Told only
	// that "that message is kubectl's", a reader has nothing to try, on the
	// two failures that are most of what a first call meets.
	//
	// Ahead of "not found", whose word one of them contains: kubectl says
	// `context was not found for specified context`, and a missing context is
	// not a missing cluster.
	case unreachable(s):
		return view.Errorf("cnpg.unreachable", "%s", firstLine(s)).
			WithHint("the API server did not answer — check the VPN, the context, and that the cluster is up; `" +
				by.kubectlLine("cluster-info") + "` is the same question without this plugin in the way")
	case strings.Contains(s, "current-context is not set"),
		strings.Contains(s, "no configuration has been provided"),
		strings.Contains(s, "context was not found"),
		strings.Contains(s, "does not exist") && strings.Contains(s, "context"):
		return view.Errorf("cnpg.context.none", "%s", firstLine(s)).
			WithHint("kubectl has no context to ask — `kubectl config get-contexts` shows what this machine " +
				"has, and the context this call used is the operator's to change").
			WithInput(contextRefused(s, args))
	case strings.Contains(s, "not found"):
		return objectMissing(by, s, askedOf(s, args))
	case s == "":
		return view.Errorf("cnpg.kubectl.failed", "kubectl exited %d without saying why",
			exitErr.ExitCode())
	}
	return view.Errorf("cnpg.kubectl.failed", "%s", firstLine(s)).
		WithHint("that message is kubectl's; this plugin shells out to it so your cluster " +
			"credentials keep working")
}

// contextRefused is the input kubectl's refusal of a context names
// (view.Error.Input): the context, when the call was given one (--context=)
// and kubectl answered that there is no such context. A kubeconfig with no
// current context, with no configuration at all, or whose current context names
// one it does not hold names none: that is the file's word, no box of the call
// holds it, and a form reopened on the empty context box would point at
// something nobody typed.
func contextRefused(stderr string, args []string) string {
	unknown := strings.Contains(stderr, "context was not found") ||
		strings.Contains(stderr, "does not exist") && strings.Contains(stderr, "context")
	if !unknown {
		return ""
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--context=") {
			return "context"
		}
	}
	return ""
}

// unreachable reports whether stderr is the connection to the API server
// failing: kubectl's own sentence for it, or the dial underneath.
func unreachable(stderr string) bool {
	for _, s := range []string{
		"Unable to connect to the server",
		"connection refused",
		"no route to host",
		"i/o timeout",
		"TLS handshake timeout",
		"dial tcp",
	} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
}

// credentialUnavailable reports whether stderr is the credential plugin failing
// before there was a request to make at all. It is asked first: a plugin's
// failure arrives inside client-go's retry noise, which can carry the API
// server's own words as well.
func credentialUnavailable(stderr string) bool {
	return containsAny(stderr,
		"getting credentials",
		"exec plugin",
		"credential plugin",
		"no Auth Provider found")
}

// credentialNotAccepted reports whether stderr is the API server's 401: about
// identity rather than permission.
func credentialNotAccepted(stderr string) bool {
	return containsAny(stderr,
		"Unauthorized",
		"You must be logged in to the server",
		"the server has asked for the client to provide credentials")
}

func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// getJSON reads one or many clusters and decodes into out.
func getJSON(ctx context.Context, s selection, name string, out any) *view.Error {
	return getResource(ctx, s, clusterCRD, name, out)
}

// namedCluster is a failed read of the one cluster a call names in its
// `cluster` input: a cluster that is not there is that input's refusal
// (view.Error.Input), and every other failure is the connection's, the
// credential's or the cluster's, which name none.
func namedCluster(verr *view.Error) *view.Error {
	if verr.Code == "cnpg.cluster.missing" {
		return verr.WithInput("cluster")
	}
	return verr
}

// getResource is getJSON over any of the two resources this plugin reads.
//
// Split out when backups arrived rather than copied, because the decode error
// and the argument order are the parts that must not drift: `-o json` after
// the name and the selection flags after that is the shape args() documents at
// length, and a second hand-assembled call is where `--all-namespaces` lands
// before the verb again.
func getResource(ctx context.Context, s selection, resource, name string, out any, extra ...string) *view.Error {
	args := []string{"get", resource}
	if name != "" {
		args = append(args, name)
	}
	args = append(args, "-o", "json")
	args = append(args, extra...)
	raw, verr := run(ctx, s.caller(), s.args(args...)...)
	if verr != nil {
		return verr
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return view.Errorf("cnpg.decode", "kubectl's JSON did not parse: %v", err).
			WithHint("that usually means a kubectl old enough to word its output differently")
	}
	return nil
}

// backupListing is the call that lists the backups of the cluster this
// selection reaches, in the namespace it reads: the capability alone when
// there is nothing to say which cluster, the call with what reaches it
// otherwise.
func (s selection) backupListing() string {
	args := s.callArgs(s.namespace)
	if len(args) == 0 {
		return s.sf.CapabilityName("cnpg.backup.list")
	}
	return "`" + s.sf.Call("cnpg.backup.list", args...) + "`"
}

// callAbout is a call of id on the cluster c, in the namespace c was found in
// and through whatever reached it. The cluster is its flag and not an argument,
// which is what each of the calls a page sends its reader to takes.
func (s selection) callAbout(id string, c cluster) string {
	args := append([]plugin.Arg{{Name: "cluster", Value: c.Metadata.Name}}, s.callArgs(c.Metadata.Namespace)...)
	return "`" + s.sf.Call(id, args...) + "`"
}

// backupListOf is the call that lists the backups of c: the one a page names
// when it has to send its reader to the Backup objects, and the one a receipt
// hands over to watch a backup by.
func (s selection) backupListOf(c cluster) string { return s.callAbout("cnpg.backup.list", c) }

// backupShowOf is the call that shows how the backups of c are arranged, the
// listing's sibling.
func (s selection) backupShowOf(c cluster) string { return s.callAbout("cnpg.backup.show", c) }

// createJSON posts one object and decodes what the API server wrote back.
//
// **Through stdin rather than a temporary file**, which is not merely tidier:
// a file has a path, a path has a directory, and rta confines what a plugin
// may touch on the operator's disk precisely so that a plugin reaching for
// somewhere to write is a decision somebody made rather than a side effect of
// how a request was encoded. Nothing about creating an object in a cluster
// needs the local filesystem, so it does not get it.
//
// The document is built by encoding/json from a typed struct here, never by
// interpolating a name into a template — the same reason checkName exists one
// layer up. A cluster name is still checked before it reaches this, because
// defence that only works when the encoder is correct is defence with one
// leg.
func createJSON(ctx context.Context, s selection, doc []byte, out any, extra ...string) *view.Error {
	args := s.args(append([]string{"create", "-f", "-", "-o", "json"}, extra...)...)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, kubectlBin, args...)
	cmd.Stdin = bytes.NewReader(doc)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return classify(cctx, err, stderr.String(), args, s.caller())
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return view.Errorf("cnpg.decode", "kubectl's JSON did not parse: %v", err).
			WithHint("the object may still have been created — " + s.backupListing() + " says")
	}
	return nil
}

// age renders a timestamp the way kubectl does, and is the form the question
// is actually asked in: "how long since the last backup" rather than "at what
// instant".
func age(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return span(time.Since(t))
}

// until is age pointed forward, for the certificate expiries: "in how long"
// with the same buckets, so the two directions read alike.
func until(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return span(time.Until(t))
}

func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
