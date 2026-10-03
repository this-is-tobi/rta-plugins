package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// connFields are the inputs every capability here shares.
//
// Every one is Local, and that is the security property rather than a detail.
// Together they name which instance this call reaches and as whom, and an MCP
// caller may not choose that: caller values resolve above config and above the
// host's own environment, so an agent that could set `endpoint` would point
// rta at an instance of its own and have the host supply $RTA_QDRANT_API_KEY
// beside it. Config still fills these and a person at a terminal still passes
// them as ordinary flags.
func connFields() []plugin.Field {
	return []plugin.Field{
		// Qdrant serves REST on 6333 and gRPC on 6334. This speaks REST, so
		// 6334 will not answer it — which classify says out loud, because the
		// failure otherwise names neither port nor protocol.
		{Name: "endpoint", Type: plugin.String, Default: "127.0.0.1:6333", Config: "endpoint",
			Local: true, Endpoint: plugin.EndpointAddress, Help: "Qdrant REST endpoint, host[:port]"},
		// Local for the downgrade reason rather than the redirect one: an
		// agent that could set this could ask for plaintext against an
		// instance the operator configured as HTTPS.
		{Name: "tls", Type: plugin.Bool, Default: false, Config: "tls",
			Local: true, Endpoint: plugin.EndpointTLS,
			Help: "use HTTPS (a local Qdrant ordinarily does not)"},
		{Name: "api-key", Type: plugin.Secret, Local: true, EnvFallback: true,
			Help: "API key, for an instance that requires one"},
		// Local for the same reason plugins/etcd's own ca-file is: it is read
		// off this machine's disk, not held as a value rta's own store could
		// manage. Not a plugin.Secret: a CA certificate is the public half
		// of a key pair, the half a CA hands out for wide distribution so
		// anyone can verify what it signed — the same reason an OS trust
		// store ships thousands of them in the clear. ca-file only needs
		// Local for the file-read-primitive reason address does, not
		// because its contents are sensitive.
		{Name: "ca-file", Type: plugin.String, Default: "", Config: "ca-file",
			Local: true, Help: "PEM bundle to verify the server against, beyond the host's own trust store"},
		// The name the certificate is checked for when it is not the host
		// dialled — above all through a kube: or ssh: forward, whose end is
		// 127.0.0.1 whatever the instance is called, and which a service's
		// certificate names only by luck. Checked as strictly as the host would
		// have been: it moves the check, never loosens it. Local for the reason
		// tls is: what a certificate has to prove is the operator's to say.
		{Name: "tls-server-name", Type: plugin.String, Default: "", Config: "tls-server-name",
			Local: true, Help: "name to check the server's certificate for, in place of the endpoint's host"},
	}
}

// requestTimeout bounds every call. Qdrant answers most of these instantly,
// and the ones that do not — a count over a large collection without an index
// — are the ones where waiting forever is the wrong behaviour.
const requestTimeout = 30 * time.Second

// get issues one REST call and decodes Qdrant's envelope into out.
//
// Qdrant wraps every answer in {"result": ..., "status": ..., "time": ...},
// so decoding straight into the caller's type would silently produce a zero
// value. The envelope is unwrapped here, once, rather than at each call site.
func get(ctx context.Context, req plugin.Request, path string, out any) *view.Error {
	return call(ctx, req, http.MethodGet, path, nil, out)
}

func post(ctx context.Context, req plugin.Request, path string, body, out any) *view.Error {
	return call(ctx, req, http.MethodPost, path, body, out)
}

// getRaw is for the one endpoint that has no envelope. Qdrant's root answers
// {"title": ..., "version": ...} directly, so decoding it through get would
// look for a "result" that is not there and quietly produce a zero value.
func getRaw(ctx context.Context, req plugin.Request, path string, out any) *view.Error {
	return call(ctx, req, http.MethodGet, path, nil, rawTarget{out})
}

// rawTarget marks a decode target as already-unwrapped, so call can tell the
// two shapes apart without a second parameter that every other caller would
// have to pass.
type rawTarget struct{ into any }

func call(ctx context.Context, req plugin.Request, method, path string, body, out any) *view.Error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return slowCall(ctx, req, method, path, body, out)
}

