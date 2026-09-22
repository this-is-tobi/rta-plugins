package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/findings"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// keycloak.audit grades one realm against named controls, the way
// rta's own `audit web` grades a host: every finding cites the weakness
// it is about, the compact view is the grade, the detail page is the
// work list. It is the reason this plugin exists — the reads above are
// what an owner looks at; this is what a reviewer asks for.
//
// The checks are the ones a realm gets wrong by default. Keycloak's
// defaults are not unsafe so much as unfinished: brute-force detection
// off, no password policy, a second factor offered but never required,
// login events not recorded, SSL required only from outside, refresh
// tokens reusable. A realm that has never been hardened fails most of
// these, and a realm somebody hardened once and forgot fails a few —
// which is what makes it worth running on a schedule rather than once.
//
// Read-only and one pass: every question is answered from the realm
// representation, the client list, the browser flow and a bounded page of
// users. The one place it fans out is the second-factor coverage check,
// which has to ask each user without an OTP whether they have a WebAuthn
// credential instead; --max bounds that, and the report says when it
// stopped short.

var (
	grpRealm      = findings.Group{ID: "realm", Title: "realm"}
	grpMFA        = findings.Group{ID: "mfa", Title: "second factor"}
	grpBruteForce = findings.Group{ID: "brute-force", Title: "brute-force detection"}
	grpPasswords  = findings.Group{ID: "passwords", Title: "password policy"}
	grpClients    = findings.Group{ID: "clients", Title: "clients"}
	grpTokens     = findings.Group{ID: "tokens", Title: "tokens and sessions"}
	grpEvents     = findings.Group{ID: "events", Title: "event logging"}
	grpAdmins     = findings.Group{ID: "admins", Title: "administrators"}
)

var auditGroupOrder = []findings.Group{grpRealm, grpMFA, grpBruteForce, grpPasswords, grpClients, grpTokens, grpEvents, grpAdmins}

func auditCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.audit",
		Summary:    "Grade the realm: second factor, brute force, passwords, clients, tokens, events, admins — each against a named control",
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Description: "Reads the realm and grades what it finds: whether a second factor is required " +
			"or merely offered and how many users have one; brute-force detection; the password " +
			"policy; every client's grants, redirect URIs, origins, PKCE and service-account " +
			"roles; token and session lifetimes and refresh-token rotation; whether login and " +
			"admin events are recorded; SSL requirement, self-registration, the master realm and " +
			"the bootstrap admin; and who holds realm-admin. Every finding cites an OWASP Top 10 " +
			"category and CWE, or RFC 9700 (OAuth 2.0 Security BCP), or the Keycloak guide. " +
			"Compact by default; --detail is the work list, grouped, with the references at the end.",
		Run: runAudit,
	},
		maxField(200, 5000, "how many users to examine for a second factor"),
	)
}

func runAudit(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		var realm realmRep
		if verr := s.get(ctx, "", nil, &realm); verr != nil {
			return nil, verr
		}
		r := &findings.Report{}
		version := s.serverVersion(ctx)
		auditRealm(r, realm, s.realm == "master", version)
		if s.realm == "master" {
			s.auditBootstrapAdmin(ctx, r)
		}
		s.auditSecondFactor(ctx, r, realm, req.Int("max"))
		auditBruteForce(r, realm)
		auditPasswords(r, realm)
		s.auditClients(ctx, r, s.realm == "master")
		auditTokens(r, realm)
		auditEvents(r, realm)
		s.auditAdmins(ctx, r)

		if !req.Bool("detail") {
			return r.Table(true), nil
		}
		summary := []view.Pair{{Key: "realm", Value: s.realm + " on " + s.base}}
		if version != "" {
			summary = append(summary, view.Pair{Key: "keycloak", Value: version})
		}
		summary = append(summary, r.Grade()...)
		return r.Page(ctx, req, auditGroupOrder, view.KeyValue{Pairs: summary}), nil
	})
}

// --- realm -----------------------------------------------------------------

