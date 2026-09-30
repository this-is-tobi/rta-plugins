package main

import (
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
	"slices"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// connFields are the inputs every capability here shares.
//
// Every one is Local, and that is the security property rather than a
// detail — the same reasoning plugins/pg's connFields documents at length.
// Together they name which Keycloak this call reaches, which realm inside
// it, and as whom; an MCP caller may not choose any of that, or an agent
// could point rta at a Keycloak of its own and have the host supply
// $RTA_KEYCLOAK_CLIENT_SECRET beside it. Config still fills these and a
// person at a terminal still passes them as ordinary flags.
//
// The credential is a client id and secret, never an admin password. The
// plugin acts as that client's service account, so what the secret can
// reach is exactly the realm-management roles the operator granted it
// (view-users, view-clients, view-realm, view-events, view-authorization) —
// and the token it mints lives five minutes by Keycloak's default and is
// minted again on the next call. An admin's own password would answer the
// same questions through the direct access grant, and that grant is one of
// the things keycloak.audit flags: the plugin must not be what it grades.
func connFields() []plugin.Field {
	return []plugin.Field{
		{Name: "url", Type: plugin.String, Default: "http://127.0.0.1:8080", Config: "url",
			Local: true, Endpoint: plugin.EndpointURL, Help: "Keycloak base URL — the part before /realms"},
		{Name: "realm", Type: plugin.String, Default: "master", Config: "realm",
			Local: true, Help: "the realm to read"},
		// A master-realm client with realm-management roles on every realm
		// is the ordinary way one service account audits a whole
		// deployment; a client that lives in the realm it reads is the
		// least-privilege way to audit one. Both are one configuration
		// away: this names where the client lives, and empty means the
		// same realm it reads.
		{Name: "auth-realm", Type: plugin.String, Default: "", Config: "auth-realm",
			Local: true, Help: "the realm the client lives in, when it is not the one being read"},
		{Name: "client-id", Type: plugin.String, Default: "rta", Config: "client-id",
			Local: true, Help: "the confidential client whose service account this acts as"},
		{Name: "client-secret", Type: plugin.Secret, Local: true, EnvFallback: true,
			Help: "that client's secret, exchanged for a short-lived token on every call"},
		// Local for the same reason plugins/qdrant's own ca-file is: a path
		// read off this machine's disk is a file-read primitive the moment
		// a caller can choose it. Not a Secret — a CA certificate is the
		// public half.
		{Name: "ca-file", Type: plugin.String, Default: "", Config: "ca-file",
			Local: true, Help: "PEM bundle to verify the server against, beyond the host's own trust store"},
		// The name the certificate is checked for when it is not the host in
		// url — above all through a kube: or ssh: forward, whose end is
		// 127.0.0.1 whatever the server is called, and which its certificate
		// names only by luck. Checked as strictly as the host would have been:
		// it moves the check, never loosens it. Local for the reason url is:
		// what a certificate has to prove is the operator's to say.
		{Name: "tls-server-name", Type: plugin.String, Default: "", Config: "tls-server-name",
			Local: true, Help: "name to check the server's certificate for, in place of the URL's host"},
	}
}

// requestTimeout bounds every call. The Admin REST API answers each of
// these from its own database; a listing that takes longer than this is a
// realm large enough that the bounded listing inputs are the answer, not
// waiting.
const requestTimeout = 30 * time.Second

// maxResponseBytes bounds one response. A page of a thousand users with
// their attributes is well under this; it is a backstop against a wrong
// URL that answers with something else, not a working limit.
var maxResponseBytes int64 = 16 << 20

// session is one capability run's view of one realm: the base URL, the
// realm every relative path is under, and the token minted for this run.
//
// One token per run rather than per request, and per run rather than
// cached across runs: a capability makes a handful of requests and the
// token outlives them by minutes, while nothing here keeps state between
// calls to begin with — call is per-request end to end, like every plugin
// in this repository. The cost is one extra round trip per capability, the
// same price kube pays for a TokenRequest, and what it buys is that no
// long-lived credential ever exists anywhere but the operator's own store.
type session struct {
	req   plugin.Request
	base  string
	realm string
	http  *http.Client
	token string
}

// connect validates the inputs and mints the run's token.
func connect(ctx context.Context, req plugin.Request) (*session, *view.Error) {
	base := strings.TrimRight(req.String("url"), "/")
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, view.Errorf("keycloak.url.invalid", "%q is not a Keycloak base URL", req.String("url")).
			WithHint("http(s)://host[:port], the part of the address before /realms")
	}
	// A name given is an operator expecting a certificate to be checked, and
	// over http:// the call would have gone in the clear with nothing said.
	if serverName(req) != "" && parsed.Scheme != "https" {
		return nil, plaintextServerName(req, base)
	}
	realm := req.String("realm")
	if realm == "" {
		return nil, view.Errorf("keycloak.realm.missing", "no realm named").
			WithHint(req.Surface().SettingName("realm") + ", or `realm:` under this plugin's section in rta's config")
	}
	// Where the secret comes from rather than a verb telling the reader to
	// set one: an agent has no host environment to set, and no secret
	// argument either, since the bridge drops a Local input given.
	secret := req.String("client-secret")
	if secret == "" {
		return nil, view.Errorf("keycloak.secret.missing", "no client secret").
			WithHint("the secret is read from " + secretSources(req))
	}
	client, verr := httpClient(req)
	if verr != nil {
		return nil, verr
	}
	s := &session{req: req, base: base, realm: realm, http: client}

	authRealm := req.String("auth-realm")
	if authRealm == "" {
		authRealm = realm
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {req.String("client-id")},
		"client_secret": {secret},
	}
	tokenURL := base + "/realms/" + url.PathEscape(authRealm) + "/protocol/openid-connect/token"
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if verr := s.post(ctx, tokenURL, form, &issued, s.classifyToken); verr != nil {
		return nil, verr
	}
	if issued.AccessToken == "" {
		return nil, view.Errorf("keycloak.token.empty", "%s issued no access token", s.base).
			WithHint("the answer had the shape of a token response without a token in it")
	}
	s.token = issued.AccessToken
	return s, nil
}

