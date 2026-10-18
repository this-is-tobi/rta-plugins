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
// plain-HTTP one: Go strips only Authorization and Cookie from a request it
// sends on, so the api-key went with it, in the clear, behind a CA that was
// never consulted for the second hop. Measured against a server that counts
// what reaches it: nothing does, and the call says why.
func TestARedirectIsNeverFollowedWithTheAPIKeyInHand(t *testing.T) {
	var sent atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{},"status":"ok"}`))
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
		rq := req(t, "qdrant.overview", map[string]any{
			"address": strings.TrimPrefix(secure.URL, "https://"), "ca-file": ca, "api-key": "qd-secret-key"})
		verr := get(context.Background(), rq, "/collections", &struct{}{})
		secure.Close()
		if verr == nil || verr.Code != "qdrant.redirect.refused" || !strings.Contains(verr.Message, plain.URL) {
			t.Errorf("a %d to plain HTTP: %+v, want qdrant.redirect.refused naming where it pointed", code, verr)
		}
	}

	// No CA at all is the other client this plugin builds, and followed redirects
	// just as freely.
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer hop.Close()
	verr := get(context.Background(), req(t, "qdrant.overview", map[string]any{
		"address": strings.TrimPrefix(hop.URL, "http://"), "api-key": "qd-secret-key"}), "/collections", &struct{}{})
	if verr == nil || verr.Code != "qdrant.redirect.refused" {
		t.Errorf("with no CA: %+v, want qdrant.redirect.refused", verr)
	}
	if n := sent.Load(); n != 0 {
		t.Errorf("%d requests reached the server a redirect pointed at, the API key on them", n)
	}
}
