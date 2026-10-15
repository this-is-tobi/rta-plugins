package main

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// A server the call trusts, through ca-file, that answers with a redirect to a
// plain-HTTP one: the token request carries the client secret in its body and a
// 307 or a 308 sends it again whole, so the hop was a secret in the clear, and
// the call reported itself connected. Measured against a server that counts what
// reaches it: nothing does, for any of the four, and the call says why.
func TestARedirectIsNeverFollowedWithTheClientSecretInHand(t *testing.T) {
	var sent atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"t"}`))
	}))
	defer plain.Close()

	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+r.URL.Path, code)
		}))
		ca := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0o600); err != nil {
			t.Fatal(err)
		}
		rq := req(t, "keycloak.overview", map[string]any{"url": secure.URL, "ca-file": ca, "client-secret": "s3cret-value"})
		_, verr := connect(context.Background(), rq)
		secure.Close()
		if verr == nil || verr.Code != "keycloak.redirect.refused" || !strings.Contains(verr.Message, plain.URL) {
			t.Errorf("a %d to plain HTTP: %+v, want keycloak.redirect.refused naming where it pointed", code, verr)
		}
	}

	// No CA at all is the other client this plugin builds, and followed redirects
	// just as freely.
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer hop.Close()
	_, verr := connect(context.Background(), req(t, "keycloak.overview", map[string]any{"url": hop.URL, "client-secret": "s3cret-value"}))
	if verr == nil || verr.Code != "keycloak.redirect.refused" {
		t.Errorf("with no CA: %+v, want keycloak.redirect.refused", verr)
	}
	if n := sent.Load(); n != 0 {
		t.Errorf("%d requests reached the server a redirect pointed at, the client secret on them", n)
	}
}
