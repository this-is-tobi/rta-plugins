package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/sdk/wire"
)

// sdktest is the definition of "a correct plugin" — no exemption for s3.
//
// It used to be called with no inputs, and every mutating capability here
// names a required bucket and key, so the suite drove *nothing*: twelve
// capabilities, zero runs, green. Behind that, s3.object.get truncated the
// operator's --out file under --dry-run, s3.object.set really uploaded, and
// s3.object.presign really minted a working bearer URL. The suite could see
// all three the moment it was given something to drive them with.
func TestConformance(t *testing.T) {
	sdktest.Check(t, Plugin(), sdktest.WithInputs(conformanceInputs), sdktest.WithSource("."))
}

// conformanceInputs points every mutating capability at a bucket and key that
// do not exist, on an endpoint nothing is listening on.
//
// The dead endpoint is the load-bearing half, copied from the built-ins' own
// fixture: a dry run that stops being dry fails here as a refused connection
// to 127.0.0.1:1 rather than as a request against somebody's real storage.
// Paths land inside dir, which is the directory the dry-run rule watches, so
// a write that should not have happened is a test failure rather than a file
// in a temp directory nobody looks at.
func conformanceInputs(dir string) map[string]map[string]any {
	conn := func(m map[string]any) map[string]any {
		m["endpoint"] = "127.0.0.1:1"
		m["access-key"], m["secret-key"] = "conformance", "conformance"
		return m
	}
	// The upload's source is inside dir, beside everything else. The suite
	// snapshots dir after this function has run, before each dry run, so a
	// fixture written here is part of what a dry run is compared against
	// rather than a stray write — and one that touched it is caught.
	upload := filepath.Join(dir, "upload")
	_ = os.Mkdir(upload, 0o700)
	_ = os.WriteFile(filepath.Join(upload, "a.txt"), []byte("x"), 0o600)
	return map[string]map[string]any{
		"s3.object.get":  conn(map[string]any{"bucket": "conformance", "key": "some/key", "out": filepath.Join(dir, "got.bin")}),
		"s3.object.set":  conn(map[string]any{"bucket": "conformance", "key": "some/key", "value": "x"}),
		"s3.object.copy": conn(map[string]any{"bucket": "conformance", "key": "some/key", "dest-key": "some/copy"}),
		"s3.object.rename": conn(map[string]any{"bucket": "conformance", "key": "some/key",
			"dest-key": "some/renamed"}),
		"s3.object.rm":      conn(map[string]any{"bucket": "conformance", "key": "some/key"}),
		"s3.object.presign": conn(map[string]any{"bucket": "conformance", "key": "some/key"}),
		"s3.bucket.download": conn(map[string]any{"bucket": "conformance",
			"out": filepath.Join(dir, "bucket-copy")}),
		"s3.bucket.upload": conn(map[string]any{"bucket": "conformance", "dir": upload}),
	}
}

// req builds a resolved request the way the host would, against the named
// capability's own declared inputs (connFields included, via cap) — so
// these test the values a handler actually sees, matching plugins/vault's
// own req helper.
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

// A name DNS cannot resolve is that, and not a port nothing listens on. The
// HTTP client wraps a failed lookup in a *net.OpError inside a *url.Error,
// and the refused-dial check, read first, answered "nothing is listening on
// nonexistent.invalid:9000" and asked whether the server was up, when the
// name was the problem.
func TestANameDNSCannotResolveIsNotReadAsNothingListening(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "http://s3.internal:9000/",
		Err: &net.OpError{Op: "dial", Net: "tcp",
			Err: &net.DNSError{Err: "no such host", Name: "s3.internal", IsNotFound: true}}}
	verr := classify(err, req(t, "s3.overview", map[string]any{"endpoint": "s3.internal:9000"}))
	if verr.Code != "s3.host.unknown" {
		t.Errorf("code = %s, want s3.host.unknown: %s", verr.Code, verr.Message)
	}
}

// An object that is not there points at the listing of its bucket as a call
// the CLI takes: s3.object.list reads the bucket from --bucket and takes no
// argument by place, and the hint once gave it one — `rta s3 object list
// shop` — which the CLI refuses as an unexpected argument.
func TestAMissingObjectPointsAtAListingTheCLITakes(t *testing.T) {
	verr := classify(minio.ErrorResponse{Code: minio.NoSuchKey, BucketName: "shop", Key: "k"},
		req(t, "s3.object.get", map[string]any{"key": "k"}))
	if want := "`rta s3 object list --bucket shop --endpoint 127.0.0.1:9000` shows what is there"; !strings.Contains(verr.Hint, want) {
		t.Errorf("hint = %q, want %q in it", verr.Hint, want)
	}
}

