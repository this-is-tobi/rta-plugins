package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// connFields are the inputs every capability here shares — the same three
// things `vault` itself needs told (VAULT_ADDR, VAULT_NAMESPACE, VAULT_TOKEN),
// resolved explicitly through plugin.Request rather than left to the
// client library's own environment-variable defaults, so a handler's
// behavior never depends on what happens to be exported in the process it
// runs in. The same shape plugins/pg's connFields documents.
//
// Every field here is Local, and that is a security property rather than a
// detail — the same reasoning plugins/pg's own connFields documents at
// length. `address` names which Vault this call reaches and
// `namespace` names which tenant inside it; an MCP caller may not choose
// either, or an agent could point rta at a Vault it controls and have the
// host supply $RTA_VAULT_TOKEN beside it. Both remain Config-backed and
// remain ordinary flags for a person at a terminal. The token differs only
// in also declaring EnvFallback, which is for values that genuinely are
// credentials.
func connFields() []plugin.Field {
	return []plugin.Field{
		// One input holding a whole URL, which is the url role — the third
		// shape, and the other reason the roles are shapes rather than names.
		{Name: "address", Type: plugin.String, Default: "http://127.0.0.1:8200", Config: "address",
			Local: true, Endpoint: plugin.EndpointURL, Help: "Vault server address"},
		{Name: "namespace", Type: plugin.String, Default: "", Config: "namespace",
			Local: true, Help: "Vault Enterprise namespace — empty for OSS or the root namespace"},
		{Name: "token", Type: plugin.Secret, Local: true, EnvFallback: true,
			Help: "Vault token"},
		// Local for the same reason plugins/etcd's own ca-file is: it is read
		// off this machine's disk, and an input naming a file that the host
		// then opens is a file-read primitive the moment a caller can choose
		// the path. Named to match etcd's field rather than inventing a
		// second word for the same thing — an operator who has configured one
		// already knows this one. Matters most for a `kube:`/`ssh:`
		// connection with `tls: true`: the server's own certificate is
		// commonly signed by a cluster-internal CA (a Vault operator's
		// generated root, cert-manager's cluster issuer) the host's trust
		// store does not carry, and address alone reaching https:// does not
		// change what rta is willing to trust.
		{Name: "ca-file", Type: plugin.String, Default: "", Config: "ca-file",
			Local: true, Help: "PEM bundle to verify the server against, beyond the host's own trust store"},
		// The name the certificate is checked for when it is not the host in
		// address — above all through a kube: or ssh: forward, whose end is
		// 127.0.0.1 whatever the Vault is called, and which its certificate
		// names only by luck. Checked as strictly as the host would have been:
		// it moves the check, never loosens it. Local for the reason address
		// is: what a certificate has to prove is the operator's to say.
		{Name: "tls-server-name", Type: plugin.String, Default: "", Config: "tls-server-name",
			Local: true, Help: "name to check the server's certificate for, in place of the address's host"},
	}
}

