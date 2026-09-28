package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	stdnet "net"
	"net/url"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// connFields are the inputs every capability here shares.
//
// Every one is Local, and that is the security property rather than a detail.
// Together they name which cluster this call reaches and as whom, and an MCP
// caller may not choose that: caller values resolve above config and above the
// host's own environment, so an agent that could set `endpoint` would point
// rta at a cluster of its own and have the host supply $RTA_ETCD_PASSWORD
// beside it. Config still fills these and a person at a terminal still passes
// them as ordinary flags.
//
// The three certificate paths are Local for the same reason and one more:
// they are read off this machine's disk. An input naming a file that the host
// then opens is a file-read primitive if a caller can choose the path, and the
// path gate is not the right place to defend that — not offering the choice is.
func connFields() []plugin.Field {
	return []plugin.Field{
		{Name: "endpoint", Type: plugin.String, Default: "127.0.0.1:2379", Config: "endpoint",
			Local: true, Endpoint: plugin.EndpointAddress, Help: "etcd endpoint, host[:port]"},
		// Defaults to plaintext because that is what a local `etcd` started
		// for a try answers on. Every production cluster is the other way, and
		// says so in its config rather than being guessed at here.
		{Name: "tls", Type: plugin.Bool, Default: false, Config: "tls",
			Local: true, Endpoint: plugin.EndpointTLS, Help: "connect over TLS"},
		{Name: "ca-file", Type: plugin.String, Default: "", Config: "ca-file",
			Local: true, Help: "PEM bundle to verify the server against"},
		// etcd clusters are commonly mTLS with no password at all, so the
		// client certificate is a credential here in the same sense a password
		// is elsewhere — but it is a path, not a secret value, so it stays a
		// String and does not take EnvFallback.
		{Name: "cert-file", Type: plugin.String, Default: "", Config: "cert-file",
			Local: true, Help: "client certificate, for a cluster using mTLS"},
		{Name: "key-file", Type: plugin.String, Default: "", Config: "key-file",
			Local: true, Help: "private key for `cert-file`"},
		{Name: "username", Type: plugin.String, Default: "", Config: "username",
			Local: true, Help: "user to authenticate as, if the cluster has auth enabled"},
		{Name: "password", Type: plugin.Secret, Local: true, EnvFallback: true,
			Help: "password for the user"},
	}
}

// dialTimeout bounds the wait for the client's connection to come up, and
// nothing after it.
//
// Setting clientv3.Config.DialTimeout to it does not do that on its own.
// etcd's client dials without blocking — grpc.NewClient, which never waits
// for the connection — so DialTimeout bounds only the token New fetches when
// a username is set. Every call after that waits for a connection for as long
// as its context lasts, WaitForReady being etcd's default, and the CLI's
// context ends only at a signal: pointed at a port nothing listens on, a call
// hung until interrupted. awaitConnection is what applies the bound. It holds
// the connection to it rather than the call, so a kv.tree walk over a large
// keyspace, or a snapshot of one, runs for as long as it takes once the
// connection is up.
const dialTimeout = 10 * time.Second

func connect(ctx context.Context, req plugin.Request) (*clientv3.Client, *view.Error) {
	return connectWithin(ctx, req, dialTimeout)
}