// A server's answer for a missing object does not always name it: real S3
// puts the key in the XML body and not the bucket, and MinIO neither. The
// refusal read `no object "" in ""` and its hint listed bucket "", so it
// names what the call asked for where the answer is silent.
func TestAMissingObjectIsNamedWhenTheServerDoesNotNameIt(t *testing.T) {
	r := req(t, "s3.object.get", map[string]any{"bucket": "shop", "key": "invoices/1.pdf"})
	verr := classify(minio.ErrorResponse{Code: minio.NoSuchKey}, r)
	if want := `no object "invoices/1.pdf" in "shop"`; verr.Message != want {
		t.Errorf("message = %q, want %q", verr.Message, want)
	}
	if want := "--bucket shop"; !strings.Contains(verr.Hint, want) {
		t.Errorf("hint = %q, want the listing of bucket shop (%q)", verr.Hint, want)
	}
	verr = classify(minio.ErrorResponse{Code: minio.NoSuchBucket}, r)
	if !strings.Contains(verr.Message, `"shop"`) {
		t.Errorf("message = %q, want the bucket the call named", verr.Message)
	}
	verr = classify(minio.ErrorResponse{Code: minio.NoSuchKey, BucketName: "other", Key: "k"}, r)
	if want := `no object "k" in "other"`; verr.Message != want {
		t.Errorf("message = %q, want the server's own names to win: %q", verr.Message, want)
	}
}

// Every classified failure has to say what to do next — the same bar
// plugins/pg and plugins/vault's own classify hold themselves to, against
// S3's error shapes.
func TestEveryClassifiedFailureNamesTheNextStep(t *testing.T) {
	r := req(t, "s3.overview", map[string]any{"endpoint": "s3.internal:9000"})
	dial := func(errno syscall.Errno) error {
		return &url.Error{Op: "Get", URL: "http://x",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}}
	}
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"no such bucket", minio.ErrorResponse{Code: minio.NoSuchBucket, BucketName: "b"}, "s3.bucket.notfound"},
		{"no such key", minio.ErrorResponse{Code: minio.NoSuchKey, BucketName: "b", Key: "k"}, "s3.object.notfound"},
		{"no such policy", minio.ErrorResponse{Code: minio.NoSuchBucketPolicy, BucketName: "b"}, "s3.policy.notfound"},
		{"denied", minio.ErrorResponse{Code: minio.AccessDenied, Message: "nope"}, "s3.denied"},
		{"bad key id", minio.ErrorResponse{Code: minio.InvalidAccessKeyID}, "s3.auth.failed"},
		{"bad signature", minio.ErrorResponse{Code: minio.SignatureDoesNotMatch}, "s3.auth.failed"},
		{"bucket exists", minio.ErrorResponse{Code: minio.BucketAlreadyExists, BucketName: "b"}, "s3.bucket.exists"},
		{"other s3 error", minio.ErrorResponse{Code: "SomeOtherCode", Message: "m"}, "s3.request.failed"},
		{"refused", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, "s3.conn.refused"},
		// A dial is read by the operating system's error it carries, never by
		// the *net.OpError every failed dial is: a reset refused nothing, and
		// a host no route reaches was sent to check a port.
		{"refused by errno", dial(syscall.ECONNREFUSED), "s3.conn.refused"},
		{"no route", dial(syscall.EHOSTUNREACH), "s3.conn.unreachable"},
		{"no network", dial(syscall.ENETUNREACH), "s3.conn.unreachable"},
		{"reset", &url.Error{Op: "Get", URL: "http://x", Err: &net.OpError{Op: "read", Net: "tcp",
			Err: os.NewSyscallError("read", syscall.ECONNRESET)}}, "s3.conn.failed"},
		{"a dial that timed out", &url.Error{Op: "Get", URL: "http://x",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}}}, "s3.conn.timeout"},
		{"unknown host", &net.DNSError{Err: "no such host", Name: "s3.internal"}, "s3.host.unknown"},
		{"timed out", &url.Error{Op: "Get", URL: "http://x", Err: timeoutError{}}, "s3.conn.timeout"},
		// The listing iterator hands a bare context error back, unwrapped by
		// any transport, so both have to be recognised on their own.
		{"deadline passed", context.DeadlineExceeded, "s3.conn.timeout"},
		{"caller went away", context.Canceled, "s3.cancelled"},
		{"anything else", errors.New("something unexpected"), "s3.conn.failed"},
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

