package main

import (
	"crypto/x509"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
)

// A verdict only macOS gives is tested where CI runs: the system's verifier is
// the one that says a certificate is revoked, or not standards compliant, in
// words Go passes on untyped, and sdktest.VerifierSystem makes the SDK's
// diagnostics read a handshake's error as that verifier would have worded it.

var surfaces = []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceMCP, plugin.SurfaceTUI}

// verdictRequest is a call that asks for TLS at true with no CA named: the one
// in which the system's verifier answers.
func verdictRequest(t *testing.T, surface plugin.Surface) plugin.Request {
	t.Helper()
	return req(t, "mariadb.overview", map[string]any{"host": "db.internal", "tls": "true"}).WithSurface(surface)
}

// namedLeaf is the certificate a handshake that reached the system's verifier
// holds: the verifier runs after the name matched, so it names the host.
func namedLeaf() *x509.Certificate { return &x509.Certificate{DNSNames: []string{"db.internal"}} }

// noWayRound fails for every setting a refusal of a certificate the system's
// checks caught could offer in their place. A CA file runs Go's verifier
// instead, which checks no revocation and none of Apple's policy, and every
// mode below true checks less than the name and the chain: whoever followed
// the hint connected to the server the check had caught.
func noWayRound(t *testing.T, r plugin.Request, hint string) {
	t.Helper()
	for _, offered := range []string{"ca-file", "skip-verify", "verify-ca", "preferred", r.Surface().SettingTo("tls", "false")} {
		if strings.Contains(hint, offered) {
			t.Errorf("hint = %q offers %q, a way round the check that refused the certificate", hint, offered)
		}
	}
}

// A certificate that names no host is read as one that would be refused by
// name the moment its chain was trusted, and answered with verify-ca, which
// checks the chain against a CA file without a name. On macOS the system's
// verifier answers when no ca-file is named, and says "revoked" untyped; a
// ca-file runs Go's verifier in its place, which checks no revocation, so a
// revoked certificate answered that way was a server the check had caught,
// reached by the operator who followed the hint. It keeps the system's
// words, with no way round.
func TestARevokedCertificateWithNoNamesIsNotAnsweredWithTheModeThatChecksNoRevocation(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	r := req(t, "mariadb.overview", map[string]any{"host": "db.internal"})
	got := classify(sdktest.SystemVerdict("db.internal", sdktest.VerdictRevoked, &x509.Certificate{}), r)
	if got.Code == "mariadb.tls.name" || strings.Contains(got.Hint, "verify-ca") {
		t.Errorf("got %s %q %q, want the system's verdict with no way round it", got.Code, got.Message, got.Hint)
	}
	if !strings.Contains(got.Message, "certificate is revoked") {
		t.Errorf("message %q does not quote the verdict", got.Message)
	}
}

// A revoked certificate is the system's to answer, in its own words, on every
// surface and whatever the handshake sent beside the verdict: not a failure to
// connect, which it was never, and not an invitation to name a CA or to check
// less.
func TestARevokedCertificateIsAnsweredInTheSystemsWordsWithNoWayRound(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for _, tc := range []struct {
		name string
		sent []*x509.Certificate
	}{
		{"with nothing sent", nil},
		{"with a certificate that names the host", []*x509.Certificate{namedLeaf()}},
		{"with a certificate that names no host", []*x509.Certificate{{}}},
	} {
		for _, surface := range surfaces {
			t.Run(tc.name+"/"+string(surface), func(t *testing.T) {
				r := verdictRequest(t, surface)
				err := sdktest.SystemVerdict("db.internal", sdktest.VerdictRevoked, tc.sent...)
				if !plugin.CertRevoked(err) {
					t.Fatalf("%v is not read as a revoked certificate, so this is no test of one", err)
				}
				got := classify(err, r)
				if got.Code != "mariadb.tls.rejected" {
					t.Fatalf("code = %q, want mariadb.tls.rejected: the server answered, with a certificate", got.Code)
				}
				if !strings.Contains(got.Message, "certificate is revoked") {
					t.Errorf("message = %q, want the system's own words", got.Message)
				}
				noWayRound(t, r, got.Hint)
			})
		}
	}
}
