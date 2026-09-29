package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	stdnet "net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// connFields are the inputs every capability here shares.
//
// endpoint/region/tls default to a local MinIO's own defaults (127.0.0.1:9000,
// plain HTTP) rather than a real AWS endpoint, the same reason plugins/vault
// defaults address to a local `vault server -dev` — zero config should reach
// something a person can actually stand up and try this against, not a SaaS
// endpoint that needs an account first. Real S3 (or R2, or Ceph) is an
// operator's own config: endpoint: s3.amazonaws.com, tls: true.
//
// access-key mirrors pg's `user`: identifying, not secret, Config-settable.
// secret-key mirrors pg's `password` and vault's `token`: Secret, Local, and
// EnvFallback, so an MCP server resolves it from RTA_S3_SECRET_KEY rather
// than an agent ever supplying or inventing one.
// Every field here is Local, and that is the security property rather than a
// detail — the same reasoning plugins/pg's own connFields documents at
// length. Together they name which endpoint this call
// reaches and as whom, and an MCP caller may not choose that: an agent that
// could set `endpoint` could point rta at a bucket it controls and have the
// host supply $RTA_S3_SECRET_KEY beside it. They remain Config-backed and
// remain ordinary flags for a person at a terminal.
func connFields() []plugin.Field {
	return []plugin.Field{
		// One input holding host[:port], which is the address role. This is
		// the shape that made a two-role Host/Port scheme unworkable and the
		// reason the roles are what they are.
		{Name: "endpoint", Type: plugin.String, Default: "127.0.0.1:9000", Config: "endpoint",
			Local: true, Endpoint: plugin.EndpointAddress, Help: "S3-compatible endpoint, host[:port]"},
		{Name: "region", Type: plugin.String, Default: "us-east-1", Config: "region",
			Local: true, Help: "bucket region"},
		// Local for the downgrade reason rather than the redirect one: an
		// agent that could set this could ask for plaintext against an
		// endpoint the operator configured as HTTPS.
		{Name: "tls", Type: plugin.Bool, Default: false, Config: "tls",
			Local: true, Endpoint: plugin.EndpointTLS,
			Help: "use HTTPS (a local MinIO ordinarily does not)"},
		{Name: "access-key", Type: plugin.String, Config: "access-key",
			Local: true, Help: "access key ID"},
		{Name: "secret-key", Type: plugin.Secret, Local: true, EnvFallback: true,
			Help: "secret access key"},
		// Local for the same reason plugins/etcd's own ca-file is: it is read
		// off this machine's disk, not held as a value rta's own store could
		// manage. Not a plugin.Secret, and deliberately: a CA certificate is
		// the public half of a key pair — it is what a CA hands out for
		// wide distribution so anyone can verify what it signed, the same
		// reason a browser or an OS trust store ships thousands of them in
		// the clear. Nothing about it needs secrecy; ca-file only needs Local
		// for the same file-read-primitive reason address does, not because
		// its contents are sensitive.
		{Name: "ca-file", Type: plugin.String, Default: "", Config: "ca-file",
			Local: true, Help: "PEM bundle to verify the server against, beyond the host's own trust store"},
		// The name the certificate is checked for when it is not the host
		// dialled — above all through a kube: or ssh: forward, whose end is
		// 127.0.0.1 whatever the server is called, and which a service's
		// certificate names only by luck. Checked as strictly as the host would
		// have been: it moves the check, never loosens it. Local for the reason
		// tls is: what a certificate has to prove is the operator's to say.
		{Name: "tls-server-name", Type: plugin.String, Default: "", Config: "tls-server-name",
			Local: true, Help: "name to check the server's certificate for, in place of the endpoint's host"},
	}
}

