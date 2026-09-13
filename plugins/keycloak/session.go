package main

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func sessionListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.session.list",
		Summary:    "Who is signed in: sessions per client, or the sessions of one user or one client",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "By default the realm's session counts per client, active and offline — the " +
			"one-line answer to \"is anyone using this\". --user lists one account's open " +
			"sessions with their address, start and last activity; --client lists the sessions " +
			"open against one application. Sessions are described, never revoked: that is a " +
			"write this plugin does not have.",
		Run: runSessionList,
	},
		plugin.Field{Name: "user", Type: plugin.String, Default: "", Help: "one user's sessions, by username or id"},
		plugin.Field{Name: "client", Type: plugin.String, Default: "", Help: "the sessions open against one client",
			Live: true, Suggest: suggestClients},
		maxField(100, 1000, "how many sessions to list for a client"),
	)
}

func runSessionList(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		switch {
		case req.String("user") != "":
			u, verr := s.user(ctx, req.String("user"))
			if verr != nil {
				return nil, verr
			}
			var sessions []userSessionRep
			if verr := s.get(ctx, "users/"+segment(u.ID)+"/sessions", nil, &sessions); verr != nil {
				return nil, verr
			}
			return sessionTable(sessions), nil
		case req.String("client") != "":
			c, verr := s.client(ctx, req.String("client"))
			if verr != nil {
				return nil, verr
			}
			var sessions []userSessionRep
			q := query("max", strconv.Itoa(req.Int("max")))
			if verr := s.get(ctx, "clients/"+segment(c.ID)+"/user-sessions", q, &sessions); verr != nil {
				return nil, verr
			}
			return sessionTable(sessions), nil
		}
		t, verr := s.sessionStats(ctx)
		if verr != nil {
			return nil, verr
		}
		return t, nil
	})
}

func (s *session) sessionStats(ctx context.Context) (view.View, *view.Error) {
	var stats []clientSessionStat
	if verr := s.get(ctx, "client-session-stats", nil, &stats); verr != nil {
		return nil, verr
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].ClientID < stats[j].ClientID })
	t := columns(col("Client"), view.Column{Name: "Active", Kind: view.KindNumber},
		view.Column{Name: "Offline", Kind: view.KindNumber})
	for _, st := range stats {
		t.Rows = append(t.Rows, []string{st.ClientID, st.Active, st.Offline})
	}
	return finish(t), nil
}

func sessionTable(sessions []userSessionRep) view.View {
	t := columns(col("User"), col("Address"), view.Column{Name: "Started", Kind: view.KindTimestamp},
		view.Column{Name: "Last access", Kind: view.KindTimestamp}, col("Clients"))
	for _, se := range sessions {
		clients := make([]string, 0, len(se.Clients))
		for _, c := range se.Clients {
			clients = append(clients, c)
		}
		sort.Strings(clients)
		t.Rows = append(t.Rows, []string{se.Username, se.IPAddress, stamp(se.Start), stamp(se.LastAccess),
			strings.Join(clients, ", ")})
	}
	return finish(t)
}
