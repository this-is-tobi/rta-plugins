package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/findings"
	"github.com/this-is-tobi/rta/pkg/view"
)

// graded reads a compact audit table into check → (status, detail,
// reference), so a test says what a finding means rather than indexing a
// row. A check that appears more than once (a client with two redirect
// URIs) keeps its worst status.
type grade struct{ status, detail, ref string }

// A compact table or a detail page both read; the page is the one to use
// when a test asserts on the detail's wording, since the compact table
// clips a detail to one line and the page to a paragraph.
func graded(t *testing.T, v view.View) map[string]grade {
	t.Helper()
	var tables []view.Table
	switch v := v.(type) {
	case view.Table:
		tables = append(tables, v)
	case view.Sections:
		for _, s := range v.Items {
			if tbl, ok := s.View.(view.Table); ok && s.ID != "references" {
				tables = append(tables, tbl)
			}
		}
	default:
		t.Fatalf("audit returned %T, want a table or sections", v)
	}
	out := map[string]grade{}
	for _, tbl := range tables {
		idx := map[string]int{}
		for i, c := range tbl.Columns {
			idx[c.Name] = i
		}
		for _, row := range tbl.Rows {
			g := grade{status: row[idx["Status"]], detail: row[idx["Detail"]], ref: row[idx["Reference"]]}
			if prev, seen := out[row[idx["Check"]]]; seen && rank(prev.status) >= rank(g.status) {
				continue
			}
			out[row[idx["Check"]]] = g
		}
	}
	return out
}

func rank(status string) int {
	switch status {
	case findings.Fail:
		return 3
	case findings.Warn:
		return 2
	case findings.OK:
		return 1
	}
	return 0
}

func expect(t *testing.T, got map[string]grade, check, status, contains string) {
	t.Helper()
	g, ok := got[check]
	if !ok {
		t.Errorf("no %q finding", check)
		return
	}
	if g.status != status {
		t.Errorf("%s graded %q, want %q (%s)", check, g.status, status, g.detail)
	}
	if contains != "" && !strings.Contains(g.detail, contains) {
		t.Errorf("%s detail %q does not say %q", check, g.detail, contains)
	}
}

// The fixture realm is a Keycloak 26.7 realm as `start-dev` creates it,
// plus one public client with every unsafe setting on, one user with an
// OTP and two without, and an administrator. This is what the audit says
// about it.
func TestAuditGradesTheFixtureRealm(t *testing.T) {
	f := newFakeKeycloak(t)
	expect(t, graded(t, run(t, f, "keycloak.audit", map[string]any{})), "overall", findings.Fail, "failing")
	got := graded(t, run(t, f, "keycloak.audit", map[string]any{"detail": true}))

	expect(t, got, "ssl-required", findings.Warn, "external")
	expect(t, got, "self-registration", findings.OK, "closed")
	expect(t, got, "version", findings.Info, "not visible")
	expect(t, got, "browser-flow", findings.Warn, "OTP Form runs only for users who configured one")
	expect(t, got, "otp-setup", findings.Info, "not required of new ones")
	expect(t, got, "coverage", findings.Warn, "1 of 3 enabled users")
	expect(t, got, "detection", findings.Fail, "off")
	expect(t, got, "policy", findings.Fail, "none")
	expect(t, got, "spa/redirect", findings.Warn, "wildcard")
	expect(t, got, "spa/origin", findings.Fail, "web origin *")
	expect(t, got, "spa/implicit", findings.Warn, "implicit flow on")
	expect(t, got, "spa/direct-access", findings.Warn, "ROPC")
	expect(t, got, "spa/pkce", findings.Fail, "without PKCE")
	expect(t, got, "spa/full-scope", findings.Info, "every role")
	expect(t, got, "admin-cli/direct-access", findings.Warn, "kcadm")
	expect(t, got, "rta-audit/service-account", findings.Info, "view-users")
	expect(t, got, "access-token", findings.OK, "5m")
	expect(t, got, "sso-idle", findings.OK, "30m")
	expect(t, got, "sso-max", findings.OK, "10h")
	expect(t, got, "offline-tokens", findings.Warn, "30d")
	expect(t, got, "refresh-rotation", findings.Warn, "do not rotate")
	expect(t, got, "login-events", findings.OK, "default event types")
	expect(t, got, "admin-events", findings.OK, "details off")
	expect(t, got, "listeners", findings.Info, "jboss-logging")
	expect(t, got, "realm-admin", findings.Info, "1 account holds it directly: alice")

	// Keycloak's own clients are not graded for what Keycloak set on them:
	// a relative redirect URI is the server's own origin, and the
	// consoles' PKCE and scope are not the realm owner's to change.
	for _, absent := range []string{"account/redirect", "account/pkce", "account-console/redirect",
		"security-admin-console/redirect", "security-admin-console/full-scope", "admin-cli/full-scope"} {
		if g, ok := got[absent]; ok {
			t.Errorf("%s graded %q — a built-in client's own setting is not a finding", absent, g.status)
		}
	}
	// The coverage check asked about the users the listing could not
	// answer for, and only those.
	asked := 0
	for _, p := range f.paths() {
		if strings.HasSuffix(strings.SplitN(p, "?", 2)[0], "/credentials") {
			asked++
		}
	}
	if asked != 4 {
		t.Errorf("credentials were fetched %d times, want 2 per run for the 2 users without an OTP", asked)
	}
}

