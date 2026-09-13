package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/view"
)

// sdktest is the definition of "a correct plugin" — no exemption for
// keycloak. Every capability here is a Read with a bound listing, so the
// suite's dry-run rule has nothing to drive; what it checks is the
// declaration: ids, inputs, redaction, verbs.
func TestConformance(t *testing.T) {
	sdktest.Check(t, Plugin(), sdktest.WithInputs(conformanceInputs))
}

// conformanceInputs points every capability at an address nothing is
// listening on, and gives the ones with a required selector something to
// select.
func conformanceInputs(string) map[string]map[string]any {
	conn := func(m map[string]any) map[string]any {
		m["url"] = "http://127.0.0.1:1"
		m["client-secret"] = "conformance"
		return m
	}
	return map[string]map[string]any{
		"keycloak.user.show":   conn(map[string]any{"user": "alice"}),
		"keycloak.client.show": conn(map[string]any{"client": "spa"}),
		"keycloak.flow.show":   conn(map[string]any{"flow": "browser"}),
	}
}

// fakeKeycloak answers the Admin REST paths these capabilities call with
// the fixtures under testdata/ — captured from a Keycloak 26.7 answering a
// service account holding the view-* roles, so what the handlers decode
// is what the real thing sends. The token endpoint is the interesting
// half: every admin request has to carry the token it issued, and the
// client secret has to reach it and nothing else.
type fakeKeycloak struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string // "METHOD path?query", in order
	secrets  []string // every path the client secret was posted to
	admin    bool     // answer /admin/serverinfo in full, as to a master-realm client
}

const fakeSecret = "fake-client-secret"
const fakeToken = "fake-access-token"

func newFakeKeycloak(t *testing.T) *fakeKeycloak {
	t.Helper()
	f := &fakeKeycloak{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeKeycloak) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	f.mu.Unlock()

	if strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token") {
		_ = r.ParseForm()
		if r.PostForm.Get("client_secret") != "" {
			f.mu.Lock()
			f.secrets = append(f.secrets, r.URL.Path)
			f.mu.Unlock()
		}
		if r.URL.Path != "/realms/demo/protocol/openid-connect/token" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Realm does not exist"}`))
			return
		}
		if r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("client_id") != "rta-audit" ||
			r.PostForm.Get("client_secret") != fakeSecret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized_client","error_description":"Invalid client or Invalid client credentials"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"` + fakeToken + `","expires_in":300,"token_type":"Bearer"}`))
		return
	}

	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"HTTP 401 Unauthorized"}`))
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"HTTP 403 Forbidden"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/admin/serverinfo" {
		if f.admin {
			_, _ = w.Write(fixture("serverinfo-admin.json"))
		} else {
			_, _ = w.Write([]byte(`{"themes":{}}`))
		}
		return
	}
	const prefix = "/admin/realms/demo"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"HTTP 403 Forbidden"}`))
		return
	}
	body, ok := f.route(strings.TrimPrefix(r.URL.Path, prefix), r.URL.Query())
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
		return
	}
	_, _ = w.Write(body)
}

// Fixture ids, so a route can name the users and clients the fixtures hold.
const (
	aliceID    = "00191715-ece1-4379-8090-6c0607d96ebd"
	carolID    = "442b7121-0502-4400-9317-6cf37a6eb06c"
	saID       = "b5a9fdbe-c1c3-487f-9eab-6f0372a9e08a"
	rtaAuditID = "db26b62b-a306-4750-a7da-592b8d994ebf"
	realmMgmt  = "69cc227e-d072-4cfa-aeef-7a83ad3590b8"
)

