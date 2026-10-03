package main

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A server that speaks only TLS and answers plain HTTP with an alert, which
// Go's client quotes as a malformed response, or with the 400 of a Go TLS
// listener, has said what it needs; it is not a Keycloak that could not be
// reached. Keycloak's own listener hangs up instead (TestAHangUpOnPlainHTTPNamesTheScheme),
// and a proxy built on OpenSSL in front of it answers with the alert.
func TestATLSOnlyServerThatAnswersPlainHTTPIsNamedForIt(t *testing.T) {
	alert := errors.New("malformed HTTP response " + strings.TrimSuffix(strconv.Quote("\x15\x03\x03\x00\x02\x02"), `"`) + `"`)
	listener := errors.New("Client sent an HTTP request to an HTTPS server")
	for _, tc := range []struct {
		name, base string
		tunnel     plugin.Tunnel
		err        error
		code, hint string
	}{
		{"an alert, reached directly", "http://sso.internal:8443", plugin.TunnelNone, alert,
			"keycloak.tls.expected", "--url names the scheme"},
		{"a Go listener's 400, through a forward", "http://127.0.0.1:54321", plugin.TunnelKube, listener,
			"keycloak.tls.expected", "tunnelTLS: true on that connection"},
		{"an alert over HTTPS is no plain HTTP", "https://sso.internal", plugin.TunnelNone, alert,
			"keycloak.conn.failed", "`rta explain keycloak.overview`"},
	} {
		s := &session{req: plugin.NewRequest(nil, false, false).WithProfile("lab", tc.tunnel), base: tc.base}
		got := s.classifyTransport(&url.Error{Op: "Post", URL: tc.base + "/realms/master/protocol/openid-connect/token",
			Err: tc.err})
		if got.Code != tc.code || !strings.Contains(got.Hint, tc.hint) {
			t.Errorf("%s: %s %q, want %s with %q in the hint", tc.name, got.Code, got.Hint, tc.code, tc.hint)
		}
	}
}
