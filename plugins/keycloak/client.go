package main

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The client reads describe how each application authenticates against
// the realm — which grants it may use, where it may be sent back to, what
// it enforces. The secret is never among what they say (types.go), and
// neither is the attribute map as a whole: an allowlist of the settings an
// operator reasons about, because the same map is where a SAML client
// keeps its signing key.

func clientListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.client.list",
		Summary:    "Every client: public or confidential, which grants it may use, whether it enforces PKCE",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "One row per registered client, built-in ones included: its kind (public, " +
			"confidential, bearer-only), the flows enabled on it, the PKCE method it enforces, " +
			"whether every role lands in its tokens (full scope), and whether it is enabled. " +
			"Never a secret.",
		Run: runClientList,
	})
}

func runClientList(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		t, verr := s.clientTable(ctx)
		if verr != nil {
			return nil, verr
		}
		return t, nil
	})
}

func (s *session) clientTable(ctx context.Context) (view.View, *view.Error) {
	var clients []clientRep
	if verr := s.get(ctx, "clients", nil, &clients); verr != nil {
		return nil, verr
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].ClientID < clients[j].ClientID })
	t := columns(col("Client"), col("Kind"), col("Flows"), col("PKCE"), col("Full scope"), col("Enabled"))
	for _, c := range clients {
		t.Rows = append(t.Rows, []string{c.ClientID, c.kind(), flows(c), firstOf(c.pkce(), "none"),
			yesNo(c.FullScopeAllowed), yesNo(c.Enabled)})
	}
	return finish(t), nil
}

// flows is the grants a client may use, in the words the console uses.
func flows(c clientRep) string {
	var on []string
	if c.StandardFlowEnabled {
		on = append(on, "standard")
	}
	if c.ImplicitFlowEnabled {
		on = append(on, "implicit")
	}
	if c.DirectAccessGrantsEnabled {
		on = append(on, "direct-access")
	}
	if c.ServiceAccountsEnabled {
		on = append(on, "service-account")
	}
	if len(on) == 0 {
		return "none"
	}
	return strings.Join(on, ", ")
}

func clientShowCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.client.show",
		Summary:    "One client: grants, redirect URIs, origins, scopes, the settings that matter, its service account's roles",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "How one application authenticates: kind and protocol, the flows enabled, " +
			"every redirect URI and web origin, default and optional scopes, the enforcement " +
			"settings (PKCE, token lifespans, refresh tokens, logout), and — for a client with a " +
			"service account — the roles that account holds. The secret is not here and there is " +
			"no capability that shows it.",
		Run: runClientShow,
	},
		plugin.Field{Name: "client", Type: plugin.String, Positional: true, Required: true,
			Help: "the client id, as the console shows it", Live: true, Suggest: suggestClients},
	)
}

func runClientShow(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		c, verr := s.client(ctx, req.String("client"))
		if verr != nil {
			return nil, verr
		}
		p := plugin.NewPage(ctx, req)
		p.PutAs("client", "client", clientSummary(c))
		if len(c.RedirectURIs) > 0 || len(c.WebOrigins) > 0 {
			p.PutAs("redirects", "redirect uris and web origins", originTable(c))
		}
		p.PutAs("scopes", "scopes", view.KeyValue{Pairs: []view.Pair{
			{Key: "default", Value: strings.Join(c.DefaultClientScopes, ", ")},
			{Key: "optional", Value: strings.Join(c.OptionalClientScopes, ", ")},
		}})
		if settings := clientSettings(c); len(settings.Pairs) > 0 {
			p.PutAs("settings", "settings", settings)
		}
		if c.ServiceAccountsEnabled {
			roles, verr := s.serviceAccountRoles(ctx, c)
			if verr != nil {
				p.Warn(verr)
			} else {
				p.PutAs("service-account", "service account roles", roles)
			}
		}
		return p.View(), nil
	})
}

// client resolves a client id — the name the console shows — to the client.
func (s *session) client(ctx context.Context, clientID string) (clientRep, *view.Error) {
	var matches []clientRep
	if verr := s.get(ctx, "clients", query("clientId", strings.TrimSpace(clientID)), &matches); verr != nil {
		return clientRep{}, verr
	}
	if len(matches) != 1 {
		return clientRep{}, view.Errorf("keycloak.client.unknown", "no client %q in realm %s", clientID, s.realm).
			WithHint("`rta keycloak client list` shows what is registered")
	}
	return matches[0], nil
}

