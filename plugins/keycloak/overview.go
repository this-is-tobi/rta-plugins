package main

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// keycloak.overview is one realm at a glance: its verdict, how many users and
// sessions it holds, which of the settings that matter most are on, and — with
// --detail — the clients, flows and sessions behind those numbers, composed
// through the one token its own Run minted rather than through
// plugin.Page.AddAs, which would mint one per section for what is supposed to be
// a single glance.
//
// A tile shows the first eight lines of the glance, so it opens with the verdict
// and ends with the realm's name: the name never changes between two refreshes,
// and the browser flow and the version are as constant, so they are the
// detailed page's.
//
// **The glance is what a tile polls, so it reads what it prints and no more.**
// A run mints a token, which Keycloak writes down as a CLIENT_LOGIN event in
// the realm's own log, so a tile that ran every thirty seconds put one hundred
// and twenty of them an hour in the log an operator reads for real failures.
// And it asked for the client list to print a tally of it — a list that carries
// every confidential client's secret to whoever holds view-clients — and for
// the whole of /admin/serverinfo to print one version. Those two are what
// --detail adds, where somebody asked for the clients; the glance is the realm,
// a count of users and the session counts, none of which carries a secret.
// Refresh is the declared pace, a floor a configuration can only lengthen.

func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.overview",
		Summary:    "One realm at a glance: its verdict, users and sessions, protections, event logging",
		Keywords:   []string{"summary", "status", "dashboard", "sso"},
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Refresh:    2 * time.Minute,
		Description: "The first row, `status`, is the verdict: ok, or fail or warn with the worst facts " +
			"in a few words. Fail is a disabled realm, which refuses every login; warn is a protection " +
			"that is off and `keycloak.audit` grades (brute-force detection, TLS required nowhere, login " +
			"or admin events not recorded) or a row the client's roles cannot read. Then the user count, how many " +
			"sessions the clients hold, brute-force detection, SSL requirement, self-registration, " +
			"whether login and admin events are recorded and, last, the realm. Nothing here reads " +
			"the client list, which carries every confidential client's secret to its reader. " +
			"`detail` adds the client tally, the flow each login goes through, the server version " +
			"when the client is allowed to see it — only a master-realm client is — the client " +
			"list, the flows and the active sessions per client. " +
			"For the grade, `keycloak.audit`.",
		Agent: "`status` is the verdict (ok; fail for a disabled realm; warn for brute force off, TLS required " +
			"nowhere, events not recorded or a row the role cannot read, with the facts), then the user " +
			"count, client sessions, brute force, SSL requirement, self-registration, events and the " +
			"realm. The client list, which carries secrets, is not read. `detail` adds the client tally, " +
			"browser flow, server version (a master-realm client only), clients, flows and sessions per " +
			"client. The grade is keycloak.audit.",
		Run: runOverview,
	})
}

func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		detail := req.Bool("detail")
		rows, reads, verr := s.glance(ctx, detail)
		if verr != nil {
			return nil, verr
		}
		if detail {
			return detailedOverview(ctx, req, s, rows, reads)
		}
		return rows, nil
	})
}

// glanceReads is what the rows were made of that --detail prints again as
// tables, handed back so the page asks the server once.
type glanceReads struct {
	clients     []clientRep
	clientsErr  *view.Error
	sessions    []clientSessionStat
	sessionsErr *view.Error
}

// valueRoom is the width a value of the glance may take on one line of 80
// columns: less the widest key of the page, `self-registration`, and the two
// cells a renderer sets after it.
const valueRoom = 61

// glance reads the realm and the rows it is the glance of, the verdict first and
// the realm last. A row its role cannot read says so: one that left the page
// read as a realm with nothing to count, and the half-provisioned service
// account is the ordinary setup. The verdict says so too, as a warning.
//
// The verdict grades the facts the page shows, as keycloak.audit grades the same
// facts, so that a tile never says ok over a row the audit fails: a realm that is
// disabled fails the tile, and brute-force detection off, TLS required nowhere
// and no login or admin events recorded are its warnings (the audit fails them;
// a tile that is red on every realm left at its defaults is not read). "external", Keycloak's default, is the
// audit's warning and not this page's: it is a setting chosen on purpose behind a
// proxy, and a tile that is amber on every realm left at its default is not read.
func (s *session) glance(ctx context.Context, detail bool) (view.KeyValue, glanceReads, *view.Error) {
	var realm realmRep
	var reads glanceReads
	if verr := s.get(ctx, "", nil, &realm); verr != nil {
		return view.KeyValue{}, reads, verr
	}
	var rows []view.Pair
	add := func(key, value string) {
		if value != "" {
			rows = append(rows, view.Pair{Key: key, Value: value})
		}
	}
	var fails, warns, unread []string
	unreadRow := func(key, why string) {
		const lead = "could not be read — "
		add(key, lead+clip(why, valueRoom-utf8.RuneCountInString(lead)))
		unread = append(unread, key)
	}

	if n, verr := s.userCount(ctx); verr != nil {
		unreadRow("users", verr.Message)
	} else {
		add("users", strconv.Itoa(n))
	}
	if detail {
		reads.clients, reads.clientsErr = s.clients(ctx)
		if reads.clientsErr != nil {
			unreadRow("clients", reads.clientsErr.Message)
		} else {
			add("clients", clientTally(reads.clients))
		}
	}
	reads.sessions, reads.sessionsErr = s.clientSessionStats(ctx)
	if reads.sessionsErr != nil {
		unreadRow("client sessions", reads.sessionsErr.Message)
	} else if line, ok := sessionTally(reads.sessions); ok {
		add("client sessions", line)
	} else {
		unreadRow("client sessions", "a session count that is not a number")
	}

	add("brute force", onOff(realm.BruteForceProtected))
	add("ssl required", realm.SSLRequired)
	add("self-registration", yesNo(realm.RegistrationAllowed)+" · verify email: "+yesNo(realm.VerifyEmail))
	add("events", "login "+onOff(realm.EventsEnabled)+" · admin "+onOff(realm.AdminEventsEnabled))

	state := "enabled"
	if !realm.Enabled {
		state = "disabled"
		fails = append(fails, "realm disabled")
	}
	if !realm.BruteForceProtected {
		warns = append(warns, "brute force off")
	}
	if realm.SSLRequired == "none" {
		warns = append(warns, "ssl not required")
	}
	switch {
	case !realm.EventsEnabled && !realm.AdminEventsEnabled:
		warns = append(warns, "events off")
	case !realm.EventsEnabled:
		warns = append(warns, "login events off")
	case !realm.AdminEventsEnabled:
		warns = append(warns, "admin events off")
	}

	// The realm is who this is, the one row of identity, and it comes last: it
	// is the part of the page that never changes between two refreshes.
	add("realm", listed(realm.Realm)+" · "+state)
	if detail {
		add("browser flow", listed(realm.BrowserFlow))
		// The version is on /admin/serverinfo, which Keycloak answers in full
		// to a master-realm admin and as a stripped-down document — themes,
		// providers, no systemInfo — to anyone else. Silent rather than an
		// error when it is the latter: the realm answered, and the version is
		// the one thing about it the operator's own choice of client scope
		// withholds. A server that does not answer at all is not that.
		if v, verr := s.serverVersion(ctx); verr != nil {
			unreadRow("version", verr.Message)
		} else if v != "" {
			add("version", v)
		}
	}
	return view.KeyValue{Pairs: append([]view.Pair{{Key: "status", Value: glanceVerdict(fails, warns, unread)}}, rows...)}, reads, nil
}