// connect builds a client from the resolved inputs. Vault's own client
// constructor also reads VAULT_ADDR/VAULT_TOKEN/VAULT_NAMESPACE from the
// process environment as its own defaults — harmless here since every field
// this sets is overwritten immediately after, but worth naming: nothing this
// plugin does depends on that fallback, on purpose.
func connect(req plugin.Request) (*vaultapi.Client, *view.Error) {
	cfg := vaultapi.DefaultConfig()
	if cfg.Error != nil {
		return nil, classify(cfg.Error, req)
	}
	cfg.Address = req.String("address")
	// One attempt. The client retries a refused connection or a 5xx twice,
	// a second or more apart, so a call to a Vault that was down answered
	// four seconds late with the refusal it would have had at once, and the
	// overview, which reads twice, took eight. Every call here is one an
	// agent or an operator can simply make again, and a write that a 5xx
	// answered is not one to repeat on their behalf.
	cfg.MaxRetries = 0
	// DefaultConfig's own ReadEnvironment already pulled in whatever
	// VAULT_CACERT/VAULT_CLIENT_CERT/VAULT_SKIP_VERIFY/etc. happen to be set
	// in this shell — harmless for Address, overwritten the line above, but
	// not for TLS trust: an operator who also uses the vault CLI directly
	// could have VAULT_SKIP_VERIFY=true exported for an unrelated reason and
	// find rta silently stopped verifying certificates, with nothing here
	// saying so. Reset to a clean baseline (DefaultConfig's own
	// MinVersion, nothing more) so the only things able to affect trust past
	// this line are the host's own trust store and ca-file below — the one
	// surface this plugin actually documents.
	if transport, ok := cfg.HttpClient.Transport.(*http.Transport); ok {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	// The path with a leading ~ resolved, as every other path a plugin reads
	// is. Opened as typed, ~/ca.pem was a path under a directory named ~, and
	// a CA sitting in the operator's home was answered as no such file.
	//
	// tls-server-name is the handshake's ServerName and the SNI it sends, in
	// place of the address's host. Refused over plain HTTP rather than
	// ignored: a name given is an operator expecting a certificate to be
	// checked, and the call would have gone in the clear with nothing said.
	name := serverName(req)
	if name != "" && !strings.HasPrefix(strings.ToLower(req.String("address")), "https://") {
		return nil, plaintextServerName(req)
	}
	if ca := plugin.ExpandHome(req.String("ca-file")); ca != "" || name != "" {
		if err := cfg.ConfigureTLS(&vaultapi.TLSConfig{CACert: ca, TLSServerName: name}); err != nil {
			return nil, view.Errorf("vault.tls.ca.invalid", "%v", err).
				WithHint(req.Surface().SettingName("ca-file") + " is a path on this machine, read by rta rather than by Vault")
		}
	}
	client, err := vaultapi.NewClient(cfg)
	if err != nil {
		return nil, classify(err, req)
	}
	client.SetToken(req.String("token"))
	if ns := req.String("namespace"); ns != "" {
		client.SetNamespace(ns)
	}
	return client, nil
}

// serverName is the name the certificate is checked for in place of the
// address's host, or "" for the host.
func serverName(req plugin.Request) string { return strings.TrimSpace(req.String("tls-server-name")) }

// plaintextServerName is the refusal for a certificate's name given to a
// call that would check no certificate: an address that is not https://.
// Through a forward that is the host's doing — it fills address with
// http:// unless the connection says its far end speaks TLS — so the way out
// is named there, not in an address the forward fills.
func plaintextServerName(req plugin.Request) *view.Error {
	sf := req.Surface()
	refusal := view.Errorf("vault.tls.plaintext", "%s names a certificate to check, and this call would "+
		"reach %s over plain HTTP", sf.SettingName("tls-server-name"), reached(req))
	if req.Tunnel() != plugin.TunnelNone {
		return refusal.WithHint("a forward carries plain http:// unless the profile's connection says its far end " +
			"speaks TLS, as Vault's own listener does: tunnelTLS: true on that connection")
	}
	return refusal.WithHint("an https:// address is what makes the call TLS, and " + sf.SettingName("tls-server-name") +
		" the name its certificate is checked for")
}

// classify turns a client error into something an operator can act on — the
// same job plugins/pg's classify does for a driver error, against Vault's
// own error shapes instead of PostgreSQL's.
func classify(err error, req plugin.Request) *view.Error {
	addr, sf := req.String("address"), req.Surface()
	// The Vault as its reader reaches it again, for a refusal the Vault
	// itself gave: through a forward the address is 127.0.0.1 and a port that
	// closed with the call, and the token it refused was the profile's, which
	// is the one thing the reader can change.
	answered := reached(req)

	var respErr *vaultapi.ResponseError
	if errors.As(err, &respErr) {
		switch respErr.StatusCode {
		case 403:
			return view.Errorf("vault.denied", "%s refused: %s", answered, joinErrors(respErr)).
				WithHint("the token's policy does not allow this, or the token itself is invalid — " +
					nextCall(req, "vault.token.status") + " shows what the current token can do")
		case 404:
			return view.Errorf("vault.notfound", "nothing at that path on %s", answered).
				WithHint("check the path and the mount — a KV v2 mount is not always named \"secret\"")
		case 400:
			// Go's own answer, from the listener rather than from Vault, to a
			// plain-HTTP request on a TLS port: the forward a profile opens
			// carries http:// unless its connection says otherwise, and Vault's
			// listener has no plaintext to fall back to. Read as Vault refusing,
			// it named nothing the operator could change.
			if plugin.TLSExpected(errors.New(joinErrors(respErr))) {
				return tlsExpected(req)
			}
			return view.Errorf("vault.badrequest", "%s rejected the request: %s", answered, joinErrors(respErr)).
				WithHint("this is Vault refusing, not rta")
		case 412:
			return view.Errorf("vault.sealed", "%s is sealed or not yet initialized", answered).
				WithHint(nextCall(req, "vault.seal.status") + " shows which")
		}
		return view.Errorf("vault.request.failed", "%s: %s", answered, joinErrors(respErr)).
			WithHint(fmt.Sprintf("HTTP %d", respErr.StatusCode))
	}

	if errors.Is(err, vaultapi.ErrSecretNotFound) {
		return view.Errorf("vault.notfound", "nothing at that path on %s", answered).
			WithHint("check the path and the mount — a KV v2 mount is not always named \"secret\"")
	}

	// The name first: a dial that could not resolve its host fails with a
	// *net.OpError wrapping the *net.DNSError, and read the other way round
	// every name nothing resolves was reported as a port nothing listens on.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		host := hostOf(addr)
		return view.Errorf("vault.host.unknown", "no address for %q", host).
			WithHint(sf.DNSHint(host))
	}
	// The certificate before the dial. A verdict on one is typed, so read
	// first it answers for nothing else; the dial's questions
	// (plugin.DialUnroutable, plugin.DialRefused) read an error's words when
	// it carries no errno, as no verdict does, and a verdict's words hold the
	// certificate's own names, which are the server's to choose. Read after
	// them, a certificate valid for "connection refused", or on macOS a
	// revoked one named so, was nothing listening, on a port that had
	// answered with a certificate.
	//
	// Asked of plugin.CertUntrusted rather than of the type Go's verifier
	// alone gives: with no ca-file, macOS answers a private CA's chain
	// untyped, and it was "could not reach". And only for a verdict that
	// means an issuer nothing here vouches for — a revoked certificate is
	// answered untyped too, and the CA file is no cure for it but a way
	// around the check that caught it.
	if plugin.CertUntrusted(err) {
		return view.Errorf("vault.tls.untrusted", "%s presented a certificate rta does not trust", reached(req)).
			WithHint("this is a real TLS trust failure, not something to work around here — a Vault " +
				"behind a tunnel commonly has its own operator- or cluster-generated CA, wanted rather than " +
				"verification turned off: " + sf.CAHint("ca-file"))
	}
	// Every other verdict is its own reason, quoted in the verifier's words —
	// the system's, for one macOS gives untyped — and never "could not reach":
	// the server was reached, and answered with a certificate.
	//
	// A certificate that is not for the end of a forward the host opened is
	// no fault of the Vault's, and not one the address can fix: through a
	// forward the host fills the address with 127.0.0.1 and a port of its
	// own, which a Vault's certificate names only by luck. So the refusal
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
		checked := "the host in " + sf.SettingName("address")
		if serverName(req) != "" {
			checked = "the name in " + sf.SettingName("tls-server-name")
		}
		rejected := view.Errorf("vault.tls.rejected", "%s presented a certificate that does not verify: %v", reached(req), verifyErr.Err)
		// A rule of macOS's own, which the verdict's words do not name: a
		// ten-year certificate, the usual one for a Vault of one's own, is
		// "not standards compliant" there, and the hint below would have
		// sent its reader to check dates that were fine.
		if hint := plugin.CertPolicyHint(err); hint != "" {
			return rejected.WithHint(hint)
		}
		return rejected.WithHint("a certificate is checked for " + checked +
			", its dates and the use it was issued for, as well as for who issued it")
	}
	// Short of the host, before the port: a dial that found no way there
	// reached nothing that could refuse it, and read as refused, a Vault
	// behind a VPN that is down, or at an address of another network's, was
	// "nothing is listening" about a port no packet reached.
	//
	// Each read by the operating system's own error (plugin.DialUnroutable,
	// plugin.DialRefused), never by the *net.OpError around it, which every
	// failed dial is: read that way, a dial that was reset was "nothing is
	// listening" too, and one that timed out never reached the timeout below.
	if plugin.DialUnroutable(err) {
		reason := err
		var netErr *net.OpError
		if errors.As(err, &netErr) {
			reason = netErr.Err
		}
		return view.Errorf("vault.conn.unreachable", "%s cannot be reached from this machine: %v", addr, reason).
			WithHint("no route leads there from here — a VPN or tunnel the server sits behind that is " +
				"down looks exactly like this, and so does " + sf.SettingName("address") + " naming " +
				"an address on a network this machine is not on")
	}
	if plugin.DialRefused(err) {
		return view.Errorf("vault.conn.refused", "nothing is listening on %s", addr).
			WithHint("is the server up, and is " + sf.SettingName("address") + " right?")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return view.Errorf("vault.conn.timeout", "%s did not answer in time", addr).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	return view.Errorf("vault.conn.failed", "could not reach %s: %v", addr, err).
		WithHint(sf.SettingsHint("vault.seal.status"))
}

