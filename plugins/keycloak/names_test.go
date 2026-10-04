package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/this-is-tobi/rta/pkg/findings"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A realm is full of names its reader did not choose: a user picks a username
// at self-registration or types one into a login form that fails, a client
// registers its own id, an administrator names a role, a group, a flow. The
// renderer cleans a cell on the way to a terminal by dropping what it cannot
// draw, which turns a name holding an escape sequence into another, ordinary
// one, and a newline splits a row. Every name a listing, a page or a finding
// puts in front of a reader goes through plugin.ListedName, so that one of
// these shows as the quoted string it is.
//
// Each case runs twice over the same data: with names no terminal draws as
// themselves, which must show quoted (and fail without the change), and with
// names that merely hold spaces and accents, which must show exactly as before.

// naming is how a test spells the name carried by one field, and how a reader
// is shown it. tag tells the fields apart, so a field left out of the change is
// the one that fails; within is the same for a name that is the tail of a
// longer string (a URL, a path), which is listed whole.
type naming struct {
	label  string
	name   func(tag string) string
	seen   func(tag string) string
	within func(prefix, tag string) string
}

var namings = []naming{
	{
		label:  "a name holding an escape sequence and a newline",
		name:   func(tag string) string { return "esc\x1b[31m" + tag + "\nline" },
		seen:   func(tag string) string { return `"esc\x1b[31m` + tag + `\nline"` },
		within: func(prefix, tag string) string { return `"` + prefix + `esc\x1b[31m` + tag + `\nline"` },
	},
	{
		label:  "a name holding spaces and accents",
		name:   func(tag string) string { return "José " + tag + " Núñez" },
		seen:   func(tag string) string { return "José " + tag + " Núñez" },
		within: func(prefix, tag string) string { return prefix + "José " + tag + " Núñez" },
	},
}

type obj = map[string]any

func (f *fakeKeycloak) answer(path string, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bodies == nil {
		f.bodies = map[string][]byte{}
	}
	f.bodies[path] = raw
}

// filtered is the list a server returns for a lookup by name: the rows whose
// field is that name, or all of them when the lookup names none.
func filtered(body []byte, field, want string) []byte {
	if want == "" {
		return body
	}
	var rows []obj
	_ = json.Unmarshal(body, &rows)
	out := []obj{}
	for _, row := range rows {
		if row[field] == want {
			out = append(out, row)
		}
	}
	body, _ = json.Marshal(out)
	return body
}

// cells is every string of a view a reader is shown: a table's cells and
// warnings, a pair's value, a tree's labels and details, and a page's sections.
func cells(v view.View) []string {
	var out []string
	switch v := v.(type) {
	case view.Table:
		for _, row := range v.Rows {
			out = append(out, row...)
		}
		for _, w := range v.Warnings {
			out = append(out, w.Message, w.Hint)
		}
	case view.KeyValue:
		for _, p := range v.Pairs {
			out = append(out, p.Value)
		}
	case view.Tree:
		var walk func([]view.Node)
		walk = func(nodes []view.Node) {
			for _, n := range nodes {
				out = append(out, n.Label, n.Detail)
				walk(n.Children)
			}
		}
		walk(v.Roots)
	case view.Sections:
		for _, s := range v.Items {
			out = append(out, cells(s.View)...)
		}
		for _, w := range v.Warnings {
			out = append(out, w.Message, w.Hint)
		}
	}
	return out
}

// reads checks what a view shows: every want is in a cell as written, no cell
// holds a character a terminal would not draw, and a name that reads as itself
// was not put in quotes.
func (n naming) reads(t *testing.T, v view.View, wants ...string) {
	t.Helper()
	got := cells(v)
	for _, want := range wants {
		found := false
		for _, c := range got {
			i := strings.Index(c, want)
			if i < 0 {
				continue
			}
			found = true
			end := i + len(want)
			if !strings.HasPrefix(want, `"`) && i > 0 && c[i-1] == '"' ||
				!strings.HasSuffix(want, `"`) && end < len(c) && c[end] == '"' {
				t.Errorf("%s: %s was put in quotes in %q", n.label, want, c)
			}
		}
		if !found {
			t.Errorf("%s: no cell holds %s in %q", n.label, want, got)
		}
	}
	for _, c := range got {
		for _, r := range c {
			if unicode.IsControl(r) {
				t.Errorf("%s: a cell holds the control character %U: %q", n.label, r, c)
				break
			}
		}
	}
}

