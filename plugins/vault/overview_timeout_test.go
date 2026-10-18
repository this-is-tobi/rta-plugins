package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A Vault that takes the connection and never answers held the overview for two
// minutes: the client waits a minute for each of the two reads, and what it gave
// up with was `vault.conn.failed could not reach ...: context deadline exceeded`,
// a failure to reach a server that had accepted the connection, with the hint
// every unclassified failure gets. The other plugins call it a timeout, say so
// in one code, and give the hint a dropped connection deserves.

// silentVault holds every request until the test is over.
func silentVault(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

func boundOverviewAt(t *testing.T, d time.Duration) {
	t.Helper()
	old := overviewTimeout
	overviewTimeout = d
	t.Cleanup(func() { overviewTimeout = old })
}

func TestAVaultThatNeverAnswersIsGivenUpOnAtTheOverviewsBound(t *testing.T) {
	boundOverviewAt(t, 150*time.Millisecond)
	srv := silentVault(t)
	for _, detail := range []bool{false, true} {
		started := time.Now()
		_, err := overviewOf(t, srv, detail)
		verr, ok := err.(*view.Error)
		if !ok || verr.Code != "vault.timeout" {
			t.Fatalf("detail %v: err = %#v, want vault.timeout", detail, err)
		}
		// The two reads of the compact page and the three of the detail share
		// the one bound: asked one after another they would each wait it out.
		if took := time.Since(started); took > 2*time.Second {
			t.Errorf("detail %v: gave up after %s, want about the bound of 150ms for the page", detail, took)
		}
		if !strings.Contains(verr.Message, "did not answer in time") || !strings.Contains(verr.Hint, "firewall that drops") {
			t.Errorf("detail %v: %q — %q, want the message and hint the other plugins give", detail, verr.Message, verr.Hint)
		}
	}
}

// The bound is the overview's. A call that reads or writes a secret is not held
// to the overview's ten seconds, and a snapshot, which moves the whole of the
// storage, is not held to any.
func TestTheOverviewsBoundIsNotTheBoundOfAnyOtherCall(t *testing.T) {
	boundOverviewAt(t, 20*time.Millisecond)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sealed":false,"initialized":true,"version":"1.14.4"}`))
	}))
	t.Cleanup(slow.Close)
	for _, c := range Plugin().Capabilities {
		if c.ID != "vault.seal.status" {
			continue
		}
		if _, err := c.Run(t.Context(), plugin.NewRequest(
			plugin.Resolve(c, plugin.Inputs{Caller: map[string]any{"url": slow.URL, "token": "t"}}), false, false)); err != nil {
			t.Errorf("vault.seal.status was cut short by the overview's bound: %v", err)
		}
	}
}

func TestADeadlineIsATimeoutAndNotAFailureToReach(t *testing.T) {
	r := req(t, "vault.seal.status", map[string]any{"url": "http://127.0.0.1:1"})
	for name, err := range map[string]error{
		"bare":    context.DeadlineExceeded,
		"wrapped": fmt.Errorf("Get \"http://x\": %w", context.DeadlineExceeded),
	} {
		verr := classify(err, r)
		if verr.Code != "vault.timeout" || !strings.Contains(verr.Hint, "firewall that drops") {
			t.Errorf("%s: %s — %s, want vault.timeout and its hint", name, verr.Code, verr.Hint)
		}
	}
}

// A 5xx is the service failing, not a setting wrong, and the hint was the page
// of every input or the bare status. Except a sealed Vault, which answers every
// path 503 "Vault is sealed" and is not helped by trying again.
func TestAServerErrorIsToldToBeRetriedAndASealedVaultToBeUnsealed(t *testing.T) {
	r := req(t, "vault.seal.status", map[string]any{"url": "http://127.0.0.1:1"})
	for _, code := range []int{500, 502, 503, 504} {
		verr := classify(&vaultapi.ResponseError{StatusCode: code, Errors: []string{"upstream trouble"}}, r)
		if verr.Code != "vault.request.failed" || !strings.Contains(verr.Hint, "the service is overloaded or restarting; retry") {
			t.Errorf("%d: %s — %s", code, verr.Code, verr.Hint)
		}
	}
	verr := classify(&vaultapi.ResponseError{StatusCode: 503, Errors: []string{"Vault is sealed"}}, r)
	if verr.Code != "vault.sealed" || strings.Contains(verr.Hint, "retry") {
		t.Errorf("a sealed Vault: %s — %s, want vault.sealed and no advice to retry", verr.Code, verr.Hint)
	}
	for _, code := range []int{400, 403, 404} {
		if verr := classify(&vaultapi.ResponseError{StatusCode: code}, r); strings.Contains(verr.Hint, "retry") {
			t.Errorf("%d: %q advises retrying a refusal", code, verr.Hint)
		}
	}
}