// withSession is the shape every capability here has: connect, or return
// the classified error; run.
func withSession(ctx context.Context, req plugin.Request,
	fn func(context.Context, *session) (view.View, error)) (view.View, error) {
	s, verr := connect(ctx, req)
	if verr != nil {
		return nil, verr
	}
	return fn(ctx, s)
}

// get reads one Admin REST resource under this session's realm. path is
// relative to /admin/realms/{realm}/; every dynamic segment in it has to
// arrive already escaped (see segment) because a username or a client id is
// caller-supplied and a raw concatenation is a path traversal.
func (s *session) get(ctx context.Context, path string, query url.Values, out any) *view.Error {
	u := s.base + "/admin/realms/" + url.PathEscape(s.realm)
	if path != "" {
		u += "/" + path
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return s.call(ctx, http.MethodGet, u, nil, "", out, s.classifyStatus)
}

// getServer reads one resource outside any realm — /admin/serverinfo is the
// only one this plugin wants.
func (s *session) getServer(ctx context.Context, path string, out any) *view.Error {
	return s.call(ctx, http.MethodGet, s.base+"/admin/"+path, nil, "", out, s.classifyStatus)
}

func (s *session) post(ctx context.Context, u string, form url.Values, out any,
	classify func(int, []byte) *view.Error) *view.Error {
	return s.call(ctx, http.MethodPost, u, strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded", out, classify)
}

func (s *session) call(ctx context.Context, method, u string, body io.Reader, contentType string,
	out any, classify func(int, []byte) *view.Error) *view.Error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return view.Errorf("keycloak.url.invalid", "%v", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	if s.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.http.Do(httpReq)
	if err != nil {
		return s.classifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded read: whatever is at that URL decides how much to send, and
	// an unbounded ReadAll is how a wrong URL becomes an out-of-memory.
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return s.classifyTransport(err)
	}
	if resp.StatusCode >= 300 {
		return classify(resp.StatusCode, payload)
	}
	if out == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return view.Errorf("keycloak.response.malformed", "%s did not answer with the JSON expected", s.base).
			WithHint("is this the Keycloak base URL, and not a realm's or the account console's?")
	}
	return nil
}

// httpClient is http.DefaultClient unless ca-file names a CA to trust
// beyond this machine's own store — read fresh on every call like every
// other input, because nothing here outlives a run.
//
// The path with a leading ~ resolved, as every other path a plugin reads
// is. Opened as typed, ~/ca.pem was a path under a directory named ~, and a
// CA sitting in the operator's home was answered as no such file.
//
// And unless tls-server-name names what the certificate is checked for in
// place of the URL's host: the handshake's ServerName, and the SNI it sends.
func httpClient(req plugin.Request) (*http.Client, *view.Error) {
	ca := plugin.ExpandHome(req.String("ca-file"))
	if ca == "" && serverName(req) == "" {
		return http.DefaultClient, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName(req)}
	if ca == "" {
		return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}, nil
	}
	sf := req.Surface()
	pem, err := os.ReadFile(ca)
	if err != nil {
		return nil, view.Errorf("keycloak.tls.ca.unreadable", "%v", err).
			WithHint(sf.SettingName("ca-file") + " is a path on this machine, read by rta rather than by the server")
	}
	pool := x509.NewCertPool()
	// What the file has to hold, rather than a guess at what it held instead.
	// The hint once said "not the server's own certificate", and a
	// self-signed server's own certificate is exactly what belongs here — the
	// untrusted-certificate hint in classifyTransport sends the reader to put
	// it here — while one in PEM never reaches this line at all: only a file
	// with no PEM certificate in it does, a private key or a DER-encoded
	// certificate.
	if !pool.AppendCertsFromPEM(pem) {
		return nil, view.Errorf("keycloak.tls.ca.invalid", "%s holds no PEM certificate", ca).
			WithHint(sf.SettingName("ca-file") + " wants a PEM certificate — the CA's, or a self-signed " +
				"server's own — and a private key or a DER-encoded certificate is not one")
	}
	cfg.RootCAs = pool
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}, nil
}

