package main

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
)

// Three verdicts only macOS gives are answered here the way the system's own
// verifier words them, which these inject (sdktest.VerifierSystem) so that they
// are exercised wherever the tests run and not on a Mac alone. Each is read
// from a handshake's error by what the verifier said, and each has a way on and
// a way that must not be offered:
//
//	revoked              the system's words, and no CA file or lesser mode:
//	                     naming a CA runs Go's verifier in the system's place,
//	                     which checks no revocation, so the cure would be to
//	                     connect to a server whose certificate was revoked.
//	not trusted          an issuer nothing here vouches for: the CA file.
//	not standards        the certificate is too long-lived for Apple's policy:
//	compliant            reissue it, never a CA file.

var verdictSurfaces = []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceTUI, plugin.SurfaceMCP}

// certValidFor is a leaf issued on 1 January 2024 and valid for days.
func certValidFor(days int) *x509.Certificate {
	issued := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return &x509.Certificate{NotBefore: issued, NotAfter: issued.AddDate(0, 0, days)}
}

// offersAWayRound reports the first thing in hint that would hand its reader a
// way past the check that refused the certificate: a CA file, a plain-HTTP
// address, a mode that skips the check.
func offersAWayRound(hint string, sf plugin.Surface) (string, bool) {
	for _, way := range []string{"ca-file", sf.SettingName("ca-file"), "plain", "http://", "skip", "insecure"} {
		if strings.Contains(strings.ToLower(hint), strings.ToLower(way)) {
			return way, true
		}
	}
	return "", false
}

func TestACertificateTheSystemRevokedIsAnsweredInItsWordsWithNoWayRound(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	err := handshakeRefusal(sdktest.SystemVerdict("vault", sdktest.VerdictRevoked, certValidFor(90)))
	for _, sf := range verdictSurfaces {
		got := classify(err, req(t, "vault.seal.status", map[string]any{"address": "https://vault.internal:8200"}).WithSurface(sf))
		if got.Code != "vault.tls.rejected" {
			t.Errorf("%s: classified %s %q, want vault.tls.rejected: a revoked certificate is neither untrusted nor a failure to connect",
				sf, got.Code, got.Message)
		}
		if !strings.Contains(got.Message, "certificate is revoked") || strings.Contains(got.Message, "could not reach") {
			t.Errorf("%s: %q does not give the system's words", sf, got.Message)
		}
		if way, offered := offersAWayRound(got.Hint, sf); offered {
			t.Errorf("%s: hint %q offers %q to a revoked certificate", sf, got.Hint, way)
		}
	}
}

func TestAnIssuerTheSystemDoesNotTrustIsAnsweredWithTheCAFileSetting(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	err := handshakeRefusal(sdktest.SystemVerdict("vault", sdktest.VerdictNotTrusted, certValidFor(90)))
	for _, sf := range verdictSurfaces {
		got := classify(err, req(t, "vault.seal.status", map[string]any{"address": "https://vault.internal:8200"}).WithSurface(sf))
		if got.Code != "vault.tls.untrusted" {
			t.Errorf("%s: classified %s %q, want vault.tls.untrusted", sf, got.Code, got.Message)
		}
		if !strings.Contains(got.Hint, sf.CAHint("ca-file")) || !strings.Contains(got.Hint, sf.SettingName("ca-file")) {
			t.Errorf("%s: hint %q does not name the CA file setting and what it costs", sf, got.Hint)
		}
	}
}

// A certificate macOS refuses for its validity period is not one with dates
// to check: the rule and its fix are what the reader is owed, and the hint
// naming the dates, the use and the issuer sent them past it.
func TestACertificateAppleRefusesForItsLengthSaysSoAndNeverOffersACAFile(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	err := handshakeRefusal(sdktest.SystemVerdict("vault", sdktest.VerdictNotStandardsCompliant, certValidFor(3650)))
	for _, sf := range verdictSurfaces {
		got := classify(err, req(t, "vault.seal.status", map[string]any{"address": "https://vault.internal:8200"}).WithSurface(sf))
		if got.Code != "vault.tls.rejected" {
			t.Errorf("%s: classified %s %q, want vault.tls.rejected", sf, got.Code, got.Message)
		}
		if !strings.Contains(got.Message, "certificate is not standards compliant") {
			t.Errorf("%s: %q does not give the system's words", sf, got.Message)
		}
		if want := plugin.CertPolicyHint(err); want == "" || got.Hint != want {
			t.Errorf("%s: hint %q, want the policy's: %q", sf, got.Hint, want)
		}
		if !strings.Contains(got.Hint, "825 days") || !strings.Contains(got.Hint, "reissue") {
			t.Errorf("%s: hint %q does not name the 825-day limit and say to reissue", sf, got.Hint)
		}
		if way, offered := offersAWayRound(got.Hint, sf); offered {
			t.Errorf("%s: hint %q offers %q to a certificate the policy refused", sf, got.Hint, way)
		}
	}
}

// The same words answer other rules than the length, so a certificate that
// keeps to it is not told it breaks it.
func TestACertificateAppleRefusesForAnotherRuleIsNotToldItIsTooLong(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for name, sent := range map[string][]*x509.Certificate{
		"a certificate that keeps to the limit": {certValidFor(90)},
		"no certificate sent":                   nil,
	} {
		err := handshakeRefusal(sdktest.SystemVerdict("vault", sdktest.VerdictNotStandardsCompliant, sent...))
		got := classify(err, req(t, "vault.seal.status", map[string]any{"address": "https://vault.internal:8200"}))
		if got.Code != "vault.tls.rejected" || !strings.Contains(got.Message, "certificate is not standards compliant") {
			t.Errorf("%s: classified %s %q, want vault.tls.rejected in the system's words", name, got.Code, got.Message)
		}
		if strings.Contains(got.Hint, "825") {
			t.Errorf("%s: hint %q names a limit the certificate keeps to", name, got.Hint)
		}
		if way, offered := offersAWayRound(got.Hint, plugin.SurfaceCLI); offered {
			t.Errorf("%s: hint %q offers %q", name, got.Hint, way)
		}
	}
}