func TestAMasterRealmClientSeesTheVersion(t *testing.T) {
	f := newFakeKeycloak(t)
	f.admin = true
	got := graded(t, run(t, f, "keycloak.audit", map[string]any{}))
	expect(t, got, "version", findings.Info, "Keycloak 26.7.3")
}

// Every warning and failure names the control it is about; a report that
// grades without citing is an opinion. Info rows may be bare — "not
// visible to this client" is a fact, not a weakness.
func TestEveryWarningOrFailureCitesAControl(t *testing.T) {
	f := newFakeKeycloak(t)
	for check, g := range graded(t, run(t, f, "keycloak.audit", map[string]any{})) {
		if check == "overall" {
			continue
		}
		if (g.status == findings.Warn || g.status == findings.Fail) && g.ref == "" {
			t.Errorf("%s is %s and cites nothing", check, g.status)
		}
	}
}

// The detail page is the same findings grouped, in the order a hardening
// pass works through them, and every finding lands in a group the page
// renders — a finding in an undeclared group would silently vanish there.
func TestTheDetailPageGroupsEveryFinding(t *testing.T) {
	f := newFakeKeycloak(t)
	page, ok := run(t, f, "keycloak.audit", map[string]any{"detail": true}).(view.Sections)
	if !ok {
		t.Fatal("--detail did not return sections")
	}
	declared := map[string]bool{"summary": true, "references": true}
	for _, g := range auditGroupOrder {
		declared[g.ID] = true
	}
	var ids []string
	for _, s := range page.Items {
		ids = append(ids, s.ID)
		if !declared[s.ID] {
			t.Errorf("section %q is not a declared group", s.ID)
		}
	}
	if ids[0] != "summary" || ids[len(ids)-1] != "references" {
		t.Errorf("sections = %v, want summary first and references last", ids)
	}
	compact := graded(t, run(t, f, "keycloak.audit", map[string]any{}))
	total := 0
	for _, s := range page.Items {
		if s.ID == "summary" || s.ID == "references" {
			continue
		}
		total += len(s.View.(view.Table).Rows)
	}
	if total != len(compact)-1+duplicates(t, f) {
		t.Errorf("the page holds %d findings, the compact table %d", total, len(compact)-1)
	}
}

// duplicates counts the rows the compact map collapsed (one check name,
// several rows), so the page total can be compared to it.
func duplicates(t *testing.T, f *fakeKeycloak) int {
	t.Helper()
	tbl := run(t, f, "keycloak.audit", map[string]any{}).(view.Table)
	seen := map[string]bool{}
	dup := 0
	for _, row := range tbl.Rows {
		if seen[row[0]] {
			dup++
		}
		seen[row[0]] = true
	}
	return dup
}

// --- the graders, on their own ------------------------------------------------