// serverName is the name the certificate is checked for in place of the
// URL's host, or "" for the host.
func serverName(req plugin.Request) string { return strings.TrimSpace(req.String("tls-server-name")) }

// plaintextServerName is the refusal for a certificate's name given to a
// call that would check no certificate: a URL that is not https://. Through
// a forward that is the host's doing — it fills url with http:// unless the
// connection says its far end speaks TLS — so the way out is named there,
// not in a URL the forward fills.
func plaintextServerName(req plugin.Request, base string) *view.Error {
	sf := req.Surface()
	where := base
	if req.Tunnel() != plugin.TunnelNone {
		where = "profile " + req.Profile() + ", through its " + string(req.Tunnel()) + ": forward"
	}
	refusal := view.Errorf("keycloak.tls.plaintext", "%s names a certificate to check, and this call would "+
		"reach Keycloak over plain HTTP (%s)", sf.SettingName("tls-server-name"), where)
	if req.Tunnel() != plugin.TunnelNone {
		return refusal.WithHint("a forward carries plain http:// unless the profile's connection says its far end " +
			"speaks TLS: tunnelTLS: true on that connection, for a Keycloak serving HTTPS on the port it forwards")
	}
	return refusal.WithHint("an https:// URL is what makes the call TLS, and " + sf.SettingName("tls-server-name") +
		" the name its certificate is checked for")
}

