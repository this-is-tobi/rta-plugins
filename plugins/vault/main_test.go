package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
)

// sdktest is the definition of "a correct plugin" — no exemption for vault.
//
// Called with no inputs it drove one capability of fourteen — vault.snapshot,
// the only mutating one whose inputs all have defaults — and passed. Behind
// that, vault.kv.set really wrote a new secret version under --dry-run,
// vault.wrap.set really minted a live single-use token and printed it, and
// vault.transit.encrypt really used the key.
func TestConformance(t *testing.T) {
	sdktest.Check(t, Plugin(), sdktest.WithInputs(conformanceInputs), sdktest.WithSource("."))
}

// conformanceInputs points every mutating capability at an address nothing is
// listening on, so a dry run that stops being dry fails here as a refused
// connection rather than as a write to somebody's Vault.
func conformanceInputs(dir string) map[string]map[string]any {
	conn := func(m map[string]any) map[string]any {
		m["address"] = "http://127.0.0.1:1"
		m["token"] = "conformance"
		return m
	}
	// The restore's input is inside dir, beside everything else. The suite
	// snapshots dir after this function has run, before each dry run, so a
	// fixture written here is part of what a dry run is compared against
	// rather than a stray write — and one that touched it is caught.
	fixture := filepath.Join(dir, "restore.snap")
	_ = os.WriteFile(fixture, []byte("archive"), 0o600)
	return map[string]map[string]any{
		"vault.kv.get":          conn(map[string]any{"path": "app/db"}),
		"vault.kv.set":          conn(map[string]any{"path": "app/db", "data": []string{"password=s3cret"}}),
		"vault.kv.history":      conn(map[string]any{"path": "app/db"}),
		"vault.kv.delete":       conn(map[string]any{"path": "app/db"}),
		"vault.kv.undelete":     conn(map[string]any{"path": "app/db", "versions": []string{"1"}}),
		"vault.kv.destroy":      conn(map[string]any{"path": "app/db", "versions": []string{"1"}}),
		"vault.transit.encrypt": conn(map[string]any{"key": "app", "plaintext": "hello"}),
		"vault.transit.decrypt": conn(map[string]any{"key": "app", "ciphertext": "vault:v1:xxxx"}),
		"vault.wrap.set":        conn(map[string]any{"data": []string{"password=s3cret"}}),
		"vault.wrap.get":        conn(map[string]any{"wrapping-token": "hvs.conformance"}),
		// Already drivable from its defaults, but its default --out is a path
		// in the operator's own home. Pointed inside dir so the rule that
		// watches for a stray write is watching the place it would land.
		"vault.snapshot": conn(map[string]any{"out": filepath.Join(dir, "vault.snap")}),
		"vault.restore":  conn(map[string]any{"file": fixture}),
	}
}

// req builds a resolved request the way the host would, against the named
// capability's own declared inputs (connFields included, via cap) — so
// these test the values a handler actually sees, matching plugins/pg's own
// req helper.
func req(t *testing.T, capID string, values map[string]any) plugin.Request {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == capID {
			return plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false)
		}
	}
	t.Fatalf("no capability %q", capID)
	return plugin.Request{}
}

