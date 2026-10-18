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

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
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
		verr := seal(t, map[string]any{"url": secure.URL, "ca-file": ca, "token": "s.secret"}, plugin.TunnelNone)
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
		"url": "https://127.0.0.1:1", "ca-file": ca, "token": "s.secret"}}), false, false)
	client, verr := connect(rq)
	if verr != nil {
		t.Fatalf("connect: %+v", verr)
	}
	if got := client.Address(); got != "https://127.0.0.1:1" {
		t.Errorf("the client dials %q, want the address the call named", got)
	}
}

// redirectingVault answers every request with a redirect to a second server,
// the way a standby answers what it cannot serve by naming the active node's
// api_addr, and returns what reached the second server.
func redirectingVault(t *testing.T, code int) (from *httptest.Server, hits func() int32) {
	t.Helper()
	var n atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sealed":false,"initialized":true,"version":"1.0.0","data":{"keys":["a"]}}`))
	}))
	t.Cleanup(target.Close)
	from = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.RequestURI(), code)
	}))
	t.Cleanup(from.Close)
	return from, n.Load
}

// A Vault names where it wants the request to go, and the client followed it
// once with the token on the request. The address is the one the operator
// chose and the token the one they gave for it; an address the server chose is
// neither, and through a forward it is the cluster's own, outside the forward.
// A standby that cannot serve a request says so as a refusal that names the
// node it pointed at, so the operator can address the active node themselves.
func TestARedirectIsRefusedNamingWhereItPointedAndNeverFollowed(t *testing.T) {
	for _, code := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			from, hits := redirectingVault(t, code)
			verr := seal(t, map[string]any{"url": from.URL, "token": "s.secret"}, plugin.TunnelNone)
			if verr == nil || verr.Code != "vault.redirected" {
				t.Fatalf("err = %+v, want vault.redirected", verr)
			}
			if n := hits(); n != 0 {
				t.Errorf("%d requests reached the node the redirect pointed at, the token on them", n)
			}
			if !strings.Contains(verr.Message, "127.0.0.1:") {
				t.Errorf("message = %q, want the node it pointed at", verr.Message)
			}
			if verr.Hint == "" {
				t.Error("the refusal does not say what to do about it")
			}
		})
	}
}

// Every capability goes through the one client, so the refusal is not the seal
// status's alone: a read that lists, and the overview, which reads twice.
func TestNoCapabilityFollowsARedirect(t *testing.T) {
	from, hits := redirectingVault(t, http.StatusTemporaryRedirect)

	_, err := runKVList(context.Background(), mountReq(t, "vault.kv.list", from.URL, map[string]any{}))
	if verr := view.AsError(err, ""); verr == nil || verr.Code != "vault.redirected" {
		t.Errorf("kv list: err = %+v, want vault.redirected", err)
	}
	_, err = overviewOf(t, from, false)
	if verr := view.AsError(err, ""); verr == nil || verr.Code != "vault.redirected" {
		t.Errorf("overview: err = %+v, want vault.redirected", err)
	}
	if n := hits(); n != 0 {
		t.Errorf("%d requests reached the node the redirects pointed at", n)
	}
}

// The library's own switch is set too, so that a redirect is not followed even
// if the refusal above is ever taken off the HTTP client: with the client put
// back to returning the redirect as a response, the library still does not
// chase it. (CloneConfig does not carry the switch, so it is measured by what
// the client does.)
func TestTheLibraryItselfIsToldNotToFollow(t *testing.T) {
	from, hits := redirectingVault(t, http.StatusTemporaryRedirect)
	client, verr := connect(req(t, "vault.seal.status", map[string]any{"url": from.URL, "token": "t"}))
	if verr != nil {
		t.Fatal(verr)
	}
	client.SetCheckRedirect(func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse })
	_, _ = client.Sys().SealStatus()
	if n := hits(); n != 0 {
		t.Errorf("the library followed the redirect: %d requests reached the node it pointed at", n)
	}
}
