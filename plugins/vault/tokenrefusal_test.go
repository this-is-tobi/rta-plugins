package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// vault.token.status is where every other refusal sends its reader, so its
// own refusal cannot send the reader to itself: told to run the call that had
// just failed, an agent ran it again.
func TestTheRefusalOfTheTokenLookupDoesNotSendTheReaderBackToIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
	}))
	t.Cleanup(srv.Close)
	for _, c := range Plugin().Capabilities {
		if c.ID != "vault.token.status" {
			continue
		}
		_, err := c.Run(t.Context(), plugin.NewRequest(
			plugin.Resolve(c, plugin.Inputs{Caller: map[string]any{"address": srv.URL, "token": "t"}}), false, false))
		verr := view.AsError(err, "")
		if verr == nil || verr.Code != "vault.denied" {
			t.Fatalf("err = %v, want vault.denied", err)
		}
		if strings.Contains(verr.Hint, "token_status") || strings.Contains(verr.Hint, "token status") ||
			strings.Contains(verr.Hint, "token.status") {
			t.Errorf("hint = %q sends the reader to the call that was refused", verr.Hint)
		}
		return
	}
	t.Fatal("vault.token.status is not declared")
}