func sessionOn(t *testing.T, f *fakeKeycloak) *session {
	t.Helper()
	s, verr := connect(context.Background(), reqAt(t, f, "keycloak.overview", map[string]any{}))
	if verr != nil {
		t.Fatalf("connect: %s: %s", verr.Code, verr.Message)
	}
	return s
}

func eachNaming(t *testing.T, fn func(t *testing.T, n naming)) {
	t.Helper()
	for _, n := range namings {
		t.Run(n.label, func(t *testing.T) { fn(t, n) })
	}
}

func TestUsersAreListedByNamesAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		f.answer("/users", []obj{{"id": aliceID, "username": n.name("user"), "email": n.name("email"),
			"enabled": true, "createdTimestamp": 1789330617603}})
		tbl := table(t, run(t, f, "keycloak.user.list", map[string]any{}))
		n.reads(t, tbl, n.seen("user"), n.seen("email"))
		if tbl.Rows[0][0] != n.seen("user") || tbl.Rows[0][1] != n.seen("email") {
			t.Errorf("username and email cells = %q, %q, want %s and %s",
				tbl.Rows[0][0], tbl.Rows[0][1], n.seen("user"), n.seen("email"))
		}
	})
}

func TestAUserPageNamesTheAccountItsGroupsRolesAndSessionsAsAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		f.answer("/users/"+aliceID, obj{"id": aliceID, "username": n.name("user"), "email": n.name("email"),
			"firstName": n.name("first"), "enabled": true, "serviceAccountClientId": n.name("sa")})
		f.answer("/users/"+aliceID+"/credentials", []obj{{"type": "password", "userLabel": n.name("label")}})
		f.answer("/users/"+aliceID+"/groups", []obj{{"name": n.name("group"), "path": "/" + n.name("path")}})
		f.answer("/users/"+aliceID+"/sessions", []obj{{"username": n.name("sessuser"), "ipAddress": "10.0.0.1",
			"clients": obj{"c1": n.name("sessclient")}}})
		f.answer("/users/"+aliceID+"/role-mappings/realm/composite", []obj{{"name": n.name("realmrole")}})
		f.answer("/users/"+aliceID+"/role-mappings", obj{"clientMappings": obj{"c1": obj{
			"client": n.name("mapclient"), "mappings": []obj{{"name": n.name("clientrole")}}}}})

		page := run(t, f, "keycloak.user.show", map[string]any{"user": aliceID})
		n.reads(t, page, n.seen("user"), n.seen("email"), n.seen("first"), n.seen("sa"), n.seen("label"),
			n.seen("group"), n.within("/", "path"), n.seen("sessuser"), n.seen("sessclient"),
			n.seen("realmrole"), n.seen("mapclient"), n.seen("clientrole"))
		groups := table(t, sections(t, page)["groups"])
		if groups.Rows[0][0] != n.seen("group") || groups.Rows[0][1] != n.within("/", "path") {
			t.Errorf("group row = %q, want %s and %s", groups.Rows[0], n.seen("group"), n.within("/", "path"))
		}
	})
}

func TestClientsAreListedAndShownByNamesAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		f.answer("/clients", []obj{{"id": "c1", "clientId": n.name("cid"), "name": n.name("cname"), "enabled": true,
			"publicClient": true, "standardFlowEnabled": true,
			"rootUrl": "https://" + n.name("root"), "baseUrl": "/" + n.name("base"),
			"redirectUris": []string{"https://" + n.name("redirect")}, "webOrigins": []string{"https://" + n.name("origin")},
			"defaultClientScopes": []string{n.name("scope")}, "optionalClientScopes": []string{n.name("optional")},
			"attributes": obj{"pkce.code.challenge.method": n.name("pkce"), "login_theme": n.name("theme")}}})

		list := table(t, run(t, f, "keycloak.client.list", map[string]any{}))
		n.reads(t, list, n.seen("cid"), n.seen("pkce"))
		if list.Rows[0][0] != n.seen("cid") || list.Rows[0][3] != n.seen("pkce") {
			t.Errorf("client and PKCE cells = %q, %q, want %s and %s", list.Rows[0][0], list.Rows[0][3],
				n.seen("cid"), n.seen("pkce"))
		}

		page := run(t, f, "keycloak.client.show", map[string]any{"client": n.name("cid")})
		n.reads(t, page, n.seen("cid"), n.seen("cname"), n.seen("pkce"), n.seen("scope"), n.seen("optional"),
			n.seen("theme"), n.within("https://", "root"), n.within("/", "base"),
			n.within("https://", "redirect"), n.within("https://", "origin"))
	})
}

func TestRolesAreListedByNamesAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		f.answer("/roles", []obj{{"id": "r1", "name": n.name("role"), "composite": true}})
		tbl := table(t, run(t, f, "keycloak.role.list", map[string]any{}))
		n.reads(t, tbl, n.seen("role"))
		if tbl.Rows[0][0] != n.seen("role") {
			t.Errorf("role cell = %q, want %s", tbl.Rows[0][0], n.seen("role"))
		}
	})
}

func TestFlowsAreListedAndDrawnByNamesAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		f.answer("/authentication/flows", []obj{{"id": "f1", "alias": n.name("alias"), "topLevel": true}})
		list := table(t, run(t, f, "keycloak.flow.list", map[string]any{}))
		n.reads(t, list, n.seen("alias"))
		if list.Rows[0][0] != n.seen("alias") {
			t.Errorf("flow cell = %q, want %s", list.Rows[0][0], n.seen("alias"))
		}

		f.answer("/authentication/flows/browser/executions", []obj{
			{"displayName": n.name("sub"), "requirement": "CONDITIONAL", "level": 0, "authenticationFlow": true},
			{"displayName": n.name("step"), "requirement": "REQUIRED", "level": 1, "providerId": "auth-otp-form"},
		})
		tree, ok := run(t, f, "keycloak.flow.show", map[string]any{"flow": "browser"}).(view.Tree)
		if !ok {
			t.Fatal("not a tree")
		}
		n.reads(t, tree, n.seen("sub"), n.seen("step"))
		if tree.Roots[0].Label != n.seen("sub") || tree.Roots[0].Children[0].Label != n.seen("step") {
			t.Errorf("tree labels = %q / %q, want %s / %s", tree.Roots[0].Label, tree.Roots[0].Children[0].Label,
				n.seen("sub"), n.seen("step"))
		}
	})
}

func TestSessionsAreListedByNamesAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		open := obj{"username": n.name("user"), "ipAddress": "10.0.0.1", "start": 1789330617603,
			"lastAccess": 1789330617603, "clients": obj{"id1": n.name("client"), "id2": "plain"}}

		f.answer("/client-session-stats", []obj{{"clientId": n.name("stat"), "active": "2", "offline": "1"}})
		stats := table(t, run(t, f, "keycloak.session.list", map[string]any{}))
		n.reads(t, stats, n.seen("stat"))

		f.answer("/users/"+aliceID+"/sessions", []obj{open})
		byUser := table(t, run(t, f, "keycloak.session.list", map[string]any{"user": "alice"}))
		n.reads(t, byUser, n.seen("user"), n.seen("client"))

		f.answer("/clients/"+rtaAuditID+"/user-sessions", []obj{open})
		byClient := table(t, run(t, f, "keycloak.session.list", map[string]any{"client": "rta-audit"}))
		n.reads(t, byClient, n.seen("user"), n.seen("client"))
		if byClient.Rows[0][0] != n.seen("user") {
			t.Errorf("session user cell = %q, want %s", byClient.Rows[0][0], n.seen("user"))
		}
	})
}

