package main

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
)

// A certificate macOS refuses for its validity period is not one with dates
// to check: the rule and its fix are what the reader is owed, and the fix is
// the certificate, never a CA file, which would get past the refusal by going
// around every check the system makes. The rule is macOS's, and the system's
// words are all there is where the length does not explain the refusal.
func TestACertificateAppleRefusesForItsLengthSaysSo(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	leaf := func(valid time.Duration) *x509.Certificate {
		issued := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		return &x509.Certificate{DNSNames: []string{"db.internal"}, NotBefore: issued, NotAfter: issued.Add(valid)}
	}
	const day = 24 * time.Hour
	for _, tc := range []struct {
		name string
		sent []*x509.Certificate
		says bool
	}{
		{"ten years", []*x509.Certificate{leaf(3653 * day)}, true},
		{"a day over the limit", []*x509.Certificate{leaf(826 * day)}, true},
		{"within the limit", []*x509.Certificate{leaf(825 * day)}, false},
		{"nothing sent", nil, false},
	} {
		for _, surface := range surfaces {
			t.Run(tc.name+"/"+string(surface), func(t *testing.T) {
				r := verdictRequest(t, surface)
				err := sdktest.SystemVerdict("db.internal", sdktest.VerdictNotStandardsCompliant, tc.sent...)
				got := classify(err, r)
				if got.Code != "mysql.tls.rejected" {
					t.Fatalf("code = %q, want mysql.tls.rejected", got.Code)
				}
				if !strings.Contains(got.Message, "certificate is not standards compliant") {
					t.Errorf("message = %q, want the system's own words", got.Message)
				}
				noWayRound(t, r, got.Hint)
				if !tc.says {
					if strings.Contains(got.Hint, "825 days") {
						t.Errorf("hint = %q names a limit the certificate is within", got.Hint)
					}
					return
				}
				if want := plugin.CertPolicyHint(err); want == "" || got.Hint != want {
					t.Errorf("hint = %q, want the policy's own: %q", got.Hint, want)
				}
				if !strings.Contains(got.Hint, "825 days") || !strings.Contains(got.Hint, "reissue") {
					t.Errorf("hint = %q, want the 825-day limit named and the certificate reissued", got.Hint)
				}
			})
		}
	}
}