func browserExecutions(t *testing.T) []executionRep {
	t.Helper()
	var execs []executionRep
	if err := json.Unmarshal(fixture("flow-browser-executions.json"), &execs); err != nil {
		t.Fatal(err)
	}
	return execs
}

func TestGradeBrowserFlowReadsTheTreeTheConsoleDraws(t *testing.T) {
	setReq := func(execs []executionRep, name, requirement string) {
		for i := range execs {
			if execs[i].DisplayName == name {
				execs[i].Requirement = requirement
			}
		}
	}

	t.Run("the default: conditional 2FA is offered", func(t *testing.T) {
		status, detail := gradeBrowserFlow(browserExecutions(t))
		if status != findings.Warn || !strings.Contains(detail, "offered, not required") {
			t.Errorf("graded %q: %s", status, detail)
		}
	})
	t.Run("the sub-flow made required enforces it", func(t *testing.T) {
		execs := browserExecutions(t)
		setReq(execs, "Browser - Conditional 2FA", "REQUIRED")
		setReq(execs, "OTP Form", "REQUIRED")
		status, detail := gradeBrowserFlow(execs)
		if status != findings.OK || !strings.Contains(detail, "OTP Form") {
			t.Errorf("graded %q: %s", status, detail)
		}
	})
	t.Run("a required step under a conditional sub-flow is still only offered", func(t *testing.T) {
		execs := browserExecutions(t)
		setReq(execs, "OTP Form", "REQUIRED")
		if status, _ := gradeBrowserFlow(execs); status != findings.Warn {
			t.Errorf("graded %q, want warn — the parent is still conditional", status)
		}
	})
	t.Run("every second-factor step disabled", func(t *testing.T) {
		execs := browserExecutions(t)
		setReq(execs, "OTP Form", "DISABLED")
		if status, _ := gradeBrowserFlow(execs); status != findings.Fail {
			t.Errorf("graded %q, want fail", status)
		}
	})
	t.Run("WebAuthn required instead of OTP counts the same", func(t *testing.T) {
		execs := browserExecutions(t)
		setReq(execs, "Browser - Conditional 2FA", "REQUIRED")
		setReq(execs, "OTP Form", "DISABLED")
		setReq(execs, "WebAuthn Authenticator", "REQUIRED")
		status, detail := gradeBrowserFlow(execs)
		if status != findings.OK || !strings.Contains(detail, "WebAuthn") {
			t.Errorf("graded %q: %s", status, detail)
		}
	})
}

func TestPasswordPolicyIsParsedAndGraded(t *testing.T) {
	rules := passwordRules("length(12) and digits(1) and notUsername(undefined) and passwordHistory(3)")
	if rules["length"] != "12" || rules["digits"] != "1" || rules["passwordHistory"] != "3" {
		t.Errorf("rules = %v", rules)
	}
	if _, ok := rules["notUsername"]; !ok {
		t.Error("notUsername not parsed")
	}

	grade := func(policy string) map[string]grade {
		r := &findings.Report{}
		auditPasswords(r, realmRep{PasswordPolicy: policy})
		return graded(t, r.Table(false))
	}
	weak := grade("length(8)")
	expect(t, weak, "length", findings.Warn, "12 or more")
	expect(t, weak, "reuse", findings.Warn, "passwordHistory")
	expect(t, weak, "not-username", findings.Warn, "notUsername")
	expect(t, grade("length(6)"), "length", findings.Fail, "")
	strong := grade("length(14) and passwordHistory(5) and notUsername(undefined)")
	expect(t, strong, "length", findings.OK, "14")
	expect(t, strong, "reuse", findings.OK, "")
	if _, flagged := strong["not-username"]; flagged {
		t.Error("notUsername present and still flagged")
	}
	expect(t, grade("digits(1)"), "length", findings.Warn, "no minimum length")
}