// secretSources names where this call's client secret is read from, for a
// refusal that has to say where one goes: the variable the host reads, the
// setting, and the store a profile's secrets: maps from.
//
// **The variable is the profile's own while a profile is in play.** The host
// reads RTA_PROFILE_<PROFILE>_CLIENT_SECRET for one (plugin.ProfileEnvVar),
// and $RTA_KEYCLOAK_CLIENT_SECRET not at all — plugin.Resolve switches the
// namespace-wide layer off whenever a profile is active — so a refusal that
// named the latter sent the reader to export a variable this call would never
// read. A labeled instance, profile/label, has no variable: the host derives
// none for one, since a spelling like that could be forged by naming another
// profile carefully, and its credential comes from secrets: alone.
func secretSources(req plugin.Request) string {
	sf := req.Surface()
	profile := req.Profile()
	switch {
	case profile == "":
		return "$" + plugin.LocalEnvVar("keycloak.overview", "client-secret") + " or " +
			sf.SettingName("client-secret") + ", or mapped from the store in a profile's secrets:"
	case strings.Contains(profile, "/"):
		return sf.SettingName("client-secret") + ", or mapped from the store in profile " + profile + "'s secrets:"
	}
	return "$" + plugin.ProfileEnvVar(profile, "client-secret") + " or " + sf.SettingName("client-secret") +
		", or mapped from the store in profile " + profile + "'s secrets:"
}

// segment escapes one caller-supplied value into a path segment.
func segment(v string) string { return url.PathEscape(v) }

// classifyToken turns the token endpoint's refusal into something an
// operator can act on. The endpoint speaks OAuth's error vocabulary
// ({"error":"unauthorized_client"}) rather than the Admin API's, and the
// two failures worth telling apart are "wrong secret" and "wrong realm" —
// both are the operator's configuration, and they are fixed in different
// places.
func (s *session) classifyToken(code int, body []byte) *view.Error {
	oauthErr, desc := oauthError(body)
	sf := s.req.Surface()
	switch {
	case code == http.StatusNotFound:
		return view.Errorf("keycloak.realm.unknown", "%s has no realm to authenticate against: %s", s.base, firstOf(desc, oauthErr)).
			WithHint(sf.SettingName("auth-realm") + " (or " + sf.SettingName("realm") + ") names the realm the client " +
				"lives in; the issuer URL's last segment is its name")
	case code == http.StatusUnauthorized, code == http.StatusBadRequest && oauthErr == "invalid_client":
		return view.Errorf("keycloak.auth.failed", "%s refused the client credentials: %s", s.base, firstOf(desc, oauthErr)).
			WithHint(sf.SettingName("client-id") + " names a confidential client with service accounts enabled, " +
				"and its secret is read from " + secretSources(s.req))
	case code == http.StatusBadRequest && oauthErr == "unauthorized_client":
		return view.Errorf("keycloak.auth.grant", "%s: %s", s.base, firstOf(desc, oauthErr)).
			WithHint("the client needs \"Service accounts roles\" (client credentials grant) enabled")
	}
	return view.Errorf("keycloak.auth.failed", "%s returned %d from the token endpoint: %s", s.base, code, firstOf(desc, oauthErr, "no detail given"))
}

// classifyStatus turns an Admin REST refusal into something an operator can
// act on. A 403 is by far the common one and it always means the same thing
// here: the service account lacks a realm-management role the read needs.
func (s *session) classifyStatus(code int, body []byte) *view.Error {
	detail := adminError(body)
	switch code {
	case http.StatusUnauthorized:
		return view.Errorf("keycloak.token.rejected", "%s refused the token it just issued", s.base).
			WithHint("a clock far off the server's makes a fresh token look expired")
	case http.StatusForbidden:
		return view.Errorf("keycloak.denied", "the service account may not read this in realm %q", s.realm).
			WithHint("grant it the realm-management roles view-users, view-clients, view-realm, " +
				"view-events and view-authorization — and, for a master-realm client, the same roles " +
				"of the realm being read")
	case http.StatusNotFound:
		return view.Errorf("keycloak.notfound", "%s", detail).
			WithHint("realm " + s.realm + " on " + s.base)
	}
	return view.Errorf("keycloak.request.failed", "%s returned %d: %s", s.base, code, detail).
		WithHint(s.req.Surface().SettingsHint("keycloak.overview"))
}