func (f *fakeKeycloak) route(path string, q map[string][]string) ([]byte, bool) {
	first := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	switch path {
	case "", "/":
		return fixture("realm.json"), true
	case "/users/count":
		return []byte("3"), true
	case "/users":
		var users []map[string]any
		_ = json.Unmarshal(fixture("users.json"), &users)
		if name := first("username"); name != "" {
			var out []map[string]any
			for _, u := range users {
				if u["username"] == name {
					out = append(out, u)
				}
			}
			users = out
		}
		if first("q") != "" {
			users = nil
		}
		if users == nil {
			users = []map[string]any{}
		}
		body, _ := json.Marshal(users)
		return body, true
	case "/users/" + carolID:
		return fixture("user-carol.json"), true
	case "/users/" + carolID + "/credentials":
		return fixture("user-carol-credentials.json"), true
	case "/users/" + aliceID + "/credentials":
		return []byte(`[{"type":"password","createdDate":1789330617626}]`), true
	case "/users/" + aliceID + "/role-mappings", "/users/" + saID + "/role-mappings":
		if strings.Contains(path, saID) {
			return fixture("sa-role-mappings.json"), true
		}
		return fixture("user-alice-role-mappings.json"), true
	case "/users/" + aliceID + "/role-mappings/realm/composite":
		return fixture("user-alice-realm-composite.json"), true
	case "/users/" + aliceID + "/groups", "/users/" + aliceID + "/sessions":
		return []byte("[]"), true
	case "/clients":
		var clients []map[string]any
		_ = json.Unmarshal(fixture("clients.json"), &clients)
		if id := first("clientId"); id != "" {
			var out []map[string]any
			for _, c := range clients {
				if c["clientId"] == id {
					out = append(out, c)
				}
			}
			clients = out
		}
		body, _ := json.Marshal(clients)
		return body, true
	case "/clients/" + rtaAuditID + "/service-account-user":
		return fixture("client-rta-audit-sa.json"), true
	case "/clients/" + realmMgmt + "/roles":
		return fixture("realm-management-roles.json"), true
	case "/clients/" + realmMgmt + "/roles/realm-admin/users":
		return fixture("realm-admin-users.json"), true
	case "/roles":
		return fixture("roles.json"), true
	case "/authentication/flows":
		return fixture("flows.json"), true
	case "/authentication/flows/browser/executions":
		return fixture("flow-browser-executions.json"), true
	case "/authentication/required-actions":
		return fixture("required-actions.json"), true
	case "/events":
		return fixture("events.json"), true
	case "/admin-events":
		return fixture("admin-events.json"), true
	case "/client-session-stats":
		return fixture("client-session-stats.json"), true
	}
	if strings.HasPrefix(path, "/users/") && strings.HasSuffix(path, "/role-mappings/realm/composite") {
		return []byte("[]"), true
	}
	if strings.HasPrefix(path, "/clients/"+realmMgmt+"/roles/") && strings.HasSuffix(path, "/users") {
		return []byte("[]"), true
	}
	if strings.HasPrefix(path, "/users/") && strings.HasSuffix(path, "/credentials") {
		return []byte("[]"), true
	}
	return nil, false
}

func (f *fakeKeycloak) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func fixture(name string) []byte {
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		panic(err)
	}
	return body
}

// req builds a resolved request the way the host would, against the named
// capability's own declared inputs (connFields included, via cap).
func req(t *testing.T, capID string, values map[string]any) plugin.Request {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == capID {
			return plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false)
		}
	}
	t.Fatalf("no capability %q", capID)
	return plugin.Request{}
}

func reqAt(t *testing.T, f *fakeKeycloak, capID string, values map[string]any) plugin.Request {
	t.Helper()
	values["url"] = f.URL
	values["realm"] = "demo"
	values["client-id"] = "rta-audit"
	values["client-secret"] = fakeSecret
	return req(t, capID, values)
}

func run(t *testing.T, f *fakeKeycloak, capID string, values map[string]any) view.View {
	t.Helper()
	c := capability(t, capID)
	v, err := c.Run(context.Background(), reqAt(t, f, capID, values))
	if err != nil {
		t.Fatalf("%s: %v", capID, err)
	}
	return v
}

func capability(t *testing.T, id string) plugin.Capability {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no capability %q", id)
	return plugin.Capability{}
}