// What a login form's visitor typed is the stranger's own text: a failed login
// is recorded under whatever username was typed, and an unknown client id is
// the one in the request.
func TestEventsNameWhatWasTypedAtALoginAndTheResourceAnAdminTouched(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		f.answer("/events", []obj{
			{"time": 1789330617603, "type": "LOGIN_ERROR", "clientId": n.name("evclient"), "ipAddress": "10.0.0.2",
				"error": "user_not_found", "details": obj{"username": n.name("typed")}},
			{"time": 1789330617604, "type": "LOGIN", "userId": aliceID, "clientId": "spa"},
		})
		events := table(t, run(t, f, "keycloak.event.list", map[string]any{}))
		n.reads(t, events, n.seen("evclient"), n.seen("typed"))
		if events.Rows[0][2] != n.seen("typed") || events.Rows[0][3] != n.seen("evclient") {
			t.Errorf("user and client cells = %q, %q, want %s and %s",
				events.Rows[0][2], events.Rows[0][3], n.seen("typed"), n.seen("evclient"))
		}
		if events.Rows[1][2] != aliceID {
			t.Errorf("a user known by id is shown as %q, want the id", events.Rows[1][2])
		}

		f.answer("/admin-events", []obj{{"time": 1789330617603, "operationType": "CREATE", "resourceType": "CLIENT_ROLE",
			"resourcePath": "clients/c1/roles/" + n.name("respath"), "authDetails": obj{"userId": aliceID, "ipAddress": "10.0.0.3"}}})
		admin := table(t, run(t, f, "keycloak.event.admin", map[string]any{}))
		n.reads(t, admin, n.within("clients/c1/roles/", "respath"))
		if admin.Rows[0][3] != n.within("clients/c1/roles/", "respath") {
			t.Errorf("resource path cell = %q, want %s", admin.Rows[0][3], n.within("clients/c1/roles/", "respath"))
		}
	})
}

// Behind a reverse proxy the address Keycloak records is the one in the request's
// X-Forwarded-For header, which the request's sender wrote.
func TestAnAddressAProxyPassedOnIsListedAsAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		f.answer("/events", []obj{{"time": 1789330617603, "type": "LOGIN_ERROR", "ipAddress": n.name("evaddr")}})
		events := table(t, run(t, f, "keycloak.event.list", map[string]any{}))
		n.reads(t, events, n.seen("evaddr"))
		if events.Rows[0][4] != n.seen("evaddr") {
			t.Errorf("event address cell = %q, want %s", events.Rows[0][4], n.seen("evaddr"))
		}

		f.answer("/admin-events", []obj{{"time": 1789330617603, "operationType": "CREATE", "resourceType": "CLIENT",
			"resourcePath": "clients/c1", "authDetails": obj{"userId": aliceID, "ipAddress": n.name("adminaddr")}}})
		admin := table(t, run(t, f, "keycloak.event.admin", map[string]any{}))
		n.reads(t, admin, n.seen("adminaddr"))
		if admin.Rows[0][5] != n.seen("adminaddr") {
			t.Errorf("admin event address cell = %q, want %s", admin.Rows[0][5], n.seen("adminaddr"))
		}

		f.answer("/users/"+aliceID+"/sessions", []obj{{"username": "alice", "ipAddress": n.name("sessaddr"),
			"start": 1789330617603, "lastAccess": 1789330617603}})
		sessions := table(t, run(t, f, "keycloak.session.list", map[string]any{"user": "alice"}))
		n.reads(t, sessions, n.seen("sessaddr"))
		if sessions.Rows[0][1] != n.seen("sessaddr") {
			t.Errorf("session address cell = %q, want %s", sessions.Rows[0][1], n.seen("sessaddr"))
		}
	})
}