func TestClientRedirectsAndOriginsAreGraded(t *testing.T) {
	grade := func(c clientRep) map[string]grade {
		c.ClientID, c.Enabled = "app", true
		r := &findings.Report{}
		auditClient(r, c)
		return graded(t, r.Table(false))
	}
	expect(t, grade(clientRep{RedirectURIs: []string{"*"}}), "app/redirect", findings.Fail, "anywhere")
	expect(t, grade(clientRep{RedirectURIs: []string{"http://app.example.com/cb"}}), "app/redirect", findings.Fail, "in the clear")
	expect(t, grade(clientRep{RedirectURIs: []string{"http://localhost:3000/*"}}), "app/redirect", findings.Warn, "wildcard")
	expect(t, grade(clientRep{WebOrigins: []string{"*"}}), "app/origin", findings.Fail, "")
	for _, clean := range []clientRep{
		{RedirectURIs: []string{"https://app.example.com/cb"}},
		{RedirectURIs: []string{"/realms/x/account/*"}},
		{WebOrigins: []string{"+"}},
		{RedirectURIs: []string{"http://localhost:3000/cb", "http://127.0.0.1/cb"}},
	} {
		if got := grade(clean); len(got) != 0 {
			t.Errorf("%v graded %v, want nothing", clean, got)
		}
	}
	if got := grade(clientRep{PublicClient: true, StandardFlowEnabled: true, Attributes: map[string]string{"pkce.code.challenge.method": "plain"}}); got["app/pkce"].status != findings.Warn {
		t.Errorf("plain PKCE graded %v", got["app/pkce"])
	}
	if got := grade(clientRep{PublicClient: false, StandardFlowEnabled: true}); len(got) != 0 {
		t.Errorf("a confidential client without PKCE graded %v, want nothing — PKCE is the public client's control", got)
	}
}

func TestTokenLifetimesAreGradedInBands(t *testing.T) {
	grade := func(realm realmRep) map[string]grade {
		r := &findings.Report{}
		auditTokens(r, realm)
		return graded(t, r.Table(false))
	}
	expect(t, grade(realmRep{AccessTokenLifespan: 7200}), "access-token", findings.Warn, "2h")
	expect(t, grade(realmRep{AccessTokenLifespan: 172800}), "access-token", findings.Fail, "2d")
	expect(t, grade(realmRep{SSOSessionMaxLifespan: 90 * 86400}), "sso-max", findings.Fail, "90d")
	expect(t, grade(realmRep{RevokeRefreshToken: true, RefreshTokenMaxReuse: 1}), "refresh-rotation", findings.OK, "reuse allowed 1")
	expect(t, grade(realmRep{OfflineSessionMaxLifespanEnabled: true, OfflineSessionMaxLifespan: 5184000}), "offline-tokens", findings.OK, "60d")
}

func TestCoverageNamesWhoIsWithout(t *testing.T) {
	if status, detail := gradeCoverage(0, 0, nil); status != findings.Info || !strings.Contains(detail, "no enabled users") {
		t.Errorf("%q: %s", status, detail)
	}
	if status, _ := gradeCoverage(3, 3, nil); status != findings.OK {
		t.Errorf("full coverage graded %q", status)
	}
	if status, detail := gradeCoverage(3, 0, []string{"c", "a", "b"}); status != findings.Fail || !strings.Contains(detail, "without: a, b, c") {
		t.Errorf("%q: %s", status, detail)
	}
	many := []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7"}
	if status, detail := gradeCoverage(10, 3, many); status != findings.Warn || !strings.Contains(detail, "and 2 more") {
		t.Errorf("%q: %s", status, detail)
	}
}

