package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Vault's client follows a redirect itself and refuses one that would take the
// token from https:// to http://. Pinned against a server that counts what
// reaches it, because the guarantee that a CA named for an https:// address is
// never stood behind by a plain hop is the plugin's to keep, not the library's:
// a library that began to follow would send the token in the clear.
func TestARedirectToPlainHTTPIsNeverFollowedWithTheTokenOnIt(t *testing.T) {
	var sent atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sealed":false,"initialized":true,"version":"1.0.0","storage_type":"raft"}`))
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
		verr := seal(t, map[string]any{"address": secure.URL, "ca-file": ca, "token": "s.secret"}, plugin.TunnelNone)
		secure.Close()
		if verr == nil {
			t.Errorf("a %d to plain HTTP was answered as if it were Vault", code)
		}
	}
	if n := sent.Load(); n != 0 {
		t.Errorf("%d requests reached the plain-HTTP server a redirect pointed at, the token on them", n)
	}
}

// The environment-reading default config fills an agent address from
// VAULT_AGENT_ADDR, which the client dials in place of the address a call
// named. The address the plaintext guard judged was the call's, so the variable
// was a way to a plain server with the token on it behind a CA that was
// accepted. Measured as the address the client says it dials.
func TestTheEnvironmentCannotChooseTheAddressTheGuardJudged(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer plain.Close()
	t.Setenv("VAULT_AGENT_ADDR", plain.URL)
	_, ca := vaultBehindAForward(t)

	c := capabilityByID(t, "vault.seal.status")
	rq := plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: map[string]any{
		"address": "https://127.0.0.1:1", "ca-file": ca, "token": "s.secret"}}), false, false)
	client, verr := connect(rq)
	if verr != nil {
		t.Fatalf("connect: %+v", verr)
	}
	if got := client.Address(); got != "https://127.0.0.1:1" {
		t.Errorf("the client dials %q, want the address the call named", got)
	}
}
