package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

func pairs(t *testing.T, v view.View) map[string]string {
	t.Helper()
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("got %T, want key/value pairs", v)
	}
	out := map[string]string{}
	for _, p := range kv.Pairs {
		out[p.Key] = p.Value
	}
	return out
}

func table(t *testing.T, v view.View) view.Table {
	t.Helper()
	tbl, ok := v.(view.Table)
	if !ok {
		t.Fatalf("got %T, want a table", v)
	}
	return tbl
}

func sections(t *testing.T, v view.View) map[string]view.View {
	t.Helper()
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("got %T, want sections", v)
	}
	out := map[string]view.View{}
	for _, s := range page.Items {
		out[s.Key()] = s.View
	}
	return out
}

func TestOverviewSaysWhatTheRealmIs(t *testing.T) {
	f := newFakeKeycloak(t)
	got := pairs(t, run(t, f, "keycloak.overview", map[string]any{}))
	for key, want := range map[string]string{
		"realm":        "demo · enabled",
		"users":        "3",
		"clients":      "8 · 1 confidential, 5 public, 2 bearer-only",
		"brute force":  "off",
		"events":       "login on · admin on",
		"browser flow": "browser",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	if _, shown := got["version"]; shown {
		t.Error("a realm-scoped client cannot see the version, yet one was shown")
	}
	f.admin = true
	if got := pairs(t, run(t, f, "keycloak.overview", map[string]any{})); got["version"] != "26.7.3" {
		t.Errorf("a master-realm client sees version %q, want 26.7.3", got["version"])
	}
}

func TestOverviewDetailComposesTheRealm(t *testing.T) {
	f := newFakeKeycloak(t)
	got := sections(t, run(t, f, "keycloak.overview", map[string]any{"detail": true}))
	for _, id := range []string{"summary", "clients", "flows", "sessions"} {
		if _, ok := got[id]; !ok {
			t.Errorf("no %q section in %v", id, got)
		}
	}
}

func TestUserListShowsWhoHasAnOTP(t *testing.T) {
	f := newFakeKeycloak(t)
	tbl := table(t, run(t, f, "keycloak.user.list", map[string]any{}))
	if len(tbl.Rows) != 3 {
		t.Fatalf("%d rows, want 3", len(tbl.Rows))
	}
	otp := map[string]string{}
	for _, row := range tbl.Rows {
		otp[row[0]] = row[4]
	}
	if otp["carol"] != "yes" || otp["alice"] != "no" {
		t.Errorf("OTP column = %v", otp)
	}
	if tbl.Columns[5].Kind != view.KindTimestamp {
		t.Errorf("Created column kind = %q, want timestamp", tbl.Columns[5].Kind)
	}
}

func TestListingsPassTheirBoundAndFiltersToTheServer(t *testing.T) {
	f := newFakeKeycloak(t)
	run(t, f, "keycloak.user.list", map[string]any{"max": 7, "search": "ali"})
	run(t, f, "keycloak.event.list", map[string]any{"type": "LOGIN_ERROR", "max": 9})
	joined := strings.Join(f.paths(), "\n")
	for _, want := range []string{"/users?briefRepresentation=false&max=7&search=ali", "/events?max=9&type=LOGIN_ERROR"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no request %q in:\n%s", want, joined)
		}
	}
}

func TestUserShowResolvesByUsernameAndById(t *testing.T) {
	f := newFakeKeycloak(t)
	byName := sections(t, run(t, f, "keycloak.user.show", map[string]any{"user": "alice"}))
	if pairs(t, byName["profile"])["username"] != "alice" {
		t.Errorf("profile = %v", byName["profile"])
	}
	for _, id := range []string{"credentials", "roles"} {
		if _, ok := byName[id]; !ok {
			t.Errorf("no %q section", id)
		}
	}
	roles := table(t, byName["roles"])
	found := false
	for _, row := range roles.Rows {
		if row[0] == "realm-management" && row[1] == "realm-admin" {
			found = true
		}
	}
	if !found {
		t.Errorf("alice's realm-admin client role is missing from %v", roles.Rows)
	}

	byID := sections(t, run(t, f, "keycloak.user.show", map[string]any{"user": carolID}))
	if pairs(t, byID["profile"])["username"] != "carol" {
		t.Errorf("an id did not resolve: %v", byID["profile"])
	}
}

