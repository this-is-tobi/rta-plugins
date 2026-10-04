package main

import (
	"crypto/x509"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
)

// Three refusals only macOS's verifier gives, answered as that verifier words
// them, from any machine the suite runs on: sdktest.VerifierSystem makes the
// diagnostics in package plugin read a handshake's error as a Mac's, and
// sdktest.SystemVerdict builds it as Go does. Each is checked on every surface
// and both through a forward and reached directly, since the answer names the
// setting its reader changes and where the call went.
//
// What each answer must not offer is as much the test as what it says: a CA
// file runs Go's verifier in the system's place, which checks no revocation
// and none of Apple's policy, so naming one to a revoked certificate or to
// one the policy refuses is the way around the check that caught it.

func tenYearLeaf() *x509.Certificate {
	issued := time.Date(2025, time.March, 1, 0, 0, 0, 0, time.UTC)
	return &x509.Certificate{NotBefore: issued, NotAfter: issued.Add(3650 * 24 * time.Hour)}
}

func ninetyDayLeaf() *x509.Certificate {
	issued := time.Date(2025, time.March, 1, 0, 0, 0, 0, time.UTC)
	return &x509.Certificate{NotBefore: issued, NotAfter: issued.Add(90 * 24 * time.Hour)}
}

type verdictCall struct {
	name string
	req  plugin.Request
}

func verdictCalls(t *testing.T) []verdictCall {
	t.Helper()
	var calls []verdictCall
	for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceTUI, plugin.SurfaceMCP} {
		direct := req(t, "qdrant.overview", map[string]any{"endpoint": "qdrant.internal:6333", "tls": true}).WithSurface(sf)
		forwarded := req(t, "qdrant.overview", map[string]any{"endpoint": "127.0.0.1:41233", "tls": true}).
			WithProfile("prod", plugin.TunnelKube).WithSurface(sf)
		calls = append(calls,
			verdictCall{string(sf) + ", reached directly", direct},
			verdictCall{string(sf) + ", through a forward", forwarded})
	}
	return calls
}

func handshakeFailed(verdict error) error {
	return &url.Error{Op: "Get", URL: "https://qdrant.internal:6333/", Err: verdict}
}

// offersAWayRound reports whether a hint hands its reader a CA file or a plain
// connection to go around the refusal with.
func offersAWayRound(sf plugin.Surface, hint string) bool {
	lower := strings.ToLower(hint)
	for _, way := range []string{strings.ToLower(sf.SettingName("ca-file")), "ca-file", "a ca file", "ca file",
		"tls=false", "plain http", "turn tls off", "turning tls off"} {
		if strings.Contains(lower, way) {
			return true
		}
	}
	return false
}

func TestARevokedCertificateIsAnsweredInTheSystemsWordsAndOffersNoWayRound(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for _, call := range verdictCalls(t) {
		t.Run(call.name, func(t *testing.T) {
			verdict := sdktest.SystemVerdict("qdrant.internal", sdktest.VerdictRevoked)
			if !plugin.CertRevoked(verdict) || plugin.CertUntrusted(verdict) {
				t.Fatalf("the verdict is not read as a revoked one: revoked %v, untrusted %v",
					plugin.CertRevoked(verdict), plugin.CertUntrusted(verdict))
			}
			got := classify(handshakeFailed(verdict), call.req)
			if got.Code != "qdrant.tls.rejected" {
				t.Fatalf("code = %s %q, want qdrant.tls.rejected: the server answered, with a certificate",
					got.Code, got.Message)
			}
			if !strings.Contains(got.Message, "certificate is revoked") {
				t.Errorf("message = %q, want the system's own words", got.Message)
			}
			if offersAWayRound(call.req.Surface(), got.Hint) {
				t.Errorf("hint = %q offers a way round the revocation check", got.Hint)
			}
		})
	}
}