// Every classified failure has to say what to do next — the same bar
// plugins/pg's classify holds itself to, against Vault's error shapes.
func TestEveryClassifiedFailureNamesTheNextStep(t *testing.T) {
	r := req(t, "vault.seal.status", map[string]any{"address": "https://vault.internal:8200"})
	dial := func(errno syscall.Errno) error {
		return &url.Error{Op: "Get", URL: "https://x",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}}
	}
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"denied", &vaultapi.ResponseError{StatusCode: 403, Errors: []string{"permission denied"}}, "vault.denied"},
		{"not found", &vaultapi.ResponseError{StatusCode: 404}, "vault.notfound"},
		{"bad request", &vaultapi.ResponseError{StatusCode: 400, Errors: []string{"missing client token"}}, "vault.badrequest"},
		{"sealed", &vaultapi.ResponseError{StatusCode: 412}, "vault.sealed"},
		{"server error", &vaultapi.ResponseError{StatusCode: 500, Errors: []string{"internal error"}}, "vault.request.failed"},
		{"kv secret missing", vaultapi.ErrSecretNotFound, "vault.notfound"},
		{"refused", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, "vault.conn.refused"},
		// A dial is read by the operating system's error it carries, never by
		// the *net.OpError every failed dial is: a reset refused nothing, a
		// dial that timed out is a timeout, and a host no route reaches was
		// sent to check a port.
		{"refused by errno", dial(syscall.ECONNREFUSED), "vault.conn.refused"},
		{"no route", dial(syscall.EHOSTUNREACH), "vault.conn.unreachable"},
		{"no network", dial(syscall.ENETUNREACH), "vault.conn.unreachable"},
		{"reset", &url.Error{Op: "Get", URL: "https://x", Err: &net.OpError{Op: "read", Net: "tcp",
			Err: os.NewSyscallError("read", syscall.ECONNRESET)}}, "vault.conn.failed"},
		{"a dial that timed out", &url.Error{Op: "Get", URL: "https://x",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}}}, "vault.conn.timeout"},
		{"a refusal flattened into text", errors.New("Get https://x: dial tcp 10.0.0.9:8200: connect: " +
			syscall.ECONNREFUSED.Error()), "vault.conn.refused"},
		{"unknown host", &net.DNSError{Err: "no such host", Name: "vault.internal"}, "vault.host.unknown"},
		{"anything else", errors.New("something unexpected"), "vault.conn.failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verr := classify(tc.err, r)
			if verr.Code != tc.code {
				t.Errorf("code = %q, want %q", verr.Code, tc.code)
			}
			if verr.Hint == "" {
				t.Error("no hint")
			}
			if verr.Message == "" {
				t.Error("no message")
			}
		})
	}
}

// timeoutError is a net.Error that timed out, as a dial's deadline reports.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// macOS verifies against the system's trust store itself when no ca-file is
// set and reports a chain it cannot anchor as a bare error: that is still a
// certificate nothing here trusts, and the answer is the CA. Every other
// verdict it gives untyped is its own reason and is quoted in its words: a
// revoked certificate answered with the CA file to name was answered with the
// way around the revocation check. Elsewhere an untyped verdict is never a
// question of trust, and none is a server that could not be reached.
func TestACertificateThatFailsVerificationIsNamedForWhy(t *testing.T) {
	r := req(t, "vault.seal.status", map[string]any{"address": "https://vault.internal:8200"})
	for _, tc := range []struct {
		name, system string
		err          error
		want, quotes string
	}{
		{"Go's own verifier", "linux", x509.UnknownAuthorityError{}, "vault.tls.untrusted", ""},
		{"the system's verifier, a chain it cannot anchor", "darwin",
			sdktest.SystemVerdict("vault", sdktest.VerdictNotTrusted), "vault.tls.untrusted", ""},
		{"the system's verifier, a revoked certificate", "darwin",
			sdktest.SystemVerdict("vault", sdktest.VerdictRevoked), "vault.tls.rejected", "certificate is revoked"},
		// The same words from a system that gives no such verdict are one of
		// Go's own errors, which is never a question of trust.
		{"the system's words on a system that gives none", "linux",
			sdktest.SystemVerdict("vault", sdktest.VerdictNotTrusted), "vault.tls.rejected", "certificate is not trusted"},
		{"a name it is not for", "linux",
			x509.HostnameError{Certificate: &x509.Certificate{}, Host: "vault.internal"}, "vault.tls.rejected", "vault.internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sdktest.VerifierSystem(t, tc.system)
			got := classify(handshakeRefusal(tc.err), r)
			if got.Code != tc.want {
				t.Errorf("classified %s, want %s", got.Code, tc.want)
			}
			if !strings.Contains(got.Message, tc.quotes) {
				t.Errorf("%q does not quote the verdict %q", got.Message, tc.quotes)
			}
		})
	}
}

// handshakeRefusal is err as the HTTP client returns a handshake the verifier
// refused: the verdict inside a *tls.CertificateVerificationError, inside the
// *url.Error of the request. A verdict that already is one is wrapped no
// further.
func handshakeRefusal(err error) error {
	var verify *tls.CertificateVerificationError
	if !errors.As(err, &verify) {
		err = &tls.CertificateVerificationError{Err: err}
	}
	return &url.Error{Op: "Get", URL: "https://vault.internal:8200/v1/sys/seal-status", Err: err}
}

