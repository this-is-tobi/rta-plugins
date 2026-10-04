package main

import (
	"crypto/x509"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Three verdicts only macOS gives of a certificate it refuses, each answered
// in the system's words and each with a different way on, or none: a revoked
// certificate has no way round that is not a way around the check that caught
// it, a certificate whose issuer nothing holds wants that issuer's CA, and one
// Apple's policy refuses wants reissuing. They are driven here as the system
// words them, on any machine, through the same classification a failed call
// goes through.

// certValidFor is a certificate as the handshake hands it over, with the one
// thing Apple's validity rule reads, issued from now.
func certValidFor(days int) *x509.Certificate {
	now := time.Now()
	return &x509.Certificate{NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(0, 0, days)}
}

func verdictRefusal(t *testing.T, sf plugin.Surface, verdict sdktest.Verdict, sent ...*x509.Certificate) *view.Error {
	t.Helper()
	return refusedBy(t, "darwin", sf, verdict, sent...)
}

// refusedBy is what a call answers when the verifier of the named system
// refused the certificate with verdict.
func refusedBy(t *testing.T, system string, sf plugin.Surface, verdict sdktest.Verdict, sent ...*x509.Certificate) *view.Error {
	t.Helper()
	sdktest.VerifierSystem(t, system)
	r := req(t, "s3.overview", map[string]any{"endpoint": "s3.internal:9000"}).WithSurface(sf)
	err := &url.Error{Op: "Get", URL: "https://s3.internal:9000/",
		Err: sdktest.SystemVerdict("s3.internal", verdict, sent...)}
	return classify(err, r)
}

// Neither the CA file nor a mode that checks less is the way on from a
// certificate its issuer revoked: naming a CA runs Go's verifier in the
// system's place, which checks no revocation, so the offer would be the way
// around the very check that said no.
func TestARevokedCertificateIsAnsweredInTheSystemsWordsAndOffersNoWayAround(t *testing.T) {
	for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceTUI, plugin.SurfaceMCP} {
		verr := verdictRefusal(t, sf, sdktest.VerdictRevoked, certValidFor(90))
		if verr.Code != "s3.tls.rejected" {
			t.Fatalf("%s: code = %s, want s3.tls.rejected: %s", sf, verr.Code, verr.Message)
		}
		if want := "certificate is revoked"; !strings.Contains(verr.Message, want) {
			t.Errorf("%s: message = %q, want the system's words %q", sf, verr.Message, want)
		}
		for _, offered := range []string{"ca-file", sf.SettingName("ca-file"), sf.SettingName("tls")} {
			if strings.Contains(verr.Message+" "+verr.Hint, offered) {
				t.Errorf("%s: %q offers %q to a certificate that was revoked", sf, verr.Message+" "+verr.Hint, offered)
			}
		}
	}
}

func TestACertificateNothingHereTrustsNamesTheCAFileAndWhatItGivesUp(t *testing.T) {
	for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceTUI, plugin.SurfaceMCP} {
		verr := verdictRefusal(t, sf, sdktest.VerdictNotTrusted, certValidFor(90))
		if verr.Code != "s3.tls.untrusted" {
			t.Fatalf("%s: code = %s, want s3.tls.untrusted: %s", sf, verr.Code, verr.Message)
		}
		if !strings.Contains(verr.Message, "nothing here trusts") {
			t.Errorf("%s: message = %q, want it to read as untrusted", sf, verr.Message)
		}
		if !strings.HasPrefix(verr.Hint, sf.CAHint("ca-file")) {
			t.Errorf("%s: hint = %q, want it to open on %q", sf, verr.Hint, sf.CAHint("ca-file"))
		}
		if !strings.Contains(verr.Hint, sf.SettingName("ca-file")) || !strings.Contains(verr.Hint, "macOS") {
			t.Errorf("%s: hint = %q, want the CA file's setting and what naming one gives up on a Mac", sf, verr.Hint)
		}
		if strings.Contains(verr.Hint, sf.SettingName("tls")) {
			t.Errorf("%s: hint = %q offers a lesser TLS mode", sf, verr.Hint)
		}
	}
}

// A ten-year certificate, the usual one for a MinIO of one's own, is refused
// by a Mac as not standards compliant and by nothing else: the way on is a
// certificate that is valid for fewer days, never a CA file, which would get
// past the refusal by going around every check the system makes.
func TestACertificateApplesPolicyRefusesIsAnsweredWithTheLimitAndNeverACAFile(t *testing.T) {
	for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceTUI, plugin.SurfaceMCP} {
		verr := verdictRefusal(t, sf, sdktest.VerdictNotStandardsCompliant, certValidFor(3650))
		if verr.Code != "s3.tls.rejected" {
			t.Fatalf("%s: code = %s, want s3.tls.rejected: %s", sf, verr.Code, verr.Message)
		}
		if want := "certificate is not standards compliant"; !strings.Contains(verr.Message, want) {
			t.Errorf("%s: message = %q, want the system's words %q", sf, verr.Message, want)
		}
		if want := plugin.CertPolicyHint(sdktest.SystemVerdict("s3.internal", sdktest.VerdictNotStandardsCompliant,
			certValidFor(3650))); verr.Hint != want {
			t.Errorf("%s: hint = %q, want the policy's own: %q", sf, verr.Hint, want)
		}
		for _, want := range []string{"825", "reissue"} {
			if !strings.Contains(verr.Hint, want) {
				t.Errorf("%s: hint = %q, want it to say %q", sf, verr.Hint, want)
			}
		}
		if strings.Contains(verr.Message+" "+verr.Hint, "ca-file") {
			t.Errorf("%s: %q points a policy refusal at a CA file", sf, verr.Message+" "+verr.Hint)
		}
	}
}

// The validity rule is the system verifier's on macOS, so elsewhere a
// certificate that long-lived is not refused for it, and an error that merely
// words the same is answered with the words alone.
func TestTheValidityLimitIsAMacsAlone(t *testing.T) {
	verr := refusedBy(t, "linux", plugin.SurfaceCLI, sdktest.VerdictNotStandardsCompliant, certValidFor(3650))
	if verr.Code != "s3.tls.rejected" || strings.Contains(verr.Hint, "825") {
		t.Errorf("%s %q, want s3.tls.rejected with no rule of Apple's named", verr.Code, verr.Hint)
	}
}

// The same words answer other rules than the validity period, so a certificate
// that is within it, or none sent at all, is answered with the system's words
// and the reasons a certificate is checked for, and still never a CA file.
func TestACertificateRefusedAsNotStandardsCompliantWithinTheLimitIsNotBlamedOnItsDates(t *testing.T) {
	for name, sent := range map[string][]*x509.Certificate{
		"a certificate valid for 90 days": {certValidFor(90)},
		"none sent":                       nil,
	} {
		verr := verdictRefusal(t, plugin.SurfaceCLI, sdktest.VerdictNotStandardsCompliant, sent...)
		if verr.Code != "s3.tls.rejected" || !strings.Contains(verr.Message, "certificate is not standards compliant") {
			t.Errorf("%s: %s %q, want s3.tls.rejected in the system's words", name, verr.Code, verr.Message)
		}
		if strings.Contains(verr.Hint, "825") || strings.Contains(verr.Message+" "+verr.Hint, "ca-file") {
			t.Errorf("%s: hint = %q, want neither the validity limit nor a CA file", name, verr.Hint)
		}
	}
}