// rendered is a view as JSON — the shape every machine consumer gets, and
// the easiest place to assert that a value is or is not anywhere in it.
func rendered(t *testing.T, v view.View) string {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// --- the declaration ---------------------------------------------------------

// Every shared connection input must be Local. These fields together name
// which Keycloak a call reaches, which realm, and as whom, and an MCP
// caller may not choose that — an agent that could would point rta at an
// instance of its own and have the host supply the operator's secret
// beside it.
func TestEveryConnectionInputIsLocal(t *testing.T) {
	for _, f := range connFields() {
		if !f.Local {
			t.Errorf("%s: connection input is not Local — an MCP caller could redirect this call", f.Name)
		}
	}
}

func TestOnlySecretsUseEnvFallback(t *testing.T) {
	for _, f := range connFields() {
		if f.EnvFallback && f.Type != plugin.Secret {
			t.Errorf("%s: non-secret input declares EnvFallback (%s); a destination must come from a caller or config",
				f.Name, f.Type)
		}
	}
}

func TestEveryCapabilityIsNoPreview(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if !c.NoPreview {
			t.Errorf("%s: NoPreview = false, want true — every capability here reaches off the box", c.ID)
		}
	}
}

// **The line this plugin draws, pinned: reads only.** Identity
// administration is authority-expanding by nature, and a write set is its
// own design with its own gates. Until that design exists, a capability
// here that is not a Read is a mistake, and so is one that needs a grant —
// the destination is profile-bound, which is the condition under which a
// network read stays an ungated Read.
func TestEveryCapabilityIsAnUngatedRead(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.Safety != plugin.Read {
			t.Errorf("%s: Safety = %s, want read — this plugin writes nothing", c.ID, c.Safety)
		}
		if c.NeedsGrant || c.HumanOnly || c.Scope != "" {
			t.Errorf("%s: gated (grant=%v human=%v scope=%q); the reads here are profile-bound and ungated",
				c.ID, c.NeedsGrant, c.HumanOnly, c.Scope)
		}
	}
}

// Every listing is bounded: the Admin API pages, a realm is as large as a
// company, and a capability that lists without a ceiling is one call from
// a report nobody can read.
func TestEveryListingIsBounded(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if !strings.HasSuffix(c.ID, ".list") && c.ID != "keycloak.audit" && c.ID != "keycloak.event.admin" {
			continue
		}
		if c.ID == "keycloak.client.list" || c.ID == "keycloak.role.list" || c.ID == "keycloak.flow.list" {
			continue // one realm's clients, roles and flows are a screenful, and the API has no page for them
		}
		found := false
		for _, in := range c.Inputs {
			if in.Name == "max" && in.Max != nil {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no bounded --max input", c.ID)
		}
	}
}

// --- the credential ----------------------------------------------------------

// The client secret goes to the token endpoint of the configured server and
// nowhere else, and everything after it carries the token that came back.
func TestTheSecretReachesOnlyTheTokenEndpoint(t *testing.T) {
	f := newFakeKeycloak(t)
	run(t, f, "keycloak.overview", map[string]any{})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.secrets) != 1 || f.secrets[0] != "/realms/demo/protocol/openid-connect/token" {
		t.Errorf("the secret was posted to %v, want the demo realm's token endpoint once", f.secrets)
	}
	admin := 0
	for _, r := range f.requests {
		if strings.Contains(r, "/admin/") {
			admin++
		}
	}
	if admin == 0 {
		t.Fatal("no admin request followed the token")
	}
}

// auth-realm is where the client lives; realm is what it reads. A
// master-realm client reading another realm is the ordinary multi-realm
// setup, and the two must not be confused in either direction.
func TestAuthRealmNamesWhereTheClientLives(t *testing.T) {
	f := newFakeKeycloak(t)
	r := reqAt(t, f, "keycloak.overview", map[string]any{"auth-realm": "master"})
	r = r.With(map[string]any{"realm": "demo"})
	_, err := capability(t, "keycloak.overview").Run(context.Background(), r)
	// The fake only issues tokens for the demo realm, so a token minted
	// against master is refused — which is exactly the request this test
	// wants to see made.
	if err == nil {
		t.Fatal("expected the fake to refuse a master-realm token request")
	}
	found := false
	for _, p := range f.paths() {
		if strings.HasPrefix(p, "POST /realms/master/protocol/openid-connect/token") {
			found = true
		}
	}
	if !found {
		t.Errorf("the token was not requested from the auth realm; requests: %v", f.paths())
	}
}

