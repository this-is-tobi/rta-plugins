package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
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
	realm := req.String("realm")
	if realm == "" {
		return nil, view.Errorf("keycloak.realm.missing", "no realm named").
			WithHint("--realm, or `realm:` under this plugin's section in rta's config")
	}
	secret := req.String("client-secret")
	if secret == "" {
		return nil, view.Errorf("keycloak.secret.missing", "no client secret").
			WithHint("set $" + plugin.LocalEnvVar("keycloak.overview", "client-secret") +
				", or map it from the store in a profile's secrets:")
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
func httpClient(req plugin.Request) (*http.Client, *view.Error) {
	ca := req.String("ca-file")
	if ca == "" {
		return http.DefaultClient, nil
	}
	pem, err := os.ReadFile(ca)
	if err != nil {
		return nil, view.Errorf("keycloak.tls.ca.unreadable", "%v", err).
			WithHint("ca-file is a path on this machine, read by rta rather than by the server")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, view.Errorf("keycloak.tls.ca.invalid", "%s holds no PEM certificate", ca).
			WithHint("this wants the CA bundle, not the server's own certificate")
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}, nil
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
	switch {
	case code == http.StatusNotFound:
		return view.Errorf("keycloak.realm.unknown", "%s has no realm to authenticate against: %s", s.base, firstOf(desc, oauthErr)).
			WithHint("auth-realm (or realm) names the realm the client lives in; the issuer URL's last segment is its name")
	case code == http.StatusUnauthorized, code == http.StatusBadRequest && oauthErr == "invalid_client":
		return view.Errorf("keycloak.auth.failed", "%s refused the client credentials: %s", s.base, firstOf(desc, oauthErr)).
			WithHint("client-id names a confidential client with service accounts enabled, and $" +
				plugin.LocalEnvVar("keycloak.overview", "client-secret") + " is its secret")
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
		WithHint("`rta explain keycloak.overview` lists every input and where each one can come from")
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
	var netErr *stdnet.OpError
	if errors.As(err, &netErr) || strings.Contains(err.Error(), "connection refused") {
		return view.Errorf("keycloak.conn.refused", "nothing is listening on %s", s.base).
			WithHint("is the server up, and is the URL right?")
	}
	var dnsErr *stdnet.DNSError
	if errors.As(err, &dnsErr) {
		return view.Errorf("keycloak.host.unknown", "no address for %q", s.host()).
			WithHint("`rta net dns " + s.host() + "` shows what DNS returns")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return view.Errorf("keycloak.timeout", "%s did not answer in time", s.base).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	var certErr x509.UnknownAuthorityError
	if errors.As(err, &certErr) {
		return view.Errorf("keycloak.tls.untrusted", "%s presented a certificate nothing here trusts", s.base).
			WithHint("a Keycloak behind an internal CA wants that CA passed with ca-file, not verification turned off")
	}
	return view.Errorf("keycloak.conn.failed", "could not reach %s: %v", s.base, err).
		WithHint("`rta explain keycloak.overview` lists every input and where each one can come from")
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
