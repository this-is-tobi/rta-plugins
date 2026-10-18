package main

import (
	"strings"
	"testing"
	"time"
)

// The tile is polled, and a run of it mints a token, which Keycloak writes
// down as a CLIENT_LOGIN event in the realm's own log, and asks for whatever
// else the page prints. So the compact page reads what it prints and nothing
// else: the realm, a count of users and the session counts, none of which
// carries a secret. The client list does — every confidential client's, to
// whoever holds view-clients — and the version needs a request of its own, so
// both are what --detail adds.

func adminRequests(f *fakeKeycloak) []string {
	var out []string
	for _, p := range f.paths() {
		if i := strings.Index(p, "/admin/"); i >= 0 {
			out = append(out, strings.TrimSuffix(p[i:], "?"))
		}
	}
	return out
}

func TestTheCompactOverviewAsksForNothingThatCarriesASecret(t *testing.T) {
	f := newFakeKeycloak(t)
	f.admin = true
	got := pairs(t, run(t, f, "keycloak.overview", map[string]any{}))

	want := []string{"/admin/realms/demo", "/admin/realms/demo/users/count", "/admin/realms/demo/client-session-stats"}
	if reqs := adminRequests(f); strings.Join(reqs, " ") != strings.Join(want, " ") {
		t.Errorf("the compact page made the requests\n%s\nwant\n%s", strings.Join(reqs, "\n"), strings.Join(want, "\n"))
	}
	for _, key := range []string{"clients", "version"} {
		if v, shown := got[key]; shown {
			t.Errorf("the compact page carries %s = %q, which is what --detail adds", key, v)
		}
	}
}

func TestTheDetailedOverviewAddsTheClientTallyAndTheVersionAndAsksOnce(t *testing.T) {
	f := newFakeKeycloak(t)
	f.admin = true
	got := sections(t, run(t, f, "keycloak.overview", map[string]any{"detail": true}))
	glance := pairs(t, got["summary"])
	if want := "8 · 1 confidential, 5 public, 2 bearer-only"; glance["clients"] != want {
		t.Errorf("clients = %q, want %q", glance["clients"], want)
	}
	if glance["version"] != "26.7.3" {
		t.Errorf("version = %q, want 26.7.3", glance["version"])
	}
	asked := map[string]int{}
	for _, r := range adminRequests(f) {
		asked[r]++
	}
	for _, path := range []string{"/admin/realms/demo/clients", "/admin/realms/demo/client-session-stats"} {
		if asked[path] != 1 {
			t.Errorf("%s was asked %d times, want once: the page shares one read between its rows and its tables", path, asked[path])
		}
	}
}

func TestTheOverviewCountsTheSessionsTheClientsHold(t *testing.T) {
	f := newFakeKeycloak(t)
	if got := pairs(t, run(t, f, "keycloak.overview", map[string]any{}))["client sessions"]; got != "none" {
		t.Errorf("a realm nobody is signed in to reads %q, want none", got)
	}
	f.answer("/client-session-stats", []obj{
		{"clientId": "account-console", "active": "3", "offline": "1"},
		{"clientId": "webapp", "active": "2", "offline": "0"},
	})
	if got := pairs(t, run(t, f, "keycloak.overview", map[string]any{}))["client sessions"]; got != "5 active, 1 offline" {
		t.Errorf("client sessions = %q, want 5 active, 1 offline", got)
	}
	f.answer("/client-session-stats", []obj{{"clientId": "webapp", "active": "many", "offline": "0"}})
	got := pairs(t, run(t, f, "keycloak.overview", map[string]any{}))["client sessions"]
	if !strings.HasPrefix(got, "could not be read — ") {
		t.Errorf("a count that is not a number reads %q, want it said could not be read", got)
	}
}

// A service account short one view-* role is the ordinary half-provisioned
// setup. A row it cannot read used to leave the page, and a page without its
// users row reads as a realm that has none worth counting.
func TestARowTheRoleCannotReadSaysSoAndKeepsTheRest(t *testing.T) {
	f := newFakeKeycloak(t)
	f.deny = []string{"/users/count", "/client-session-stats"}
	got := pairs(t, run(t, f, "keycloak.overview", map[string]any{}))
	for _, key := range []string{"users", "client sessions"} {
		if !strings.HasPrefix(got[key], "could not be read — ") || !strings.Contains(got[key], "may not read this") {
			t.Errorf("%s = %q, want \"could not be read — \" and why", key, got[key])
		}
	}
	if got["realm"] != "demo · enabled" || got["brute force"] != "off" {
		t.Errorf("the rows that could be read were not kept: %v", got)
	}

	f = newFakeKeycloak(t)
	f.deny = []string{"/clients", "/serverinfo"}
	glance := pairs(t, sections(t, run(t, f, "keycloak.overview", map[string]any{"detail": true}))["summary"])
	for _, key := range []string{"clients", "version"} {
		if !strings.HasPrefix(glance[key], "could not be read — ") {
			t.Errorf("%s = %q in a detailed page, want \"could not be read — \" and why", key, glance[key])
		}
	}
}

// What a run costs is the capability's to say and the host's to wait out: a
// run is a token and three reads, in a log the operator reads for real
// failures, and a realm's posture does not move in half a minute.
func TestTheOverviewAndTheAuditDeclareHowOftenATileMayRunThem(t *testing.T) {
	for id, want := range map[string]time.Duration{
		"keycloak.overview": 2 * time.Minute,
		"keycloak.audit":    time.Hour,
	} {
		if got := capability(t, id).Refresh; got != want {
			t.Errorf("%s declares Refresh %v, want %v", id, got, want)
		}
	}
}
