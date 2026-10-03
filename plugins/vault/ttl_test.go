package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Vault reports a ttl of 0 for a token that never expires, the root token
// among them. Under "ttl (seconds)" a bare 0 reads as a token already out of
// time, which is the opposite fact: an agent asked whether the token it holds
// is about to lapse answered that it already had.
func TestATokenThatNeverExpiresIsNotShownAsOutOfTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "seal-status") {
			_, _ = w.Write([]byte(`{"sealed":false,"initialized":true,"version":"1.18.0"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"policies":["root"],"ttl":0,"renewable":false}}`))
	}))
	t.Cleanup(srv.Close)

	overview, err := overviewOf(t, srv, false)
	if err != nil {
		t.Fatal(err)
	}
	status := tokenStatusOf(t, srv)
	for name, v := range map[string]view.View{"vault.overview": overview, "vault.token.status": status} {
		kv, ok := v.(view.KeyValue)
		if !ok {
			t.Fatalf("%s is %s, want a KeyValue", name, view.TypeOf(v))
		}
		var ttl string
		for _, p := range kv.Pairs {
			if strings.Contains(p.Key, "ttl") {
				ttl = p.Value
			}
		}
		if ttl != "0 — does not expire" {
			t.Errorf("%s: ttl = %q, want it to say the token does not expire", name, ttl)
		}
	}

}

// A token with time left still shows the seconds, bare.
func TestATokenWithTimeLeftStillShowsTheSeconds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"policies":["default"],"ttl":3600}}`))
	}))
	t.Cleanup(srv.Close)
	for _, p := range tokenStatusOf(t, srv).(view.KeyValue).Pairs {
		if p.Key == "ttl (seconds)" && p.Value != "3600" {
			t.Errorf("ttl = %q, want 3600", p.Value)
		}
	}
}

func tokenStatusOf(t *testing.T, srv *httptest.Server) view.View {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID != "vault.token.status" {
			continue
		}
		v, err := c.Run(t.Context(), plugin.NewRequest(
			plugin.Resolve(c, plugin.Inputs{Caller: map[string]any{"address": srv.URL, "token": "t"}}), false, false))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	t.Fatal("no vault.token.status capability")
	return nil
}
