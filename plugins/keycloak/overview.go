package main

import (
	"context"
	"net/url"
	"strconv"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// keycloak.overview is one realm at a glance: how big it is, which of the
// settings that matter most are on, and — with --detail — the clients,
// flows and sessions behind those numbers, composed through the one token
// its own Run minted rather than through plugin.Page.AddAs, which would
// mint one per section for what is supposed to be a single glance.

func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.overview",
		Summary:    "One realm at a glance: size, protections, event logging, the flows in force",
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Description: "Whether this realm is worth talking to and what state it is in: user and " +
			"client counts, brute-force detection, SSL requirement, self-registration, whether " +
			"login and admin events are recorded, and the flow each login goes through. The " +
			"server version when the client is allowed to see it — only a master-realm client " +
			"is. --detail adds the client list, the flows and the active sessions per client. " +
			"For the grade, `rta keycloak audit`.",
		Run: runOverview,
	})
}

func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		if req.Bool("detail") {
			return detailedOverview(ctx, req, s)
		}
		return compactOverview(ctx, s)
	})
}

func compactOverview(ctx context.Context, s *session) (view.View, error) {
	var realm realmRep
	if verr := s.get(ctx, "", nil, &realm); verr != nil {
		return nil, verr
	}
	kv := view.KeyValue{}
	add := func(key, value string) {
		if value != "" {
			kv.Pairs = append(kv.Pairs, view.Pair{Key: key, Value: value})
		}
	}
	state := "enabled"
	if !realm.Enabled {
		state = "disabled"
	}
	add("realm", realm.Realm+" · "+state)
	// The version is on /admin/serverinfo, which Keycloak answers in full
	// to a master-realm admin and as a stripped-down document — themes,
	// providers, no systemInfo — to anyone else. Silent rather than an
	// error when it is the latter: the realm answered, and the version is
	// the one thing about it the operator's own choice of client scope
	// withholds.
	if v := s.serverVersion(ctx); v != "" {
		add("version", v)
	}
	if n, ok := s.userCount(ctx); ok {
		add("users", strconv.Itoa(n))
	}
	var clients []clientRep
	if verr := s.get(ctx, "clients", nil, &clients); verr == nil {
		add("clients", clientTally(clients))
	}
	add("brute force", onOff(realm.BruteForceProtected))
	add("ssl required", realm.SSLRequired)
	add("self-registration", yesNo(realm.RegistrationAllowed)+" · verify email: "+yesNo(realm.VerifyEmail))
	add("events", "login "+onOff(realm.EventsEnabled)+" · admin "+onOff(realm.AdminEventsEnabled))
	add("browser flow", realm.BrowserFlow)
	return kv, nil
}

func detailedOverview(ctx context.Context, req plugin.Request, s *session) (view.View, error) {
	p := plugin.NewPage(ctx, req)
	put := func(id, title string, v view.View, verr *view.Error) {
		if verr != nil {
			p.Warn(verr)
			return
		}
		p.PutAs(id, title, v)
	}

	glance, err := compactOverview(ctx, s)
	if err != nil {
		return nil, err
	}
	p.PutAs("summary", "at a glance", glance)

	clients, verr := s.clientTable(ctx)
	put("clients", "clients", clients, verr)
	flows, verr := s.flowTable(ctx)
	put("flows", "flows", flows, verr)
	sessions, verr := s.sessionStats(ctx)
	put("sessions", "sessions per client", sessions, verr)

	return p.View(), nil
}

// serverVersion is the Keycloak version, or "" when this client may not
// see it.
func (s *session) serverVersion(ctx context.Context) string {
	var info serverInfo
	if verr := s.getServer(ctx, "serverinfo", &info); verr != nil {
		return ""
	}
	return info.SystemInfo.Version
}

// userCount is the realm's user count — the API has an endpoint for the
// number alone, so a realm of fifty thousand costs one small answer.
func (s *session) userCount(ctx context.Context) (int, bool) {
	var n int
	if verr := s.get(ctx, "users/count", nil, &n); verr != nil {
		return 0, false
	}
	return n, true
}

// clientTally is "8 · 3 confidential, 5 public" — the split is the part
// that says what kind of realm this is.
func clientTally(clients []clientRep) string {
	var confidential, public, bearer int
	for _, c := range clients {
		switch c.kind() {
		case "confidential":
			confidential++
		case "public":
			public++
		default:
			bearer++
		}
	}
	out := strconv.Itoa(len(clients)) + " · " + strconv.Itoa(confidential) + " confidential, " + strconv.Itoa(public) + " public"
	if bearer > 0 {
		out += ", " + strconv.Itoa(bearer) + " bearer-only"
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// query is the shorthand for the Admin API's paging and filter parameters.
func query(pairs ...string) url.Values {
	q := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			q.Set(pairs[i], pairs[i+1])
		}
	}
	return q
}