// A credential is a type and a date. The fixture's OTP credential carries
// its secret and its parameters in the representation Keycloak sends, and
// none of that reaches the page.
func TestCredentialsAreTypesNeverValues(t *testing.T) {
	f := newFakeKeycloak(t)
	v := run(t, f, "keycloak.user.show", map[string]any{"user": carolID})
	creds := table(t, sections(t, v)["credentials"])
	types := map[string]bool{}
	for _, row := range creds.Rows {
		types[row[0]] = true
	}
	if !types["password"] || !types["otp"] {
		t.Errorf("credential types = %v", types)
	}
	if out := rendered(t, v); strings.Contains(out, "credentialData") || strings.Contains(out, "secretData") || strings.Contains(out, "HmacSHA1") {
		t.Error("the page carries credential data")
	}
}

func TestClientListNamesKindFlowsAndPKCE(t *testing.T) {
	f := newFakeKeycloak(t)
	tbl := table(t, run(t, f, "keycloak.client.list", map[string]any{}))
	rows := map[string][]string{}
	for _, row := range tbl.Rows {
		rows[row[0]] = row
	}
	if spa := rows["spa"]; spa[1] != "public" || spa[2] != "standard, implicit, direct-access" || spa[3] != "none" {
		t.Errorf("spa = %v", spa)
	}
	if console := rows["account-console"]; console[3] != "S256" {
		t.Errorf("account-console = %v", console)
	}
	if rta := rows["rta-audit"]; rta[1] != "confidential" || rta[2] != "service-account" {
		t.Errorf("rta-audit = %v", rta)
	}
}

func TestClientShowPagesTheClientAndItsServiceAccount(t *testing.T) {
	f := newFakeKeycloak(t)
	spa := sections(t, run(t, f, "keycloak.client.show", map[string]any{"client": "spa"}))
	if p := pairs(t, spa["client"]); p["pkce"] != "not enforced" || p["flows"] != "standard, implicit, direct-access" {
		t.Errorf("client = %v", p)
	}
	if redirects := table(t, spa["redirects"]); len(redirects.Rows) != 3 {
		t.Errorf("redirects = %v", redirects.Rows)
	}
	if _, ok := spa["service-account"]; ok {
		t.Error("a public client has no service account")
	}

	rta := sections(t, run(t, f, "keycloak.client.show", map[string]any{"client": "rta-audit"}))
	sa, ok := rta["service-account"]
	if !ok {
		t.Fatal("no service-account section for a client with one")
	}
	found := false
	for _, row := range table(t, sa).Rows {
		if row[0] == "realm-management" && row[1] == "view-users" {
			found = true
		}
	}
	if !found {
		t.Errorf("service account roles = %v", table(t, sa).Rows)
	}
}

func TestRoleListReadsARealmOrAClient(t *testing.T) {
	f := newFakeKeycloak(t)
	realm := table(t, run(t, f, "keycloak.role.list", map[string]any{}))
	if len(realm.Rows) != 3 {
		t.Errorf("realm roles = %v", realm.Rows)
	}
	client := table(t, run(t, f, "keycloak.role.list", map[string]any{"client": "realm-management"}))
	if len(client.Rows) != 22 {
		t.Errorf("%d realm-management roles, want 22", len(client.Rows))
	}
	for _, row := range client.Rows {
		if strings.HasPrefix(row[2], "${") {
			t.Errorf("a translation key leaked as a description: %v", row)
		}
		if row[0] == "realm-admin" && row[1] != "yes" {
			t.Errorf("realm-admin is a composite: %v", row)
		}
	}
}

