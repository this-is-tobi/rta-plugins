package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What macOS's own verifier says of a certificate it refuses — revoked, not
// trusted, not standards compliant — reaches a plugin as words in an untyped
// error, and nothing but a Mac gives it, so a plugin's answer to each was
// tested on a Mac or not at all. The verdicts are injected instead
// (sdktest.VerifierSystem, sdktest.SystemVerdict), in the words the system
// gives them, and each is driven through classify, which is where every
// connection's failure is answered, on every system.
//
// What each is owed is not the same. A revoked certificate is answered in the
// system's words with no way round it: a CA file replaces the system's checks
// with Go's verifier, which checks no revocation, and a lesser sslmode checks
// nothing, so either would be the way to the server the check had caught. A
// certificate the system does not trust is the one a CA file is the cure for,
// at the cost the hint says. One Apple's policy refuses is answered with the
// rule and with reissuing it, never with a CA file, since the certificate is
// not untrusted at all.

// verdictCalls is every shape of call a macOS verdict can arrive on: the
// system's own verifier runs whenever no CA file is named and the mode
// verifies the server, directly or through a forward that was turned to TLS.
var verdictCalls = []struct {
	name   string
	values map[string]any
	tunnel plugin.Tunnel
}{
	{"direct, no CA named", map[string]any{"host": "db.internal", "port": 5432, "sslmode": "verify-full"},
		plugin.TunnelNone},
	{"direct, the system's own store", map[string]any{"host": "db.internal", "port": 5432,
		"sslmode": "verify-full", "sslrootcert": "system"}, plugin.TunnelNone},
	{"through a kube forward", map[string]any{"host": "127.0.0.1", "port": 54321, "sslmode": "disable",
		"tls-server-name": "db.internal"}, plugin.TunnelKube},
}

func verdictRequest(t *testing.T, values map[string]any, tunnel plugin.Tunnel) plugin.Request {
	t.Helper()
	r := reqFor(t, "pg.status", values)
	if tunnel != plugin.TunnelNone {
		r = r.WithProfile("prod", tunnel)
	}
	return r
}

func leafValidFor(valid time.Duration, issued time.Time) *x509.Certificate {
	return &x509.Certificate{DNSNames: []string{"db.internal"}, NotBefore: issued, NotAfter: issued.Add(valid)}
}

func refusalOf(t *testing.T, err error, r plugin.Request) *view.Error {
	t.Helper()
	verr := classify(err, r)
	if verr == nil {
		t.Fatal("classify answered nothing")
	}
	return verr
}

// noWayRound fails the test when text offers a CA file or a mode that checks
// less: the two ways to a server a refused certificate must not have.
func noWayRound(t *testing.T, what, text string) {
	t.Helper()
	for _, forbidden := range []string{"sslrootcert", "CA", "disable", "prefer", "require", "verify-ca"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("%s offers %q, a way round a refusal that has none: %q", what, forbidden, text)
		}
	}
}

func TestARevokedCertificateIsAnsweredInTheSystemsWordsWithNoWayRound(t *testing.T) {
	closing := string(rune(0x201d))
	for _, system := range []string{"darwin", "ios"} {
		for _, call := range verdictCalls {
			t.Run(system+"/"+call.name, func(t *testing.T) {
				sdktest.VerifierSystem(t, system)
				r := verdictRequest(t, call.values, call.tunnel)
				for name, err := range map[string]error{
					"as the handshake returns it": sdktest.SystemVerdict("db.internal", sdktest.VerdictRevoked,
						leafValidFor(90*24*time.Hour, time.Now())),
					"inside the driver's own error": fmt.Errorf("failed to connect: tls error: %w",
						sdktest.SystemVerdict("db.internal", sdktest.VerdictRevoked)),
					"named to read as untrusted": sdktest.SystemVerdict(
						"db.internal"+closing+" certificate is not trusted", sdktest.VerdictRevoked),
				} {
					verr := refusalOf(t, err, r)
					if verr.Code != "pg.tls.rejected" {
						t.Errorf("%s: code = %s, want pg.tls.rejected, the server answered with a certificate: %s",
							name, verr.Code, verr.Message)
					}
					if !strings.Contains(verr.Message, "certificate is revoked") {
						t.Errorf("%s: message = %q, want the system's words", name, verr.Message)
					}
					noWayRound(t, name+": hint", verr.Hint)
				}
			})
		}
	}
}

func TestACertificateTheSystemDoesNotTrustIsAnsweredWithTheCAAndWhatItCosts(t *testing.T) {
	for _, system := range []string{"darwin", "ios"} {
		for _, call := range verdictCalls {
			t.Run(system+"/"+call.name, func(t *testing.T) {
				sdktest.VerifierSystem(t, system)
				verr := refusalOf(t, sdktest.SystemVerdict("db.internal", sdktest.VerdictNotTrusted),
					verdictRequest(t, call.values, call.tunnel))
				if verr.Code != "pg.tls.untrusted" || !strings.Contains(verr.Message, "a certificate nothing here trusts") {
					t.Errorf("got %s %q, want it read as untrusted", verr.Code, verr.Message)
				}
				if !strings.Contains(verr.Hint, "--sslrootcert") || !strings.Contains(verr.Hint, "replaces the system's") ||
					!strings.Contains(verr.Hint, "revocation") {
					t.Errorf("hint = %q, want the CA file's setting named and what naming one gives up", verr.Hint)
				}
			})
		}
	}
}