// A certificate's names are the server's to choose, and a verdict quotes
// them: one valid for "connection refused" or "no route to host" alone, or
// a revoked one the system names so, is still a certificate that does not
// verify — never a port nothing listens on or a host no route reaches, as
// the dial's words, read first, would have had it.
func TestACertificatesOwnNamesAreNeverReadAsTheDialsFailure(t *testing.T) {
	r := req(t, "vault.seal.status", map[string]any{"address": "https://vault.internal:8200"})
	sdktest.VerifierSystem(t, "darwin")
	for _, verdict := range []error{
		x509.HostnameError{Certificate: &x509.Certificate{DNSNames: []string{syscall.ECONNREFUSED.Error()}},
			Host: "vault.internal"},
		x509.HostnameError{Certificate: &x509.Certificate{DNSNames: []string{syscall.EHOSTUNREACH.Error()}},
			Host: "vault.internal"},
		sdktest.SystemVerdict(syscall.ECONNREFUSED.Error(), sdktest.VerdictRevoked),
	} {
		if got := classify(handshakeRefusal(verdict), r); got.Code != "vault.tls.rejected" {
			t.Errorf("%v: classified %s %q, want vault.tls.rejected", verdict, got.Code, got.Message)
		}
	}
}

// A name DNS cannot resolve is that, and not a port nothing listens on. The
// HTTP client wraps a failed lookup in a *net.OpError inside a *url.Error,
// and the refused-dial check, read first, answered "nothing is listening on
// http://nonexistent.invalid:8200" and asked whether the server was up, when
// the name was the problem.
func TestANameDNSCannotResolveIsNotReadAsNothingListening(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "http://vault.internal:8200/v1/sys/seal-status",
		Err: &net.OpError{Op: "dial", Net: "tcp",
			Err: &net.DNSError{Err: "no such host", Name: "vault.internal", IsNotFound: true}}}
	verr := classify(err, req(t, "vault.seal.status", map[string]any{"address": "http://vault.internal:8200"}))
	if verr.Code != "vault.host.unknown" {
		t.Errorf("code = %s, want vault.host.unknown: %s", verr.Code, verr.Message)
	}
}

// A token its policy refuses is sent to what the token can do — the
// capability that says so is vault.token.status. The hint once named `rta
// vault token lookup`, Vault's own verb for it and no command rta has, which
// the CLI refuses as unknown.
func TestARefusedTokenIsSentToTheTokensStatus(t *testing.T) {
	verr := classify(&vaultapi.ResponseError{StatusCode: 403, Errors: []string{"permission denied"}},
		req(t, "vault.kv.get", map[string]any{"path": "app/db"}))
	if !strings.Contains(verr.Hint, "`rta vault token status --address http://127.0.0.1:8200` shows what the current token can do") {
		t.Errorf("hint = %q, want it to name vault.token.status", verr.Hint)
	}
}

// A wrapped fmt.Errorf around vaultapi.ErrSecretNotFound — exactly what
// KVv2.Get/Put actually return, per the vendored source — must still
// classify: errors.Is has to see through the %w wrapping, not just match a
// bare sentinel nobody hands it directly.
func TestClassifyUnwrapsAWrappedSecretNotFound(t *testing.T) {
	wrapped := fmt.Errorf("error encountered while reading secret at secret/data/x: %w", vaultapi.ErrSecretNotFound)
	verr := classify(wrapped, req(t, "vault.kv.get", map[string]any{"path": "x"}))
	if verr.Code != "vault.notfound" {
		t.Errorf("code = %q, want vault.notfound", verr.Code)
	}
}

func TestDataFieldsParsesRepeatedKeyValuePairs(t *testing.T) {
	data, verr := dataFields(plugin.SurfaceCLI, []string{"user=admin", "pass=hunter2"})
	if verr != nil {
		t.Fatal(verr)
	}
	if data["user"] != "admin" || data["pass"] != "hunter2" {
		t.Errorf("data = %v", data)
	}
}

func TestDataFieldsRejectsAPairWithNoEquals(t *testing.T) {
	_, verr := dataFields(plugin.SurfaceCLI, []string{"user"})
	if verr == nil {
		t.Fatal("expected an error for a pair with no '='")
	}
	if verr.Code != "vault.data.invalid" {
		t.Errorf("code = %q", verr.Code)
	}
}

