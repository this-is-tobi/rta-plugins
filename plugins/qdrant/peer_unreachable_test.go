package main

import (
	"net/http"
	"strings"
	"testing"
)

const (
	// GET /collections/c1 at qd1 while qd3, which holds one of its three
	// shards, was stopped and its name no longer resolved.
	fxShowPeerGone = `{"status":{"error":"Service internal error: 1 of 1 read operations failed:\n  Service internal error: Tonic status error: code: 'The service is currently unavailable', message: \"dns error\", source: tonic::transport::Error(Transport, ConnectError(ConnectError(\"dns error\", Custom { kind: Uncategorized, error: \"failed to lookup address information: Name or service not known\" })))"},"time":0.3854595}`

	// The same, the moment after qd3 stopped, while its name still resolved.
	fxShowPeerRefused = `{"status":{"error":"Service internal error: 1 of 1 read operations failed:\n  Service internal error: Tonic status error: code: 'The service is currently unavailable', message: \"Failed to connect to http://dbwp-r12plug-qd3:6335/, error: transport error\""},"time":0.399360167}`
)

// A read that needs a shard on a peer that does not answer is a fact about the
// cluster, and was "returned 500" with the page of every input for a hint. It
// is named, the peer's address with it when the server quotes one, and the
// reader is sent to the page that says which peer.
func TestAReadThatNeedsAPeerThatDoesNotAnswerNamesThePeer(t *testing.T) {
	r := req(t, "qdrant.collection.show", map[string]any{"endpoint": "10.0.0.1:6333"})
	for name, tc := range map[string]struct {
		body, message string
	}{
		"refused": {fxShowPeerRefused,
			"10.0.0.1:6333 could not read from the peer at http://dbwp-r12plug-qd3:6335/, which does not answer"},
		"its name no longer resolving": {fxShowPeerGone, "10.0.0.1:6333 could not read from a peer that does not answer"},
	} {
		t.Run(name, func(t *testing.T) {
			verr := classifyStatus(http.StatusInternalServerError, []byte(tc.body), r)
			if verr.Code != "qdrant.peer.unreachable" || verr.Message != tc.message {
				t.Errorf("got %s: %q, want qdrant.peer.unreachable: %q", verr.Code, verr.Message, tc.message)
			}
			if !strings.Contains(verr.Hint, "rta qdrant overview") || !strings.Contains(verr.Hint, "which of them do not answer") {
				t.Errorf("hint = %q, want the overview named", verr.Hint)
			}
		})
	}
	if verr := classifyStatus(http.StatusInternalServerError, []byte(`{"status":{"error":"boom"}}`), r); verr.Code != "qdrant.request.failed" {
		t.Errorf("another 500 = %s, want qdrant.request.failed as before", verr.Code)
	}
}