func auditRealm(r *findings.Report, realm realmRep, master bool, version string) {
	switch realm.SSLRequired {
	case "all":
		r.Add(grpRealm, "ssl-required", findings.OK, "all — every request must use TLS", refCleartext)
	case "none":
		r.Add(grpRealm, "ssl-required", findings.Fail,
			"none — logins and tokens travel in the clear from anywhere", refCleartext)
	default:
		r.Add(grpRealm, "ssl-required", findings.Warn,
			"external (Keycloak's default) — requests from private addresses may use HTTP, which "+
				"includes a reverse proxy on the same network; \"all\" is the setting for a server "+
				"that is only ever reached over TLS", refCleartext)
	}

	switch {
	case realm.RegistrationAllowed && !realm.VerifyEmail:
		r.Add(grpRealm, "self-registration", findings.Info,
			"open, and email addresses are not verified — anyone can create an account under any address",
			findings.Reference{})
	case realm.RegistrationAllowed:
		r.Add(grpRealm, "self-registration", findings.Info, "open — anyone can create an account; verified by email",
			findings.Reference{})
	default:
		r.Add(grpRealm, "self-registration", findings.OK, "closed — accounts are created by an administrator or federated in",
			findings.Reference{})
	}

	if master {
		r.Add(grpRealm, "master-realm", findings.Info,
			"this is the master realm — it administers the server; applications and their users belong in a realm of their own",
			refMasterRealm)
	}

	if version != "" {
		r.Add(grpRealm, "version", findings.Info,
			"Keycloak "+version+" — `rta eol check keycloak` says whether that release is still supported",
			findings.Reference{})
	} else {
		r.Add(grpRealm, "version", findings.Info,
			"not visible to this client — /admin/serverinfo answers in full to a master-realm client only",
			findings.Reference{})
	}
}

// auditBootstrapAdmin looks for the temporary administrator a first start
// creates. Keycloak marks it with the is_temporary_admin attribute and
// tells the operator, on every console login, to replace it with a
// permanent account and delete it; the console is the only place that
// says so, which is why an audit repeats it.
func (s *session) auditBootstrapAdmin(ctx context.Context, r *findings.Report) {
	var users []userRep
	q := query("q", "is_temporary_admin:true", "briefRepresentation", "false", "max", "10")
	if verr := s.get(ctx, "users", q, &users); verr != nil {
		r.Add(grpRealm, "bootstrap-admin", findings.Info, "could not be checked: "+verr.Message, findings.Reference{})
		return
	}
	var names []string
	for _, u := range users {
		if len(u.Attributes["is_temporary_admin"]) > 0 && u.Attributes["is_temporary_admin"][0] == "true" {
			names = append(names, u.Username)
		}
	}
	if len(names) == 0 {
		r.Add(grpRealm, "bootstrap-admin", findings.OK, "the temporary bootstrap administrator has been removed", refBootstrapAdmin)
		return
	}
	r.Add(grpRealm, "bootstrap-admin", findings.Fail,
		"the temporary bootstrap administrator still exists: "+strings.Join(names, ", ")+
			" — create a permanent administrator and delete it", refBootstrapAdmin)
}

// --- second factor -----------------------------------------------------------

// secondFactorSteps are the authenticators that ask for something beyond a
// password. The recovery-code form counts: it is the fallback for a lost
// OTP device and is only reachable once a second factor exists.
var secondFactorSteps = map[string]bool{
	"auth-otp-form":                       true,
	"auth-conditional-otp-form":           true,
	"webauthn-authenticator":              true,
	"webauthn-authenticator-passwordless": true,
	"auth-recovery-authn-code-form":       true,
}