// connect opens a client. minio-go's New never itself dials the network — it
// only parses the endpoint and builds the signer — so a bad endpoint or a
// dead server surfaces on the first real call, classified the same way any
// other request failure is.
func connect(req plugin.Request) (*minio.Client, *view.Error) {
	endpoint := req.String("endpoint")
	access := req.String("access-key")
	secret := req.String("secret-key")
	opts := &minio.Options{
		Creds: credentials.NewStaticV4(access, secret, ""),
		// ca-file only means anything over TLS, so setting it turns TLS on
		// the same way etcd's own ca-file does — the alternative is a value
		// that silently does nothing until --tls is also typed. tls-server-name,
		// for the same reason: through a forward the host turns tls off, and a
		// name given in the profile beside it was a plain-HTTP call to a TLS port.
		Secure: req.Bool("tls") || req.String("ca-file") != "" || serverName(req) != "",
		Region: req.String("region"),
	}
	// The path with a leading ~ resolved, as every other path a plugin reads
	// is. Opened as typed, ~/ca.pem was a path under a directory named ~, and
	// a CA sitting in the operator's home was answered as no such file.
	if ca := plugin.ExpandHome(req.String("ca-file")); ca != "" || serverName(req) != "" {
		transport, verr := tlsTransport(req.Surface(), ca, serverName(req))
		if verr != nil {
			return nil, verr
		}
		opts.Transport = transport
	}
	client, err := minio.New(endpoint, opts)
	if err != nil {
		return nil, view.Errorf("s3.conn.invalid", "%s: %v", endpoint, err).
			WithHint("endpoint is host[:port] with no scheme — set " + req.Surface().SettingName("tls") + " separately")
	}
	return client, nil
}

// tlsTransport is minio-go's own default HTTPS transport (proxying, idle
// connection pooling, timeouts — the same one Secure:true would have built
// anyway) with its trust replaced by ca-file's bundle rather than the host's
// system trust store, the same full-replacement etcd's and vault's own
// ca-file already give: an operator naming a private CA means exactly that
// CA, not that CA in addition to the public web PKI. And with name, when
// tls-server-name gives one, as what the certificate is checked for and the
// SNI sent, in place of the endpoint's host.
func tlsTransport(sf plugin.Surface, ca, name string) (*http.Transport, *view.Error) {
	transport, err := minio.DefaultTransport(true)
	if err != nil {
		return nil, view.Errorf("s3.tls.transport", "%v", err)
	}
	transport.TLSClientConfig.ServerName = name
	if ca == "" {
		return transport, nil
	}
	pem, err := os.ReadFile(ca)
	if err != nil {
		return nil, view.Errorf("s3.tls.ca.unreadable", "%v", err).
			WithHint(sf.SettingName("ca-file") + " is a path on this machine, read by rta rather than by the server")
	}
	pool := x509.NewCertPool()
	// What the file has to hold, rather than a guess at what it held instead.
	// The hint once said "not the server's own certificate", and a local
	// MinIO's self-signed public.crt is exactly what belongs here — the
	// untrusted-certificate hint in classify sends the reader to put it here —
	// while one in PEM never reaches this line at all: only a file with no PEM
	// certificate in it does, a private key or a DER-encoded certificate.
	if !pool.AppendCertsFromPEM(pem) {
		return nil, view.Errorf("s3.tls.ca.invalid", "%s holds no PEM certificate", ca).
			WithHint(sf.SettingName("ca-file") + " wants a PEM certificate — the CA's, or a self-signed " +
				"server's own such as a local MinIO's public.crt — and a private key or a DER-encoded " +
				"certificate is not one")
	}
	transport.TLSClientConfig.RootCAs = pool
	return transport, nil
}

// serverName is the name the certificate is checked for in place of the
// endpoint's host, or "" for the host.
func serverName(req plugin.Request) string { return strings.TrimSpace(req.String("tls-server-name")) }