// A real address is plain text and an event nobody recorded one for has none:
// neither is put in quotes.
func TestARealAddressIsShownAsItIsAndNoAddressStaysEmpty(t *testing.T) {
	f := newFakeKeycloak(t)
	f.answer("/events", []obj{
		{"time": 1789330617604, "type": "LOGIN", "ipAddress": "203.0.113.7"},
		{"time": 1789330617603, "type": "LOGIN", "ipAddress": "2001:db8::1"},
		{"time": 1789330617602, "type": "LOGIN"},
	})
	events := table(t, run(t, f, "keycloak.event.list", map[string]any{}))
	for i, want := range []string{"203.0.113.7", "2001:db8::1", ""} {
		if got := events.Rows[i][4]; got != want {
			t.Errorf("event %d address = %q, want %q", i, got, want)
		}
	}
}

func TestTheOverviewNamesTheRealmAndItsBrowserFlowAsAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		var realm obj
		if err := json.Unmarshal(fixture("realm.json"), &realm); err != nil {
			t.Fatal(err)
		}
		realm["realm"], realm["browserFlow"] = n.name("realm"), n.name("bflow")
		f.answer("", realm)
		got := pairs(t, run(t, f, "keycloak.overview", map[string]any{}))
		if got["realm"] != n.seen("realm")+" · enabled" || got["browser flow"] != n.seen("bflow") {
			t.Errorf("realm and browser flow = %q, %q, want %s and %s", got["realm"], got["browser flow"],
				n.seen("realm")+" · enabled", n.seen("bflow"))
		}
	})
}

// The audit says who and what, by name, in every group of its report: which
// users lack a second factor, which step of the browser flow asks for one,
// which client a finding is about, which redirect URI is the problem and who
// holds the roles that administer the realm.
func TestTheAuditNamesWhoAndWhatItGradesAsAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		f.answer("/users", []obj{
			{"id": "u1", "username": n.name("nofactor"), "enabled": true},
			{"id": "u2", "username": n.name("unread"), "enabled": true},
		})
		f.deny = []string{"/users/u2/credentials", "/clients/cC/service-account-user"}
		f.answer("/authentication/flows/browser/executions", []obj{
			{"displayName": n.name("otp"), "requirement": "REQUIRED", "level": 0, "providerId": "auth-otp-form"},
		})
		var clients []obj
		if err := json.Unmarshal(fixture("clients.json"), &clients); err != nil {
			t.Fatal(err)
		}
		f.answer("/clients", append(clients,
			obj{"id": "cA", "clientId": n.name("cidA"), "enabled": true, "serviceAccountsEnabled": true,
				"redirectUris": []string{"http://clear.example/" + n.name("clear"), "https://*.example/" + n.name("wild")}},
			obj{"id": "cB", "clientId": n.name("cidB"), "enabled": true, "serviceAccountsEnabled": true},
			obj{"id": "cC", "clientId": n.name("cidC"), "enabled": true, "serviceAccountsEnabled": true},
		))
		f.answer("/clients/cA/service-account-user", obj{"id": "saA"})
		f.answer("/users/saA/role-mappings", obj{"clientMappings": obj{"rm": obj{"client": "realm-management",
			"mappings": []obj{{"name": "manage-" + n.name("manage")}}}}})
		f.answer("/clients/cB/service-account-user", obj{"id": "saB"})
		f.answer("/users/saB/role-mappings", obj{"clientMappings": obj{"rm": obj{"client": "realm-management",
			"mappings": []obj{{"name": "view-" + n.name("viewer")}}}}})
		f.answer("/clients/"+realmMgmt+"/roles/realm-admin/users", []obj{{"id": "a1", "username": n.name("admin")}})

		n.reads(t, run(t, f, "keycloak.audit", map[string]any{"detail": true}),
			n.seen("nofactor"), n.seen("unread"), n.seen("otp"),
			n.within("http://clear.example/", "clear"), n.within("https://*.example/", "wild"),
			n.within("manage-", "manage"), n.within("view-", "viewer"), n.seen("admin"),
			// A client's finding is addressed by its id, in the check column.
			n.seen("cidA")+"/redirect", n.seen("cidA")+"/service-account",
			n.seen("cidB")+"/service-account", n.seen("cidC")+"/service-account")
	})
}