// connectWithin is connect with the bound given, so a test can reach the end
// of the wait without sitting out the real one.
func connectWithin(ctx context.Context, req plugin.Request, within time.Duration) (*clientv3.Client, *view.Error) {
	cfg := clientv3.Config{
		Endpoints:   []string{endpointOf(req)},
		DialTimeout: within,
		Username:    req.String("username"),
		Password:    req.String("password"),
		Context:     ctx,
	}

	var tlsCfg *tls.Config
	if req.Bool("tls") || req.String("ca-file") != "" || req.String("cert-file") != "" {
		var verr *view.Error
		if tlsCfg, verr = tlsConfig(req); verr != nil {
			return nil, verr
		}
		cfg.TLS = tlsCfg
	}
	to := dialTarget(endpointOf(req), tlsCfg)

	client, err := clientv3.New(cfg)
	if err != nil {
		// With a username, New fetches a token before it returns, and a
		// connection that never came up ends that as a bare deadline — so
		// the endpoint is asked why, as awaitConnection asks it. The bound
		// is spent by then, so the asking gets settle rather than the whole
		// of it over again: given within, a host that drops every packet
		// was answered at twice the bound, and as a cluster that lost
		// quorum, which is a connection that came up and a token that did
		// not.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			dctx, cancel := context.WithTimeout(ctx, settle)
			defer cancel()
			switch why := to.dial(dctx); {
			case why != nil && dctx.Err() == nil:
				return nil, classify(why, req)
			case why != nil:
				return nil, noConnection(req)
			}
		}
		return nil, classify(err, req)
	}
	if verr := awaitConnection(ctx, client, req, to, within); verr != nil {
		_ = client.Close()
		return nil, verr
	}
	return client, nil
}

// awaitConnection waits, for within at most, for the client's connection to
// come up, and says why it did not.
//
// A connection that fails leaves the client retrying it, and the reason —
// nothing listening, a name DNS does not know, a certificate nothing here
// trusts — never reaches the caller: the call waiting on it ends in a deadline
// and nothing else, which classify can only read as a cluster that answers
// nothing. So the first time the connection fails, the endpoint is dialled
// once more by hand, with the same TLS, and a failure there is the answer,
// given at once rather than at the end of the wait.
//
// A dial that gets through says something listens there, and nothing more: a
// member that was restarting when the client first tried gets that far. So
// the client is given until its next attempt, settle, to come up anyway, and
// a connection still down then — to a port that answers every dial — is a
// listener that does not speak etcd's client protocol over it: the peer port,
// or TLS on one side only. That is the answer, a few seconds in, rather than
// the whole bound's worth later.
//
// Failure is read once, not counted: gRPC holds a failed connection at
// TRANSIENT_FAILURE until it is READY, through every retry between.
func awaitConnection(ctx context.Context, c *clientv3.Client, req plugin.Request, to target, within time.Duration) *view.Error {
	wctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	conn := c.ActiveConnection()
	conn.Connect()
	s := until(wctx, conn, connectivity.Ready, connectivity.TransientFailure)
	if s == connectivity.TransientFailure {
		why := to.dial(wctx)
		switch {
		case why != nil && wctx.Err() == nil:
			return classify(why, req)
		case why == nil && to.reachable:
			sctx, scancel := context.WithTimeout(wctx, settle)
			s = until(sctx, conn, connectivity.Ready)
			scancel()
			if s != connectivity.Ready && wctx.Err() == nil {
				return wrongPort(req, to)
			}
		default:
			s = until(wctx, conn, connectivity.Ready)
		}
	}
	if s == connectivity.Ready {
		return nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return classify(ctx.Err(), req)
	}
	return noConnection(req)
}

// noConnection is the bound run out with no connection up.
func noConnection(req plugin.Request) *view.Error {
	return view.Errorf("etcd.timeout", "%s did not answer in time", endpointOf(req)).
		WithHint("no connection came up: a firewall that drops rather than refuses looks exactly " +
			"like this, and so does a listener that takes the connection and never speaks")
}

// settle is how long a connection that failed, to a port that answered the
// dial, is given to come up anyway: past gRPC's first reconnect, a second
// after the failure give or take a fifth.
const settle = 3 * time.Second

// until waits for conn to be in one of states, and returns the state it is in
// when it is, or when ctx ends first.
func until(ctx context.Context, conn *grpc.ClientConn, states ...connectivity.State) connectivity.State {
	for {
		s := conn.GetState()
		if slices.Contains(states, s) || !conn.WaitForStateChange(ctx, s) {
			return s
		}
	}
}