// tlsExpected is the refusal for a plain-HTTP call to a port that speaks
// only TLS, which Vault's listener does.
func tlsExpected(req plugin.Request) *view.Error {
	refusal := view.Errorf("vault.tls.expected", "this call spoke plain HTTP to %s, which speaks only HTTPS",
		reached(req))
	if req.Tunnel() != plugin.TunnelNone {
		return refusal.WithHint("a forward carries plain http:// unless the profile's connection says its far end " +
			"speaks TLS, as Vault's own listener does: tunnelTLS: true on that connection")
	}
	return refusal.WithHint("an https:// address is what makes the call TLS: " + req.Surface().SettingName("address") +
		" names the scheme")
}

// forwardName is the refusal for a certificate checked for the end of a
// forward the host opened — 127.0.0.1 — and not for the name the Vault
// answers as, which the certificate names instead.
func forwardName(req plugin.Request, hostErr x509.HostnameError) *view.Error {
	return view.Errorf("vault.tls.forward", "the certificate behind %s is for %s, not for %s, "+
		"where the forward ends", reached(req), plugin.CertNames(hostErr.Certificate), hostErr.Host).
		WithHint("a forward always ends at 127.0.0.1, so the certificate is checked for the name the Vault " +
			"answers as instead: " + req.Surface().SettingName("tls-server-name") + ", which the profile can " +
			"hold beside its forward, names it — one the certificate is for — and it is checked as strictly " +
			"as the host it replaces")
}