func TestACertificateNothingHereTrustsIsReadAsUntrustedAndNamesTheCAFile(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for _, call := range verdictCalls(t) {
		t.Run(call.name, func(t *testing.T) {
			verdict := sdktest.SystemVerdict("qdrant.internal", sdktest.VerdictNotTrusted)
			got := classify(handshakeFailed(verdict), call.req)
			if got.Code != "qdrant.tls.untrusted" || !strings.Contains(got.Message, "a certificate nothing here trusts") {
				t.Fatalf("got %s %q, want qdrant.tls.untrusted reading as an untrusted certificate", got.Code, got.Message)
			}
			sf := call.req.Surface()
			if !strings.HasPrefix(got.Hint, sf.CAHint("ca-file")) || !strings.Contains(got.Hint, sf.SettingName("ca-file")) {
				t.Errorf("hint = %q, want it to open on the CA hint naming %s", got.Hint, sf.SettingName("ca-file"))
			}
			if !strings.Contains(got.Hint, "macOS makes") {
				t.Errorf("hint = %q, want what the CA file gives up on macOS said", got.Hint)
			}
			if !strings.Contains(got.Hint, "no way round it") {
				t.Errorf("hint = %q, want it to say turning TLS off is no way round", got.Hint)
			}
		})
	}
}

func TestACertificateApplesPolicyRefusesNamesTheLimitAndNeverTheCAFile(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	for _, call := range verdictCalls(t) {
		t.Run(call.name+", valid too long", func(t *testing.T) {
			verdict := sdktest.SystemVerdict("qdrant.internal", sdktest.VerdictNotStandardsCompliant, tenYearLeaf())
			if plugin.CertUntrusted(verdict) {
				t.Fatal("a policy refusal is read as an untrusted issuer")
			}
			got := classify(handshakeFailed(verdict), call.req)
			if got.Code != "qdrant.tls.rejected" || !strings.Contains(got.Message, "certificate is not standards compliant") {
				t.Fatalf("got %s %q, want qdrant.tls.rejected in the system's words", got.Code, got.Message)
			}
			if got.Hint != plugin.CertPolicyHint(verdict) {
				t.Errorf("hint = %q, want the SDK's policy hint %q", got.Hint, plugin.CertPolicyHint(verdict))
			}
			for _, want := range []string{"825 days", "reissue"} {
				if !strings.Contains(got.Hint, want) {
					t.Errorf("hint = %q, want %q said", got.Hint, want)
				}
			}
			if offersAWayRound(call.req.Surface(), got.Hint) {
				t.Errorf("hint = %q points a policy refusal at a CA file", got.Hint)
			}
		})
		// The same words answer other rules of Apple's, which the validity
		// period does not explain: a certificate within the limit, or one the
		// handshake sent no copy of, is the system's words alone.
		for name, sent := range map[string][]*x509.Certificate{
			"valid within the limit": {ninetyDayLeaf()}, "none sent": nil,
		} {
			t.Run(call.name+", "+name, func(t *testing.T) {
				verdict := sdktest.SystemVerdict("qdrant.internal", sdktest.VerdictNotStandardsCompliant, sent...)
				got := classify(handshakeFailed(verdict), call.req)
				if got.Code != "qdrant.tls.rejected" || !strings.Contains(got.Message, "certificate is not standards compliant") {
					t.Fatalf("got %s %q, want qdrant.tls.rejected in the system's words", got.Code, got.Message)
				}
				if strings.Contains(got.Hint, "825") || offersAWayRound(call.req.Surface(), got.Hint) {
					t.Errorf("hint = %q, want neither the validity rule nor a CA file", got.Hint)
				}
			})
		}
	}
}

// The same words on a system that is not a Mac are Go's own error and no
// verdict: never a question of trust, never a policy, and no CA file offered
// for them.
func TestTheSystemsVerdictsAreNotReadAsOnesOnAnyOtherSystem(t *testing.T) {
	sdktest.VerifierSystem(t, "linux")
	call := verdictCalls(t)[0]
	for name, verdict := range map[string]error{
		"revoked":                 sdktest.SystemVerdict("qdrant.internal", sdktest.VerdictRevoked),
		"not trusted":             sdktest.SystemVerdict("qdrant.internal", sdktest.VerdictNotTrusted),
		"not standards compliant": sdktest.SystemVerdict("qdrant.internal", sdktest.VerdictNotStandardsCompliant, tenYearLeaf()),
	} {
		got := classify(handshakeFailed(verdict), call.req)
		if got.Code != "qdrant.tls.rejected" {
			t.Errorf("%s: classified %s %q, want qdrant.tls.rejected", name, got.Code, got.Message)
		}
		if strings.Contains(got.Hint, "825") || offersAWayRound(call.req.Surface(), got.Hint) {
			t.Errorf("%s: hint = %q, want neither the validity rule nor a CA file", name, got.Hint)
		}
	}
}
