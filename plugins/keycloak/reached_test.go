package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A refusal the server gave names the server as its reader reaches it again.
// Through a forward the base URL is 127.0.0.1 and a port that closed with the
// call, and "http://127.0.0.1:41233 refused the client credentials" named
// nothing the reader could change.
func TestARefusalTheServerGaveNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	s := &session{
		req:   plugin.NewRequest(nil, false, false).WithProfile("lab", plugin.TunnelKube),
		base:  "http://127.0.0.1:41233",
		realm: "master",
	}
	token := []byte(`{"error":"unauthorized_client","error_description":"Invalid client secret"}`)
	for name, got := range map[string]string{
		"token endpoint":   s.classifyToken(http.StatusUnauthorized, token).Message,
		"no such realm":    s.classifyToken(http.StatusNotFound, []byte(`{"error":"Realm does not exist"}`)).Message,
		"token rejected":   s.classifyStatus(http.StatusUnauthorized, nil).Message,
		"other admin code": s.classifyStatus(http.StatusInternalServerError, []byte(`{"errorMessage":"boom"}`)).Message,
	} {
		if !strings.Contains(got, "profile lab (through its kube: forward)") || strings.Contains(got, "41233") {
			t.Errorf("%s: message = %q, want the profile and its forward, not the forward's end", name, got)
		}
	}
}