func TestSpanReadsInTheUnitAPersonUses(t *testing.T) {
	for n, want := range map[int]string{0: "0s", 90: "1m30s", 300: "5m", 36000: "10h", 2592000: "30d", 5184000: "60d"} {
		if got := span(n); got != want {
			t.Errorf("span(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestBuiltinClientsAreKeycloaksOwn(t *testing.T) {
	for _, id := range []string{"account", "admin-cli", "realm-management", "security-admin-console", "demo-realm"} {
		if !builtinClient(id) {
			t.Errorf("%s not recognised as built in", id)
		}
	}
	for _, id := range []string{"spa", "rta-audit", "realm"} {
		if builtinClient(id) {
			t.Errorf("%s taken for a built-in", id)
		}
	}
}

// A Source/Control citation carries the link to where its text is read —
// an RFC section, a guide's heading — so the references section of a
// detail page answers "where does it say that" for every row, not only
// the CWE ones. Pinned per reference, because a link is the citation with
// a click attached and a missing one is the easy thing to forget.
func TestEverySourceCitationLinksToWhereItIsRead(t *testing.T) {
	for _, ref := range []findings.Reference{refImplicit, refROPC, refPKCE, refRefresh, refMasterRealm, refBootstrapAdmin} {
		if !strings.HasPrefix(ref.URL(), "https://") {
			t.Errorf("%s links to %q", ref, ref.URL())
		}
	}
	f := newFakeKeycloak(t)
	page := run(t, f, "keycloak.audit", map[string]any{"detail": true}).(view.Sections)
	for _, s := range page.Items {
		if s.ID != "references" {
			continue
		}
		for _, row := range s.View.(view.Table).Rows {
			if row[2] == "" {
				t.Errorf("reference %q has no lookup", row[0])
			}
		}
	}
}

// **An audit's job is to say what it found, and a check it could not run is
// not a check that passed.**
//
// The service account rta itself audits with holds five separate view-*
// roles, and a realm where it holds four of them is the ordinary
// half-provisioned setup rather than a broken server. Every read in this
// file says so when it fails — "the browser flow could not be read", "users
// could not be listed", "clients could not be listed" — except the three
// below, which returned early, skipped the row, or answered false. The
// audit then graded the realm on what it had managed to look at, and said
// nothing about the rest.
func TestAServiceAccountThatCouldNotBeReadIsNotCountedClean(t *testing.T) {
	f := newFakeKeycloak(t)
	f.deny = []string{"/service-account-user"}
	got := graded(t, run(t, f, "keycloak.audit", map[string]any{"detail": true}))

	g, ok := got["rta-audit/service-account"]
	if !ok {
		t.Fatal("a service account that could not be read produced no finding — the client counts as clean")
	}
	if !strings.Contains(g.detail, "could not be read") {
		t.Errorf("service-account detail = %q, want it to say the read failed", g.detail)
	}
	// And the clean tally cannot count a client whose service account was
	// never examined: a finding was recorded, so it is not one of them.
	if clean, ok := got["clean"]; ok && strings.HasPrefix(clean.detail, "4 clients") {
		t.Errorf("clean = %q — a client nobody examined was counted as having no findings", clean.detail)
	}
}

func TestARoleWhoseHoldersCannotBeReadSaysSo(t *testing.T) {
	f := newFakeKeycloak(t)
	f.deny = []string{"/roles/realm-admin/users"}
	got := graded(t, run(t, f, "keycloak.audit", map[string]any{"detail": true}))
	expect(t, got, "realm-admin", findings.Info, "could not be read")
}

// The MFA coverage count is a sentence with two exact numbers in it, so a
// user whose credentials could not be read has to leave the count rather
// than land silently on the "without a second factor" side of it — and be
// named, since "who is not covered" is the question the row exists to
// answer.
func TestUsersWhoseCredentialsCannotBeReadLeaveTheCoverageCount(t *testing.T) {
	f := newFakeKeycloak(t)
	f.deny = []string{"/credentials"}
	got := graded(t, run(t, f, "keycloak.audit", map[string]any{"detail": true}))

	// The fixture realm has 3 enabled users: one holds an OTP, and alice
	// and bob do not — their WebAuthn answer is what the denial hides.
	expect(t, got, "coverage", findings.OK, "1 of 1 enabled users")
	g, ok := got["coverage-unread"]
	if !ok {
		t.Fatal("two users whose credentials could not be read were counted as having no second factor")
	}
	if !strings.Contains(g.detail, "could not be read") {
		t.Errorf("coverage-unread detail = %q, want it to say the credentials could not be read", g.detail)
	}
	for _, who := range []string{"alice", "bob"} {
		if !strings.Contains(g.detail, who) {
			t.Errorf("coverage-unread detail = %q, want it to name %s", g.detail, who)
		}
	}
}
