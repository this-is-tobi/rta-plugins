package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/view"
)

// macOS verifies a server's certificate with the system's own verifier and
// passes its verdict on in its own words, which Go does not type: a revoked
// certificate, one whose issuer nothing holds, and one that breaks a rule of
// Apple's are each a bare error inside the handshake's. What this plugin
// answers to each was exercised on a Mac or not at all; sdktest.VerifierSystem
// makes the diagnostics read an error as that verifier worded it, so these run
// wherever CI does.
//
// They do not run in parallel with each other: the system they pick is one
// package-wide value.

var verdictSurfaces = []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceTUI, plugin.SurfaceMCP}

// refusedAs is what the plugin answers a handshake that failed with err, as
// the call's own classification of a transport failure reads it.
func refusedAs(sf plugin.Surface, tunnel plugin.Tunnel, err error) *view.Error {
	req := plugin.NewRequest(nil, false, false).WithSurface(sf)
	if tunnel != plugin.TunnelNone {
		req = req.WithProfile("lab", tunnel)
	}
	s := &session{req: req, base: "https://sso.internal"}
	return s.classifyTransport(&url.Error{Op: "Post",
		URL: "https://sso.internal/realms/demo/protocol/openid-connect/token", Err: err})
}

// issued is a certificate valid for days from the date given.
func issued(from time.Time, days int) *x509.Certificate {
	return &x509.Certificate{NotBefore: from, NotAfter: from.AddDate(0, 0, days)}
}

var (
	tenYears      = issued(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), 3650)
	threeMonths   = issued(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), 90)
	grandfathered = issued(time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC), 3650)
)

// waysAround is what an answer to a certificate that no CA vouches for or no
// setting here can make right must not offer: the CA file, which runs Go's
// verifier in the system's place and so goes around every check the system
// makes, and a mode that checks less.
func waysAround(got *view.Error) []string {
	text := strings.ToLower(got.Message + " " + got.Hint)
	var found []string
	for _, word := range []string{"ca-file", "ca file", "tls-server-name", "insecure", "skip", "verification turned off"} {
		if strings.Contains(text, word) {
			found = append(found, word)
		}
	}
	return found
}

// A revoked certificate is answered in the system's own words, and with nothing
// that would get past the check that caught it: read as untrusted it was
// answered with the CA to name, and the operator who named it connected to a
// server with a revoked certificate, the revocation check gone.
func TestARevokedCertificateIsAnsweredInTheSystemsWordsWithNoWayAround(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for _, sf := range verdictSurfaces {
		for _, tunnel := range []plugin.Tunnel{plugin.TunnelNone, plugin.TunnelKube} {
			err := sdktest.SystemVerdict("sso", sdktest.VerdictRevoked, threeMonths)
			got := refusedAs(sf, tunnel, err)
			if got.Code != "keycloak.tls.rejected" {
				t.Errorf("%s through %q: classified %s %q, want keycloak.tls.rejected", sf, tunnel, got.Code, got.Message)
			}
			if !strings.Contains(got.Message, "certificate is revoked") {
				t.Errorf("%s through %q: message %q does not give the system's words", sf, tunnel, got.Message)
			}
			if ways := waysAround(got); len(ways) > 0 {
				t.Errorf("%s through %q: a revoked certificate was answered with %q in %q", sf, tunnel, ways,
					got.Message+" / "+got.Hint)
			}
		}
	}
}

// A chain the system cannot anchor is a certificate nothing here trusts, and
// the answer is the CA, named where it belongs on the surface that asked.
func TestACertificateNotTrustedIsNamedAsUntrustedWithTheCAFile(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for _, sf := range verdictSurfaces {
		err := sdktest.SystemVerdict("sso", sdktest.VerdictNotTrusted, threeMonths)
		got := refusedAs(sf, plugin.TunnelNone, err)
		if got.Code != "keycloak.tls.untrusted" || !strings.Contains(got.Message, "nothing here trusts") {
			t.Errorf("%s: classified %s %q, want keycloak.tls.untrusted", sf, got.Code, got.Message)
		}
		if !strings.Contains(got.Hint, sf.CAHint("ca-file")) || !strings.Contains(got.Hint, sf.SettingName("ca-file")) {
			t.Errorf("%s: hint %q does not name the CA file setting as the SDK words it, %q", sf, got.Hint, sf.CAHint("ca-file"))
		}
	}
}

// A certificate that breaks a rule of Apple's is not one with dates to check
// or a CA to name: the rule and its fix are what the reader is owed. The one
// the SDK reads is the validity period, and only when the certificate sent
// breaks it; any other "not standards compliant" is answered in the system's
// words alone — and never with a CA file, which would get past the refusal by
// going around every check the system makes.
func TestACertificateNotStandardsCompliantNamesTheRuleAndNeverACAFile(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for _, sf := range verdictSurfaces {
		err := sdktest.SystemVerdict("sso", sdktest.VerdictNotStandardsCompliant, tenYears)
		got := refusedAs(sf, plugin.TunnelNone, err)
		if got.Code != "keycloak.tls.rejected" || !strings.Contains(got.Message, "certificate is not standards compliant") {
			t.Errorf("%s: classified %s %q, want keycloak.tls.rejected in the system's words", sf, got.Code, got.Message)
		}
		if want := plugin.CertPolicyHint(err); want == "" || got.Hint != want {
			t.Errorf("%s: hint %q, want the SDK's %q", sf, got.Hint, want)
		}
		for _, want := range []string{"825 days", "reissue"} {
			if !strings.Contains(got.Hint, want) {
				t.Errorf("%s: hint %q does not say %q", sf, got.Hint, want)
			}
		}
		if ways := waysAround(got); len(ways) > 0 {
			t.Errorf("%s: a policy refusal was answered with %q in %q", sf, ways, got.Message+" / "+got.Hint)
		}
	}

	for name, sent := range map[string][]*x509.Certificate{
		"a certificate within the limit":       {threeMonths},
		"a certificate from before the rule":   {grandfathered},
		"no certificate sent with the verdict": nil,
	} {
		err := sdktest.SystemVerdict("sso", sdktest.VerdictNotStandardsCompliant, sent...)
		got := refusedAs(plugin.SurfaceCLI, plugin.TunnelNone, err)
		if got.Code != "keycloak.tls.rejected" || !strings.Contains(got.Message, "certificate is not standards compliant") {
			t.Errorf("%s: classified %s %q, want keycloak.tls.rejected in the system's words", name, got.Code, got.Message)
		}
		if strings.Contains(got.Hint, "825 days") {
			t.Errorf("%s: hint %q names a limit the certificate does not break", name, got.Hint)
		}
		if ways := waysAround(got); len(ways) > 0 {
			t.Errorf("%s: a policy refusal was answered with %q in %q", name, ways, got.Message+" / "+got.Hint)
		}
	}
}

