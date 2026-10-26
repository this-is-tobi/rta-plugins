package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A Vault that answers with no body, an empty 204, is handed back by vault/api
// as a nil secret and no error. Every handler that read the secret's data
// panicked on it; each now refuses it, and transit.encrypt says the mount kept
// the plaintext, since a KV version 1 mount is what answers a write that way.
func TestAnAnswerWithNoBodyIsRefusedRatherThanPanicking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	at := func(values map[string]any) map[string]any {
		values["url"], values["token"] = srv.URL, "t"
		return values
	}
	dry := func(id string, values map[string]any) plugin.Request {
		for _, c := range Plugin().Capabilities {
			if c.ID == id {
				return plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), true, false)
			}
		}
		t.Fatalf("no capability %q", id)
		return plugin.Request{}
	}
	for _, c := range []struct {
		name string
		run  func() error
		code string
	}{
		{"transit.encrypt", func() error {
			_, err := runTransitEncrypt(context.Background(), req(t, "vault.transit.encrypt", at(map[string]any{"key": "k", "plaintext": "p"})))
			return err
		}, "vault.transit.notransit"},
		{"transit.decrypt", func() error {
			_, err := runTransitDecrypt(context.Background(), req(t, "vault.transit.decrypt", at(map[string]any{"key": "k", "ciphertext": "vault:v1:x"})))
			return err
		}, "vault.answer.empty"},
		{"wrap.set", func() error {
			_, err := runWrapSet(context.Background(), req(t, "vault.wrap.set", at(map[string]any{"data": []string{"a=b"}})))
			return err
		}, "vault.answer.empty"},
		{"wrap.get, dry run", func() error {
			_, err := runWrapGet(context.Background(), dry("vault.wrap.get", at(map[string]any{"wrapping-token": "w"})))
			return err
		}, "vault.answer.empty"},
		{"wrap.get", func() error {
			_, err := runWrapGet(context.Background(), req(t, "vault.wrap.get", at(map[string]any{"wrapping-token": "w"})))
			return err
		}, "vault.answer.empty"},
		{"token.status", func() error {
			_, err := runTokenStatus(context.Background(), req(t, "vault.token.status", at(map[string]any{})))
			return err
		}, "vault.answer.empty"},
		{"lease.show", func() error {
			_, err := runLeaseShow(context.Background(), req(t, "vault.lease.show", at(map[string]any{"id": "l"})))
			return err
		}, "vault.answer.empty"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var verr *view.Error
			if err := c.run(); !errors.As(err, &verr) || verr.Code != c.code {
				t.Fatalf("refused with %v, want %s", err, c.code)
			}
			if verr.Code == "vault.transit.notransit" && (verr.Input != "mount" || !strings.Contains(verr.Hint, "delete it")) {
				t.Errorf("the encrypt refusal = %+v, want it naming the mount and the plaintext left behind", verr)
			}
		})
	}
}
