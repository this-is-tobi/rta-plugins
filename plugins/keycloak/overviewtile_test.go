package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The glance is what a tile and an agent read, and a tile shows the first eight
// lines of it: the verdict first, then the live counts, then the settings, and
// the identity of the realm last, in one row.

var glanceKeys = []string{"status", "users", "client sessions", "brute force", "ssl required",
	"self-registration", "events", "realm"}

func pairsInOrder(t *testing.T, v view.View) []view.Pair {
	t.Helper()
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("got %T, want key/value pairs", v)
	}
	return kv.Pairs
}

// aRealm is the fixture's realm with what the test changes, answered for the
// realm path.
func aRealm(t *testing.T, f *fakeKeycloak, change obj) {
	t.Helper()
	var realm obj
	if err := json.Unmarshal(fixture("realm.json"), &realm); err != nil {
		t.Fatal(err)
	}
	for k, v := range change {
		realm[k] = v
	}
	f.answer("", realm)
}

func statusOf(t *testing.T, f *fakeKeycloak) string {
	t.Helper()
	return pairs(t, run(t, f, "keycloak.overview", map[string]any{}))["status"]
}

// A realm nobody has hardened is the realm the fixture is: Keycloak's defaults.
// The audit grades each of these facts a failure, and the tile must not say ok
// over a row the audit fails.
func TestTheGlanceLeadsWithTheVerdictAndKeepsTheRealmLast(t *testing.T) {
	f := newFakeKeycloak(t)
	got := pairsInOrder(t, run(t, f, "keycloak.overview", map[string]any{}))
	keys := make([]string, len(got))
	for i, p := range got {
		keys[i] = p.Key
	}
	if !slices.Equal(keys, glanceKeys) {
		t.Fatalf("rows = %v, want %v", keys, glanceKeys)
	}
	widest := 0
	for _, p := range got {
		widest = max(widest, utf8.RuneCountInString(p.Key))
	}
	for _, p := range got {
		if w := widest + 2 + utf8.RuneCountInString(p.Value); w > 80 || strings.ContainsAny(p.Value, "\n\r") {
			t.Errorf("%s is %d columns on %q, want one line of at most 80", p.Key, w, p.Value)
		}
	}
	rows := pairs(t, view.KeyValue{Pairs: got})
	if want := "warn — brute force off"; rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}
	if rows["realm"] != "demo · enabled" {
		t.Errorf("realm = %q", rows["realm"])
	}
	if _, shown := rows["browser flow"]; shown {
		t.Error("the browser flow is identity, and belongs to --detail")
	}
}

func TestTheVerdictIsOkWhenNothingTheTileShowsIsWrong(t *testing.T) {
	f := newFakeKeycloak(t)
	aRealm(t, f, obj{"bruteForceProtected": true})
	if got := statusOf(t, f); got != "ok" {
		t.Errorf("status = %q, want ok for a realm with brute force on, SSL from outside, events recorded and nothing unread", got)
	}
}

func TestTheVerdictNamesTheWorstFactsInTheAuditsGrades(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change obj
		want   string
	}{
		{"a realm that refuses every login", obj{"bruteForceProtected": true, "enabled": false}, "fail — realm disabled"},
		{"no TLS required anywhere", obj{"bruteForceProtected": true, "sslRequired": "none"}, "warn — ssl not required"},
		{"login events not recorded", obj{"bruteForceProtected": true, "eventsEnabled": false}, "warn — login events off"},
		{"admin events not recorded", obj{"bruteForceProtected": true, "adminEventsEnabled": false}, "warn — admin events off"},
		{"neither recorded", obj{"bruteForceProtected": true, "eventsEnabled": false, "adminEventsEnabled": false}, "warn — events off"},
		{"several at once", obj{"enabled": false, "sslRequired": "none", "eventsEnabled": false},
			"fail — realm disabled, brute force off, ssl not required +1"},
		{"ssl from outside only, which is Keycloak's default and a choice behind a proxy",
			obj{"bruteForceProtected": true, "sslRequired": "external"}, "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeKeycloak(t)
			aRealm(t, f, tc.change)
			if got := statusOf(t, f); got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

// Rule five: a row the role cannot read says so, and so does the verdict, which
// is a warning and not a failure: the service account short one view-* role is
// the ordinary half-provisioned setup.
func TestAnUnreadRowIsAWarningInTheVerdict(t *testing.T) {
	f := newFakeKeycloak(t)
	aRealm(t, f, obj{"bruteForceProtected": true})
	f.deny = []string{"/users/count"}
	if got, want := statusOf(t, f), "warn — users could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}

	f = newFakeKeycloak(t)
	aRealm(t, f, obj{"bruteForceProtected": true})
	f.deny = []string{"/users/count", "/client-session-stats"}
	if got, want := statusOf(t, f), "warn — users, client sessions could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}

	// A protection that is off comes before a row that could not be read, whatever the order of the rows.
	f = newFakeKeycloak(t)
	f.deny = []string{"/users/count"}
	if got, want := statusOf(t, f), "warn — brute force off, users could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}

	// And a failure comes before both: a disabled realm refuses every login.
	f = newFakeKeycloak(t)
	aRealm(t, f, obj{"enabled": false})
	f.deny = []string{"/users/count"}
	if got, want := statusOf(t, f), "fail — realm disabled, brute force off +1"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// The detailed page is the glance with the verdict in front and what the compact
// page moved or leaves out behind: the browser flow, the version and the clients.
func TestTheDetailedGlanceKeepsWhatTheCompactOneMovedAndLeadsWithTheVerdict(t *testing.T) {
	f := newFakeKeycloak(t)
	f.admin = true
	got := pairsInOrder(t, sections(t, run(t, f, "keycloak.overview", map[string]any{"detail": true}))["summary"])
	keys := make([]string, len(got))
	for i, p := range got {
		keys[i] = p.Key
	}
	want := []string{"status", "users", "clients", "client sessions", "brute force", "ssl required",
		"self-registration", "events", "realm", "browser flow", "version"}
	if !slices.Equal(keys, want) {
		t.Fatalf("rows = %v, want %v", keys, want)
	}
	rows := pairs(t, view.KeyValue{Pairs: got})
	if rows["browser flow"] != "browser" || rows["version"] != "26.7.3" || !strings.HasPrefix(rows["status"], "warn — ") {
		t.Errorf("rows = %v", rows)
	}
}

// A clients list the role cannot read is a row of the detailed page, and a
// warning in its verdict as the compact page's unread rows are.
func TestAnUnreadClientListIsAWarningInTheDetailedVerdictToo(t *testing.T) {
	f := newFakeKeycloak(t)
	aRealm(t, f, obj{"bruteForceProtected": true})
	f.deny = []string{"/clients", "/serverinfo"}
	summary := pairs(t, sections(t, run(t, f, "keycloak.overview", map[string]any{"detail": true}))["summary"])
	if want := "warn — clients, version could not be read"; summary["status"] != want {
		t.Errorf("status = %q, want %q", summary["status"], want)
	}
}
