//go:build livekeycloak

// The questions a fake cannot answer, run against a real Keycloak: that
// the endpoints this plugin names exist on the version in front of it,
// answer a service account holding only the view-* roles, and answer in
// the shapes types.go decodes. The fixtures under testdata/ were captured
// from exactly this setup on 26.7; this is how they are re-captured when
// the next Keycloak moves a field.
//
//	docker run -d --name rta-kc -p 18080:8080 \
//	  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin-throwaway \
//	  quay.io/keycloak/keycloak:latest start-dev
//
// Then, as the bootstrap admin: create a realm, a confidential client in it
// with service accounts enabled (say `rta`), grant its service account the
// realm-management roles view-users, view-clients, view-realm, view-events
// and view-authorization, and read its secret from the console's
// Credentials tab. The audit findings are more interesting with a user
// carrying an OTP credential and a public client with the implicit flow
// and a wildcard redirect, but nothing here requires them.
//
//	RTA_KEYCLOAK_LIVE=http://127.0.0.1:18080 RTA_KEYCLOAK_LIVE_REALM=demo \
//	  RTA_KEYCLOAK_LIVE_CLIENT=rta RTA_KEYCLOAK_CLIENT_SECRET=… \
//	  go test ./plugins/keycloak/ -tags livekeycloak -count=1 -v
package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

func liveRequest(t *testing.T, capID string, values map[string]any) (view.View, error) {
	t.Helper()
	url := os.Getenv("RTA_KEYCLOAK_LIVE")
	if url == "" {
		t.Skip("set RTA_KEYCLOAK_LIVE=http://host:port — see this file's package comment for the container")
	}
	values["url"] = url
	values["realm"] = firstOf(os.Getenv("RTA_KEYCLOAK_LIVE_REALM"), "demo")
	values["client-id"] = firstOf(os.Getenv("RTA_KEYCLOAK_LIVE_CLIENT"), "rta")
	values["client-secret"] = os.Getenv("RTA_KEYCLOAK_CLIENT_SECRET")
	return capability(t, capID).Run(context.Background(), req(t, capID, values))
}

func TestLiveEveryReadAnswers(t *testing.T) {
	for _, tc := range []struct {
		cap    string
		values map[string]any
	}{
		{"keycloak.overview", map[string]any{}},
		{"keycloak.overview", map[string]any{"detail": true}},
		{"keycloak.user.list", map[string]any{}},
		{"keycloak.client.list", map[string]any{}},
		{"keycloak.role.list", map[string]any{}},
		{"keycloak.role.list", map[string]any{"client": "realm-management"}},
		{"keycloak.flow.list", map[string]any{}},
		{"keycloak.flow.show", map[string]any{"flow": "browser"}},
		{"keycloak.session.list", map[string]any{}},
		{"keycloak.event.list", map[string]any{}},
		{"keycloak.event.admin", map[string]any{}},
		{"keycloak.audit", map[string]any{}},
		{"keycloak.audit", map[string]any{"detail": true}},
	} {
		if _, err := liveRequest(t, tc.cap, tc.values); err != nil {
			t.Errorf("%s %v: %v", tc.cap, tc.values, err)
		}
	}
}

func TestLiveTheClientShowsItselfWithoutItsSecret(t *testing.T) {
	v, err := liveRequest(t, "keycloak.client.show", map[string]any{"client": firstOf(os.Getenv("RTA_KEYCLOAK_LIVE_CLIENT"), "rta")})
	if err != nil {
		t.Fatal(err)
	}
	if secret := os.Getenv("RTA_KEYCLOAK_CLIENT_SECRET"); secret != "" && strings.Contains(rendered(t, v), secret) {
		t.Fatal("the client's own secret was rendered")
	}
}

func TestLiveTheAuditGradesTheRealm(t *testing.T) {
	v, err := liveRequest(t, "keycloak.audit", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	got := graded(t, v)
	for _, check := range []string{"overall", "browser-flow", "coverage", "detection", "policy", "access-token", "login-events"} {
		if _, ok := got[check]; !ok {
			t.Errorf("no %q finding against the live realm", check)
		}
	}
}