// macOS verifies against the system's trust store itself when no ca-file is
// set and reports a chain it cannot anchor as a bare error: that is still a
// certificate nothing here trusts, and the answer is the CA. Every other
// verdict it gives untyped is its own reason and is quoted in its words: a
// revoked certificate answered with the CA file to name was answered with the
// way around the revocation check. Elsewhere an untyped verdict is never a
// question of trust, and none is a server that could not be reached.
func TestACertificateThatFailsVerificationIsNamedForWhy(t *testing.T) {
	r := req(t, "s3.overview", map[string]any{"endpoint": "s3.internal:9000"})
	open, closing := string(rune(0x201c)), string(rune(0x201d))
	onMac := "s3.tls.rejected"
	if runtime.GOOS == "darwin" {
		onMac = "s3.tls.untrusted"
	}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"Go's own verifier", x509.UnknownAuthorityError{}, "s3.tls.untrusted"},
		{"the system's verifier, a chain it cannot anchor",
			errors.New("x509: " + open + "minio" + closing + " certificate is not trusted"), onMac},
		{"the system's verifier, a revoked certificate",
			errors.New("x509: " + open + "minio" + closing + " certificate is revoked"), "s3.tls.rejected"},
		{"a name it is not for", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "s3.internal"},
			"s3.tls.rejected"},
	} {
		err := &url.Error{Op: "Get", URL: "https://s3.internal:9000/",
			Err: &tls.CertificateVerificationError{Err: tc.err}}
		got := classify(err, r)
		if got.Code != tc.want {
			t.Errorf("%s: classified %s, want %s", tc.name, got.Code, tc.want)
		}
		if got.Code == "s3.tls.rejected" && !strings.Contains(got.Message, tc.err.Error()) {
			t.Errorf("%s: %q does not quote the verdict", tc.name, got.Message)
		}
	}
}

// A certificate's names are the server's to choose, and a verdict quotes
// them: one valid for "connection refused" or "no route to host" alone, or
// a revoked one the system names so, is still a certificate that does not
// verify — never a port nothing listens on or a host no route reaches, as
// the dial's words, read first, would have had it.
func TestACertificatesOwnNamesAreNeverReadAsTheDialsFailure(t *testing.T) {
	r := req(t, "s3.overview", map[string]any{"endpoint": "minio.internal:9000"})
	open, closing := string(rune(0x201c)), string(rune(0x201d))
	for _, verdict := range []error{
		x509.HostnameError{Certificate: &x509.Certificate{DNSNames: []string{syscall.ECONNREFUSED.Error()}},
			Host: "minio.internal"},
		x509.HostnameError{Certificate: &x509.Certificate{DNSNames: []string{syscall.EHOSTUNREACH.Error()}},
			Host: "minio.internal"},
		errors.New("x509: " + open + syscall.ECONNREFUSED.Error() + closing + " certificate is revoked"),
	} {
		err := &url.Error{Op: "Get", URL: "https://minio.internal:9000/",
			Err: &tls.CertificateVerificationError{Err: verdict}}
		if got := classify(err, r); got.Code != "s3.tls.rejected" {
			t.Errorf("%v: classified %s %q, want s3.tls.rejected", verdict, got.Code, got.Message)
		}
	}
}

// timeoutError satisfies net.Error's Timeout() so *url.Error.Timeout()
// (which asks its wrapped error) reports true, the same shape a real
// deadline exceeded error from the underlying transport has.
type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// Every capability that reaches an S3 endpoint reaches off the box, so cap
// must have forced NoPreview on all of them — the same property
// plugins/pg's and plugins/vault's own conformance tests pin, since it is
// what keeps the automatic dashboard from deciding, on its own, that a live
// endpoint is worth polling.
func TestEveryCapabilityIsNoPreview(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if !c.NoPreview {
			t.Errorf("%s: NoPreview = false, want true — every capability here reaches off the box", c.ID)
		}
	}
}