// reachArgs points a call this one hands its reader at the Vault it reached:
// the profile it came through whenever there was one, since the token it used
// may be the profile's and no other layer holds it, and the address only when
// the host opened no forward (Request.ReachArgs) — through one, the address
// was 127.0.0.1 and a port that closed with the call, and the profile is what
// reaches the same Vault again. Reached directly the address stays, since it
// may be one typed over the profile's, and with it the namespace, which
// decides what the token was asked about, and the CA file and the name the
// certificate was checked for. Never the token. Over MCP the call gives the
// profile alone: the rest are Local, and the bridge drops one an agent sends.
//
// **Without them, the status a refusal offers reads another Vault.** Pasted,
// "what the current token can do" was asked of whatever the configuration
// there named.
func reachArgs(req plugin.Request) []plugin.Arg {
	if req.Surface() == plugin.SurfaceMCP {
		return req.ReachArgs()
	}
	args := req.ReachArgs(plugin.Arg{Name: "address", Value: req.String("address")})
	for _, name := range []string{"namespace", "ca-file", "tls-server-name"} {
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

// dataHint says how data carries a secret's fields: on the CLI a flag repeated
// once per field, and elsewhere a list with one field per value.
func dataHint(sf plugin.Surface) string {
	if sf == plugin.SurfaceMCP || sf == plugin.SurfaceTUI {
		return "each value in " + sf.InputName("data") + " is one key=value pair, one per field"
	}
	return "each " + sf.InputName("data") + " is one key=value pair, repeated for more than one"
}

// reached names the Vault this call reached the way its reader reaches it
// again once the call is over (Request.Reached): the address is the input
// that names it.
func reached(req plugin.Request) string { return req.Reached(req.String("address")) }

// hostOf is the name in address, the one DNS was asked for: address is a
// URL, and a lookup given the whole of it, scheme and port and all, answers
// for no name anybody has. The address itself when it holds no host to take.
func hostOf(address string) string {
	if u, err := url.Parse(address); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return address
}

// joinErrors renders a ResponseError's Errors slice the way Vault's own CLI
// does — one line, since a view.Error's Message is a line, not a list.
//
// Vault's own errors come in two shapes, and only one was a line. A rejected
// token is answered as one error holding a hashicorp/go-multierror's text —
// "2 errors occurred:", then each reason on a line of its own behind a tab and
// an asterisk — which went into the message with its newlines and tabs, and
// the first thing an agent read of a refused token was a list.
func joinErrors(respErr *vaultapi.ResponseError) string {
	if len(respErr.Errors) == 0 {
		// ResponseError's own text is a paragraph — the request, the URL and
		// the code over several lines — for an answer that gave no reason.
		return fmt.Sprintf("HTTP status %d, with no reason given", respErr.StatusCode)
	}
	var reasons []string
	for _, e := range respErr.Errors {
		if !multiError.MatchString(e) {
			reasons = append(reasons, e)
			continue
		}
		for _, line := range strings.Split(e, "\n")[1:] {
			if reason := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*")); reason != "" {
				reasons = append(reasons, reason)
			}
		}
	}
	return strings.Join(reasons, "; ")
}

// multiError matches the first line of a go-multierror's text.
var multiError = regexp.MustCompile(`^\d+ errors? occurred:\n`)

// dataFields parses a repeated key=value input into a map, the shape every
// KV-writing capability here needs — the same convention `kubectl create
// secret generic --from-literal` uses, chosen because a Vault secret is a
// small document (several fields), not the single value builtin/kv stores,
// and plugin.Field has no map type to ask for one directly.
func dataFields(sf plugin.Surface, pairs []string) (map[string]interface{}, *view.Error) {
	data := make(map[string]interface{}, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, view.Errorf("vault.data.invalid", "%q is not key=value", pair).
				WithHint(dataHint(sf))
		}
		data[key] = value
	}
	return data, nil
}