// slowCall is call without the requestTimeout bound. The snapshot operations
// behind qdrant.dump run for as long as the collection is large — creating a
// snapshot walks every segment, and 30 seconds is the bound for API chatter,
// not for work proportional to the data. The operator's own interrupt still
// cancels through ctx.
func slowCall(ctx context.Context, req plugin.Request, method, path string, body, out any) *view.Error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return view.Errorf("qdrant.request.invalid", "%v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	httpReq, verr := newRequest(ctx, req, method, path, reader)
	if verr != nil {
		return verr
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	client, verr := httpClient(req)
	if verr != nil {
		return verr
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return classify(err, req)
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded read. A response body is attacker-controlled in the sense that
	// matters here — whatever is at that endpoint decides how much to send —
	// and an unbounded ReadAll is how a wrong endpoint becomes an out-of-memory.
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return classify(err, req)
	}
	if resp.StatusCode >= 300 {
		return classifyStatus(resp.StatusCode, payload, req)
	}

	if raw, ok := out.(rawTarget); ok {
		if err := json.Unmarshal(payload, raw.into); err != nil {
			return malformed(req)
		}
		return nil
	}

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Status any             `json:"status"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return malformed(req)
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return view.Errorf("qdrant.response.unexpected", "could not read the answer: %v", err).
				WithHint("this may be a Qdrant version whose response shape has moved")
		}
	}
	return nil
}

// newRequest builds one authenticated request against the configured
// endpoint — the scheme decision, the Accept header and the api-key in one
// place, so the JSON path above and the snapshot transfers in dump.go cannot
// drift on any of them.
func newRequest(ctx context.Context, req plugin.Request, method, path string,
	body io.Reader) (*http.Request, *view.Error) {
	base := "http://"
	if tlsOn(req) {
		base = "https://"
	}
	base += req.String("endpoint")

	httpReq, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, view.Errorf("qdrant.endpoint.invalid", "%v", err).
			WithHint("endpoint is host[:port] with no scheme — set " + req.Surface().SettingName("tls") + " separately")
	}
	httpReq.Header.Set("Accept", "application/json")
	if key := req.String("api-key"); key != "" {
		httpReq.Header.Set("api-key", key)
	}
	return httpReq, nil
}

// httpClient is http.DefaultClient unless ca-file names a CA to trust beyond
// this machine's own store, in which case it is a client built for exactly
// that — read and parsed fresh on every call, the same as every other input
// here, because this plugin keeps no client or connection across calls to
// begin with (call is the only entry point, per-request end to end).
//
// The path with a leading ~ resolved, as every other path a plugin reads
// is. Opened as typed, ~/ca.pem was a path under a directory named ~, and a
// CA sitting in the operator's home was answered as no such file.
//
// And unless tls-server-name names what the certificate is checked for in
// place of the endpoint's host: the handshake's ServerName, and the SNI it
// sends.
func httpClient(req plugin.Request) (*http.Client, *view.Error) {
	ca := plugin.ExpandHome(req.String("ca-file"))
	if ca == "" && serverName(req) == "" {
		return http.DefaultClient, nil
	}
	// MinVersion is Go's own client default already; stated so the config
	// says what it accepts, as plugins/keycloak's does.
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName(req)}
	if ca == "" {
		return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}, nil
	}
	sf := req.Surface()
	pem, err := os.ReadFile(ca)
	if err != nil {
		return nil, view.Errorf("qdrant.tls.ca.unreadable", "%v", err).
			WithHint(sf.SettingName("ca-file") + " is a path on this machine, read by rta rather than by the server")
	}
	pool := x509.NewCertPool()
	// What the file has to hold, rather than a guess at what it held instead.
	// The hint once said "not the server's own certificate", and a
	// self-signed server's own certificate is exactly what belongs here — the
	// untrusted-certificate hint in classify sends the reader to put it here —
	// while one in PEM never reaches this line at all: only a file with no PEM
	// certificate in it does, a private key or a DER-encoded certificate.
	if !pool.AppendCertsFromPEM(pem) {
		return nil, view.Errorf("qdrant.tls.ca.invalid", "%s holds no PEM certificate", ca).
			WithHint(sf.SettingName("ca-file") + " wants a PEM certificate — the CA's, or a self-signed " +
				"server's own — and a private key or a DER-encoded certificate is not one")
	}
	cfg.RootCAs = pool
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}, nil
}

// serverName is the name the certificate is checked for in place of the
// endpoint's host, or "" for the host.
func serverName(req plugin.Request) string { return strings.TrimSpace(req.String("tls-server-name")) }

// reachArgs points a call this one hands its reader at the instance it reached
// (Request.ReachArgs): the profile whenever there was one, and the endpoint
// only when the host opened no forward — through one it was 127.0.0.1 and a
// port that closed with the call, and the profile reaches the instance again.
// Reached directly the endpoint stays, with how it was reached when that was
// protected: tls when on, the CA when named, and the name the certificate was
// checked for, each of which turns TLS on by itself. Never the api-key. To an
// agent the profile alone: the rest is Local, and the bridge drops what an
// agent sends.
//
// **Without them, the call named reached another instance.** Pasted, it ran
// against whatever endpoint the configuration there names, and a collection
// "not found" was looked for again somewhere it was never going to be.
func reachArgs(req plugin.Request) []plugin.Arg {
	if req.Surface() == plugin.SurfaceMCP {
		return req.ReachArgs()
	}
	args := req.ReachArgs(plugin.Arg{Name: "endpoint", Value: req.String("endpoint")})
	if req.Bool("tls") {
		args = append(args, plugin.Arg{Name: "tls", Value: true})
	}
	if ca := req.String("ca-file"); ca != "" {
		args = append(args, plugin.Arg{Name: "ca-file", Value: ca})
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

// tlsOn reports whether a call speaks TLS: tls, or anything that only means
// something over it. ca-file turns it on the same way etcd's own ca-file
// does — the alternative is a value that silently does nothing until --tls is
// also typed. tls-server-name, for the same reason: through a forward the
// host turns tls off, and a name given in the profile beside it was a
// plain-HTTP call to a TLS port.
func tlsOn(req plugin.Request) bool {
	return req.Bool("tls") || req.String("ca-file") != "" || serverName(req) != ""
}

// tlsExpected is the refusal for a plain-HTTP call to a port that speaks only
// TLS. Through a forward that is the host's doing — it turns tls off unless
// the profile's connection says its far end speaks TLS — so the way out is
// named there, where an endpoint or a tls typed over the forward's would
// have named nothing the operator could change.
func tlsExpected(req plugin.Request) *view.Error {
	refusal := view.Errorf("qdrant.tls.expected", "this call spoke plain HTTP to %s, which speaks only TLS",
		req.Reached(req.String("endpoint")))
	if req.Tunnel() != plugin.TunnelNone {
		return refusal.WithHint("a forward carries plain HTTP unless the profile's connection says its far end " +
			"speaks TLS: tunnelTLS: true on that connection")
	}
	return refusal.WithHint(req.Surface().SettingName("tls") + " turns TLS on")
}

// maxResponseBytes bounds one response. Points carry payloads and vectors, and
// a scroll over a collection of documents is genuinely large — this is a
// backstop against a wrong endpoint, not a working limit.
//
// A var rather than a const so a test can lower it and actually reach the
// bound. Filling 32 MB to prove a limit works is a slow test nobody runs, and
// an unreached bound is one nothing would notice the removal of.
var maxResponseBytes int64 = 32 << 20

// classifyStatus turns an HTTP failure into something an operator can act on.
// Qdrant puts a reason in the body, and it is usually the useful half.
func classifyStatus(code int, body []byte, req plugin.Request) *view.Error {
	where := req.Reached(req.String("endpoint"))
	detail := qdrantErrorText(body)

	switch code {
	case http.StatusUnauthorized:
		return view.Errorf("qdrant.auth.failed", "%s rejected the credentials", where).
			WithHint("set $" + plugin.LocalEnvVar("qdrant.overview", "api-key") +
				" — an instance started without an API key refuses one that is sent, too")
	case http.StatusForbidden:
		// A 401 is a key or token the instance does not know, and the hint above
		// is for it. A 403 is one it knows and will not let do this: a read-only
		// key asked to write, or a token scoped to collections asked for the
		// cluster's own state, which wants global access. Sent to set the key
		// again, somebody with the right key and the wrong access changed
		// nothing and read the same refusal.
		return view.Errorf("qdrant.denied", "%s: %s", where, detail).
			WithHint("the credentials are valid but their access does not cover this: the read-only API key " +
				"reads everything and writes nothing, and a JWT scoped to collections cannot read the cluster's " +
				"own state, which needs global access")
	case http.StatusNotFound:
		return view.Errorf("qdrant.notfound", "%s: %s", where, detail).
			WithHint(nextCall(req, "qdrant.collection.list") + " shows what is there")
	case http.StatusTooManyRequests:
		return view.Errorf("qdrant.ratelimited", "%s is rate limiting: %s", where, detail).
			WithHint("this is the instance's own limit, not rta's")
	case http.StatusServiceUnavailable:
		return view.Errorf("qdrant.unavailable", "%s is not serving: %s", where, detail).
			WithHint("a Qdrant loading a collection from disk answers this until it is ready")
	}
	return view.Errorf("qdrant.request.failed", "%s returned %d: %s", where, code, detail).
		WithHint(req.Surface().SettingsHint("qdrant.overview"))
}

// qdrantErrorText digs the message out of Qdrant's error envelope, falling
// back to the raw body. A truncated body is better than "unknown error", and
// the whole body is worse than either — some of these run to kilobytes.
func qdrantErrorText(body []byte) string {
	var envelope struct {
		Status struct {
			Error string `json:"error"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Status.Error != "" {
		return truncate(envelope.Status.Error, 300)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "no detail given"
	}
	return truncate(text, 300)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// classify turns a transport failure into something an operator can act on.
func classify(err error, req plugin.Request) *view.Error {
	var already *view.Error
	if errors.As(err, &already) {
		return already
	}
	where := req.String("endpoint")

	if errors.Is(err, context.DeadlineExceeded) {
		return view.Errorf("qdrant.timeout", "%s did not answer in time", where).
			WithHint("a count or scroll over a large collection with no index does this — " +
				"narrow it, or check the collection is indexed")
	}
	// The name first: a dial that could not resolve its host fails with a
	// *net.OpError wrapping the *net.DNSError, and read the other way round
	// every name nothing resolves was reported as a port nothing listens on.
	var dnsErr *stdnet.DNSError
	if errors.As(err, &dnsErr) {
		return view.Errorf("qdrant.host.unknown", "no address for %q", hostOnly(where)).
			WithHint(req.Surface().DNSHint(hostOnly(where)))
	}
	sf := req.Surface()
	// The certificate before the dial. A verdict on one is typed, so read
	// first it answers for nothing else; the dial's questions
	// (plugin.DialUnroutable, plugin.DialRefused) read an error's words when
	// it carries no errno, as no verdict does, and a verdict's words hold the
	// certificate's own names, which are the server's to choose. Read after
	// them, a certificate valid for "connection refused", or on macOS a
	// revoked one named so, was nothing listening, on a port that had
	// answered with a certificate.
	//
	// The CA, and never TLS off. A server that got as far as presenting a
	// certificate speaks only TLS on that port and refuses a plain-HTTP
	// request, so turning tls off reaches nothing — and with ca-file set it
	// does not even turn TLS off, since ca-file alone turns it on. The hint
	// once offered it anyway, as the quick way round.
	//
	// Asked of plugin.CertUntrusted rather than of the type Go's verifier
	// alone gives: with no ca-file, macOS answers a private CA's chain
	// untyped, and it was "could not reach". And only for a verdict that
	// means an issuer nothing here vouches for — a revoked certificate is
	// answered untyped too, and the CA file is no cure for it but a way
	// around the check that caught it.
	if plugin.CertUntrusted(err) {
		return view.Errorf("qdrant.tls.untrusted", "%s presented a certificate nothing here trusts", req.Reached(where)).
			WithHint(sf.CAHint("ca-file") + "; turning TLS off is no way round it, as the server refuses plain HTTP")
	}
	// Every other verdict is its own reason, quoted in the verifier's words —
	// the system's, for one macOS gives untyped — and never "could not reach":
	// the server was reached, and answered with a certificate.
	//
	// A certificate that is not for the end of a forward the host opened is
	// no fault of the instance's, and not one the endpoint can fix: through a
	// forward the host fills the endpoint with 127.0.0.1 and a port of its
	// own, which a service's certificate names only by luck. So the refusal
	// names the forward and the name the certificate is for, and sends the
	// reader to tls-server-name, which checks that name in 127.0.0.1's place
	// — never to anything that checks less. Only when tls-server-name is not
	// set: a name given and not matched is the certificate's to explain.
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) && req.Tunnel() != plugin.TunnelNone && serverName(req) == "" {
		return forwardName(req, hostErr)
	}
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		checked := "the host in " + sf.SettingName("endpoint")
		if serverName(req) != "" {
			checked = "the name in " + sf.SettingName("tls-server-name")
		}
		rejected := view.Errorf("qdrant.tls.rejected", "%s presented a certificate that does not verify: %v", req.Reached(where), verifyErr.Err)
		// A rule of macOS's own, which the verdict's words do not name: a
		// ten-year certificate, the usual one for an instance of one's own,
		// is "not standards compliant" there, and the hint below would have
		// sent its reader to check dates that were fine.
		if hint := plugin.CertPolicyHint(err); hint != "" {
			return rejected.WithHint(hint)
		}
		return rejected.WithHint("a certificate is checked for " + checked +
			", its dates and the use it was issued for, as well as for who issued it")
	}
	// Short of the host, before the port: a dial that found no way there
	// reached nothing that could refuse it, and read as refused, an instance
	// behind a VPN that is down, or at an address of another network's, was
	// "nothing is listening" about a port no packet reached.
	//
	// Each read by the operating system's own error (plugin.DialUnroutable,
	// plugin.DialRefused), never by the *net.OpError around it, which every
	// failed dial is: read that way, a dial that was reset was "nothing is
	// listening" too, and one that timed out never reached the timeout below.
	if plugin.DialUnroutable(err) {
		reason := err
		var netErr *stdnet.OpError
		if errors.As(err, &netErr) {
			reason = netErr.Err
		}
		return view.Errorf("qdrant.conn.unreachable", "%s cannot be reached from this machine: %v", where, reason).
			WithHint("no route leads there from here — a VPN or tunnel the instance sits behind that is " +
				"down looks exactly like this, and so does " + sf.SettingName("endpoint") + " naming " +
				"an address on a network this machine is not on")
	}
	if plugin.DialRefused(err) {
		return view.Errorf("qdrant.conn.refused", "nothing is listening on %s", where).
			WithHint("Qdrant serves REST on 6333 and gRPC on 6334 — the gRPC port will not answer this")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return view.Errorf("qdrant.timeout", "%s did not answer in time", where).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	// A TLS alert where a response line belongs: plain HTTP to a Qdrant that
	// speaks only TLS, which a forward carries unless the profile says its far
	// end does. Read as the failure it quoted, it was "could not reach" a
	// server that had answered, in bytes, with the page of every input for a
	// hint. Only for a call that spoke plain HTTP: over TLS no alert reaches
	// the client as a response line.
	if !tlsOn(req) && plugin.TLSExpected(err) {
		return tlsExpected(req)
	}
	return view.Errorf("qdrant.conn.failed", "could not reach %s: %v", where, err).
		WithHint(sf.SettingsHint("qdrant.overview"))
}

// forwardName is the refusal for a certificate checked for the end of a
// forward the host opened — 127.0.0.1 — and not for the name the instance
// answers as, which the certificate names instead.
func forwardName(req plugin.Request, hostErr x509.HostnameError) *view.Error {
	return view.Errorf("qdrant.tls.forward", "the certificate behind %s is for %s, not for %s, "+
		"where the forward ends", req.Reached(req.String("endpoint")), plugin.CertNames(hostErr.Certificate), hostErr.Host).
		WithHint("a forward always ends at 127.0.0.1, so the certificate is checked for the name the instance " +
			"answers as instead: " + req.Surface().SettingName("tls-server-name") + ", which the profile can " +
			"hold beside its forward, names it — one the certificate is for — and it is checked as strictly " +
			"as the host it replaces")
}

func hostOnly(endpoint string) string {
	host, _, err := stdnet.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}
	return host
}

// pathFor escapes a collection name into a URL path. A name is caller-supplied
// and Qdrant accepts a wide range of them, so a raw concatenation is a path
// traversal waiting to happen: a name of "../cluster" would reach a different
// endpoint entirely.
func pathFor(format string, name string) string {
	return fmt.Sprintf(format, url.PathEscape(name))
}

// malformed is the answer when whatever is at that endpoint is not Qdrant. The
// hint names the likeliest cause rather than the likeliest-sounding one: 6334
// is the gRPC port, it is one character away from 6333, and it accepts the
// connection before failing to answer.
func malformed(req plugin.Request) *view.Error {
	return view.Errorf("qdrant.response.malformed", "%s did not answer with JSON", req.Reached(req.String("endpoint"))).
		WithHint("Qdrant serves REST on 6333 and gRPC on 6334 — the gRPC port will not answer this")
}
