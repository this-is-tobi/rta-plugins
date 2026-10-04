package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Three refusals only macOS's verifier gives, which Go passes on untyped inside
// the handshake's error, in the system's words: a certificate its issuer
// revoked, one whose issuer nothing holds, and one that breaks a rule of
// Apple's own. They are read by words, and by the system that said them, so
// each is driven here as that system words it (sdktest.VerifierSystem), on
// whatever machine runs the tests. None of the three is answered with a way
// around the check that refused it: a CA file runs Go's verifier in the
// system's place, which checks no revocation and none of Apple's policy, and
// no setting here loosens a certificate check.

var verdictSurfaces = []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceTUI, plugin.SurfaceMCP}

// waysAround are the words of an answer that sends its reader past the check
// that refused the certificate: a CA file to name, or a mode that checks less.
var waysAround = []string{"ca-file", "CA file", "the CA that issued", "tls-server-name", "insecure", "skip", "plaintext"}

func wayAround(hint string) string {
	for _, w := range waysAround {
		if strings.Contains(hint, w) {
			return w
		}
	}
	return ""
}

// handshakeOf is err as the diagnostic dial hands it to classify: the
// handshake's own error, which is where every certificate refusal arrives.
func handshakeOf(err error) error { return handshakeError{err} }

// theSystemsWords is what the verifier said, past the handshake's own prefix.
func theSystemsWords(t *testing.T, err error) string {
	t.Helper()
	var verifyErr *tls.CertificateVerificationError
	if !errors.As(err, &verifyErr) {
		t.Fatalf("%v is not a certificate verification error", err)
	}
	return verifyErr.Err.Error()
}

func TestARevokedCertificateIsAnsweredInTheSystemsWordsWithNoWayAround(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	closing := string(rune(0x201d))
	for _, tc := range []struct{ name, certName string }{
		{"a name", "etcd-0"},
		// The system's words end the same way whatever the certificate is called,
		// and a name that spells the untrusted verdict is still a revoked one.
		{"a name that ends like the untrusted verdict", "a" + closing + " certificate is not trusted"},
	} {
		for _, sf := range verdictSurfaces {
			r := req(t, "etcd.overview", map[string]any{"endpoint": "etcd-0.internal:2379"}).WithSurface(sf)
			sent := sdktest.SystemVerdict(tc.certName, sdktest.VerdictRevoked)
			for call, got := range map[string]*view.Error{
				"a connection": classify(handshakeOf(sent), r),
				"a snapshot":   classifySnapshot(handshakeOf(sent), r),
			} {
				if got.Code != "etcd.tls.rejected" {
					t.Errorf("%s, %s, %s: classified %s %q, want the system's refusal quoted as etcd.tls.rejected",
						tc.name, sf, call, got.Code, got.Message)
				}
				if words := theSystemsWords(t, sent); !strings.Contains(got.Message, words) {
					t.Errorf("%s, %s, %s: message %q does not carry the system's words %q", tc.name, sf, call, got.Message, words)
				}
				if way := wayAround(got.Hint); way != "" {
					t.Errorf("%s, %s, %s: hint %q offers a way around the revocation check (%q)", tc.name, sf, call, got.Hint, way)
				}
			}
		}
	}

	// A member that is not the endpoint's own answers in the same words, in a cell.
	member := sdktest.SystemVerdict("etcd-1", sdktest.VerdictRevoked)
	why := unreachableWhy(member)
	if !plugin.CertRevoked(member) || !strings.Contains(why, string(sdktest.VerdictRevoked)) || wayAround(why) != "" {
		t.Errorf("a member whose certificate is revoked reads %q, want the system's words and no way around", why)
	}
}

func TestACertificateNoIssuerHereVouchesForIsAnsweredWithTheCAFileToName(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for _, sf := range verdictSurfaces {
		r := req(t, "etcd.overview", map[string]any{"endpoint": "etcd-0.internal:2379"}).WithSurface(sf)
		got := classify(handshakeOf(sdktest.SystemVerdict("etcd-0", sdktest.VerdictNotTrusted)), r)
		if got.Code != "etcd.tls.untrusted" || !strings.Contains(got.Message, "a certificate nothing here trusts") {
			t.Errorf("%s: classified %s %q, want it read as untrusted", sf, got.Code, got.Message)
		}
		if want := "etcd clusters usually have their own CA: " + sf.CAHint("ca-file"); got.Hint != want {
			t.Errorf("%s: hint %q, want %q", sf, got.Hint, want)
		}
		if !strings.Contains(got.Hint, sf.SettingName("ca-file")) {
			t.Errorf("%s: hint %q does not name the CA file setting", sf, got.Hint)
		}
	}
}