// wrongPort is a listener that takes the connection and does not speak etcd's
// client protocol over it.
func wrongPort(req plugin.Request, to target) *view.Error {
	refusal := view.Errorf("etcd.conn.protocol", "%s takes a connection and answers nothing etcd's client "+
		"understands", endpointOf(req))
	if to.tls != nil {
		return refusal.WithHint("etcd listens on 2379 for clients and 2380 for peers, and the peer port " +
			"will not answer this — nor will a client port serving TLS to a certificate it does not accept")
	}
	return refusal.WithHint("etcd listens on 2379 for clients and 2380 for peers, and the peer port will " +
		"not answer this — nor will a client port serving TLS, to a client without " +
		setting(req.Surface(), "tls") + " on")
}

// target is where etcd's client dials an endpoint, and with what TLS.
type target struct {
	addr string
	// tls is nil for a plaintext connection.
	tls *tls.Config
	// reachable is false for a form dial does not reach: a unix socket, or
	// a scheme etcd's client has and this does not know.
	reachable bool
}

// dialTarget reads endpoint as etcd's client reads it: a bare host:port,
// using TLS when the client was given it, or one behind http://, which drops
// it, or https://, which requires it.
func dialTarget(endpoint string, tlsCfg *tls.Config) target {
	if strings.HasPrefix(endpoint, "unix:") || strings.HasPrefix(endpoint, "unixs:") {
		return target{}
	}
	scheme, _, ok := strings.Cut(endpoint, "://")
	if !ok {
		return target{addr: endpoint, tls: tlsCfg, reachable: true}
	}
	u, err := url.Parse(endpoint)
	switch {
	case err != nil:
		return target{}
	case scheme == "http":
		return target{addr: u.Host, reachable: true}
	case scheme == "https":
		if tlsCfg == nil {
			tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		return target{addr: u.Host, tls: tlsCfg, reachable: true}
	}
	return target{}
}

// dial reaches the target as etcd's client does — TCP, then TLS when the
// client would use it — and returns what stopped it, or nil when nothing did,
// or when the target is not one it reaches. It is the diagnosis
// awaitConnection runs once the client's own connection has failed, and no
// call ever runs over it.
func (to target) dial(ctx context.Context) error {
	if !to.reachable {
		return nil
	}
	raw, err := (&stdnet.Dialer{}).DialContext(ctx, "tcp", to.addr)
	if err != nil {
		return err
	}
	defer func() { _ = raw.Close() }()
	if to.tls == nil {
		return nil
	}
	cfg := to.tls.Clone()
	if cfg.ServerName == "" {
		cfg.ServerName = hostOnly(to.addr)
	}
	if err := tls.Client(raw, cfg).HandshakeContext(ctx); err != nil {
		return handshakeError{err}
	}
	return nil
}

// handshakeError is a TLS handshake that failed after the connection was
// made, marked so that classify never reads it as a port nobody is on: a
// reset or a hang-up inside a handshake arrives as the same *net.OpError a
// refused dial does.
type handshakeError struct{ err error }

func (e handshakeError) Error() string { return e.err.Error() }
func (e handshakeError) Unwrap() error { return e.err }

func tlsConfig(req plugin.Request) (*tls.Config, *view.Error) {
	sf := req.Surface()
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if ca := req.String("ca-file"); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, view.Errorf("etcd.tls.ca.unreadable", "%v", err).
				WithHint(setting(sf, "ca-file") + " is a path on this machine, read by rta rather than by the cluster")
		}
		pool := x509.NewCertPool()
		// What the file has to hold, rather than a guess at what it held
		// instead. The hint once said "not the client certificate", and a
		// client certificate in PEM never reaches this line: only a file with
		// no PEM certificate in it does, the client's private key among them,
		// or a DER-encoded certificate. And a self-signed cluster's own
		// certificate is exactly what belongs here, the file the
		// untrusted-certificate hint in classify sends the reader to name.
		if !pool.AppendCertsFromPEM(pem) {
			return nil, view.Errorf("etcd.tls.ca.invalid", "%s holds no PEM certificate", ca).
				WithHint(setting(sf, "ca-file") + " wants a PEM certificate — the CA's, or a self-signed " +
					"server's own — and a private key, which belongs in " + setting(sf, "key-file") +
					", or a DER-encoded certificate is not one")
		}
		cfg.RootCAs = pool
	}

	cert, key := req.String("cert-file"), req.String("key-file")
	// Half of an mTLS pair is not a working configuration and not a partial
	// one — it is a connection that fails at handshake with an error naming
	// neither file. Refusing here says which half is missing.
	switch {
	case cert != "" && key == "":
		return nil, view.Errorf("etcd.tls.key.missing", "%s given without %s", setting(sf, "cert-file"), setting(sf, "key-file")).
			WithHint("a client certificate is unusable without its private key")
	case key != "" && cert == "":
		return nil, view.Errorf("etcd.tls.cert.missing", "%s given without %s", setting(sf, "key-file"), setting(sf, "cert-file")).
			WithHint("a private key is unusable without the certificate it belongs to")
	case cert != "":
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, view.Errorf("etcd.tls.pair.invalid", "%v", err).
				WithHint("both paths are read on this machine — check they are PEM and belong together")
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}