func (s *session) auditSecondFactor(ctx context.Context, r *findings.Report, realm realmRep, max int) {
	execs, verr := s.executions(ctx, realm.BrowserFlow)
	if verr != nil {
		r.Add(grpMFA, "browser-flow", findings.Info, "the browser flow could not be read: "+verr.Message, findings.Reference{})
	} else {
		status, detail := gradeBrowserFlow(execs)
		r.Add(grpMFA, "browser-flow", status, detail, refMFA)
	}

	var actions []requiredActionRep
	if verr := s.get(ctx, "authentication/required-actions", nil, &actions); verr != nil {
		r.Add(grpMFA, "otp-setup", findings.Info, "required actions could not be read: "+verr.Message, findings.Reference{})
	} else {
		status, detail, ref := gradeOTPSetup(actions)
		r.Add(grpMFA, "otp-setup", status, detail, ref)
	}

	users, verr := s.users(ctx, "", max)
	if verr != nil {
		r.Add(grpMFA, "coverage", findings.Info, "users could not be listed: "+verr.Message, findings.Reference{})
		return
	}
	var examined, with int
	var without, unread []string
	for _, u := range users {
		if !u.Enabled || u.ServiceAccountClientID != "" {
			continue
		}
		if u.TOTP {
			examined++
			with++
			continue
		}
		has, known := s.hasWebAuthn(ctx, u.ID)
		// A user whose credentials could not be read leaves the count
		// entirely rather than joining either side of it: "7 of 20" is a
		// claim about users whose status was established, and padding
		// either number with a guess makes the sentence false in a way its
		// two exact figures hide.
		if !known {
			unread = append(unread, u.Username)
			continue
		}
		examined++
		if has {
			with++
			continue
		}
		without = append(without, u.Username)
	}
	if examined > 0 || len(unread) == 0 {
		status, detail := gradeCoverage(examined, with, without)
		r.Add(grpMFA, "coverage", status, detail, refMFA)
	}
	if len(unread) > 0 {
		sort.Strings(unread)
		shown := unread
		if len(shown) > 5 {
			shown = shown[:5]
		}
		who := strings.Join(shown, ", ")
		if len(unread) > 5 {
			who += fmt.Sprintf(" and %d more", len(unread)-5)
		}
		r.Add(grpMFA, "coverage-unread", findings.Info,
			fmt.Sprintf("%s left out of the count above — their credentials could not be read: %s",
				findings.Plural(len(unread), "user"), who), findings.Reference{})
	}
	if len(users) >= max {
		r.Add(grpMFA, "coverage-bound", findings.Info,
			fmt.Sprintf("only the first %d users were examined — raise --max to cover the realm", max),
			findings.Reference{})
	}
}

// gradeBrowserFlow reads the executions the way the console draws them.
// A second-factor step is enforced when it is REQUIRED and nothing above
// it is CONDITIONAL; the default browser flow puts the OTP form under
// "Browser - Conditional 2FA", a conditional sub-flow whose condition is
// "the user configured one" — which offers a second factor and requires
// nothing.
func gradeBrowserFlow(execs []executionRep) (string, string) {
	var conditionalAt []bool // conditionalAt[level] — is the sub-flow at that level CONDITIONAL
	var enforced, offered []string
	for _, e := range execs {
		if e.Level < len(conditionalAt) {
			conditionalAt = conditionalAt[:e.Level]
		}
		if e.AuthenticationFlow {
			conditionalAt = append(conditionalAt, e.Requirement == "CONDITIONAL")
			continue
		}
		if !secondFactorSteps[e.ProviderID] || e.Requirement == "DISABLED" {
			continue
		}
		underConditional := false
		for _, c := range conditionalAt {
			underConditional = underConditional || c
		}
		if e.Requirement == "REQUIRED" && !underConditional {
			enforced = append(enforced, e.DisplayName)
		} else {
			offered = append(offered, e.DisplayName)
		}
	}
	switch {
	case len(enforced) > 0:
		return findings.OK, "a second factor is required of every browser login: " + strings.Join(enforced, ", ")
	case len(offered) > 0:
		return findings.Warn, "a second factor is offered, not required: " + strings.Join(offered, ", ") +
			" runs only for users who configured one (conditional sub-flow, Keycloak's default)"
	}
	return findings.Fail, "no second-factor step is enabled in the browser flow — a password is the whole login"
}

func gradeOTPSetup(actions []requiredActionRep) (string, string, findings.Reference) {
	for _, a := range actions {
		if a.Alias != "CONFIGURE_TOTP" {
			continue
		}
		switch {
		case a.Enabled && a.DefaultAction:
			return findings.OK, "OTP set-up is a default action — every new user configures one at first login", refMFA
		case a.Enabled:
			return findings.Info, "OTP set-up is available to users but not required of new ones (not a default action)", refMFA
		}
		return findings.Warn, "OTP set-up is disabled as a required action — users cannot be asked to configure one", refMFA
	}
	return findings.Info, "the CONFIGURE_TOTP required action is not registered in this realm", findings.Reference{}
}

func gradeCoverage(examined, with int, without []string) (string, string) {
	if examined == 0 {
		return findings.Info, "no enabled users to examine"
	}
	sort.Strings(without)
	shown := without
	if len(shown) > 5 {
		shown = shown[:5]
	}
	who := strings.Join(shown, ", ")
	if len(without) > 5 {
		who += fmt.Sprintf(" and %d more", len(without)-5)
	}
	detail := fmt.Sprintf("%d of %d enabled users have a second factor (OTP or WebAuthn)", with, examined)
	switch with {
	case examined:
		return findings.OK, detail
	case 0:
		return findings.Fail, detail + " — without: " + who
	}
	return findings.Warn, detail + " — without: " + who
}