func TestARefusedSecretIsNamedAsSuch(t *testing.T) {
	f := newFakeKeycloak(t)
	r := reqAt(t, f, "keycloak.overview", map[string]any{})
	r = r.With(map[string]any{"client-secret": "wrong"})
	_, err := capability(t, "keycloak.overview").Run(context.Background(), r)
	verr := view.AsError(err, "")
	if verr == nil || verr.Code != "keycloak.auth.failed" {
		t.Fatalf("error = %v, want keycloak.auth.failed", err)
	}
	if !strings.Contains(verr.Hint, "RTA_KEYCLOAK_CLIENT_SECRET") {
		t.Errorf("the hint does not name the environment variable: %q", verr.Hint)
	}
}

func TestAMissingRealmAtTheTokenEndpointIsNamed(t *testing.T) {
	f := newFakeKeycloak(t)
	r := reqAt(t, f, "keycloak.overview", map[string]any{})
	r = r.With(map[string]any{"realm": "nope"})
	_, err := capability(t, "keycloak.overview").Run(context.Background(), r)
	if verr := view.AsError(err, ""); verr == nil || verr.Code != "keycloak.realm.unknown" {
		t.Fatalf("error = %v, want keycloak.realm.unknown", err)
	}
}

func TestNothingListeningIsNamed(t *testing.T) {
	r := req(t, "keycloak.overview", map[string]any{"url": "http://127.0.0.1:1", "client-secret": "x"})
	_, err := capability(t, "keycloak.overview").Run(context.Background(), r)
	if verr := view.AsError(err, ""); verr == nil || verr.Code != "keycloak.conn.refused" {
		t.Fatalf("error = %v, want keycloak.conn.refused", err)
	}
}

func TestADeniedReadNamesTheRolesToGrant(t *testing.T) {
	f := newFakeKeycloak(t)
	r := reqAt(t, f, "keycloak.overview", map[string]any{})
	// The fake refuses any realm but demo with a 403, after issuing a token.
	r = r.With(map[string]any{"realm": "other", "auth-realm": "demo"})
	_, err := capability(t, "keycloak.overview").Run(context.Background(), r)
	verr := view.AsError(err, "")
	if verr == nil || verr.Code != "keycloak.denied" {
		t.Fatalf("error = %v, want keycloak.denied", err)
	}
	if !strings.Contains(verr.Hint, "view-users") {
		t.Errorf("the hint does not name the roles: %q", verr.Hint)
	}
}

// --- what is never said ------------------------------------------------------

// A client representation carries the secret to anyone with view-clients.
// No view this plugin produces may contain it — the list, the page, the
// audit — and no request may reach the endpoint that hands it out alone.
func TestTheClientSecretIsNeverRendered(t *testing.T) {
	const secret = "FIXTURE-CLIENT-SECRET"
	if !strings.Contains(string(fixture("clients.json")), secret) {
		t.Fatal("the fixture no longer carries a secret, so this test proves nothing")
	}
	f := newFakeKeycloak(t)
	for _, tc := range []struct {
		cap    string
		values map[string]any
	}{
		{"keycloak.client.list", map[string]any{}},
		{"keycloak.client.show", map[string]any{"client": "rta-audit"}},
		{"keycloak.overview", map[string]any{"detail": true}},
		{"keycloak.audit", map[string]any{"detail": true}},
	} {
		if out := rendered(t, run(t, f, tc.cap, tc.values)); strings.Contains(out, secret) {
			t.Errorf("%s rendered the client secret", tc.cap)
		}
	}
	for _, p := range f.paths() {
		if strings.Contains(p, "client-secret") {
			t.Errorf("a request reached the client-secret endpoint: %s", p)
		}
	}
}

// A user's attributes are where a realm keeps the things it bolted on —
// and none of them is any of this plugin's business.
func TestUserAttributesAreNeverRendered(t *testing.T) {
	f := newFakeKeycloak(t)
	for _, tc := range []struct {
		cap    string
		values map[string]any
	}{
		{"keycloak.user.list", map[string]any{}},
		{"keycloak.user.show", map[string]any{"user": "alice"}},
	} {
		if out := rendered(t, run(t, f, tc.cap, tc.values)); strings.Contains(out, "attributes") || strings.Contains(out, "is_temporary_admin") {
			t.Errorf("%s rendered user attributes", tc.cap)
		}
	}
}