// An entry with no "=" is most often a value given without its key, which is
// the secret: the refusal says where it is and never repeats it, since the
// message goes back to the caller and into the agent ledger, which holds the
// input masked.
func TestDataFieldsNeverRepeatAnEntryTheyRefuse(t *testing.T) {
	for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceTUI, plugin.SurfaceMCP} {
		_, verr := dataFields(sf, []string{"user=admin", "hunter2"})
		if verr == nil || verr.Code != "vault.data.invalid" {
			t.Fatalf("%s: %v, want vault.data.invalid", sf, verr)
		}
		if !strings.Contains(verr.Message, "entry 2 of "+sf.InputName("data")+" is not key=value") {
			t.Errorf("%s: %q does not say which entry it is", sf, verr.Message)
		}
		if strings.Contains(verr.Message, "hunter2") || strings.Contains(verr.Hint, "hunter2") {
			t.Errorf("%s: %q / %q repeat the entry it refused", sf, verr.Message, verr.Hint)
		}
	}
}

// A value containing '=' itself (a base64 blob, most commonly) must keep
// everything after the first '=' — strings.Cut splits on the first
// occurrence, not something re-derived here, but the case is worth pinning
// since a naive strings.Split(pair, "=") would silently drop the rest.
func TestDataFieldsKeepsEqualsSignsInsideTheValue(t *testing.T) {
	data, verr := dataFields(plugin.SurfaceCLI, []string{"cert=MIIB==Q=="})
	if verr != nil {
		t.Fatal(verr)
	}
	if data["cert"] != "MIIB==Q==" {
		t.Errorf("cert = %q", data["cert"])
	}
}

func TestCellRendersEveryJSONShapeVaultActuallyReturns(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{"nil", nil, ""},
		{"string", "root", "root"},
		{"bool", true, "true"},
		{"number", float64(300), "300"},
		{"string slice", []interface{}{"default", "admin"}, "default, admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cell(tc.in); got != tc.want {
				t.Errorf("cell(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Every capability that reaches Vault reaches off the box, so cap must have
// forced NoPreview on all of them — the property plugins/pg's own
// overview_test.go pins the same way, since it is what keeps the automatic
// dashboard from deciding, on its own, that a live deployment is worth
// polling.
func TestEveryCapabilityIsNoPreview(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if !c.NoPreview {
			t.Errorf("%s: NoPreview = false, want true — every capability here reaches off the box", c.ID)
		}
	}
}

// The capabilities called out explicitly as "Both Write+NeedsGrant"
// (kv.get's reveal, kv.put's overwrite, transit.decrypt's reveal, and the
// two wrap capabilities) must actually declare it — a design note is not an
// enforcement mechanism, the struct field is.
func TestWriteAndDestructiveCapabilitiesNeedAGrant(t *testing.T) {
	want := map[string]bool{
		"vault.kv.get":      true,
		"vault.kv.set":      true,
		"vault.kv.history":  false, // the shape of the chain, never a link of it
		"vault.kv.delete":   true,
		"vault.kv.undelete": true,
		// Refuses SurfaceMCP like snapshot and restore: nothing undoes it, so
		// no grant can name what it costs, and the unset NeedsGrant follows.
		"vault.kv.destroy":      false,
		"vault.transit.encrypt": false, // uses the caller's own plaintext, nothing revealed
		"vault.transit.decrypt": true,
		"vault.wrap.set":        true,
		"vault.wrap.get":        true,
		"vault.seal.status":     false,
		"vault.kv.list":         false,
		"vault.kv.tree":         false, // the same names kv.list gives, in one call instead of many
		"vault.token.status":    false,
		"vault.lease.show":      false,
		"vault.policy.list":     false,
		"vault.policy.get":      false,
		"vault.overview":        false,
		// Refuses SurfaceMCP outright rather than taking a grant: a snapshot of
		// the whole Vault has no blast radius a grant could name. NeedsGrant stays
		// unset for keys.backup's reason — a grant that can never be exercised is
		// an entry in `grant list` that means nothing.
		"vault.snapshot": false,
		// Its mirror: everything arriving instead of everything leaving, and
		// the same reasoning for the unset NeedsGrant.
		"vault.restore": false,
	}
	seen := map[string]bool{}
	for _, c := range Plugin().Capabilities {
		seen[c.ID] = true
		wantGrant, ok := want[c.ID]
		if !ok {
			t.Errorf("%s: not accounted for in this test's table", c.ID)
			continue
		}
		if c.NeedsGrant != wantGrant {
			t.Errorf("%s: NeedsGrant = %v, want %v", c.ID, c.NeedsGrant, wantGrant)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("%s: declared in this test's table but not in Plugin()", id)
		}
	}
}