// hasWebAuthn asks whether a user without an OTP has a WebAuthn credential
// instead — the one question the user listing cannot answer, and the
// reason the coverage check is bounded.
//
// known is what separates "this user has no second factor" from "this
// user's credentials could not be read". Returning only the bool folded the
// second into the first, and the coverage row then named that user in a
// list titled "without" — an exact-looking sentence about who is exposed,
// with somebody in it whose status nobody had established.
func (s *session) hasWebAuthn(ctx context.Context, id string) (has, known bool) {
	var creds []credentialRep
	if verr := s.get(ctx, "users/"+segment(id)+"/credentials", nil, &creds); verr != nil {
		return false, false
	}
	for _, c := range creds {
		switch c.Type {
		case "otp", "webauthn", "webauthn-passwordless":
			return true, true
		}
	}
	return false, true
}

// --- brute force -------------------------------------------------------------

func auditBruteForce(r *findings.Report, realm realmRep) {
	if !realm.BruteForceProtected {
		r.Add(grpBruteForce, "detection", findings.Fail,
			"off (Keycloak's default) — an attacker may guess passwords without limit", refBruteForce)
		return
	}
	detail := fmt.Sprintf("on — locks after %d failures", realm.FailureFactor)
	if realm.PermanentLockout {
		detail += ", permanently until an administrator unlocks the account"
	} else {
		detail += fmt.Sprintf(", for %s at first and up to %s",
			span(realm.WaitIncrementSeconds), span(realm.MaxFailureWaitSeconds))
	}
	r.Add(grpBruteForce, "detection", findings.OK, detail, refBruteForce)
}

// --- passwords ---------------------------------------------------------------

func auditPasswords(r *findings.Report, realm realmRep) {
	if strings.TrimSpace(realm.PasswordPolicy) == "" {
		r.Add(grpPasswords, "policy", findings.Fail, "none (Keycloak's default) — any password is accepted", refPasswords)
		return
	}
	rules := passwordRules(realm.PasswordPolicy)
	names := make([]string, 0, len(rules))
	for name := range rules {
		names = append(names, name)
	}
	sort.Strings(names)
	r.Add(grpPasswords, "policy", findings.Info, strings.Join(names, ", "), refPasswords)

	if n, ok := rules["length"]; !ok {
		r.Add(grpPasswords, "length", findings.Warn, "no minimum length in the policy", refPasswords)
	} else if v, err := strconv.Atoi(n); err == nil {
		switch {
		case v < 8:
			r.Add(grpPasswords, "length", findings.Fail, fmt.Sprintf("minimum length %d — below any current guidance", v), refPasswords)
		case v < 12:
			r.Add(grpPasswords, "length", findings.Warn, fmt.Sprintf("minimum length %d — 12 or more is the usual bar", v), refPasswords)
		default:
			r.Add(grpPasswords, "length", findings.OK, fmt.Sprintf("minimum length %d", v), refPasswords)
		}
	}
	if _, ok := rules["passwordHistory"]; ok {
		r.Add(grpPasswords, "reuse", findings.OK, "previous passwords may not be reused (passwordHistory)", refPasswords)
	} else {
		r.Add(grpPasswords, "reuse", findings.Warn, "a changed password may be changed back — no passwordHistory rule", refPasswords)
	}
	if _, ok := rules["notUsername"]; !ok {
		r.Add(grpPasswords, "not-username", findings.Warn, "the password may equal the username — no notUsername rule", refPasswords)
	}
}

// passwordRules parses Keycloak's policy string, "length(12) and digits(1)
// and notUsername(undefined)", into rule → argument.
func passwordRules(policy string) map[string]string {
	rules := map[string]string{}
	for _, part := range strings.Split(policy, " and ") {
		part = strings.TrimSpace(part)
		name, arg, _ := strings.Cut(part, "(")
		rules[strings.TrimSpace(name)] = strings.TrimSuffix(arg, ")")
	}
	return rules
}

// --- clients -----------------------------------------------------------------