// The capabilities designed as revealing content, granting
// access or overwriting/destroying it must actually declare NeedsGrant — a
// design note is not an enforcement mechanism, the struct field is.
func TestWriteAndDestructiveCapabilitiesNeedAGrant(t *testing.T) {
	want := map[string]bool{
		"s3.overview":    false,
		"s3.bucket.list": false,
		"s3.policy.get":  false,
		"s3.object.list": false,
		// Ungated for the reason a listing is ungated: this discloses exactly
		// what a caller could already have by walking s3.object.list one
		// --prefix at a time. Fewer round trips is not a wider permission —
		// the blast radius of a list of names and sizes is that same list.
		"s3.object.tree":    false,
		"s3.object.show":    false,
		"s3.object.get":     true,
		"s3.object.set":     true,
		"s3.object.copy":    true,
		"s3.object.rename":  true,
		"s3.object.rm":      false, // Destructive already implies a grant
		"s3.object.presign": true,
		// Refuses SurfaceMCP outright rather than taking a grant: a whole bucket
		// has no blast radius a grant could name. NeedsGrant stays unset for
		// keys.backup's reason — a grant that can never be exercised is an entry
		// in `grant list` that means nothing.
		"s3.bucket.download": false,
		// Its mirror: everything arriving instead of everything leaving, and
		// the same reasoning for the unset NeedsGrant. Destructive besides —
		// --overwrite can replace remote objects.
		"s3.bucket.upload": false,
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
		if c.ID == "s3.object.rm" && c.Safety != plugin.Destructive {
			t.Errorf("s3.object.rm: Safety = %s, want Destructive", c.Safety)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("%s: declared in this test's table but not in Plugin()", id)
		}
	}
}

func TestDestinationDefaultsTheBucketToTheSource(t *testing.T) {
	r := req(t, "s3.object.copy", map[string]any{"bucket": "src", "key": "k", "dest-key": "k2"})
	bucket, key := destination(r)
	if bucket != "src" || key != "k2" {
		t.Errorf("destination = (%q, %q), want (src, k2)", bucket, key)
	}
}

func TestDestinationHonorsAnExplicitDestBucket(t *testing.T) {
	r := req(t, "s3.object.copy", map[string]any{"bucket": "src", "key": "k", "dest-bucket": "dst", "dest-key": "k2"})
	bucket, key := destination(r)
	if bucket != "dst" || key != "k2" {
		t.Errorf("destination = (%q, %q), want (dst, k2)", bucket, key)
	}
}

// A copy or a rename names two objects, the one it reads and the one it
// writes, and a grant has to cover both: checked only at its source, a copy
// grant on one key under reports/ wrote that key anywhere in the bucket, and
// beside a read grant on scratch/ read it back from there. There is no host
// in this module to issue a grant against, so this pins what the host judges
// the call by — the declaration as it reads it, after the wire — which is
// where the whole fix lives.
func TestACopyOrARenameIsJudgedAtWhereItLandsToo(t *testing.T) {
	if err := Plugin().ValidateOutOfProcess(); err != nil {
		t.Fatalf("the declaration a host would refuse to load: %v", err)
	}
	host, unknown := wire.PluginFromProto(wire.PluginToProto(Plugin()))
	if len(unknown) > 0 {
		t.Fatalf("parts of the declaration a host cannot read: %v", unknown)
	}
	for _, id := range []string{"s3.object.copy", "s3.object.rename"} {
		i := slices.IndexFunc(host.Capabilities, func(c plugin.Capability) bool { return c.ID == id })
		if i < 0 {
			t.Fatalf("%s: not declared", id)
		}
		c := host.Capabilities[i]
		if c.Scope != "key" || !slices.Equal(c.ScopeAlso, []string{"dest-key"}) {
			t.Errorf("%s: judged by Scope %q and ScopeAlso %v, want key and [dest-key]", id, c.Scope, c.ScopeAlso)
		}
		// The bucket is judged by nothing, so no caller a grant is checked
		// for may choose it: an MCP call lands in the operator's own bucket.
		for _, f := range c.Inputs {
			if (f.Name == "bucket" || f.Name == "dest-bucket") && !f.Local {
				t.Errorf("%s: %s is offered to a remote caller, and no grant judges it", id, f.Name)
			}
		}
	}
}