// classify turns a client error into something an operator can act on.
//
// minio-go returns its own errors as a value type, minio.ErrorResponse (not
// a pointer — confirmed against the vendored source's own ToErrorResponse,
// which type-switches on the bare value rather than a pointer). errors.As
// still works against a value type target: it compares the target's type,
// not its kind, against each error in the chain. Anything that never got a
// response at all — the connection itself failing — comes back as whatever
// the standard http.Client produced, the same net.OpError/net.DNSError/
// *url.Error family plugins/pg and plugins/vault already classify.
func classify(err error, req plugin.Request) *view.Error {
	where, sf := req.String("endpoint"), req.Surface()

	// Checked ahead of the network-error family below because a context
	// error can arrive bare — the listing iterator hands back ctx.Err()
	// directly, with no transport in between to wrap it in a *url.Error —
	// and errors.Is still finds it wrapped, where a caller's own cancel
	// reaches classify through a *url.Error that has already unwound the
	// in-flight request.
	if errors.Is(err, context.Canceled) {
		return view.Errorf("s3.cancelled", "the call to %s was abandoned before it answered", where).
			WithHint("whoever made this call stopped waiting for it — nothing here to fix")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return view.Errorf("s3.conn.timeout", "%s did not answer in time", where).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}

	var errResp minio.ErrorResponse
	if errors.As(err, &errResp) {
		switch errResp.Code {
		case minio.NoSuchBucket:
			return view.Errorf("s3.bucket.notfound", "%s has no bucket %q", where, errResp.BucketName).
				WithHint(sf.CapabilityName("s3.bucket.list") + " shows what is there")
		case minio.NoSuchKey:
			return view.Errorf("s3.object.notfound", "no object %q in %q", errResp.Key, errResp.BucketName).
				WithHint("`" + sf.Call("s3.object.list", plugin.Arg{Name: "bucket", Value: errResp.BucketName}) +
					"` shows what is there")
		case minio.NoSuchBucketPolicy:
			return view.Errorf("s3.policy.notfound", "%q has no bucket policy set", errResp.BucketName).
				WithHint("an absent policy is not the same as a deny-all one — access still follows IAM/bucket ACLs")
		case minio.AccessDenied:
			return view.Errorf("s3.denied", "%s refused: %s", where, errResp.Message).
				WithHint("the credentials are valid but not authorized for this — check the bucket policy or IAM")
		case minio.InvalidAccessKeyID, minio.SignatureDoesNotMatch:
			return view.Errorf("s3.auth.failed", "%s rejected the credentials", where).
				WithHint("set $" + plugin.LocalEnvVar("s3.overview", "secret-key") + ", or check " + sf.SettingName("access-key"))
		case minio.BucketAlreadyExists, minio.BucketAlreadyOwnedByYou:
			return view.Errorf("s3.bucket.exists", "%q already exists", errResp.BucketName).
				WithHint(sf.CapabilityName("s3.bucket.list") + " shows who owns what this plugin can see")
		}
		return view.Errorf("s3.request.failed", "%s: %s", errResp.Code, errResp.Message).
			WithHint(sf.SettingsHint("s3.overview"))
	}

	// The name first: a dial that could not resolve its host fails with a
	// *net.OpError wrapping the *net.DNSError, and read the other way round
	// every name nothing resolves was reported as a port nothing listens on.
	var dnsErr *stdnet.DNSError
	if errors.As(err, &dnsErr) {
		return view.Errorf("s3.host.unknown", "no address for %q", hostOnly(where)).
			WithHint(sf.DNSHint(hostOnly(where)))
	}
	// Short of the host, before the port: a dial that found no way there
	// reached nothing that could refuse it, and read as refused, an endpoint
	// behind a VPN that is down, or at an address of another network's, was
	// "nothing is listening" about a port no packet reached.
	//
	// Each read by the operating system's own error (plugin.DialUnroutable,
	// plugin.DialRefused), never by the *net.OpError around it, which every
	// failed dial is: read that way, a dial that was reset was "nothing is
	// listening" too, and one that timed out never reached the timeout below.
	if plugin.DialUnroutable(err) {
		reason := err
		var netErr *stdnet.OpError
		if errors.As(err, &netErr) {
			reason = netErr.Err
		}
		return view.Errorf("s3.conn.unreachable", "%s cannot be reached from this machine: %v", where, reason).
			WithHint("no route leads there from here — a VPN or tunnel the server sits behind that is " +
				"down looks exactly like this, and so does " + sf.SettingName("endpoint") + " naming " +
				"an address on a network this machine is not on")
	}
	if plugin.DialRefused(err) {
		return view.Errorf("s3.conn.refused", "nothing is listening on %s", where).
			WithHint("is the server up, and is " + sf.SettingName("endpoint") + " right?")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return view.Errorf("s3.conn.timeout", "%s did not answer in time", where).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	// The CA, and never TLS off. A server that got as far as presenting a
	// certificate speaks only TLS on that port — a MinIO given a certs
	// directory serves HTTPS alone — so turning tls off reaches nothing, and
	// with ca-file set it does not even turn TLS off, since ca-file alone
	// turns it on. The hint once offered it anyway, as the quick way round.
	//
	// Asked of plugin.CertUntrusted rather than of the type Go's verifier
	// alone gives: with no ca-file, macOS answers a private CA's chain
	// untyped, and it was "could not reach". And only for a verdict that
	// means an issuer nothing here vouches for — a revoked certificate is
	// answered untyped too, and the CA file is no cure for it but a way
	// around the check that caught it.
	if plugin.CertUntrusted(err) {
		return view.Errorf("s3.tls.untrusted", "%s presented a certificate nothing here trusts", where).
			WithHint(sf.CAHint("ca-file") + "; for a local MinIO that is its public.crt, and " +
				"turning TLS off is no way round it, as the server refuses plain HTTP")
	}
	// Every other verdict is its own reason, quoted in the verifier's words —
	// the system's, for one macOS gives untyped — and never "could not reach":
	// the server was reached, and answered with a certificate.
	//
	// A certificate that is not for the end of a forward the host opened is
	// no fault of the server's, and not one the endpoint can fix: through a
	// forward the host fills the endpoint with 127.0.0.1 and a port of its
	// own, which a service's certificate names only by luck. So the refusal
	// names the forward and the name the certificate is for, and sends the
	// reader to tls-server-name, which checks that name in 127.0.0.1's place
	// — never to anything that checks less. Only when tls-server-name is not
	// set: a name given and not matched is the certificate's to explain.
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) && req.Tunnel() != plugin.TunnelNone && serverName(req) == "" {
		return forwardName(req, hostErr)
	}
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		checked := "the host in " + sf.SettingName("endpoint")
		if serverName(req) != "" {
			checked = "the name in " + sf.SettingName("tls-server-name")
		}
		return view.Errorf("s3.tls.rejected", "%s presented a certificate that does not verify: %v", where, verifyErr.Err).
			WithHint("a certificate is checked for " + checked +
				", its dates and the use it was issued for, as well as for who issued it")
	}
	return view.Errorf("s3.conn.failed", "could not reach %s: %v", where, err).
		WithHint(sf.SettingsHint("s3.overview"))
}

