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
	"time"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
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
		// The name the certificate is checked for when it is not the host
		// dialled — above all through a kube: or ssh: forward, whose end is
		// 127.0.0.1 whatever the member is called, and which a cluster's
		// certificate names only by luck. Checked as strictly as the host would
		// have been: it moves the check, never loosens it. Local for the reason
		// tls is: what a certificate has to prove is the operator's to say.
		{Name: "tls-server-name", Type: plugin.String, Default: "", Config: "tls-server-name",
			Local: true, Help: "name to check the server's certificate for, in place of the endpoint's host"},
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

// tlsRequested is whether any setting asks for TLS: the switch itself, or a
// file or a name that only means something over it.
func tlsRequested(req plugin.Request) bool {
	return req.Bool("tls") || req.String("ca-file") != "" || req.String("cert-file") != "" ||
		serverName(req) != ""
}

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
	if tlsRequested(req) {
		var verr *view.Error
		if tlsCfg, verr = tlsConfig(req); verr != nil {
			return nil, verr
		}
		cfg.TLS = tlsCfg
		// And as the connection's authority, since gRPC's TLS handshake sets
		// the config's ServerName to the authority's host on every
		// connection, whatever the config held: given only there, the name
		// reached the diagnostic dial and never the client's own handshake,
		// which went on checking the certificate for 127.0.0.1.
		if name := serverName(req); name != "" {
			cfg.DialOptions = append(cfg.DialOptions, grpc.WithAuthority(name))
		}
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
	return view.Errorf("etcd.timeout", "%s did not answer in time", req.Reached(endpointOf(req))).
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
		"understands", req.Reached(endpointOf(req)))
	if to.tls != nil {
		return refusal.WithHint("etcd listens on 2379 for clients and 2380 for peers, and the peer port " +
			"will not answer this — nor will a client port serving TLS to a certificate it does not accept")
	}
	// **Through a forward, tls is no way on.** The host turns it off for the
	// forward it opens, and refuses it given beside one, so a hint naming it
	// sent the reader to a setting that could not apply. A CA file or the
	// name a certificate is checked for turns TLS on over the forward, as
	// connect reads them.
	if req.Tunnel() != plugin.TunnelNone {
		return refusal.WithHint("etcd listens on 2379 for clients and 2380 for peers, and the peer port will " +
			"not answer this — nor will a client port serving TLS, to the plaintext " +
			req.Reached(endpointOf(req)) + " asks for: " +
			req.Surface().SettingName("ca-file", "tls-server-name") + " each turn TLS on over the forward")
	}
	return refusal.WithHint("etcd listens on 2379 for clients and 2380 for peers, and the peer port will " +
		"not answer this — nor will a client port serving TLS, to a client without " +
		req.Surface().SettingName("tls") + " on")
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