// classifyTransport turns a failure to reach the server at all into
// something an operator can act on.
func (s *session) classifyTransport(err error) *view.Error {
	var already *view.Error
	if errors.As(err, &already) {
		return already
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return view.Errorf("keycloak.timeout", "%s did not answer in time", s.base).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	// The name first: a dial that could not resolve its host fails with a
	// *net.OpError wrapping the *net.DNSError, and read the other way round
	// every name nothing resolves was reported as a port nothing listens on.
	var dnsErr *stdnet.DNSError
	if errors.As(err, &dnsErr) {
		return view.Errorf("keycloak.host.unknown", "no address for %q", s.host()).
			WithHint(s.req.Surface().DNSHint(s.host()))
	}
	sf := s.req.Surface()
	// The certificate before the dial. A verdict on one is typed, so read
	// first it answers for nothing else; the dial's questions
	// (plugin.DialUnroutable, plugin.DialRefused) read an error's words when
	// it carries no errno, as no verdict does, and a verdict's words hold the
	// certificate's own names, which are the server's to choose. Read after
	// them, a certificate valid for "connection refused", or on macOS a
	// revoked one named so, was nothing listening, on a port that had
	// answered with a certificate.
	//
	// The CA named as where it belongs, not as something to pass: over MCP
	// ca-file is the operator's setting, Local, and an agent told to pass it
	// has no such argument to give and would read this refusal again. Only for
	// a verdict that means an issuer nothing here vouches for
	// (plugin.CertUntrusted): macOS answers a revoked certificate untyped too,
	// and read as untrusted it was answered with the CA file to name — which
	// runs Go's verifier in the system's place, with no revocation check, and
	// connects.
	if plugin.CertUntrusted(err) {
		return view.Errorf("keycloak.tls.untrusted", "%s presented a certificate nothing here trusts", s.base).
			WithHint("a Keycloak behind an internal CA wants that CA rather than verification turned off: " +
				sf.CAHint("ca-file"))
	}
	// Every other verdict is its own reason, quoted in the verifier's words —
	// the system's, for one macOS gives untyped — and never "could not reach":
	// the server was reached, and answered with a certificate.
	//
	// A certificate that is not for the end of a forward the host opened is
	// no fault of the server's, and not one the URL can fix: through a
	// forward the host fills the URL with 127.0.0.1 and a port of its own,
	// which a server's certificate names only by luck. So the refusal names
	// the forward and the name the certificate is for, and sends the reader
	// to tls-server-name, which checks that name in 127.0.0.1's place — never
	// to anything that checks less. Only when tls-server-name is not set: a
	// name given and not matched is the certificate's to explain.
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) && s.req.Tunnel() != plugin.TunnelNone && serverName(s.req) == "" {
		return forwardName(s.req, hostErr)
	}
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		checked := "the host in " + sf.SettingName("url")
		if serverName(s.req) != "" {
			checked = "the name in " + sf.SettingName("tls-server-name")
		}
		return view.Errorf("keycloak.tls.rejected", "%s presented a certificate that does not verify: %v", s.base, verifyErr.Err).
			WithHint("a certificate is checked for " + checked +
				", its dates and the use it was issued for, as well as for who issued it")
	}
	// Short of the host, before the port: a dial that found no way there
	// reached nothing that could refuse it, and read as refused, a Keycloak
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
		return view.Errorf("keycloak.conn.unreachable", "%s cannot be reached from this machine: %v", s.base, reason).
			WithHint("no route leads there from here — a VPN or tunnel the server sits behind that is " +
				"down looks exactly like this, and so does " + sf.SettingName("url") + " naming " +
				"an address on a network this machine is not on")
	}
	if plugin.DialRefused(err) {
		return view.Errorf("keycloak.conn.refused", "nothing is listening on %s", s.base).
			WithHint("is the server up, and is " + sf.SettingName("url") + " right?")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return view.Errorf("keycloak.timeout", "%s did not answer in time", s.base).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	failed := view.Errorf("keycloak.conn.failed", "could not reach %s: %v", s.base, err)
	// A hang-up with no answer, to an http:// URL: Keycloak's HTTPS listener
	// closes a plain-HTTP connection this way, with no alert and no 400 to
	// tell it by, and a forward carries plain HTTP unless the profile's
	// connection says its far end speaks TLS. EOF says no more than that the
	// server hung up, so it stays conn.failed, and the hint names the likelier
	// reason and where it is changed rather than the page of every input.
	if parsed, perr := url.Parse(s.base); perr == nil && parsed.Scheme == "http" && errors.Is(err, io.EOF) {
		if s.req.Tunnel() != plugin.TunnelNone {
			return failed.WithHint("a Keycloak serving only HTTPS hangs up on plain HTTP like this, and a forward " +
				"carries plain HTTP unless the profile's connection says its far end speaks TLS: tunnelTLS: true " +
				"on that connection")
		}
		return failed.WithHint("a Keycloak serving only HTTPS on that port hangs up on plain HTTP like this — " +
			"an https:// URL is what makes the call TLS: " + sf.SettingName("url") + " names the scheme")
	}
	return failed.WithHint(sf.SettingsHint("keycloak.overview"))
}