// auditClients grades every enabled client's registration. One row per
// client and issue, so each carries the control it is about; a client with
// nothing to say gets no row, and the closing row counts them.
func (s *session) auditClients(ctx context.Context, r *findings.Report, master bool) {
	var clients []clientRep
	if verr := s.get(ctx, "clients", nil, &clients); verr != nil {
		r.Add(grpClients, "clients", findings.Info, "clients could not be listed: "+verr.Message, findings.Reference{})
		return
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].ClientID < clients[j].ClientID })
	clean := 0
	for _, c := range clients {
		if !c.Enabled {
			continue
		}
		before := len(r.Findings)
		auditClient(r, c)
		if c.ServiceAccountsEnabled {
			s.auditServiceAccount(ctx, r, c)
		}
		if len(r.Findings) == before {
			clean++
		}
	}
	if master {
		var apps []string
		for _, c := range clients {
			if !builtinClient(c.ClientID) {
				apps = append(apps, c.ClientID)
			}
		}
		if len(apps) > 0 {
			r.Add(grpClients, "master-realm", findings.Warn,
				"the master realm serves application clients: "+strings.Join(apps, ", ")+
					" — an application's users and an administrator's belong in different realms", refMasterRealm)
		}
	}
	r.Add(grpClients, "clean", findings.OK,
		fmt.Sprintf("%s without findings of %d enabled", findings.Plural(clean, "client"), enabledCount(clients)), findings.Reference{})
}

func enabledCount(clients []clientRep) int {
	n := 0
	for _, c := range clients {
		if c.Enabled {
			n++
		}
	}
	return n
}

// builtinClient is a client every realm has, or master's per-realm one
// (`<realm>-realm`, which administers that realm).
func builtinClient(id string) bool {
	switch id {
	case "account", "account-console", "admin-cli", "broker", "realm-management", "security-admin-console":
		return true
	}
	return strings.HasSuffix(id, "-realm")
}

// auditClient grades one client's registration. Keycloak's own clients
// are graded like any other with two exceptions a reviewer could not act
// on: a relative redirect URI (`/realms/<realm>/account/*`) is the server's
// own origin rather than a destination, and the consoles' PKCE and scope
// settings are Keycloak's to set, not the realm owner's.
func auditClient(r *findings.Report, c clientRep) {
	check := func(kind string) string { return c.ClientID + "/" + kind }
	builtin := builtinClient(c.ClientID)

	for _, u := range c.RedirectURIs {
		switch {
		case strings.HasPrefix(u, "/"):
		case u == "*" || u == "http://*" || u == "https://*":
			r.Add(grpClients, check("redirect"), findings.Fail,
				"redirect URI "+u+" — an authorization code can be sent anywhere", refRedirect)
		case strings.HasPrefix(u, "http://") && !localhostURL(u):
			r.Add(grpClients, check("redirect"), findings.Fail,
				"redirect URI "+u+" — the authorization code travels in the clear", refCleartext)
		case strings.Contains(u, "*"):
			r.Add(grpClients, check("redirect"), findings.Warn,
				"redirect URI "+u+" — a wildcard; RFC 9700 wants exact matching, and any path under it receives the code", refRedirect)
		}
	}
	for _, o := range c.WebOrigins {
		if o == "*" {
			r.Add(grpClients, check("origin"), findings.Fail,
				"web origin * — any site may make credentialed cross-origin requests to this client's endpoints", refCORS)
		}
	}
	if c.ImplicitFlowEnabled {
		r.Add(grpClients, check("implicit"), findings.Warn,
			"implicit flow on — tokens are issued in the front channel, where the browser history and referrers see them", refImplicit)
	}
	if c.DirectAccessGrantsEnabled {
		detail := "direct access grants on — the client may exchange a user's password for tokens (ROPC)"
		if c.ClientID == "admin-cli" {
			detail += "; Keycloak enables this on admin-cli by default, and it is how `kcadm` and an admin's password become an admin token"
		}
		r.Add(grpClients, check("direct-access"), findings.Warn, detail, refROPC)
	}
	if c.PublicClient && c.StandardFlowEnabled && !builtin {
		switch c.pkce() {
		case "S256":
		case "plain":
			r.Add(grpClients, check("pkce"), findings.Warn, "public client with PKCE plain — S256 is the method RFC 9700 names", refPKCE)
		default:
			r.Add(grpClients, check("pkce"), findings.Fail,
				"public client without PKCE — an intercepted authorization code is a token", refPKCE)
		}
	}
	if c.FullScopeAllowed && !c.BearerOnly && !builtin {
		r.Add(grpClients, check("full-scope"), findings.Info,
			"full scope allowed — every role the user holds is put in this client's tokens, whether it needs them or not", refPrivilege)
	}
}

