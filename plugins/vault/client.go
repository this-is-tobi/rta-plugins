package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
	if ca := plugin.ExpandHome(req.String("ca-file")); ca != "" {
		if err := cfg.ConfigureTLS(&vaultapi.TLSConfig{CACert: ca}); err != nil {
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

// classify turns a client error into something an operator can act on — the
// same job plugins/pg's classify does for a driver error, against Vault's
// own error shapes instead of PostgreSQL's.
func classify(err error, req plugin.Request) *view.Error {
	addr, sf := req.String("address"), req.Surface()

	var respErr *vaultapi.ResponseError
	if errors.As(err, &respErr) {
		switch respErr.StatusCode {
		case 403:
			return view.Errorf("vault.denied", "%s refused: %s", addr, joinErrors(respErr)).
				WithHint("the token's policy does not allow this, or the token itself is invalid — " +
					sf.CapabilityName("vault.token.status") + " shows what the current token can do")
		case 404:
			return view.Errorf("vault.notfound", "nothing at that path on %s", addr).
				WithHint("check the path and the mount — a KV v2 mount is not always named \"secret\"")
		case 400:
			return view.Errorf("vault.badrequest", "%s rejected the request: %s", addr, joinErrors(respErr)).
				WithHint("this is Vault refusing, not rta")
		case 412:
			return view.Errorf("vault.sealed", "%s is sealed or not yet initialized", addr).
				WithHint(sf.CapabilityName("vault.seal.status") + " shows which")
		}
		return view.Errorf("vault.request.failed", "%s: %s", addr, joinErrors(respErr)).
			WithHint(fmt.Sprintf("HTTP %d", respErr.StatusCode))
	}

	if errors.Is(err, vaultapi.ErrSecretNotFound) {
		return view.Errorf("vault.notfound", "nothing at that path on %s", addr).
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
	// Asked of plugin.CertUntrusted rather than of the type Go's verifier
	// alone gives: with no ca-file, macOS answers a private CA's chain
	// untyped, and it was "could not reach". And only for a verdict that
	// means an issuer nothing here vouches for — a revoked certificate is
	// answered untyped too, and the CA file is no cure for it but a way
	// around the check that caught it.
	if plugin.CertUntrusted(err) {
		return view.Errorf("vault.tls.untrusted", "%s presented a certificate rta does not trust", addr).
			WithHint("this is a real TLS trust failure, not something to work around here — a Vault " +
				"behind a tunnel commonly has its own operator- or cluster-generated CA, wanted rather than " +
				"verification turned off: " + sf.CAHint("ca-file"))
	}
	// Every other verdict is its own reason, quoted in the verifier's words —
	// the system's, for one macOS gives untyped — and never "could not reach":
	// the server was reached, and answered with a certificate.
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		return view.Errorf("vault.tls.rejected", "%s presented a certificate that does not verify: %v", addr, verifyErr.Err).
			WithHint("a certificate is checked for the host in " + sf.SettingName("address") +
				", its dates and the use it was issued for, as well as for who issued it")
	}
	return view.Errorf("vault.conn.failed", "could not reach %s: %v", addr, err).
		WithHint(sf.SettingsHint("vault.seal.status"))
}

// dataHint says how data carries a secret's fields: on the CLI a flag repeated
// once per field, and elsewhere a list with one field per value.
func dataHint(sf plugin.Surface) string {
	if sf == plugin.SurfaceMCP || sf == plugin.SurfaceTUI {
		return "each value in " + sf.InputName("data") + " is one key=value pair, one per field"
	}
	return "each " + sf.InputName("data") + " is one key=value pair, repeated for more than one"
}

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
func joinErrors(respErr *vaultapi.ResponseError) string {
	if len(respErr.Errors) == 0 {
		return respErr.Error()
	}
	return strings.Join(respErr.Errors, "; ")
}

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