// forwardName is the refusal for a certificate checked for the end of a
// forward the host opened — 127.0.0.1 — and not for the name the server
// answers as, which the certificate names instead.
func forwardName(req plugin.Request, hostErr x509.HostnameError) *view.Error {
	return view.Errorf("keycloak.tls.forward", "the certificate behind profile %s's %s: forward is for %s, not for %s, "+
		"where the forward ends", req.Profile(), req.Tunnel(), certNames(hostErr.Certificate), hostErr.Host).
		WithHint("a forward always ends at 127.0.0.1, so the certificate is checked for the name the server " +
			"answers as instead: " + req.Surface().SettingName("tls-server-name") + ", which the profile can " +
			"hold beside its forward, names it — one the certificate is for — and it is checked as strictly " +
			"as the host it replaces")
}

// certNames lists the names a certificate is for, the ones a check reads:
// its DNS names and its IP addresses, never the subject's common name, which
// Go's verifier ignores.
func certNames(cert *x509.Certificate) string {
	if cert == nil {
		return "another name"
	}
	names := slices.Clone(cert.DNSNames)
	for _, ip := range cert.IPAddresses {
		names = append(names, ip.String())
	}
	switch {
	case len(names) == 0:
		return "no name a check reads"
	case len(names) > 3:
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" and %d more", len(names)-3)
	}
	return strings.Join(names, ", ")
}

func (s *session) host() string {
	parsed, err := url.Parse(s.base)
	if err != nil {
		return s.base
	}
	return parsed.Hostname()
}

// adminError digs the message out of the Admin API's error body — it is
// {"error": "..."} sometimes with an "errorMessage" beside it — falling back
// to a bounded slice of the raw body.
func adminError(body []byte) string {
	var envelope struct {
		Error        string `json:"error"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if msg := firstOf(envelope.ErrorMessage, envelope.Error); msg != "" {
			return truncate(msg, 300)
		}
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "no detail given"
	}
	return truncate(text, 300)
}

func oauthError(body []byte) (code, description string) {
	var envelope struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &envelope)
	return envelope.Error, envelope.Description
}

func firstOf(candidates ...string) string {
	for _, c := range candidates {
		if c != "" {
			return c
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
