package main

import (
	"context"
	"crypto/tls"
	stdnet "net"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A certificate the server presented and nothing here accepts is the
// server's answer, so through a forward it names the profile. The local end
// of a forward is a port that closed with the call: a refusal naming it sent
// its reader to an address that no longer meant anything, as the refusals
// naming a login or a database did before they named the profile.
func TestACertificateRefusalThroughAForwardNamesTheProfile(t *testing.T) {
	ca := newPrivateCA(t)
	caFile := writeFile(t, "ca.pem", ca.pem)
	for _, tc := range []struct {
		name, code string
		cert       tls.Certificate
		values     map[string]any
	}{
		{"an unknown issuer", "mysql.tls.untrusted", ca.serverCert(t), map[string]any{"tls": "true"}},
		{"another name", "mysql.tls.name", ca.certFor(t, "db.internal", nil, "db.internal"),
			map[string]any{"tls": "true", "ca-file": caFile}},
		{"no name at all", "mysql.tls.name", ca.certFor(t, "MySQL_Server_Auto_Generated_Server_Certificate", nil),
			map[string]any{"tls": "true", "ca-file": caFile}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := tlsServer(t, tc.cert)
			tc.values["host"], tc.values["port"] = "127.0.0.1", port
			_, verr := connect(context.Background(), req(t, "mysql.status", tc.values).
				WithProfile("prod", plugin.TunnelKube))
			if verr == nil || verr.Code != tc.code {
				t.Fatalf("err = %v, want %s", verr, tc.code)
			}
			end := stdnet.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			if !strings.Contains(verr.Message, "profile prod (through its kube: forward)") ||
				strings.Contains(verr.Message, end) {
				t.Errorf("message = %q, want the profile named and not the forward's end %s", verr.Message, end)
			}
		})
	}
}
