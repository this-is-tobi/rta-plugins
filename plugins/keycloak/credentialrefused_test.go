package main

import (
	"net/http"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A dashboard stops asking a tile whose credential was refused twice in a row,
// and reads that off the code (view.Error.CredentialRefused): one ending in
// `.auth.failed` (the account was not accepted) or `.denied` (the account is
// known and may not do this). Every way Keycloak turns the client down says so
// in one of them, and the failures that are not a refusal — a missing secret, a
// token endpoint that issued nothing, a server that does not answer — say it in
// neither, since a tile that stopped asking after those would be hiding an
// outage or a setting not yet made.
func TestEveryWayKeycloakRefusesTheClientEndsInTheFamily(t *testing.T) {
	s := &session{req: plugin.NewRequest(nil, false, false), base: "http://kc", realm: "master"}
	invalidClient := []byte(`{"error":"invalid_client","error_description":"Invalid client secret"}`)
	notThisGrant := []byte(`{"error":"unauthorized_client","error_description":"Client not enabled to retrieve service account"}`)
	for name, tc := range map[string]struct {
		verr    *view.Error
		code    string
		refused bool
	}{
		"the secret is not accepted, 401":     {s.classifyToken(http.StatusUnauthorized, invalidClient), "keycloak.auth.failed", true},
		"the secret is not accepted, 400":     {s.classifyToken(http.StatusBadRequest, invalidClient), "keycloak.auth.failed", true},
		"the client may not use the grant":    {s.classifyToken(http.StatusBadRequest, notThisGrant), "keycloak.grant.denied", true},
		"the token just issued is refused":    {s.classifyStatus(http.StatusUnauthorized, nil), "keycloak.token.auth.failed", true},
		"the service account may not read it": {s.classifyStatus(http.StatusForbidden, nil), "keycloak.denied", true},
		"no such realm to authenticate in":    {s.classifyToken(http.StatusNotFound, nil), "keycloak.realm.unknown", false},
		"a read of something that is not there": {s.classifyStatus(http.StatusNotFound, []byte(`{"error":"x"}`)),
			"keycloak.notfound", false},
		"a server that failed": {s.classifyStatus(http.StatusInternalServerError, nil), "keycloak.request.failed", false},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.verr.Code != tc.code {
				t.Fatalf("code = %s, want %s: %v", tc.verr.Code, tc.code, tc.verr)
			}
			if got := tc.verr.CredentialRefused(); got != tc.refused {
				t.Errorf("%s.CredentialRefused() = %v, want %v", tc.verr.Code, got, tc.refused)
			}
		})
	}
}