// The temporary administrator and the applications in the master realm are
// found by looking at the realm named master, which the fake does not serve,
// so these two read through a session on the realm it does.
func TestTheAuditNamesTheBootstrapAdministratorAndTheMasterRealmsApplicationsAsAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		f := newFakeKeycloak(t)
		f.answer("/users", []obj{{"id": "u1", "username": n.name("bootstrap"), "enabled": true,
			"attributes": obj{"is_temporary_admin": []string{"true"}}}})
		f.answer("/clients", []obj{{"id": "c1", "clientId": n.name("app"), "enabled": true}})
		s := sessionOn(t, f)

		bootstrap := &findings.Report{}
		s.auditBootstrapAdmin(context.Background(), bootstrap)
		n.reads(t, bootstrap.Table(false), n.seen("bootstrap"))

		apps := &findings.Report{}
		s.auditClients(context.Background(), apps, true)
		n.reads(t, apps.Table(false), n.seen("app"))
	})
}

// A step of the browser flow is named in the finding that says whether a second
// factor is required of everyone, or only offered.
func TestTheBrowserFlowGradeNamesTheStepsAsAReaderCanTellApart(t *testing.T) {
	eachNaming(t, func(t *testing.T, n naming) {
		_, offered := gradeBrowserFlow([]executionRep{{DisplayName: n.name("offered"), ProviderID: "webauthn-authenticator",
			Requirement: "ALTERNATIVE"}})
		_, enforced := gradeBrowserFlow([]executionRep{{DisplayName: n.name("enforced"), ProviderID: "auth-otp-form",
			Requirement: "REQUIRED"}})
		n.reads(t, view.KeyValue{Pairs: []view.Pair{{Value: offered}, {Value: enforced}}},
			n.seen("offered"), n.seen("enforced"))
	})
}

// A user without an email, a credential without a label and an event nobody
// logged in for carry nothing where there is nothing: ListedName writes the two
// quotes of an empty string, and a cell of those claims a name that is not there.
func TestWhereThereIsNoNameTheCellStaysEmpty(t *testing.T) {
	f := newFakeKeycloak(t)
	f.answer("/users", []obj{{"id": aliceID, "username": "alice", "enabled": true}})
	f.answer("/users/"+aliceID, obj{"id": aliceID, "username": "alice", "enabled": true})
	f.answer("/users/"+aliceID+"/credentials", []obj{{"type": "password"}})
	f.answer("/events", []obj{{"time": 1789330617603, "type": "CODE_TO_TOKEN"}})

	users := table(t, run(t, f, "keycloak.user.list", map[string]any{}))
	if users.Rows[0][1] != "" {
		t.Errorf("a user with no email shows %q, want an empty cell", users.Rows[0][1])
	}
	page := sections(t, run(t, f, "keycloak.user.show", map[string]any{"user": aliceID}))
	if creds := table(t, page["credentials"]); creds.Rows[0][1] != "" {
		t.Errorf("a credential with no label shows %q, want an empty cell", creds.Rows[0][1])
	}
	if p := pairs(t, page["profile"]); p["email"] != "" || p["name"] != "" {
		t.Errorf("a user with no email or name shows %q and %q, want neither listed", p["email"], p["name"])
	}
	events := table(t, run(t, f, "keycloak.event.list", map[string]any{}))
	if events.Rows[0][2] != "" || events.Rows[0][3] != "" {
		t.Errorf("an event with no user or client shows %q and %q, want empty cells", events.Rows[0][2], events.Rows[0][3])
	}
}

// A description is prose somebody wrote, a paragraph where a name is a label:
// it keeps its line breaks, and the renderer cleans what it cannot draw as it
// does a message.
func TestADescriptionIsLeftAsProse(t *testing.T) {
	f := newFakeKeycloak(t)
	f.answer("/roles", []obj{{"id": "r1", "name": "viewer", "description": "reads the realm.\nWrites nothing."}})
	if got := table(t, run(t, f, "keycloak.role.list", map[string]any{})).Rows[0][2]; got != "reads the realm.\nWrites nothing." {
		t.Errorf("description = %q, want it as written", got)
	}
}