func localhostURL(u string) bool {
	rest := strings.TrimPrefix(u, "http://")
	host, _, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, ":")
	return host == "localhost" || host == "127.0.0.1" || host == "[::1]"
}

// auditServiceAccount grades what a client's service account may do. A
// realm-management role is the one that matters: manage-* or realm-admin on
// a service account is an application that can administer the realm it
// lives in, and its secret is then an administrator credential wherever it
// is deployed.
// A read that fails is a row of its own, not an early return. Excessive
// service-account privilege is the finding this function exists to catch,
// so a client whose service account could not be examined must not reach
// auditClients' clean tally — which counts a client with no findings, and
// cannot otherwise tell one that was checked and found sound from one
// nobody managed to look at.
func (s *session) auditServiceAccount(ctx context.Context, r *findings.Report, c clientRep) {
	unread := func(what string, verr *view.Error) {
		r.Add(grpClients, c.ClientID+"/service-account", findings.Info,
			what+" could not be read: "+verr.Message+" — what it may do was not examined",
			findings.Reference{})
	}
	var sa userRep
	if verr := s.get(ctx, "clients/"+segment(c.ID)+"/service-account-user", nil, &sa); verr != nil {
		unread("its service account", verr)
		return
	}
	var mappings roleMappings
	if verr := s.get(ctx, "users/"+segment(sa.ID)+"/role-mappings", nil, &mappings); verr != nil {
		unread("its service account's roles", verr)
		return
	}
	var manage, viewer []string
	for _, m := range mappings.ClientMappings {
		if m.Client != "realm-management" && !strings.HasSuffix(m.Client, "-realm") {
			continue
		}
		for _, role := range m.Mappings {
			if role.Name == "realm-admin" || strings.HasPrefix(role.Name, "manage-") || role.Name == "impersonation" {
				manage = append(manage, role.Name)
			} else {
				viewer = append(viewer, role.Name)
			}
		}
	}
	for _, role := range mappings.RealmMappings {
		if role.Name == "admin" {
			manage = append(manage, "admin (realm role)")
		}
	}
	sort.Strings(manage)
	sort.Strings(viewer)
	switch {
	case len(manage) > 0:
		r.Add(grpClients, c.ClientID+"/service-account", findings.Warn,
			"its service account can administer the realm: "+strings.Join(manage, ", "), refExcessivePriv)
	case len(viewer) > 0:
		r.Add(grpClients, c.ClientID+"/service-account", findings.Info,
			"its service account can read the realm: "+strings.Join(viewer, ", "), refExcessivePriv)
	}
}

// --- tokens and sessions -----------------------------------------------------

func auditTokens(r *findings.Report, realm realmRep) {
	graded := func(check string, n int, okUpTo, warnUpTo time.Duration, what string) {
		d := time.Duration(n) * time.Second
		switch {
		case d <= okUpTo:
			r.Add(grpTokens, check, findings.OK, what+" "+span(n), refSession)
		case d <= warnUpTo:
			r.Add(grpTokens, check, findings.Warn, what+" "+span(n)+" — long for a token a leak turns into access", refSession)
		default:
			r.Add(grpTokens, check, findings.Fail, what+" "+span(n)+" — a leaked token stays valid for that long", refSession)
		}
	}
	graded("access-token", realm.AccessTokenLifespan, 15*time.Minute, 24*time.Hour, "access tokens live")
	graded("sso-idle", realm.SSOSessionIdleTimeout, 24*time.Hour, 30*24*time.Hour, "sessions idle out after")
	graded("sso-max", realm.SSOSessionMaxLifespan, 24*time.Hour, 30*24*time.Hour, "sessions end at the latest after")

	if realm.OfflineSessionMaxLifespanEnabled {
		r.Add(grpTokens, "offline-tokens", findings.OK,
			"offline tokens end at the latest after "+span(realm.OfflineSessionMaxLifespan), refSession)
	} else {
		r.Add(grpTokens, "offline-tokens", findings.Warn,
			"offline tokens have no maximum lifetime (Keycloak's default) — one used at least every "+
				span(realm.OfflineSessionIdleTimeout)+" never expires", refSession)
	}
	if realm.RevokeRefreshToken {
		r.Add(grpTokens, "refresh-rotation", findings.OK,
			fmt.Sprintf("refresh tokens rotate — a used one is revoked (reuse allowed %d times)", realm.RefreshTokenMaxReuse), refRefresh)
	} else {
		r.Add(grpTokens, "refresh-rotation", findings.Warn,
			"refresh tokens do not rotate (Keycloak's default) — a stolen one keeps working beside the real one", refRefresh)
	}
}