// forwardName is the refusal for a certificate checked for the end of a
// forward the host opened — 127.0.0.1 — and not for the name the server
// answers as, which the certificate names instead.
func forwardName(req plugin.Request, hostErr x509.HostnameError) *view.Error {
	return view.Errorf("s3.tls.forward", "the certificate behind profile %s's %s: forward is for %s, not for %s, "+
		"where the forward ends", req.Profile(), req.Tunnel(), certNames(hostErr.Certificate), hostErr.Host).
		WithHint("a forward always ends at 127.0.0.1, so the certificate is checked for the name the server " +
			"answers as instead: " + req.Surface().SettingName("tls-server-name") + ", which the profile can " +
			"hold beside its forward, names it — one the certificate is for — and it is checked as strictly " +
			"as the host it replaces")
}

// certNames lists the names a certificate is for, the ones a check reads:
// its DNS names and its IP addresses, never the subject's common name, which
// Go's verifier ignores.
func certNames(cert *x509.Certificate) string {
	if cert == nil {
		return "another name"
	}
	names := slices.Clone(cert.DNSNames)
	for _, ip := range cert.IPAddresses {
		names = append(names, ip.String())
	}
	switch {
	case len(names) == 0:
		return "no name a check reads"
	case len(names) > 3:
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" and %d more", len(names)-3)
	}
	return strings.Join(names, ", ")
}