func TestFlowListShowsWhatEachFlowIsBoundTo(t *testing.T) {
	f := newFakeKeycloak(t)
	tbl := table(t, run(t, f, "keycloak.flow.list", map[string]any{}))
	bound := map[string]string{}
	for _, row := range tbl.Rows {
		bound[row[0]] = row[1]
	}
	if bound["browser"] != "browser" || bound["direct grant"] != "direct grant" || bound["docker auth"] != "" {
		t.Errorf("bindings = %v", bound)
	}
}

func TestFlowShowNestsTheStepsTheWayTheConsoleDoes(t *testing.T) {
	f := newFakeKeycloak(t)
	tree, ok := run(t, f, "keycloak.flow.show", map[string]any{"flow": "browser"}).(view.Tree)
	if !ok {
		t.Fatal("not a tree")
	}
	if len(tree.Roots) != 5 {
		t.Fatalf("%d roots, want 5: %v", len(tree.Roots), tree.Roots)
	}
	forms := tree.Roots[4]
	if forms.Label != "forms" || len(forms.Children) != 2 {
		t.Fatalf("forms = %+v", forms)
	}
	twoFA := forms.Children[1]
	if twoFA.Label != "Browser - Conditional 2FA" || twoFA.Detail != "conditional" || len(twoFA.Children) != 5 {
		t.Errorf("2FA sub-flow = %+v", twoFA)
	}
}

func TestSessionListDefaultsToCountsPerClient(t *testing.T) {
	f := newFakeKeycloak(t)
	tbl := table(t, run(t, f, "keycloak.session.list", map[string]any{}))
	if len(tbl.Columns) != 3 || tbl.Columns[1].Kind != view.KindNumber {
		t.Errorf("columns = %v", tbl.Columns)
	}
	byUser := table(t, run(t, f, "keycloak.session.list", map[string]any{"user": "alice"}))
	if byUser.Columns[0].Name != "User" {
		t.Errorf("user sessions columns = %v", byUser.Columns)
	}
}

func TestEventsNameTheUserByUsernameWhenTheEventCarriesIt(t *testing.T) {
	f := newFakeKeycloak(t)
	tbl := table(t, run(t, f, "keycloak.event.list", map[string]any{}))
	if len(tbl.Rows) == 0 {
		t.Fatal("no events")
	}
	if tbl.Rows[0][1] != "CLIENT_LOGIN" || tbl.Rows[0][2] != "service-account-rta-audit" {
		t.Errorf("first event = %v", tbl.Rows[0])
	}
	admin := table(t, run(t, f, "keycloak.event.admin", map[string]any{}))
	if len(admin.Rows) == 0 || admin.Rows[0][1] != "CREATE" {
		t.Errorf("admin events = %v", admin.Rows)
	}
}

// A caller-supplied name becomes one path segment, never a path. A flow
// called "a/b" is looked up as a%2Fb; anything else would let a name reach
// a different endpoint.
func TestCallerNamesAreEscapedIntoOneSegment(t *testing.T) {
	f := newFakeKeycloak(t)
	_, _ = capability(t, "keycloak.flow.show").Run(t.Context(), reqAt(t, f, "keycloak.flow.show", map[string]any{"flow": "../clients"}))
	found := false
	for _, p := range f.paths() {
		if strings.Contains(p, "/authentication/flows/..%2Fclients/executions") {
			found = true
		}
		if strings.Contains(p, "/authentication/flows/../") {
			t.Errorf("a name escaped its segment: %s", p)
		}
	}
	if !found {
		t.Errorf("the escaped path was not requested: %v", f.paths())
	}
}

func TestExecutionTreeHandlesADepthJump(t *testing.T) {
	// A step claiming level 3 right after a root cannot nest three deep;
	// it is clamped to the depth that exists rather than dropped or panicked.
	tree := executionTree([]executionRep{
		{DisplayName: "root", Level: 0, Requirement: "REQUIRED", AuthenticationFlow: true},
		{DisplayName: "deep", Level: 3, Requirement: "REQUIRED"},
	}).(view.Tree)
	if len(tree.Roots) != 1 || len(tree.Roots[0].Children) != 1 || tree.Roots[0].Children[0].Label != "deep" {
		t.Errorf("tree = %+v", tree.Roots)
	}
}
