package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// **The line this plugin draws, pinned: the read tier describes the realm, and
// what returns the people in it is behind a grant.** Identity administration is
// authority-expanding by nature, so nothing here writes; but usernames, e-mail
// addresses, the address a login came from and who changed what are values
// somebody stored, and this repository's rule is that anything that returns one
// is a write, as pg.query's rows and etcd.kv.get's values are. A capability that
// returns them and is a Read is a mistake, and so is one of the others that is
// not.
func TestTheReadTierDescribesTheRealmAndTheWriteTierHoldsItsPeople(t *testing.T) {
	people := map[string]string{ // the input a grant can be narrowed to, "" for none
		"keycloak.user.list":    "search",
		"keycloak.user.show":    "user",
		"keycloak.session.list": "user",
		"keycloak.event.list":   "user",
		"keycloak.event.admin":  "",
	}
	seen := map[string]bool{}
	for _, c := range Plugin().Capabilities {
		scope, isPeople := people[c.ID]
		if !isPeople {
			if c.Safety != plugin.Read || c.NeedsGrant || c.HumanOnly || c.Scope != "" {
				t.Errorf("%s: safety %s, grant %v, human-only %v, scope %q; want an ungated read, as it describes the realm",
					c.ID, c.Safety, c.NeedsGrant, c.HumanOnly, c.Scope)
			}
			continue
		}
		seen[c.ID] = true
		if c.Safety != plugin.Write || !c.NeedsGrant || c.HumanOnly || c.Scope != scope {
			t.Errorf("%s: safety %s, grant %v, human-only %v, scope %q; want Write with a grant, narrowed to %q",
				c.ID, c.Safety, c.NeedsGrant, c.HumanOnly, c.Scope, scope)
		}
		if !strings.Contains(c.Description, "Classified write for what it discloses") {
			t.Errorf("%s: its description does not say why it is Write", c.ID)
		}
	}
	for id := range people {
		if !seen[id] {
			t.Errorf("%s is not declared", id)
		}
	}
}

// **A dry run that reached the realm to describe the call would be the call.**
// The conformance suite watches a directory and cannot see a request that
// leaves the machine, so this watches the realm: not a request, not the token's,
// reaches it, and what the call would list is said — with the inputs it was
// given, the realm and where.
func TestADryRunOfWhatReturnsPeopleReachesNothing(t *testing.T) {
	f := newFakeKeycloak(t)
	for _, tc := range []struct {
		id     string
		values map[string]any
		want   []string
	}{
		{"keycloak.user.list", map[string]any{"search": "alice", "limit": 5}, []string{"list up to 5 users matching \"alice\""}},
		{"keycloak.user.list", map[string]any{"limit": 1}, []string{"list up to 1 user in realm"}},
		{"keycloak.user.show", map[string]any{"user": "alice"}, []string{"show user \"alice\""}},
		{"keycloak.session.list", map[string]any{"user": "alice"}, []string{"list the open sessions of user \"alice\""}},
		{"keycloak.session.list", map[string]any{"client": "spa", "limit": 7}, []string{"list up to 7 sessions open against client \"spa\""}},
		{"keycloak.session.list", map[string]any{}, []string{"list the session counts per client"}},
		{"keycloak.event.list", map[string]any{"type": "LOGIN_ERROR", "user": "alice"},
			[]string{"newest first matching type \"LOGIN_ERROR\", user \"alice\""}},
		{"keycloak.event.admin", map[string]any{"limit": 3}, []string{"list up to 3 admin events, newest first"}},
	} {
		r := reqAt(t, f, tc.id, tc.values)
		r.DryRun = true
		v, err := capability(t, tc.id).Run(context.Background(), r)
		if err != nil {
			t.Errorf("%s: %v", tc.id, err)
			continue
		}
		text, ok := v.(view.Text)
		if !ok || !strings.HasPrefix(text.Body, "would ") || !strings.Contains(text.Body, "in realm demo on "+f.URL) {
			t.Errorf("%s: preview %#v, want a line that starts \"would\" and names realm demo and %s", tc.id, v, f.URL)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(text.Body, want) {
				t.Errorf("%s: preview %q does not say %q", tc.id, text.Body, want)
			}
		}
	}
	if got := f.paths(); len(got) != 0 {
		t.Errorf("a dry run reached the realm: %v", got)
	}
}

// A preview is told what the call would be refused for before the server is
// reached, as the call is: a URL that is not one, or no secret to send, is not a
// call that would have listed anybody.
func TestADryRunIsRefusedForWhatTheCallWouldBeRefusedBeforeTheServer(t *testing.T) {
	f := newFakeKeycloak(t)
	for name, tc := range map[string]struct {
		values map[string]any
		code   string
	}{
		"a URL that is not one": {map[string]any{"url": "ftp://nope"}, "keycloak.url.invalid"},
		"no client secret":      {map[string]any{"client-secret": ""}, "keycloak.secret.missing"},
	} {
		r := reqAt(t, f, "keycloak.user.list", map[string]any{}).With(tc.values)
		r.DryRun = true
		_, err := capability(t, "keycloak.user.list").Run(context.Background(), r)
		var verr *view.Error
		if !errors.As(err, &verr) || verr.Code != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
	}
	if got := f.paths(); len(got) != 0 {
		t.Errorf("a refused dry run reached the realm: %v", got)
	}
}