// classify turns a client error into something an operator can act on.
//
// etcd speaks gRPC, so most failures arrive as a status code rather than as a
// typed error. The codes are the stable part; the message beside them varies
// with the version and with which member answered.
func classify(err error, req plugin.Request) *view.Error {
	var already *view.Error
	if errors.As(err, &already) {
		return already
	}
	where, sf := endpointOf(req), req.Surface()

	// etcd's own sentinel errors are checked before the gRPC codes, because
	// several of them share a code and only the sentinel says which is which.
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return view.Errorf("etcd.timeout", "%s did not answer in time", where).
			WithHint("a cluster that has lost quorum accepts connections and answers nothing — " +
				sf.CapabilityName("etcd.overview") + " shows whether the members can see each other")
	case errors.Is(err, clientv3.ErrNoAvailableEndpoints):
		return view.Errorf("etcd.unreachable", "no endpoint answered at %s", where).
			WithHint("is the cluster up, and is " + setting(sf, "endpoint") + " right? etcd listens on 2379 for clients " +
				"and 2380 for peers, and the peer port will not answer this")
	}

	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unauthenticated:
			// Where the password comes from rather than a verb telling the
			// reader to set one: an agent has no host environment to set, and
			// no password argument either, since the bridge drops a Local
			// input given.
			return view.Errorf("etcd.auth.failed", "%s rejected the credentials", where).
				WithHint("the password is read from $" + plugin.LocalEnvVar("etcd.overview", "password") +
					" or " + setting(sf, "password") + " — check it, and " + setting(sf, "username") +
					": a cluster with auth disabled refuses a username too")
		case codes.PermissionDenied:
			return view.Errorf("etcd.denied", "%s: %s", where, st.Message()).
				WithHint("the credentials are valid but the role does not cover this key range")
		case codes.Unavailable:
			return view.Errorf("etcd.unavailable", "%s is not serving: %s", where, st.Message()).
				WithHint("a member that has lost quorum reports exactly this — check the others")
		case codes.DeadlineExceeded:
			return view.Errorf("etcd.timeout", "%s did not answer in time", where).
				WithHint("a firewall that drops rather than refuses looks exactly like this")
		}
	}

	// The name first, and the name alone: a dial that could not resolve its
	// host fails with a *net.OpError wrapping the *net.DNSError, and read the
	// other way round every name nothing resolves was reported as a port
	// nothing listens on. What DNS was asked is the host, never host:port —
	// quoting the port beside it names something no lookup was ever made for.
	var dnsErr *stdnet.DNSError
	if errors.As(err, &dnsErr) {
		return view.Errorf("etcd.host.unknown", "no address for %q", hostOnly(where)).
			WithHint(dnsHint(sf, hostOnly(where)))
	}
	// The handshake before the port: a connection that was made and then
	// failed its handshake is something listening, and a reset or a hang-up
	// inside the handshake arrives as the same *net.OpError a refused dial
	// does. The certificate is the most specific of the three.
	//
	// The CA is named as where it belongs, not as something to pass: over MCP
	// ca-file is the operator's setting, and an agent told to pass it has no
	// such argument to give.
	if untrusted(err) {
		return view.Errorf("etcd.tls.untrusted", "%s presented a certificate nothing here trusts", where).
			WithHint("etcd clusters usually have their own CA, and it belongs in " + setting(sf, "ca-file") +
				" — a self-signed certificate is its own CA")
	}
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		return view.Errorf("etcd.tls.rejected", "%s presented a certificate that does not verify: %v", where, verifyErr.Err).
			WithHint("a certificate is checked for the host in " + setting(sf, "endpoint") +
				", its dates and the use it was issued for, as well as for who issued it")
	}
	var hsErr handshakeError
	if errors.As(err, &hsErr) {
		return view.Errorf("etcd.tls.failed", "TLS with %s failed: %v", where, hsErr.err).
			WithHint("a client port serving plaintext hangs up on TLS: TLS is for a cluster whose client " +
				"URLs are https://, and an https:// endpoint, " + setting(sf, "tls") + ", " + setting(sf, "ca-file") +
				" and " + setting(sf, "cert-file") + " each turn it on")
	}
	// Short of the host, before the port: a dial that found no way there
	// reached nothing that could refuse it, and read as refused, a cluster on
	// a network this machine is not on — behind a VPN that is down, at an
	// address of another network's — was "nothing is listening" about a port
	// no packet reached. Windows numbers its socket errors otherwise, and
	// there this falls through to the refusal below, as it always did.
	var netErr *stdnet.OpError
	if errors.As(err, &netErr) && unroutable(err) {
		return view.Errorf("etcd.unreachable", "%s cannot be reached from this machine: %v", where, netErr.Err).
			WithHint("no route leads there from here — a VPN or tunnel the cluster sits behind that is down " +
				"looks exactly like this, and so does " + setting(sf, "endpoint") + " naming an address on " +
				"a network this machine is not on")
	}
	if errors.As(err, &netErr) || strings.Contains(err.Error(), "connection refused") {
		return view.Errorf("etcd.conn.refused", "nothing is listening on %s", where).
			WithHint("etcd listens on 2379 for clients and 2380 for peers — the peer port will not answer this")
	}
	return view.Errorf("etcd.conn.failed", "could not reach %s: %v", where, err).
		WithHint(explainHint(sf, "etcd.overview"))
}

