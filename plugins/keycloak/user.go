package main

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The user reads describe who exists and how they authenticate — never
// what they authenticate with. A credential is listed by type (password,
// otp, webauthn) and by when it was set, which is what "does this person
// have a second factor" needs; the API does not hand out the value and
// this plugin would not ask.
//
// Usernames and email addresses are personal data handed to whatever
// asked, and the reads stay ungated for the same reason pg.table.list's
// rows do: the destination is one the operator named in a profile, never
// one the caller chose, and the persona is the realm's own administrator.
// Attributes are the exception and are never listed — a realm's custom
// attributes are where the sensitive things people bolt onto a user go.

func userListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.user.list",
		Summary:    "Who exists in the realm, whether each is enabled, verified and has a second factor",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "One row per user: username, email, enabled, email verified, whether an OTP " +
			"is configured, and when the account was created. Service accounts are not users " +
			"here — Keycloak lists them under their client. Bounded by --max; a search narrows " +
			"by username, email or name.",
		Run: runUserList,
	},
		plugin.Field{Name: "search", Type: plugin.String, Positional: true, Default: "",
			Help: "username, email, first or last name to look for; empty lists from the start"},
		maxField(100, 1000, "how many users to list"),
	)
}

func runUserList(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		users, verr := s.users(ctx, req.String("search"), req.Int("max"))
		if verr != nil {
			return nil, verr
		}
		t := columns(col("Username"), col("Email"), col("Enabled"), col("Verified"), col("OTP"),
			view.Column{Name: "Created", Kind: view.KindTimestamp})
		for _, u := range users {
			t.Rows = append(t.Rows, []string{u.Username, u.Email, yesNo(u.Enabled), yesNo(u.EmailVerified),
				yesNo(u.TOTP), stamp(u.CreatedTimestamp)})
		}
		return finish(t), nil
	})
}

// users lists up to max users, in full representation. Full rather than
// brief because brief leaves `totp` null, and whether an OTP is configured
// is the one column this listing exists for.
func (s *session) users(ctx context.Context, search string, max int) ([]userRep, *view.Error) {
	var users []userRep
	q := query("briefRepresentation", "false", "max", strconv.Itoa(max), "search", search)
	if verr := s.get(ctx, "users", q, &users); verr != nil {
		return nil, verr
	}
	return users, nil
}

func userShowCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.user.show",
		Summary:    "One user: profile, credential types, effective roles, groups and sessions",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Everything the realm knows about one account except its secrets: the " +
			"profile and its required actions, which kinds of credential are set (password, " +
			"otp, webauthn — types and dates, never values), the realm roles in effect once " +
			"composites are expanded, client roles per client, group membership, and the " +
			"sessions open right now.",
		Run: runUserShow,
	},
		plugin.Field{Name: "user", Type: plugin.String, Positional: true, Required: true,
			Help: "username, or the user's id"},
	)
}