// Every verdict is read by the system that gave it. Go's own verifier says a
// chain it cannot anchor in a typed error on every system, and that is a
// question of trust; the system's untyped words are, only where they are the
// system's. Elsewhere an untyped answer is never a verdict on trust, and a name
// or a date that fails is not the CA's to fix either — nor is any of them a
// server that could not be reached.
func TestACertificateThePlatformDoesNotTrustIsNamedAsUntrusted(t *testing.T) {
	typed := func(reason error) error { return &tls.CertificateVerificationError{Err: reason} }
	for _, tc := range []struct {
		name, goos string
		err        error
		want       string
	}{
		{"Go's own verifier", "linux", typed(x509.UnknownAuthorityError{}), "keycloak.tls.untrusted"},
		{"Go's own verifier, which a Mac runs too once a CA file is named", "darwin",
			typed(x509.UnknownAuthorityError{}), "keycloak.tls.untrusted"},
		{"the system's verifier, a chain it cannot anchor", "darwin",
			sdktest.SystemVerdict("sso", sdktest.VerdictNotTrusted), "keycloak.tls.untrusted"},
		{"the system's words on a system that does not speak them", "linux",
			sdktest.SystemVerdict("sso", sdktest.VerdictNotTrusted), "keycloak.tls.rejected"},
		{"the system's verifier, a revoked certificate", "darwin",
			sdktest.SystemVerdict("sso", sdktest.VerdictRevoked), "keycloak.tls.rejected"},
		{"a revoked certificate's words elsewhere", "linux",
			sdktest.SystemVerdict("sso", sdktest.VerdictRevoked), "keycloak.tls.rejected"},
		{"the system's verifier, a rule of its own", "darwin",
			sdktest.SystemVerdict("sso", sdktest.VerdictNotStandardsCompliant), "keycloak.tls.rejected"},
		{"a name it is not for", "darwin",
			typed(x509.HostnameError{Certificate: &x509.Certificate{}, Host: "sso.internal"}), "keycloak.tls.rejected"},
		{"a date it is not valid on", "darwin",
			typed(x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired}), "keycloak.tls.rejected"},
		// Go's verifier types each reason of its own, and no CA cures one.
		{"a signature algorithm it refuses", "linux",
			typed(x509.InsecureAlgorithmError(x509.SHA1WithRSA)), "keycloak.tls.rejected"},
		{"a critical extension it does not handle", "linux",
			typed(x509.UnhandledCriticalExtension{}), "keycloak.tls.rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sdktest.VerifierSystem(t, tc.goos)
			got := refusedAs(plugin.SurfaceCLI, plugin.TunnelNone, tc.err)
			if got.Code != tc.want {
				t.Fatalf("classified %s %q, want %s", got.Code, got.Message, tc.want)
			}
			var verdict *tls.CertificateVerificationError
			if got.Code == "keycloak.tls.rejected" && errors.As(tc.err, &verdict) &&
				!strings.Contains(got.Message, verdict.Err.Error()) {
				t.Errorf("%q does not quote the verdict %q", got.Message, verdict.Err)
			}
		})
	}
}

// A certificate's names are the server's to choose, and a verdict quotes
// them: one valid for "connection refused" or "no route to host" alone, or a
// revoked one the system names so, is still a certificate that does not
// verify — never a port nothing listens on or a host no route reaches, as the
// dial's words, read first, would have had it.
func TestACertificatesOwnNamesAreNeverReadAsTheDialsFailure(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for _, verdict := range []error{
		&tls.CertificateVerificationError{Err: x509.HostnameError{
			Certificate: &x509.Certificate{DNSNames: []string{syscall.ECONNREFUSED.Error()}}, Host: "sso.internal"}},
		&tls.CertificateVerificationError{Err: x509.HostnameError{
			Certificate: &x509.Certificate{DNSNames: []string{syscall.EHOSTUNREACH.Error()}}, Host: "sso.internal"}},
		sdktest.SystemVerdict(syscall.ECONNREFUSED.Error(), sdktest.VerdictRevoked),
		sdktest.SystemVerdict(syscall.EHOSTUNREACH.Error(), sdktest.VerdictNotStandardsCompliant),
	} {
		if got := refusedAs(plugin.SurfaceCLI, plugin.TunnelNone, verdict); got.Code != "keycloak.tls.rejected" {
			t.Errorf("%v: classified %s %q, want keycloak.tls.rejected", verdict, got.Code, got.Message)
		}
	}
}