func clientSummary(c clientRep) view.View {
	kv := view.KeyValue{}
	add := func(key, value string) {
		if value != "" {
			kv.Pairs = append(kv.Pairs, view.Pair{Key: key, Value: value})
		}
	}
	add("client", c.ClientID)
	add("name", c.Name)
	add("description", c.Description)
	add("kind", c.kind()+" · "+c.Protocol)
	add("enabled", yesNo(c.Enabled))
	add("flows", flows(c))
	add("pkce", firstOf(c.pkce(), "not enforced"))
	add("full scope", yesNo(c.FullScopeAllowed))
	add("consent required", yesNo(c.ConsentRequired))
	if !c.PublicClient && !c.BearerOnly {
		add("authenticates with", c.ClientAuthenticatorType)
	}
	add("root url", c.RootURL)
	add("base url", c.BaseURL)
	return kv
}

func originTable(c clientRep) view.View {
	t := columns(col("Kind"), col("Value"))
	for _, u := range c.RedirectURIs {
		t.Rows = append(t.Rows, []string{"redirect", u})
	}
	for _, o := range c.WebOrigins {
		t.Rows = append(t.Rows, []string{"web origin", o})
	}
	return finish(t)
}

// clientSettings renders the attributes an operator reasons about, by
// name. An allowlist rather than the map: attributes are where a client's
// private key material lives when it has any, and "every attribute except
// the ones that look like keys" is a rule that breaks the first time
// Keycloak adds one.
func clientSettings(c clientRep) view.KeyValue {
	kv := view.KeyValue{}
	for _, a := range []struct{ key, label string }{
		{"access.token.lifespan", "access token lifespan"},
		{"client.session.idle.timeout", "client session idle"},
		{"client.session.max.lifespan", "client session max"},
		{"use.refresh.tokens", "refresh tokens"},
		{"client_credentials.use_refresh_token", "refresh tokens for client credentials"},
		{"oauth2.device.authorization.grant.enabled", "device authorization grant"},
		{"oidc.ciba.grant.enabled", "ciba grant"},
		{"post.logout.redirect.uris", "post-logout redirect uris"},
		{"backchannel.logout.url", "backchannel logout url"},
		{"backchannel.logout.session.required", "backchannel logout session required"},
		{"require.pushed.authorization.requests", "pushed authorization requests required"},
		{"token.response.type.bearer.lower-case", "lower-case bearer"},
		{"login_theme", "login theme"},
	} {
		if v, ok := c.Attributes[a.key]; ok && v != "" {
			kv.Pairs = append(kv.Pairs, view.Pair{Key: a.label, Value: v})
		}
	}
	if v := c.Attributes["client.secret.creation.time"]; v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
			kv.Pairs = append(kv.Pairs, view.Pair{Key: "secret rotated", Value: time.Unix(secs, 0).UTC().Format(time.RFC3339)})
		}
	}
	return kv
}

// serviceAccountRoles is what a client's service account can do: the roles
// its user holds, realm and per client. The realm-management ones are the
// rows that matter — that is the client which can administer the realm.
func (s *session) serviceAccountRoles(ctx context.Context, c clientRep) (view.View, *view.Error) {
	var sa userRep
	if verr := s.get(ctx, "clients/"+segment(c.ID)+"/service-account-user", nil, &sa); verr != nil {
		return nil, verr
	}
	return s.effectiveRoles(ctx, sa.ID)
}

// suggestClients completes a client id from the realm's own list — names
// only, the same thing keycloak.client.list's ungated answer already hands
// out. Silent on every failure, like any Suggest.
func suggestClients(ctx context.Context, req plugin.Request) []string {
	s, verr := connect(ctx, req)
	if verr != nil {
		return nil
	}
	var clients []clientRep
	if verr := s.get(ctx, "clients", nil, &clients); verr != nil {
		return nil
	}
	out := make([]string, 0, len(clients))
	for _, c := range clients {
		out = append(out, c.ClientID)
	}
	sort.Strings(out)
	return capped(out)
}

// completionCap bounds one listing's answer — an assist is a screenful,
// not an inventory.
const completionCap = 60

func capped(s []string) []string {
	if len(s) > completionCap {
		return s[:completionCap]
	}
	return s
}