// glanceVerdict is ok, or the worst grade and the facts that earn it: the
// failures, then the protections that are off, then the rows that could not be
// read, as many as fit and the rest counted. Only a fact that stops the realm
// from working fails the tile; a protection left off is a posture, amber and not
// red, because a tile that is red on every realm left at its defaults is a tile
// nobody reads (keycloak.audit is where each of them is graded in full).
func glanceVerdict(fails, warns, unread []string) string {
	texts := slices.Clone(fails)
	texts = append(texts, warns...)
	if len(unread) > 0 {
		texts = append(texts, strings.Join(unread, ", ")+" could not be read")
	}
	if len(texts) == 0 {
		return "ok"
	}
	lead := "warn — "
	if len(fails) > 0 {
		lead = "fail — "
	}
	room := valueRoom - utf8.RuneCountInString(lead)
	for k := len(texts); k >= 1; k-- {
		s := strings.Join(texts[:k], ", ")
		if k < len(texts) {
			s += " +" + strconv.Itoa(len(texts)-k)
		}
		if utf8.RuneCountInString(s) <= room {
			return lead + s
		}
	}
	return lead + texts[0] + " +" + strconv.Itoa(len(texts)-1)
}

// clip cuts text to max columns, with an ellipsis where it was cut.
func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}

func detailedOverview(ctx context.Context, req plugin.Request, s *session, glance view.KeyValue, reads glanceReads) (view.View, error) {
	p := plugin.NewPage(ctx, req)
	put := func(id, title string, v view.View, verr *view.Error) {
		if verr != nil {
			p.Warn(verr)
			return
		}
		p.PutAs(id, title, v)
	}

	p.PutAs("summary", "at a glance", glance)

	var clients view.View
	if reads.clientsErr == nil {
		clients = clientTableOf(reads.clients)
	}
	put("clients", "clients", clients, reads.clientsErr)
	flows, verr := s.flowTable(ctx)
	put("flows", "flows", flows, verr)
	var sessions view.View
	if reads.sessionsErr == nil {
		sessions = sessionStatTable(reads.sessions)
	}
	put("sessions", "sessions per client", sessions, reads.sessionsErr)

	return p.View(), nil
}

// serverVersion is the Keycloak version, or "" when this client may not
// see it.
func (s *session) serverVersion(ctx context.Context) (string, *view.Error) {
	var info serverInfo
	if verr := s.getServer(ctx, "serverinfo", &info); verr != nil {
		return "", verr
	}
	return info.SystemInfo.Version, nil
}

// userCount is the realm's user count — the API has an endpoint for the
// number alone, so a realm of fifty thousand costs one small answer.
func (s *session) userCount(ctx context.Context) (int, *view.Error) {
	var n int
	if verr := s.get(ctx, "users/count", nil, &n); verr != nil {
		return 0, verr
	}
	return n, nil
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

// sessionTally is "5 active, 1 offline": the client sessions the realm holds
// open. A user signed in to two applications is two of them, which is what the
// per-client table below sums to as well. The counts are strings, which is
// Keycloak's choice, so one that is not a number is the reply being something
// other than what was asked for, and said so rather than counted as nothing.
func sessionTally(stats []clientSessionStat) (string, bool) {
	var active, offline int
	for _, st := range stats {
		a, errA := strconv.Atoi(st.Active)
		o, errO := strconv.Atoi(st.Offline)
		if errA != nil || errO != nil {
			return "", false
		}
		active += a
		offline += o
	}
	if active == 0 && offline == 0 {
		return "none", true
	}
	return strconv.Itoa(active) + " active, " + strconv.Itoa(offline) + " offline", true
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
