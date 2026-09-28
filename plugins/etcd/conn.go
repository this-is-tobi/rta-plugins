package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	stdnet "net"
	"os"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/codes"
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

// dialTimeout bounds the connect itself. etcd's client will otherwise retry a
// dead endpoint for as long as the context allows, which turns "the cluster is
// down" into a call that hangs rather than one that answers.
const dialTimeout = 10 * time.Second

func connect(ctx context.Context, req plugin.Request) (*clientv3.Client, *view.Error) {
	cfg := clientv3.Config{
		Endpoints:   []string{req.String("endpoint")},
		DialTimeout: dialTimeout,
		Username:    req.String("username"),
		Password:    req.String("password"),
		Context:     ctx,
	}

	if req.Bool("tls") || req.String("ca-file") != "" || req.String("cert-file") != "" {
		tlsCfg, verr := tlsConfig(req)
		if verr != nil {
			return nil, verr
		}
		cfg.TLS = tlsCfg
	}

	client, err := clientv3.New(cfg)
	if err != nil {
		return nil, classify(err, req)
	}
	return client, nil
}

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
		if !pool.AppendCertsFromPEM(pem) {
			return nil, view.Errorf("etcd.tls.ca.invalid", "%s holds no PEM certificate", ca).
				WithHint("this wants the CA bundle, not the client certificate")
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
	where, sf := req.String("endpoint"), req.Surface()

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
			return view.Errorf("etcd.auth.failed", "%s rejected the credentials", where).
				WithHint("set $" + plugin.LocalEnvVar("etcd.overview", "password") +
					", or check " + setting(sf, "username") + " — a cluster with auth disabled refuses a username too")
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
	var netErr *stdnet.OpError
	if errors.As(err, &netErr) || strings.Contains(err.Error(), "connection refused") {
		return view.Errorf("etcd.conn.refused", "nothing is listening on %s", where).
			WithHint("etcd listens on 2379 for clients and 2380 for peers — the peer port will not answer this")
	}
	var authErr x509.UnknownAuthorityError
	if errors.As(err, &authErr) {
		return view.Errorf("etcd.tls.untrusted", "%s presented a certificate nothing here trusts", where).
			WithHint("etcd clusters usually have their own CA — pass it with " + setting(sf, "ca-file"))
	}
	return view.Errorf("etcd.conn.failed", "could not reach %s: %v", where, err).
		WithHint(explainHint(sf, "etcd.overview"))
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
