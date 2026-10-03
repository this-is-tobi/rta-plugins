package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The status page names the server as its reader reaches it again. Through a
// forward the address is 127.0.0.1 and a port that closed with the call.
func TestTheStatusPageNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	for _, tc := range []struct {
		name, profile string
		tunnel        plugin.Tunnel
		want          string
	}{
		{"no profile", "", plugin.TunnelNone, "127.0.0.1:54321"},
		{"a profile reached directly", "prod", plugin.TunnelNone, "127.0.0.1:54321 (profile prod)"},
		{"a profile through a forward", "prod", plugin.TunnelKube, "profile prod (through its kube: forward)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t, "mariadb.status", map[string]any{"host": "127.0.0.1", "port": 54321}).
				WithProfile(tc.profile, tc.tunnel)
			pairs := statusPairs(serverInfo{version: "8.4.0", flavour: "MariaDB"}, r)
			if pairs[0].Key != "server" || pairs[0].Value != tc.want {
				t.Errorf("first row = %q: %q, want server: %q", pairs[0].Key, pairs[0].Value, tc.want)
			}
		})
	}
}