// tlsConfig is the TLS the three certificate paths describe, each with a
// leading ~ resolved as every other path a plugin reads is. Opened as typed,
// ~/ca.pem was a path under a directory named ~, and a CA sitting in the
// operator's home was answered as no such file.
func tlsConfig(req plugin.Request) (*tls.Config, *view.Error) {
	sf := req.Surface()
	// ServerName is what the certificate is checked for, by the client's own
	// handshake and by the diagnostic dial alike (target.dial), and what is
	// sent as SNI; empty, each takes the host it dials.
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName(req)}

	if ca := plugin.ExpandHome(req.String("ca-file")); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, view.Errorf("etcd.tls.ca.unreadable", "%v", err).
				WithHint(sf.SettingName("ca-file") + " is a path on this machine, read by rta rather than by the cluster")
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
				WithHint(sf.SettingName("ca-file") + " wants a PEM certificate — the CA's, or a self-signed " +
					"server's own — and a private key, which belongs in " + sf.SettingName("key-file") +
					", or a DER-encoded certificate is not one")
		}
		cfg.RootCAs = pool
	}

	cert, key := plugin.ExpandHome(req.String("cert-file")), plugin.ExpandHome(req.String("key-file"))
	// Half of an mTLS pair is not a working configuration and not a partial
	// one — it is a connection that fails at handshake with an error naming
	// neither file. Refusing here says which half is missing.
	switch {
	case cert != "" && key == "":
		return nil, view.Errorf("etcd.tls.key.missing", "%s given without %s", sf.SettingName("cert-file"), sf.SettingName("key-file")).
			WithHint("a client certificate is unusable without its private key")
	case key != "" && cert == "":
		return nil, view.Errorf("etcd.tls.cert.missing", "%s given without %s", sf.SettingName("key-file"), sf.SettingName("cert-file")).
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

// serverCode is the gRPC code a failure carries and the server's words for
// it, however the client delivered it.
//
// **The client delivers most of them as an rpctypes.EtcdError, which is not a
// gRPC status.** clientv3 turns a status into its own error type on the way
// out, and status.FromError reads that type as no status at all, so a refused
// password, a role without the permission and a member without quorum all
// fell past the code switch below and were reported as "could not reach" the
// endpoint, with a hint about the settings. The tests fed the switch
// status.Error values, which is what the server sends and not what the
// client hands back.
func serverCode(err error) (codes.Code, string, bool) {
	if st, ok := status.FromError(err); ok {
		return st.Code(), st.Message(), true
	}
	var ee rpctypes.EtcdError
	if errors.As(err, &ee) {
		// etcd answers a wrong password and a missing user name with
		// InvalidArgument, and only a token it no longer honours with
		// Unauthenticated: the four are one finding for the person who has to
		// fix it, and the code alone sent three of them to the generic error.
		switch ee {
		case rpctypes.ErrAuthFailed, rpctypes.ErrUserEmpty, rpctypes.ErrInvalidAuthToken, rpctypes.ErrAuthOldRevision:
			return codes.Unauthenticated, ee.Error(), true
		}
		return ee.Code(), ee.Error(), true
	}
	return codes.OK, "", false
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
		return view.Errorf("etcd.timeout", "%s did not answer in time", req.Reached(where)).
			WithHint("a cluster that has lost quorum accepts connections and answers nothing — " +
				nextCall(req, "etcd.overview") + " shows whether the members can see each other")
	case errors.Is(err, clientv3.ErrNoAvailableEndpoints):
		return view.Errorf("etcd.unreachable", "no endpoint answered at %s", where).
			WithHint("is the cluster up, and is " + sf.SettingName("endpoint") + " right? etcd listens on 2379 for clients " +
				"and 2380 for peers, and the peer port will not answer this")
	}

	if code, msg, ok := serverCode(err); ok {
		switch code {
		case codes.Unauthenticated:
			// Where the password comes from rather than a verb telling the
			// reader to set one: an agent has no host environment to set, and
			// no password argument either, since the bridge drops a Local
			// input given.
			return view.Errorf("etcd.auth.failed", "%s rejected the credentials", req.Reached(where)).
				WithHint("the password is read from $" + plugin.LocalEnvVar("etcd.overview", "password") +
					" or " + sf.SettingName("password") + " — check it, and " + sf.SettingName("username") +
					": a cluster with auth disabled refuses a username too")
		case codes.PermissionDenied:
			return view.Errorf("etcd.denied", "%s: %s", req.Reached(where), msg).
				WithHint("the credentials are valid but the role does not cover this: a key range for a read of keys, " +
					"and for the cluster's own status, which etcd answers only to a user holding the root role")
		case codes.Unavailable:
			return view.Errorf("etcd.unavailable", "%s is not serving: %s", req.Reached(where), msg).
				WithHint("a member that has lost quorum reports exactly this — check the others")
		case codes.DeadlineExceeded:
			return view.Errorf("etcd.timeout", "%s did not answer in time", req.Reached(where)).
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
			WithHint(sf.DNSHint(hostOnly(where)))
	}
	// The handshake before the port: a connection that was made and then
	// failed its handshake is something listening, and a reset or a hang-up
	// inside the handshake arrives as the same *net.OpError a refused dial
	// does. The certificate is the most specific of the three.
	//
	// The CA is named as where it belongs, not as something to pass: over MCP
	// ca-file is the operator's setting, and an agent told to pass it has no
	// such argument to give. Only for a verdict that means an issuer nothing
	// here vouches for (plugin.CertUntrusted): macOS answers a revoked
	// certificate untyped too, and read as untrusted it was answered with the
	// CA file to name — which runs Go's verifier in the system's place, with
	// no revocation check, and connects. Every other verdict is quoted below
	// in the system's own words.
	if plugin.CertUntrusted(err) {
		return view.Errorf("etcd.tls.untrusted", "%s presented a certificate nothing here trusts", req.Reached(where)).
			WithHint("etcd clusters usually have their own CA: " + sf.CAHint("ca-file"))
	}
	// A certificate that is not for the end of a forward the host opened is
	// no fault of the member's, and not one the endpoint can fix: through a
	// forward the host fills the endpoint with 127.0.0.1 and a port of its
	// own, which a member's certificate names only by luck (kubeadm's does;
	// cert-manager's, for a service's name, does not). So the refusal names
	// the forward and the name the certificate is for, and sends the reader
	// to tls-server-name, which checks that name in 127.0.0.1's place — never
	// to anything that checks less. Only when tls-server-name is not set: a
	// name given and not matched is the certificate's to explain, below.
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) && req.Tunnel() != plugin.TunnelNone && serverName(req) == "" {
		return plugin.ForwardNameRefusal(req, "etcd.tls.forward", endpointOf(req), "member", hostErr)
	}
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		checked := "the host in " + sf.SettingName("endpoint")
		if serverName(req) != "" {
			checked = "the name in " + sf.SettingName("tls-server-name")
		}
		rejected := view.Errorf("etcd.tls.rejected", "%s presented a certificate that does not verify: %v", req.Reached(where), verifyErr.Err)
		// A rule of macOS's own, which the verdict's words do not name: a
		// ten-year certificate, the usual one for a cluster of one's own, is
		// "not standards compliant" there, and the hint below would have
		// sent its reader to check dates that were fine.
		if hint := plugin.CertPolicyHint(err); hint != "" {
			return rejected.WithHint(hint)
		}
		return rejected.WithHint("a certificate is checked for " + checked +
			", its dates and the use it was issued for, as well as for who issued it")
	}
	var hsErr handshakeError
	if errors.As(err, &hsErr) {
		return view.Errorf("etcd.tls.failed", "TLS with %s failed: %v", req.Reached(where), hsErr.err).
			WithHint("a client port serving plaintext hangs up on TLS: TLS is for a cluster whose client " +
				"URLs are https://, and an https:// endpoint turns it on, as do " +
				sf.SettingName("tls", "ca-file", "cert-file", "tls-server-name"))
	}
	// Short of the host, before the port: a dial that found no way there
	// reached nothing that could refuse it, and read as refused, a cluster on
	// a network this machine is not on — behind a VPN that is down, at an
	// address of another network's — was "nothing is listening" about a port
	// no packet reached.
	//
	// Each read by the operating system's own error (plugin.DialUnroutable,
	// plugin.DialRefused), never by the *net.OpError around it, which every
	// failed dial is: read that way, a dial that timed out or was reset was
	// "nothing is listening" too.
	if plugin.DialUnroutable(err) {
		reason := err
		var netErr *stdnet.OpError
		if errors.As(err, &netErr) {
			reason = netErr.Err
		}
		return view.Errorf("etcd.unreachable", "%s cannot be reached from this machine: %v", where, reason).
			WithHint("no route leads there from here — a VPN or tunnel the cluster sits behind that is down " +
				"looks exactly like this, and so does " + sf.SettingName("endpoint") + " naming an address on " +
				"a network this machine is not on")
	}
	if plugin.DialRefused(err) {
		return view.Errorf("etcd.conn.refused", "nothing is listening on %s", where).
			WithHint("etcd listens on 2379 for clients and 2380 for peers — the peer port will not answer this")
	}
	return view.Errorf("etcd.conn.failed", "could not reach %s: %v", where, err).
		WithHint(sf.SettingsHint("etcd.overview"))
}