// untrusted reports whether err is a certificate that nothing here vouches
// for. Go's own verifier says so as an x509.UnknownAuthorityError, and it is
// the one that runs whenever ca-file is set. Without one, on macOS, the
// system's trust store is consulted through the platform's verifier, and an
// untrusted chain comes back from it as a bare error inside the handshake's
// *tls.CertificateVerificationError — as does a self-signed certificate
// valid for longer than Apple's policy allows, "not standards compliant" —
// so a verification failure the platform answers untyped is read as one too.
// Read the typed way alone, a cluster with its own CA reached from a Mac was
// told everything but the CA.
//
// Untyped, and not merely not UnknownAuthorityError: Go's verifier types
// every failure it names — a host the certificate is not for, a date or a
// use it is not valid for, a signature algorithm it will not accept, a
// critical extension it does not handle — and each of those is a reason of
// its own, which no CA in ca-file would cure. Read as untrusted, a SHA-1
// certificate was answered "nothing here trusts" with the CA to name, and the
// reason itself, which etcd.tls.rejected quotes, went unsaid.
func untrusted(err error) bool {
	var authErr x509.UnknownAuthorityError
	if errors.As(err, &authErr) {
		return true
	}
	var verifyErr *tls.CertificateVerificationError
	if !errors.As(err, &verifyErr) {
		return false
	}
	for _, reason := range []any{new(x509.HostnameError), new(x509.CertificateInvalidError),
		new(x509.InsecureAlgorithmError), new(x509.UnhandledCriticalExtension),
		new(x509.ConstraintViolationError)} {
		if errors.As(verifyErr.Err, reason) {
			return false
		}
	}
	return true
}