func TestACertificateApplesPolicyRefusesIsAnsweredWithTheRuleAndNeverACAFile(t *testing.T) {
	issued := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, system := range []string{"darwin", "ios"} {
		for _, call := range verdictCalls {
			t.Run(system+"/"+call.name, func(t *testing.T) {
				sdktest.VerifierSystem(t, system)
				r := verdictRequest(t, call.values, call.tunnel)

				long := sdktest.SystemVerdict("db.internal", sdktest.VerdictNotStandardsCompliant,
					leafValidFor(3650*24*time.Hour, issued))
				verr := refusalOf(t, long, r)
				if verr.Code != "pg.tls.rejected" || !strings.Contains(verr.Message, "certificate is not standards compliant") {
					t.Errorf("got %s %q, want the system's words in pg.tls.rejected", verr.Code, verr.Message)
				}
				if !strings.Contains(verr.Hint, "825 days") || !strings.Contains(verr.Hint, "reissue it") {
					t.Errorf("hint = %q, want the 825-day rule and the certificate to reissue", verr.Hint)
				}
				noWayRound(t, "the policy hint", verr.Hint)

				// The same words are another rule's when the certificate keeps
				// this one — a critical extension, a name its CA may not sign —
				// and are then the system's alone, with no rule to name and no
				// CA file either.
				for name, short := range map[string]error{
					"valid within the limit": sdktest.SystemVerdict("db.internal", sdktest.VerdictNotStandardsCompliant,
						leafValidFor(398*24*time.Hour, issued)),
					"issued before the rule": sdktest.SystemVerdict("db.internal", sdktest.VerdictNotStandardsCompliant,
						leafValidFor(3650*24*time.Hour, time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC))),
					"no certificate sent": sdktest.SystemVerdict("db.internal", sdktest.VerdictNotStandardsCompliant),
				} {
					verr := refusalOf(t, short, r)
					if verr.Code != "pg.tls.rejected" || strings.Contains(verr.Hint, "825") {
						t.Errorf("%s: got %s %q, want the system's words and no rule it did not break",
							name, verr.Code, verr.Hint)
					}
					noWayRound(t, name+": hint", verr.Hint)
				}
			})
		}
	}
}

// The hint beside a refusal that is none of the ones with a cure of their own
// says what a certificate is checked for, and names sslmode's verify-full as
// the mode that checks the host too. Through a forward sslmode is not the
// caller's: the host sets it to disable and refuses one given, so the hint sent
// its reader to a setting that opens no forward at all, for a certificate
// that was being checked at verify-full already.
func TestARefusedCertificateThroughAForwardDoesNotOfferTheModeTheHostRefuses(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for name, err := range map[string]error{
		"an expired certificate": &tls.CertificateVerificationError{Err: x509.CertificateInvalidError{
			Cert: leafValidFor(time.Hour, time.Now()), Reason: x509.Expired}},
		"a revoked one": sdktest.SystemVerdict("db.internal", sdktest.VerdictRevoked),
	} {
		forwarded := refusalOf(t, err, verdictRequest(t, verdictCalls[2].values, verdictCalls[2].tunnel))
		if forwarded.Code != "pg.tls.rejected" || strings.Contains(forwarded.Hint, "sslmode") {
			t.Errorf("%s through a forward: %s %q, want a hint that names no sslmode", name, forwarded.Code, forwarded.Hint)
		}
		direct := refusalOf(t, err, verdictRequest(t, verdictCalls[0].values, verdictCalls[0].tunnel))
		if !strings.Contains(direct.Hint, "--sslmode verify-full for the host in --host too") {
			t.Errorf("%s directly: hint = %q, want verify-full named as the mode that checks the host", name, direct.Hint)
		}
	}
}

// Where no system's verifier gave them, the same words are nobody's verdict: a
// Linux handshake has no revoked, and a plugin that read them as one would be
// reading a certificate's name, which anybody can choose.
func TestTheSystemsWordsAreNotAVerdictWhereNoSystemGaveThem(t *testing.T) {
	for _, verdict := range []sdktest.Verdict{sdktest.VerdictRevoked, sdktest.VerdictNotTrusted,
		sdktest.VerdictNotStandardsCompliant} {
		t.Run(string(verdict), func(t *testing.T) {
			sdktest.VerifierSystem(t, "linux")
			err := sdktest.SystemVerdict("db.internal", verdict, leafValidFor(3650*24*time.Hour,
				time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
			verr := refusalOf(t, err, verdictRequest(t, verdictCalls[0].values, plugin.TunnelNone))
			if verr.Code != "pg.tls.rejected" || !strings.Contains(verr.Message, string(verdict)) {
				t.Errorf("got %s %q, want %q kept in the verifier's own words", verr.Code, verr.Message, verdict)
			}
			noWayRound(t, "the hint", verr.Hint)
		})
	}
}

// Go's own verifier runs wherever a CA file is named, on every system, and its
// refusal for an issuer no pool holds is the same answer as the system's, so
// the CA file's hint reads alike on every system.
func TestAnUnknownIssuerIsAnsweredWithTheCAOnEverySystem(t *testing.T) {
	for _, system := range []string{"darwin", "ios", "linux"} {
		t.Run(system, func(t *testing.T) {
			sdktest.VerifierSystem(t, system)
			verr := refusalOf(t, errors.Join(errors.New("tls"), x509.UnknownAuthorityError{}),
				verdictRequest(t, verdictCalls[0].values, plugin.TunnelNone))
			if verr.Code != "pg.tls.untrusted" || !strings.Contains(verr.Hint, "--sslrootcert") ||
				!strings.Contains(verr.Hint, "replaces the system's") {
				t.Errorf("got %s %q, want the CA file named and what it replaces", verr.Code, verr.Hint)
			}
		})
	}
}