// --- events ------------------------------------------------------------------

func auditEvents(r *findings.Report, realm realmRep) {
	if realm.EventsEnabled {
		detail := "recorded — the default event types"
		if n := len(realm.EnabledEventTypes); n > 0 {
			detail = "recorded — " + findings.Plural(n, "event type")
		}
		if realm.EventsExpiration > 0 {
			detail += ", kept for " + span(int(realm.EventsExpiration))
		} else {
			detail += ", never expire"
		}
		r.Add(grpEvents, "login-events", findings.OK, detail, refLogging)
	} else {
		r.Add(grpEvents, "login-events", findings.Fail,
			"not recorded (Keycloak's default) — no trace of logins, failures or token refreshes", refLogging)
	}
	if realm.AdminEventsEnabled {
		detail := "recorded"
		if !realm.AdminEventsDetailsEnabled {
			detail += ", without the representation that changed (details off)"
		}
		r.Add(grpEvents, "admin-events", findings.OK, detail, refLogging)
	} else {
		r.Add(grpEvents, "admin-events", findings.Fail,
			"not recorded (Keycloak's default) — a client or role that appears has no record of who added it", refLogging)
	}
	listeners := append([]string(nil), realm.EventsListeners...)
	sort.Strings(listeners)
	forwarded := false
	for _, l := range listeners {
		if l != "jboss-logging" {
			forwarded = true
		}
	}
	if forwarded {
		r.Add(grpEvents, "listeners", findings.OK, strings.Join(listeners, ", "), refLogging)
	} else {
		r.Add(grpEvents, "listeners", findings.Info,
			"events go to the server log only ("+strings.Join(listeners, ", ")+") — nothing forwards them anywhere an alert could fire",
			refLogging)
	}
}

// --- administrators ----------------------------------------------------------

// auditAdmins names who holds the realm-management roles that administer
// the realm — directly assigned, which is what the API's per-role listing
// answers; a role reached through a group or a composite is not in it, and
// the detail says so.
func (s *session) auditAdmins(ctx context.Context, r *findings.Report) {
	rm, verr := s.client(ctx, "realm-management")
	if verr != nil {
		r.Add(grpAdmins, "realm-admin", findings.Info, "the realm-management client could not be read: "+verr.Message, findings.Reference{})
		return
	}
	for _, role := range []string{"realm-admin", "manage-users", "manage-clients", "manage-realm", "impersonation"} {
		var users []userRep
		if verr := s.get(ctx, "clients/"+segment(rm.ID)+"/roles/"+segment(role)+"/users", query("max", "100"), &users); verr != nil {
			// Said rather than skipped: this group answers "who administers
			// this realm", and a role whose holders could not be listed
			// leaves that question open — while a group with no rows at all
			// gets no heading, so silence here removes the question too.
			r.Add(grpAdmins, role, findings.Info,
				"who holds "+role+" could not be read: "+verr.Message, findings.Reference{})
			continue
		}
		if len(users) == 0 {
			if role == "realm-admin" {
				r.Add(grpAdmins, role, findings.Info, "nobody holds realm-admin directly", refExcessivePriv)
			}
			continue
		}
		names := make([]string, 0, len(users))
		serviceAccounts := 0
		for _, u := range users {
			names = append(names, u.Username)
			if strings.HasPrefix(u.Username, "service-account-") {
				serviceAccounts++
			}
		}
		sort.Strings(names)
		holds := "hold"
		if len(users) == 1 {
			holds = "holds"
		}
		detail := fmt.Sprintf("%s %s it directly: %s", findings.Plural(len(users), "account"), holds, strings.Join(names, ", "))
		status := findings.Info
		if serviceAccounts > 0 {
			status = findings.Warn
			detail += fmt.Sprintf(" — %s among them", findings.Plural(serviceAccounts, "service account"))
		}
		r.Add(grpAdmins, role, status, detail, refExcessivePriv)
	}
}
