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

// The row that says a read failed says it in a line, in the words the other
// refusals use. The client's own text is a paragraph over several lines that
// names the request's URL, which through a forward is the end of one that
// closed with the call.
func TestAnOverviewRowForAFailedReadIsOneLineNamingTheProfile(t *testing.T) {
	srv := halfBlindVault(t)
	r := mountReq(t, "vault.overview", srv.URL, map[string]any{}).WithProfile("prod", plugin.TunnelKube)
	v, err := runOverview(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	var state string
	for _, p := range v.(view.KeyValue).Pairs {
		if p.Key == "state" {
			state = p.Value
		}
	}
	if want := "unreadable — profile prod (through its kube: forward) refused: permission denied"; state != want {
		t.Errorf("state = %q, want %q", state, want)
	}

	// The token's own row too: a Vault that says what it is and refuses the
	// token.
	tokenless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "lookup-self") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"sealed":false,"initialized":true,"version":"1.0.0"}`))
	}))
	t.Cleanup(tokenless.Close)
	r = mountReq(t, "vault.overview", tokenless.URL, map[string]any{}).WithProfile("prod", plugin.TunnelKube)
	if v, err = runOverview(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	var token string
	for _, p := range v.(view.KeyValue).Pairs {
		if p.Key == "token" {
			token = p.Value
		}
	}
	if want := "unreadable — profile prod (through its kube: forward) refused: permission denied"; token != want {
		t.Errorf("token = %q, want %q", token, want)
	}
}

// An overview that read nothing says why unless it was the token's policy.
// It said "nothing could be read" with no hint whatever the cause — the same
// sentence for a token whose policy reads neither call as for a Vault nothing
// listens at — so the failures an agent can act on (the address is wrong, the
// server is down, the request is not one Vault accepts) were told as the one it
// cannot. A server answering 400 stands for them here: a refused connection is
// retried by the client for several seconds, which no unit test should wait on.
func TestAnOverviewThatReadNothingSaysWhyWhenItWasNotThePolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":["missing request"]}`))
	}))
	t.Cleanup(srv.Close)

	for _, detail := range []bool{false, true} {
		_, err := overviewOf(t, srv, detail)
		verr := view.AsError(err, "")
		if verr == nil || verr.Code != "vault.badrequest" {
			t.Errorf("detail=%v: err = %+v, want vault.badrequest", detail, verr)
		}
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

// unsealedVault answers the seal status and the token as a healthy Vault does
// and refuses everything else — the policy list among it — with the paragraph
// the client library writes for a refusal: several lines, the request's URL and
// the status code.
func unsealedVault(t *testing.T, refusal int) (*httptest.Server, func() []string) {
	t.Helper()
	return recordingVaultWith(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "seal-status"):
			_, _ = w.Write([]byte(`{"sealed":false,"initialized":true,"version":"1.14.4"}`))
		case strings.Contains(r.URL.Path, "lookup-self"):
			_, _ = w.Write([]byte(`{"data":{"policies":["default"],"ttl":3600}}`))
		default:
			w.WriteHeader(refusal)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
		}
	})
}

// A section that could not be read becomes a warning on the page, and a
// warning is a line: it was the client's own paragraph, which names the
// request's URL and its status code over several lines, and through a forward
// that URL is a port that closed with the call. The overview's own header
// says a failed read is classified before it is shown, and the detail did not
// do it for the policy list.
func TestADetailWarningIsTheClassifiedMessageNotTheClientsParagraph(t *testing.T) {
	srv, _ := unsealedVault(t, http.StatusForbidden)
	v, err := overviewOf(t, srv, true)
	if err != nil {
		t.Fatal(err)
	}
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("overview is %s, want a page", view.TypeOf(v))
	}
	if len(page.Warnings) == 0 {
		t.Fatal("the policy list was refused and the page says nothing about it")
	}
	for _, w := range page.Warnings {
		for _, raw := range []string{"URL:", "Code:", "Error making API request", "\n"} {
			if strings.Contains(w.Message, raw) {
				t.Errorf("warning %q carries the client's own text, %q", w.Message, raw)
			}
		}
		if w.Code != "vault.denied" {
			t.Errorf("warning code = %q, want the classified vault.denied", w.Code)
		}
	}
}