// The validity period is Apple's rule and its fix is the certificate: a CA file
// would get past it by going around every check the system makes.
func TestACertificateAppleRefusesForItsLengthSaysSoAndNeverNamesACAFile(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	tenYears := &x509.Certificate{
		NotBefore: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:  time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for _, sf := range verdictSurfaces {
		r := req(t, "etcd.overview", map[string]any{"endpoint": "etcd-0.internal:2379"}).WithSurface(sf)
		sent := sdktest.SystemVerdict("etcd-0", sdktest.VerdictNotStandardsCompliant, tenYears)
		got := classify(handshakeOf(sent), r)
		if got.Code != "etcd.tls.rejected" {
			t.Errorf("%s: classified %s %q, want etcd.tls.rejected", sf, got.Code, got.Message)
		}
		if !strings.Contains(got.Hint, "825 days") || !strings.Contains(got.Hint, "reissue") {
			t.Errorf("%s: hint %q, want the 825-day rule and the reissue that cures it", sf, got.Hint)
		}
		if got.Hint != plugin.CertPolicyHint(sent) {
			t.Errorf("%s: hint %q, want the SDK's %q", sf, got.Hint, plugin.CertPolicyHint(sent))
		}
		if way := wayAround(got.Hint); way != "" {
			t.Errorf("%s: hint %q offers a way around the policy check (%q)", sf, got.Hint, way)
		}
	}
}

// The same words answer other rules, and a certificate that is not too long
// lived is left to the system's own words.
func TestAPolicyRefusalNotAboutTheLengthIsLeftToTheSystemsWords(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	short := &x509.Certificate{
		NotBefore: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:  time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC),
	}
	r := req(t, "etcd.overview", map[string]any{"endpoint": "etcd-0.internal:2379"})
	for name, sent := range map[string]error{
		"a certificate valid for a year": sdktest.SystemVerdict("etcd-0", sdktest.VerdictNotStandardsCompliant, short),
		"no certificate sent":            sdktest.SystemVerdict("etcd-0", sdktest.VerdictNotStandardsCompliant),
	} {
		got := classify(handshakeOf(sent), r)
		if got.Code != "etcd.tls.rejected" || !strings.Contains(got.Message, theSystemsWords(t, sent)) {
			t.Errorf("%s: classified %s %q, want the system's words in etcd.tls.rejected", name, got.Code, got.Message)
		}
		if strings.Contains(got.Hint, "825") {
			t.Errorf("%s: hint %q names a rule the certificate does not break", name, got.Hint)
		}
		if way := wayAround(got.Hint); way != "" {
			t.Errorf("%s: hint %q offers a way around the policy check (%q)", name, got.Hint, way)
		}
	}
}

// Elsewhere an untyped answer is one of Go's own errors and never a verdict on
// trust, a revocation or Apple's policy.
func TestTheVerdictsOnlyAMacGivesAreNotReadAnywhereElse(t *testing.T) {
	tenYears := &x509.Certificate{
		NotBefore: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:  time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	sdktest.VerifierSystem(t, "linux")
	r := req(t, "etcd.overview", map[string]any{"endpoint": "etcd-0.internal:2379"})
	for name, sent := range map[string]error{
		"revoked":                 sdktest.SystemVerdict("etcd-0", sdktest.VerdictRevoked),
		"not trusted":             sdktest.SystemVerdict("etcd-0", sdktest.VerdictNotTrusted),
		"not standards compliant": sdktest.SystemVerdict("etcd-0", sdktest.VerdictNotStandardsCompliant, tenYears),
	} {
		got := classify(handshakeOf(sent), r)
		if got.Code != "etcd.tls.rejected" || !strings.Contains(got.Message, theSystemsWords(t, sent)) {
			t.Errorf("%s: classified %s %q, want the words quoted in etcd.tls.rejected", name, got.Code, got.Message)
		}
		if strings.Contains(got.Hint, "825") || wayAround(got.Hint) != "" {
			t.Errorf("%s: hint %q answers a verdict this system does not give", name, got.Hint)
		}
	}
}