// unroutable reports whether err is a dial that found no way to the host: no
// route to it, a network this machine has no way onto, a host its own network
// reports down.
func unroutable(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EHOSTDOWN)
}

// clientPort is the port etcd serves its clients on.
const clientPort = "2379"

// endpointOf is the endpoint as etcd's client is handed it: the one given,
// with the client port when it names none, behind a scheme or not.
//
// The input's help says host[:port], as redis's address does, and etcd's
// client has no default port: a bare host is dialled as it stands and fails
// "missing port in address" on every retry, so a call over one hung until
// interrupted, and once the connect was bounded it read "nothing is
// listening" about a port nobody had named. A unix socket, and a host this
// cannot name a port for — an IPv6 address with a zone, a bracket left open —
// pass as given, for the dial to refuse.
func endpointOf(req plugin.Request) string {
	endpoint := req.String("endpoint")
	if strings.HasPrefix(endpoint, "unix:") || strings.HasPrefix(endpoint, "unixs:") {
		return endpoint
	}
	if scheme, rest, ok := strings.Cut(endpoint, "://"); ok {
		hostport, path, slash := strings.Cut(rest, "/")
		if slash {
			path = "/" + path
		}
		return scheme + "://" + withPort(hostport) + path
	}
	return withPort(endpoint)
}

func withPort(hostport string) string {
	if _, _, err := stdnet.SplitHostPort(hostport); err == nil {
		return hostport
	}
	if strings.HasPrefix(hostport, "[") != strings.HasSuffix(hostport, "]") {
		return hostport
	}
	host := strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	if host == "" || strings.ContainsAny(host, "[]") || (strings.Contains(host, ":") && stdnet.ParseIP(host) == nil) {
		return hostport
	}
	return stdnet.JoinHostPort(host, clientPort)
}

func hostOnly(endpoint string) string {
	// An http:// or https:// endpoint is one etcd's client takes as readily
	// as a bare host:port, and split as host:port whole it is not one: a name
	// DNS did not know behind https:// was quoted scheme, port and all, with
	// a lookup for the whole URL offered as the next step.
	if _, rest, ok := strings.Cut(endpoint, "://"); ok {
		endpoint, _, _ = strings.Cut(rest, "/")
	}
	host, _, err := stdnet.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}
	return host
}

// setting names connection input name in a message the way its reader
// changes it: the flag on the CLI, the box in a TUI form. Not the argument
// over MCP, as plugin.Surface.InputName would: every connection input is
// Local, so the tool's schema hides it and the bridge drops one given, and an
// agent told to check the "username" argument would pass one that is thrown
// away and read the same refusal again. It is named there as the declaration
// names it, `username` — a setting of the operator's, which the agent can
// report and cannot change.
func setting(sf plugin.Surface, name string) string {
	if sf == plugin.SurfaceMCP {
		return "`" + name + "`"
	}
	return sf.InputName(name)
}

// explainHint sends the reader to the page listing every input and where each
// one can come from. That page is `rta explain`, a terminal's command with no
// capability behind it, and what it answers here is where the connection
// inputs come from — the operator's to set — so over MCP it is the operator
// who is asked to read it.
func explainHint(sf plugin.Surface, id string) string {
	if sf == plugin.SurfaceMCP {
		return plugin.AskOperator("explain "+id) + ", which lists every input and where each one can come from"
	}
	return "`rta explain " + id + "` lists every input and where each one can come from"
}

// dnsHint is the call that shows what DNS returns for host, spelled for the
// surface that will make it.
func dnsHint(sf plugin.Surface, host string) string {
	return "`" + sf.Call("net.dns", plugin.Arg{Name: "name", Value: host, Positional: true}) + "` shows what DNS returns"
}

// given names input name set to value, as the reader would give it: "--out
// ./etcd.snap" on the CLI, and elsewhere the input the surface names, with
// the value beside it.
func given(sf plugin.Surface, name, value string) string {
	if sf == plugin.SurfaceMCP || sf == plugin.SurfaceTUI {
		return sf.InputName(name) + " set to " + value
	}
	return sf.InputName(name) + " " + value
}
