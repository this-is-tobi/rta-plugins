package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A call is one attempt. The client retried a refused connection or a 5xx
// twice, a second or more apart, so a Vault that was down answered four
// seconds late with the refusal it would have had at once — measured, against
// a closed port: 3.7 seconds for vault.seal.status, 7.6 for the overview, which
// reads twice, where the redis, etcd and pg plugins answered in 40
// milliseconds. A server answering 503 stands for the failure here: counting
// what reaches it needs no wait on a timer that a refused connection would.
func TestACallThatFailsIsNotAskedAgainBehindTheCallersBack(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"errors":["Vault is sealed"]}`))
	}))
	t.Cleanup(srv.Close)

	_, err := overviewOf(t, srv, false)
	if err == nil {
		t.Fatal("a Vault answering 503 to everything produced a view")
	}
	// The overview reads twice: the seal status and the token. One attempt
	// each, and not the three each the client makes unprompted.
	if got := hits.Load(); got != 2 {
		t.Errorf("the Vault was asked %d times, want 2 — one attempt for each of the overview's two reads", got)
	}
}
