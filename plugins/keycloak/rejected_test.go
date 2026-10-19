package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A dashboard stops asking a tile whose credential was refused twice, and reads that off
// a code ending in `.auth.failed` or `.denied` (view.Error.CredentialRefused). Only the
// token endpoint saying the client was not accepted is that. An outage, a rate limit and
// an answer that is no OAuth refusal at all are the server's, and a tile that stopped
// asking after one of them would be hiding an outage that clears by itself: they come
// back as keycloak.request.failed, which has neither ending.
func TestATokenEndpointThatFailedOrAnsweredNothingItCanReadIsNotARefusedClient(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"500":                       {http.StatusInternalServerError, `{"error":"server_error"}`},
		"502":                       {http.StatusBadGateway, `<html>bad gateway</html>`},
		"503":                       {http.StatusServiceUnavailable, ``},
		"429":                       {http.StatusTooManyRequests, `{"error":"too_many_requests"}`},
		"a 403 of a proxy":          {http.StatusForbidden, `<html>forbidden</html>`},
		"a 400 that is no OAuth":    {http.StatusBadRequest, `<html>bad request</html>`},
		"a 400 for something else":  {http.StatusBadRequest, `{"error":"invalid_request","error_description":"Missing form parameter: grant_type"}`},
		"a 400 with an empty error": {http.StatusBadRequest, `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(token.Close)
			r := req(t, "keycloak.overview", map[string]any{
				"url": token.URL, "realm": "demo", "client-id": "rta-audit", "client-secret": fakeSecret})
			_, err := capability(t, "keycloak.overview").Run(context.Background(), r)
			verr := view.AsError(err, "")
			if verr == nil || verr.Code != "keycloak.request.failed" || verr.CredentialRefused() {
				t.Fatalf("error = %v (credential refused: %v), want keycloak.request.failed and not refused",
					err, verr != nil && verr.CredentialRefused())
			}
			if !strings.Contains(verr.Message, strconv.Itoa(tc.status)) {
				t.Errorf("message = %q, want the status the server answered with", verr.Message)
			}
		})
	}
}

// What the token endpoint says when it does not accept the client, each of its ways: a
// 401 whatever it says, and a 400 that names the client or the grant as not valid.
func TestATokenEndpointNotAcceptingTheClientIsARefusedCredentialInEachOfItsWays(t *testing.T) {
	s := &session{req: plugin.NewRequest(nil, false, false), base: "http://kc", realm: "master"}
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"401 invalid_client":          {http.StatusUnauthorized, `{"error":"invalid_client"}`},
		"401, nothing readable":       {http.StatusUnauthorized, `<html>unauthorized</html>`},
		"401 unauthorized_client":     {http.StatusUnauthorized, `{"error":"unauthorized_client"}`},
		"400 invalid_client":          {http.StatusBadRequest, `{"error":"invalid_client"}`},
		"400 invalid_grant":           {http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Invalid user credentials"}`},
		"400 invalid_grant, no words": {http.StatusBadRequest, `{"error":"invalid_grant"}`},
	} {
		t.Run(name, func(t *testing.T) {
			verr := s.classifyToken(tc.status, []byte(tc.body))
			if verr.Code != "keycloak.auth.failed" || !verr.CredentialRefused() {
				t.Errorf("code = %s (credential refused: %v), want keycloak.auth.failed refused", verr.Code, verr.CredentialRefused())
			}
		})
	}
}

// The Admin API answering 401 to the token it was just given is the token endpoint's
// client not being accepted by another name, and a read after the realm's own (the users,
// the client sessions, the flows, the server version, an audit's clients) can meet it as
// the first can: the call's error then, and not a row or a finding saying a read could not
// be made, which a dashboard cannot count. A 403 of the same reads is the half-provisioned
// service account and stays a row.
func TestACallWhoseTokenTheAdminAPIRefusedOnAnyReadIsTheRefusalAndNotARow(t *testing.T) {
	for name, tc := range map[string]struct {
		capID, suffix string
		detail        bool
	}{
		"the overview's user count":      {"keycloak.overview", "/users/count", false},
		"the overview's client sessions": {"keycloak.overview", "/client-session-stats", false},
		"the overview's clients":         {"keycloak.overview", "/clients", true},
		"the overview's flows":           {"keycloak.overview", "/authentication/flows", true},
		"the overview's server version":  {"keycloak.overview", "/admin/serverinfo", true},
		"the audit's clients":            {"keycloak.audit", "/clients", false},
		"the audit's required actions":   {"keycloak.audit", "/authentication/required-actions", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeKeycloak(t)
			f.admin = true
			var refuse bool
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if refuse && strings.HasSuffix(r.URL.Path, tc.suffix) {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"HTTP 401 Unauthorized"}`))
					return
				}
				f.serve(w, r)
			}))
			t.Cleanup(front.Close)
			values := map[string]any{"detail": tc.detail, "url": front.URL, "realm": "demo", "client-id": "rta-audit",
				"client-secret": fakeSecret}

			if _, err := capability(t, tc.capID).Run(context.Background(), req(t, tc.capID, values)); err != nil {
				t.Fatalf("a server that accepts the token ended the call: %v", err)
			}

			refuse = true
			v, err := capability(t, tc.capID).Run(context.Background(), req(t, tc.capID, values))
			verr := view.AsError(err, "")
			if verr == nil || verr.Code != "keycloak.token.auth.failed" || !verr.CredentialRefused() {
				t.Fatalf("err = %v and the page %s, want keycloak.token.auth.failed refused", err, rendered(t, v))
			}
		})
	}

	f := newFakeKeycloak(t)
	f.deny = []string{"/users/count", "/client-session-stats"}
	v := run(t, f, "keycloak.overview", map[string]any{})
	if body := rendered(t, v); !strings.Contains(body, "could not be read") {
		t.Errorf("a 403 of the reads is not a row that says so:\n%s", body)
	}
}
