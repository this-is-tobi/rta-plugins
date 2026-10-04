package main

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
)

// The three refusals only macOS's own verifier gives, answered the way the
// plugin answers them on any machine: each in the system's words, and each
// with the way on that is its own. A revoked certificate has none — a CA
// file would run Go's verifier in the system's place, which checks no
// revocation, so naming one connects to the server with the certificate that
// was revoked; a certificate the system does not trust is the one a CA file
// cures, with the cost said; one Apple's policy refuses for its length is
// cured by reissuing it, and a CA file would only get round the rule.
func TestAVerdictOnlyAMacGivesIsAnsweredForWhatItIs(t *testing.T) {
	sdktest.VerifierSystem(t, "darwin")
	tenYears := &x509.Certificate{
		NotBefore: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:  time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	withinPolicy := &x509.Certificate{
		NotBefore: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for _, tc := range []struct {
		name    string
		verdict sdktest.Verdict
		sent    []*x509.Certificate
		code    string
		// hint is the whole hint, where the SDK words it.
		hint func(sf plugin.Surface, err error) string
	}{
		{"a revoked certificate", sdktest.VerdictRevoked, []*x509.Certificate{tenYears}, "redis.tls.rejected", nil},
		{"a certificate the system does not trust", sdktest.VerdictNotTrusted, []*x509.Certificate{tenYears},
			"redis.tls.untrusted", func(sf plugin.Surface, _ error) string { return sf.CAHint("ca-file") }},
		{"a certificate valid for longer than the policy allows", sdktest.VerdictNotStandardsCompliant,
			[]*x509.Certificate{tenYears}, "redis.tls.rejected",
			func(_ plugin.Surface, err error) string { return plugin.CertPolicyHint(err) }},
		{"a policy refusal for another rule", sdktest.VerdictNotStandardsCompliant,
			[]*x509.Certificate{withinPolicy}, "redis.tls.rejected", nil},
		{"a policy refusal with no certificate sent to read", sdktest.VerdictNotStandardsCompliant, nil,
			"redis.tls.rejected", nil},
	} {
		for _, surface := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceMCP} {
			t.Run(tc.name+" on "+string(surface), func(t *testing.T) {
				err := sdktest.SystemVerdict("cache.internal", tc.verdict, tc.sent...)
				r := req(t, "redis.overview", nil).WithSurface(surface)
				got := classifyDial(err, "cache.internal:6379", r)
				if got.Code != tc.code {
					t.Fatalf("code = %s, want %s: %s", got.Code, tc.code, got.Message)
				}
				if tc.code == "redis.tls.rejected" && (!strings.Contains(got.Message, string(tc.verdict)) ||
					!strings.Contains(got.Message, "cache.internal")) {
					t.Errorf("message = %q, want the system's words: %q about cache.internal", got.Message, tc.verdict)
				}
				if tc.hint != nil {
					if want := tc.hint(r.Surface(), err); want == "" || got.Hint != want {
						t.Errorf("hint = %q, want %q", got.Hint, want)
					}
				}
				if tc.code == "redis.tls.untrusted" {
					if !strings.Contains(got.Hint, "ca-file") {
						t.Errorf("hint = %q, want the CA file setting named", got.Hint)
					}
					return
				}
				for _, offered := range []string{"ca-file", "tls", "skip", "insecure"} {
					if strings.Contains(got.Hint, offered) {
						t.Errorf("hint = %q offers %q, a way round a refusal the system gave for a reason of its own",
							got.Hint, offered)
					}
				}
				if tc.verdict == sdktest.VerdictNotStandardsCompliant && tc.hint != nil &&
					(!strings.Contains(got.Hint, "825 days") || !strings.Contains(got.Hint, "reissue")) {
					t.Errorf("hint = %q, want the 825-day limit and the certificate reissued", got.Hint)
				}
			})
		}
	}
}

// The system's words are nobody's but the system's: asked on another, the
// same untyped error is no verdict on trust and no Apple policy, and is
// answered as a certificate that does not verify, in the words it came in.
func TestAMacsVerdictIsNoVerdictElsewhere(t *testing.T) {
	sdktest.VerifierSystem(t, "linux")
	tenYears := &x509.Certificate{
		NotBefore: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:  time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for _, verdict := range []sdktest.Verdict{sdktest.VerdictNotTrusted, sdktest.VerdictNotStandardsCompliant} {
		t.Run(string(verdict), func(t *testing.T) {
			got := classifyDial(sdktest.SystemVerdict("cache.internal", verdict, tenYears), "cache.internal:6379",
				req(t, "redis.overview", nil).WithSurface(plugin.SurfaceCLI))
			if got.Code != "redis.tls.rejected" || !strings.Contains(got.Message, string(verdict)) {
				t.Errorf("got %s %q, want redis.tls.rejected in the verdict's own words", got.Code, got.Message)
			}
			if strings.Contains(got.Hint, "825") || strings.Contains(got.Hint, "ca-file") {
				t.Errorf("hint = %q, want neither Apple's rule nor a CA file read into words that are no verdict here", got.Hint)
			}
		})
	}
}