func runUserShow(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		u, verr := s.user(ctx, req.String("user"))
		if verr != nil {
			return nil, verr
		}
		p := plugin.NewPage(ctx, req)
		p.PutAs("profile", "profile", userProfile(u))

		var creds []credentialRep
		if verr := s.get(ctx, "users/"+segment(u.ID)+"/credentials", nil, &creds); verr != nil {
			p.Warn(verr)
		} else {
			p.PutAs("credentials", "credentials", credentialTable(creds))
		}

		roles, verr := s.effectiveRoles(ctx, u.ID)
		if verr != nil {
			p.Warn(verr)
		} else {
			p.PutAs("roles", "roles", roles)
		}

		var groups []groupRep
		if verr := s.get(ctx, "users/"+segment(u.ID)+"/groups", nil, &groups); verr != nil {
			p.Warn(verr)
		} else if len(groups) > 0 {
			t := columns(col("Group"), col("Path"))
			for _, g := range groups {
				t.Rows = append(t.Rows, []string{g.Name, g.Path})
			}
			p.PutAs("groups", "groups", finish(t))
		}

		var sessions []userSessionRep
		if verr := s.get(ctx, "users/"+segment(u.ID)+"/sessions", nil, &sessions); verr != nil {
			p.Warn(verr)
		} else if len(sessions) > 0 {
			p.PutAs("sessions", "sessions", sessionTable(sessions))
		}
		return p.View(), nil
	})
}

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// user resolves a username or an id to the account. Username first, exactly
// — Keycloak's `sub` is the id, and a person reading a token wants to paste
// it, so an id-shaped name that matched no username is tried as one.
func (s *session) user(ctx context.Context, name string) (userRep, *view.Error) {
	name = strings.TrimSpace(name)
	var matches []userRep
	if verr := s.get(ctx, "users", query("username", name, "exact", "true", "briefRepresentation", "false"), &matches); verr != nil {
		return userRep{}, verr
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if uuidShape.MatchString(strings.ToLower(name)) {
		var u userRep
		if verr := s.get(ctx, "users/"+segment(name), nil, &u); verr == nil {
			return u, nil
		}
	}
	return userRep{}, view.Errorf("keycloak.user.unknown", "no user %q in realm %s", name, s.realm).
		WithHint("`rta keycloak user list " + name + "` searches by username, email and name")
}

func userProfile(u userRep) view.View {
	kv := view.KeyValue{}
	add := func(key, value string) {
		if value != "" {
			kv.Pairs = append(kv.Pairs, view.Pair{Key: key, Value: value})
		}
	}
	add("username", u.Username)
	add("id", u.ID)
	add("email", u.Email)
	add("name", strings.TrimSpace(u.FirstName+" "+u.LastName))
	state := "enabled"
	if !u.Enabled {
		state = "disabled"
	}
	add("state", state+" · email verified: "+yesNo(u.EmailVerified))
	add("created", stamp(u.CreatedTimestamp))
	add("required actions", strings.Join(u.RequiredActions, ", "))
	add("federated from", u.FederationLink)
	if u.ServiceAccountClientID != "" {
		add("service account of", u.ServiceAccountClientID)
	}
	return kv
}

func credentialTable(creds []credentialRep) view.View {
	t := columns(col("Type"), col("Label"), view.Column{Name: "Created", Kind: view.KindTimestamp})
	for _, c := range creds {
		t.Rows = append(t.Rows, []string{c.Type, c.UserLabel, stamp(c.CreatedDate)})
	}
	return finish(t)
}

// effectiveRoles is what the user can actually do: realm roles with
// composites expanded (the API's own /composite view, so a user who holds
// only default-roles-<realm> is shown what that composite grants), and
// client roles as directly assigned, per client.
func (s *session) effectiveRoles(ctx context.Context, id string) (view.View, *view.Error) {
	var realmRoles []roleRep
	if verr := s.get(ctx, "users/"+segment(id)+"/role-mappings/realm/composite", nil, &realmRoles); verr != nil {
		return nil, verr
	}
	var mappings roleMappings
	if verr := s.get(ctx, "users/"+segment(id)+"/role-mappings", nil, &mappings); verr != nil {
		return nil, verr
	}
	t := columns(col("Scope"), col("Role"))
	for _, r := range sortedRoles(realmRoles) {
		t.Rows = append(t.Rows, []string{"realm", r})
	}
	clients := make([]string, 0, len(mappings.ClientMappings))
	for _, m := range mappings.ClientMappings {
		clients = append(clients, m.Client)
	}
	sort.Strings(clients)
	for _, c := range clients {
		for _, m := range mappings.ClientMappings {
			if m.Client != c {
				continue
			}
			for _, r := range sortedRoles(m.Mappings) {
				t.Rows = append(t.Rows, []string{c, r})
			}
		}
	}
	return finish(t), nil
}

func sortedRoles(roles []roleRep) []string {
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return names
}