// serverName is the name the certificate is checked for in place of the
// endpoint's host, or "" for the host.
func serverName(req plugin.Request) string { return strings.TrimSpace(req.String("tls-server-name")) }

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

// reachArgs points a call this one hands its reader at the cluster it read
// (Request.ReachArgs), with what else decided how it was reached: the
// settings that turned TLS on and what the certificate was checked against,
// and the user it authenticated as, whose roles decide which keys it may see.
// Over MCP the call gives the profile alone, since the rest are Local and the
// bridge drops one an agent sends.
//
// **Without them, the call named reached another cluster.** Pasted, it read
// whatever endpoint the configuration there named, and a key "not found" was
// looked for again somewhere it was never going to be.
func reachArgs(req plugin.Request) []plugin.Arg {
	if req.Surface() == plugin.SurfaceMCP {
		return req.ReachArgs()
	}
	args := req.ReachArgs(plugin.Arg{Name: "endpoint", Value: req.String("endpoint")})
	if req.Bool("tls") {
		args = append(args, plugin.Arg{Name: "tls", Value: true})
	}
	for _, name := range []string{"ca-file", "tls-server-name", "cert-file", "key-file", "username"} {
		if v := strings.TrimSpace(req.String(name)); v != "" {
			args = append(args, plugin.Arg{Name: name, Value: v})
		}
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
