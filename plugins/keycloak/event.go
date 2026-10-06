package main

import (
	"context"
	"strconv"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The event log is the identity provider's own record — every login, every
// failure, every administrative change — and reading it is what an
// incident starts with. It is also empty for the wrong reason more often
// than for the right one: a realm records nothing until somebody turns
// events on, which is why keycloak.audit has a finding for exactly that,
// and why an empty answer here is worth a second look rather than relief.

func eventListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:       "keycloak.event.list",
		Summary:  "Login events, newest first: logins, failures, token refreshes, who and from where",
		Keywords: []string{"audit", "bruteforce", "history", "security", "authentication"},
		Examples: []plugin.Example{
			{Title: "the last failed logins", Inputs: map[string]any{"type": "LOGIN_ERROR", "limit": 50}},
		},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "The realm's login event log, newest first — LOGIN, LOGIN_ERROR, LOGOUT, " +
			"CODE_TO_TOKEN, REFRESH_TOKEN and the rest — with the user, the client and the " +
			"address each came from. Filter by `type`, `user` or `client`; bound by `limit`, and it " +
			"says when it stopped. Empty when the realm does not record events, which " +
			"`keycloak.audit` flags.",
		Run: runEventList,
	},
		plugin.Field{Name: "type", Short: "t", Type: plugin.String, Default: "", Help: "one event type, e.g. LOGIN_ERROR"},
		plugin.Field{Name: "user", Short: "u", Type: plugin.String, Default: "", Help: "events of one user, by username or id"},
		plugin.Field{Name: "client", Short: "c", Type: plugin.String, Default: "", Help: "events of one client",
			Live: true, Suggest: suggestClients},
		limitField(50, 1000, "how many events to list"),
	)
}

func runEventList(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		limit := req.Int("limit")
		q := query("max", strconv.Itoa(askFor(limit)), "type", req.String("type"), "client", req.String("client"))
		if name := req.String("user"); name != "" {
			u, verr := s.user(ctx, name)
			if verr != nil {
				return nil, verr
			}
			q.Set("user", u.ID)
		}
		var got []eventRep
		if verr := s.get(ctx, "events", q, &got); verr != nil {
			return nil, verr
		}
		events, more := bounded(got, limit)
		t := columns(view.Column{Name: "Time", Kind: view.KindTimestamp}, col("Type"), col("User"),
			col("Client"), col("Address"), col("Error"))
		for _, e := range events {
			t.Rows = append(t.Rows, []string{stamp(e.Time), e.Type, listed(firstOf(e.Details["username"], e.UserID)),
				listed(e.ClientID), listed(e.IPAddress), e.Error})
		}
		if more {
			sf := req.Surface()
			t = stoppedAt(t, limit, "event", sf, ", or narrow it with "+sf.InputName("type")+", "+
				sf.InputName("user")+" or "+sf.InputName("client"))
		}
		return finish(t), nil
	})
}

func eventAdminCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.event.admin",
		Summary:    "Admin events, newest first: what was created, changed or deleted in the realm, by whom",
		Keywords:   []string{"audit", "changes", "history", "who", "security"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "The realm's administrative change log: each operation, the resource it " +
			"touched, the path to it, and the account and address it came from. What an " +
			"incident wants first when a client or a role appeared that nobody remembers " +
			"adding. Bound by `limit`, and it says when it stopped.",
		Run: runEventAdmin,
	},
		limitField(50, 1000, "how many events to list"),
	)
}

func runEventAdmin(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		limit := req.Int("limit")
		var got []adminEventRep
		if verr := s.get(ctx, "admin-events", query("max", strconv.Itoa(askFor(limit))), &got); verr != nil {
			return nil, verr
		}
		events, more := bounded(got, limit)
		t := columns(view.Column{Name: "Time", Kind: view.KindTimestamp}, col("Operation"), col("Resource"),
			col("Path"), col("By"), col("Address"), col("Error"))
		for _, e := range events {
			t.Rows = append(t.Rows, []string{stamp(e.Time), e.OperationType, e.ResourceType, listed(e.ResourcePath),
				e.AuthDetails.UserID, listed(e.AuthDetails.IPAddress), e.Error})
		}
		if more {
			t = stoppedAt(t, limit, "event", req.Surface(), "")
		}
		return finish(t), nil
	})
}