// ctxErr is what a ListObjectsIter walk needs checked once it stops,
// whatever stopped it. A context already done when the walk starts makes
// minio-go's own iterator return having yielded nothing at all — no error
// value, no round trip, the same shape as a bucket with nothing in it — so
// a caller that only reacts to what came out of the loop reads a cancelled
// call as an empty listing and answers as if the walk had actually run.
// Checked after the loop rather than raced against inside it: that is the
// one place a normal finish, a break at some caller-side bound, and this
// silent stop all rejoin.
func ctxErr(ctx context.Context, req plugin.Request) *view.Error {
	if err := ctx.Err(); err != nil {
		return classify(err, req)
	}
	return nil
}

// rmCall is the removal that clears the destination a copy or a move found
// taken, spelled for the surface that will make it. The bucket is Local on
// s3.object.rm, so an agent's call carries the key alone and runs against
// the bucket the operator configured — the only one an agent's copy can have
// written to, since the destination bucket is Local too and defaults to the
// source's.
func rmCall(sf plugin.Surface, bucket, key string) string {
	args := []plugin.Arg{{Name: "key", Value: key, Positional: true}}
	if sf != plugin.SurfaceMCP {
		args = append(args, plugin.Arg{Name: "bucket", Value: bucket})
	}
	return sf.Call("s3.object.rm", args...)
}

// outHint says how to have an object too large to print written to a file
// instead. out is Local — a person's input, since a grant authorizes revealing
// the content and not choosing where on this machine it lands — so over MCP
// the file is the operator's to ask for, in the one phrase that hands an
// agent a command line.
func outHint(req plugin.Request, bucket, key string) string {
	sf := req.Surface()
	if sf == plugin.SurfaceMCP {
		return "a file is the operator's to write — " + plugin.AskOperator(strings.TrimPrefix(
			plugin.SurfaceCLI.Call("s3.object.get",
				plugin.Arg{Name: "key", Value: key, Positional: true},
				plugin.Arg{Name: "bucket", Value: bucket},
				plugin.Arg{Name: "out", Value: "<file>"}), "rta "))
	}
	return "use " + sf.InputName("out") + " to write it to a file instead of printing it"
}

func hostOnly(endpoint string) string {
	host, _, err := stdnet.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}
	return host
}

// withClient is the shape every capability here has: connect, or return the
// classified error; run. ctx is the call's own — the host cancels it when
// the caller stops waiting, which is what makes a cancelled call stop
// before it reaches the endpoint instead of running to completion for
// nobody.
func withClient(ctx context.Context, req plugin.Request, fn func(context.Context, *minio.Client) (view.View, error)) (view.View, error) {
	client, verr := connect(req)
	if verr != nil {
		return nil, verr
	}
	return fn(ctx, client)
}

// cap builds a capability with the shared connection inputs appended, so no
// declaration here can forget one and no two can disagree about a default —
// the same helper plugins/pg and plugins/vault document at length. Every
// capability here reaches off the box for the same reason theirs do, so
// every one is NoPreview for the same reason: the automatic dashboard runs
// Read capabilities unasked, and a live bucket is not something this plugin
// gets to decide, on its own, is fine to poll every few seconds. An operator
// who has looked at their own deployment and decided otherwise still can —
// dashboard.tiles accepts any capability regardless of NoPreview, because
// naming one in a config file is the asking.
func cap(c plugin.Capability, own ...plugin.Field) plugin.Capability {
	c.Inputs = append(own, connFields()...)
	c.NoPreview = true
	return c
}
