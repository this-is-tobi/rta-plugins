package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// overviewOf runs vault.overview against one server and hands back its view.
func overviewOf(t *testing.T, srv *httptest.Server, detail bool) (view.View, error) {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID != "vault.overview" {
			continue
		}
		values := map[string]any{"address": srv.URL, "token": "t"}
		if detail {
			values["detail"] = true
		}
		return c.Run(t.Context(), plugin.NewRequest(
			plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false))
	}
	t.Fatal("no vault.overview capability")
	return nil, nil
}

// halfBlindVault answers the token lookup and refuses the seal status — a
// token whose policy covers auth/token/lookup-self and not sys/seal-status,
// which is an ordinary least-privilege Vault token rather than a broken one.
func halfBlindVault(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "seal-status") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"policies":["default"],"ttl":3600}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// **"Is this Vault sealed" is the one thing this capability exists to say,
// and a dropped row is the wrong way to say nothing.**
//
// Every read here was wrapped in `if err == nil`, so a seal status the token
// may not read, or a Vault that stopped answering mid-glance, produced a
// shorter page rather than a question nobody answered — and a KeyValue with
// one fewer row reads as a deliberately compact report, not as a failure.
// Only when *both* reads failed did anything say so.
func TestOverviewSaysWhichReadFailedInsteadOfDroppingIt(t *testing.T) {
	v, err := overviewOf(t, halfBlindVault(t), false)
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("overview is %s, want a KeyValue", view.TypeOf(v))
	}
	got := map[string]string{}
	for _, p := range kv.Pairs {
		got[p.Key] = p.Value
	}
	state, present := got["state"]
	if !present {
		t.Fatalf("the seal status was dropped rather than reported: %+v", kv.Pairs)
	}
	if !strings.HasPrefix(state, "unreadable") {
		t.Errorf("state = %q, want it to say the seal status could not be read", state)
	}
	// The read that did work still answers, so a half-blind token still gets
	// the half it is entitled to.
	if got["token policies"] == "" {
		t.Errorf("the token lookup that succeeded was lost: %+v", kv.Pairs)
	}
}

// And a Vault where nothing answers is still an error rather than a page of
// apologies — the existing behaviour, kept now that the rows above are no
// longer what distinguishes the two.
func TestOverviewStillRefusesWhenNothingCanBeRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
	}))
	t.Cleanup(srv.Close)

	_, err := overviewOf(t, srv, false)
	if err == nil {
		t.Fatal("a Vault that answered nothing produced a view instead of an error")
	}
	if verr := view.AsError(err, ""); verr.Code != "vault.overview.unavailable" {
		t.Errorf("error code = %q, want vault.overview.unavailable", verr.Code)
	}
}
