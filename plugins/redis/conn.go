package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// connFields are the inputs every capability here shares.
//
// Every one is Local, and that is the security property rather than a detail.
// Together they name which server this call reaches and as whom, and an MCP
// caller may not choose that: caller values resolve above config and above the
// host's own environment, so an agent that could set `address` would point
// rta at a server of its own and have the host supply $RTA_REDIS_PASSWORD
// beside it. Config still fills these and a person at a terminal still passes
// them as ordinary flags.
//
// The three certificate paths are Local for the same reason and one more:
// they are read off this machine's disk. An input naming a file that the host
// then opens is a file-read primitive if a caller can choose the path.
func connFields() []plugin.Field {
	return []plugin.Field{
		{Name: "address", Type: plugin.String, Default: "127.0.0.1:6379", Config: "address",
			Local: true, Endpoint: plugin.EndpointAddress, Help: "redis address, host[:port]"},
		{Name: "tls", Type: plugin.Bool, Default: false, Config: "tls",
			Local: true, Endpoint: plugin.EndpointTLS, Help: "connect over TLS"},
		{Name: "ca-file", Type: plugin.String, Default: "", Config: "ca-file",
			Local: true, Help: "PEM bundle to verify the server against"},
		// The name the certificate is checked for when it is not the host
		// dialled — above all through a kube: or ssh: forward, whose end is
		// 127.0.0.1 whatever the server is called, and which a service's
		// certificate names only by luck. Checked as strictly as the host would
		// have been: it moves the check, never loosens it. Local for the reason
		// tls is: what a certificate has to prove is the operator's to say.
		{Name: "tls-server-name", Type: plugin.String, Default: "", Config: "tls-server-name",
			Local: true, Help: "name to check the server's certificate for, in place of the address's host"},
		{Name: "cert-file", Type: plugin.String, Default: "", Config: "cert-file",
			Local: true, Help: "client certificate, for a server using mTLS"},
		{Name: "key-file", Type: plugin.String, Default: "", Config: "key-file",
			Local: true, Help: "private key for `cert-file`"},
		// Redis 6 ACLs name a user; before that, and on most servers still,
		// AUTH takes a bare password and the user is "default". Empty means
		// the latter, which is why this has no default of its own.
		{Name: "username", Type: plugin.String, Default: "", Config: "username",
			Local: true, Help: "ACL user to authenticate as (Redis 6+); empty for the default user"},
		{Name: "password", Type: plugin.Secret, Local: true, EnvFallback: true,
			Help: "password, or the ACL user's password"},
		// No Max: 16 databases is only the default of the server's
		// `databases` setting, which an operator can raise. A bound here would
		// refuse a caller's --db 20 on a server that has it, and a host that
		// clamps a configured number into range would quietly SELECT 15 — a
		// different database, read as though it were the one configured. The
		// server knows its own count; classify names its refusal.
		{Name: "db", Type: plugin.Int, Default: 0, Config: "db", Min: 0,
			Local: true, Help: "logical database to SELECT"},
	}
}

const (
	dialTimeout = 10 * time.Second
	// ioTimeout bounds every single round trip. Redis answers in microseconds
	// or it is not answering; a call that waits longer than this is waiting
	// on a server that is loading, blocked, or gone.
	ioTimeout = 10 * time.Second
)

// client speaks RESP2 over one connection.
//
// RESP2 rather than RESP3, deliberately: every server since 2.0 speaks it, the
// six reply types below are the whole protocol, and nothing this plugin reads
// needs the typed maps RESP3 adds. HELLO is never sent, so a Redis 5 answers
// as well as a Redis 7.
type client struct {
	conn stdnet.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	addr string
	// reached names the server as its reader reaches it again, for what the
	// server said once connected (Request.Reached): through a forward addr is
	// 127.0.0.1 and a port that closed with the call, which "rejected the
	// credentials" named and the reader could do nothing with. addr stays what
	// was dialled, for the failures of the dial.
	reached string
	// sf is the surface the request came through, so a message about this
	// connection names an input the way its reader gives one.
	sf plugin.Surface
}

func (c *client) Close() { _ = c.conn.Close() }

func connect(ctx context.Context, req plugin.Request) (*client, *view.Error) {
	addr := req.String("address")
	if _, _, err := stdnet.SplitHostPort(addr); err != nil {
		addr = stdnet.JoinHostPort(addr, "6379")
	}
	dialer := stdnet.Dialer{Timeout: dialTimeout}
	var conn stdnet.Conn
	var err error
	// tls-server-name turns TLS on as ca-file does, for the same reason:
	// through a forward the host turns tls off, and a name given in the
	// profile beside it would have been a plaintext call to a TLS port, the
	// name checked by nothing.
	if req.Bool("tls") || req.String("ca-file") != "" || req.String("cert-file") != "" || serverName(req) != "" {
		cfg, verr := tlsConfig(req)
		if verr != nil {
			return nil, verr
		}
		if host, _, splitErr := stdnet.SplitHostPort(addr); splitErr == nil && cfg.ServerName == "" {
			cfg.ServerName = host
		}
		// tls.Dialer and not tls.DialWithDialer, which takes the deadline and
		// drops the context: a caller who stopped waiting left this sitting
		// on a handshake for the whole of dialTimeout with nobody to answer.
		// The plain branch below always honoured ctx; this is the same
		// promise for the TLS one.
		conn, err = (&tls.Dialer{NetDialer: &dialer, Config: cfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, classifyDial(err, addr, req)
	}
	c := &client{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn), addr: addr, reached: req.Reached(addr), sf: req.Surface()}

	if pw := req.String("password"); pw != "" {
		args := []string{"AUTH", pw}
		if user := req.String("username"); user != "" {
			args = []string{"AUTH", user, pw}
		}
		if _, err := c.do(ctx, args...); err != nil {
			c.Close()
			return nil, classifyDial(err, addr, req)
		}
	}
	if db := req.Int("db"); db != 0 {
		if _, err := c.do(ctx, "SELECT", strconv.Itoa(db)); err != nil {
			c.Close()
			return nil, classifyDial(err, addr, req)
		}
	}
	// One PING, so that a server that requires a password nobody supplied is
	// reported as exactly that, here, rather than as a NOAUTH on whichever
	// command a capability happens to send first.
	if _, err := c.do(ctx, "PING"); err != nil {
		c.Close()
		return nil, classifyDial(err, addr, req)
	}
	return c, nil
}

// tlsConfig is the TLS the three certificate paths describe, each with a
// leading ~ resolved as every other path a plugin reads is. Opened as typed,
// ~/ca.pem was a path under a directory named ~, and a CA sitting in the
// operator's home was answered as no such file.
func tlsConfig(req plugin.Request) (*tls.Config, *view.Error) {
	sf := req.Surface()
	// ServerName is what the certificate is checked for, and what is sent as
	// SNI; empty, connect fills it with the host it dials.
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName(req)}
	if ca := plugin.ExpandHome(req.String("ca-file")); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, view.Errorf("redis.tls.ca.unreadable", "%v", err).
				WithHint(sf.SettingName("ca-file") + " is a path on this machine, read by rta rather than by the server")
		}
		pool := x509.NewCertPool()
		// What the file has to hold, rather than a guess at what it held
		// instead. The hint once said "not the client certificate", and a
		// client certificate in PEM never reaches this line: only a file with
		// no PEM certificate in it does, the client's private key among them,
		// or a DER-encoded certificate. And a self-signed server's own
		// certificate is exactly what belongs here, the file the
		// untrusted-certificate hint in classify sends the reader to name.
		if !pool.AppendCertsFromPEM(pem) {
			return nil, view.Errorf("redis.tls.ca.invalid", "%s holds no PEM certificate", ca).
				WithHint(sf.SettingName("ca-file") + " wants a PEM certificate — the CA's, or a self-signed " +
					"server's own — and a private key, which belongs in " + sf.SettingName("key-file") +
					", or a DER-encoded certificate is not one")
		}
		cfg.RootCAs = pool
	}
	cert, key := plugin.ExpandHome(req.String("cert-file")), plugin.ExpandHome(req.String("key-file"))
	switch {
	case cert != "" && key == "":
		return nil, view.Errorf("redis.tls.key.missing", "%s given without %s", sf.SettingName("cert-file"), sf.SettingName("key-file")).
			WithHint("a client certificate is unusable without its private key")
	case key != "" && cert == "":
		return nil, view.Errorf("redis.tls.cert.missing", "%s given without %s", sf.SettingName("key-file"), sf.SettingName("cert-file")).
			WithHint("a private key is unusable without the certificate it belongs to")
	case cert != "":
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, view.Errorf("redis.tls.pair.invalid", "%v", err).
				WithHint("both paths are read on this machine — check they are PEM and belong together")
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}

// reply is one RESP2 value. kind is the type byte the server sent.
type reply struct {
	kind  byte // '+' simple, '-' error, ':' integer, '$' bulk, '*' array
	str   string
	num   int64
	null  bool
	items []reply
}

// serverError is a `-` reply: the server answered, and the answer is no.
type serverError struct{ msg string }

func (e *serverError) Error() string { return e.msg }

// do sends one command and reads its reply. Every command is an array of
// bulk strings on the wire, which is the only request form Redis has needed
// since 1.2 and the one every version accepts.
func (c *client) do(ctx context.Context, args ...string) (reply, error) {
	deadline := time.Now().Add(ioTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return reply{}, err
	}
	fmt.Fprintf(c.w, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(c.w, "$%d\r\n%s\r\n", len(a), a)
	}
	if err := c.w.Flush(); err != nil {
		return reply{}, err
	}
	r, err := c.read()
	if err != nil {
		return reply{}, err
	}
	if r.kind == '-' {
		return reply{}, &serverError{msg: r.str}
	}
	return r, nil
}

func (c *client) read() (reply, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return reply{}, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return reply{}, errors.New("empty reply line")
	}
	kind, rest := line[0], line[1:]
	switch kind {
	case '+', '-':
		return reply{kind: kind, str: rest}, nil
	case ':':
		n, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return reply{}, fmt.Errorf("bad integer reply %q", rest)
		}
		return reply{kind: kind, num: n}, nil
	case '$':
		n, err := strconv.Atoi(rest)
		if err != nil {
			return reply{}, fmt.Errorf("bad bulk length %q", rest)
		}
		if n < 0 {
			return reply{kind: kind, null: true}, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return reply{}, err
		}
		return reply{kind: kind, str: string(buf[:n])}, nil
	case '*':
		n, err := strconv.Atoi(rest)
		if err != nil {
			return reply{}, fmt.Errorf("bad array length %q", rest)
		}
		if n < 0 {
			return reply{kind: kind, null: true}, nil
		}
		items := make([]reply, 0, n)
		for i := 0; i < n; i++ {
			item, err := c.read()
			if err != nil {
				return reply{}, err
			}
			items = append(items, item)
		}
		return reply{kind: kind, items: items}, nil
	default:
		return reply{}, fmt.Errorf("unknown reply type %q", line)
	}
}

// text is the reply as a string, whatever it was: a simple string, a bulk
// string, or an integer spelled out. An array or a null is empty.
func (r reply) text() string {
	switch r.kind {
	case ':':
		return strconv.FormatInt(r.num, 10)
	default:
		return r.str
	}
}

// strings is an array reply's items as text, in order.
func (r reply) strings() []string {
	out := make([]string, 0, len(r.items))
	for _, it := range r.items {
		out = append(out, it.text())
	}
	return out
}

// pairs reads the flat key-value array shape Redis uses for CONFIG GET,
// HGETALL and MEMORY STATS: [k1, v1, k2, v2, ...].
func (r reply) pairs() [][2]string {
	out := make([][2]string, 0, len(r.items)/2)
	for i := 0; i+1 < len(r.items); i += 2 {
		out = append(out, [2]string{r.items[i].text(), r.items[i+1].text()})
	}
	return out
}

// classify turns a connection or server error into something an operator can
// act on. Server errors arrive as a `-` line whose first word is the code;
// the words are the stable part, the sentence after them is not.
//
// addr is where the connection was made, for the failures of the dial; reached
// is the server as its reader reaches it again, for what the server answered
// (Request.Reached), since through a forward addr is the end of one that closed
// with the call.
func classify(err error, addr, reached string, sf plugin.Surface) *view.Error {
	var already *view.Error
	if errors.As(err, &already) {
		return already
	}
	var srv *serverError
	if errors.As(err, &srv) {
		code, _, _ := strings.Cut(srv.msg, " ")
		switch code {
		case "NOAUTH":
			// Where the password comes from rather than a verb telling the
			// reader to pass one: an agent told to pass `password` has no
			// such argument, since the bridge drops a Local input given, and
			// would read this refusal again.
			return view.Errorf("redis.auth.required", "%s requires a password", reached).
				WithHint("the password belongs in $" + plugin.LocalEnvVar("redis.overview", "password") +
					" or " + sf.SettingName("password"))
		case "WRONGPASS":
			return view.Errorf("redis.auth.failed", "%s rejected the credentials", reached).
				WithHint("check the password, and " + sf.SettingName("username") + " if the server uses ACLs")
		case "NOPERM":
			return view.Errorf("redis.denied", "%s: %s", reached, srv.msg).
				WithHint("the ACL user is valid but not allowed this command or key")
		case "LOADING":
			return view.Errorf("redis.loading", "%s is still loading its dataset", reached).
				WithHint("a server restoring a large RDB or AOF answers this until it is done — try again shortly")
		case "MOVED", "ASK":
			return view.Errorf("redis.cluster.redirect", "%s: %s", reached, srv.msg).
				WithHint("this is a cluster and that key lives on another node — " + sf.CapabilityName("redis.cluster") +
					" lists them; point " + sf.SettingName("address") + " at the one named")
		case "ERR":
			if strings.Contains(srv.msg, "unknown command") {
				return view.Errorf("redis.unsupported", "%s: %s", reached, srv.msg).
					WithHint("the server is older than the command, or a proxy in front of it does not pass it through")
			}
			if strings.Contains(srv.msg, "DB index is out of range") {
				return view.Errorf("redis.db.range", "%s has no database with that index", reached).
					WithHint("the server's `databases` setting counts them from 0 (16 unless raised) — pick " + sf.SettingName("db") + " below it")
			}
			if strings.Contains(srv.msg, "AUTH") && strings.Contains(srv.msg, "no password") {
				return view.Errorf("redis.auth.unneeded", "%s has no password set, and one was given", reached).
					WithHint("drop " + sf.SettingName("password") + " (or the environment variable) for this server")
			}
		}
		return view.Errorf("redis.server.error", "%s: %s", reached, srv.msg)
	}

	// A dial that found no way to the host, and one the host refused, by the
	// operating system's own error, as plugin.DialUnroutable and DialRefused
	// read it, and never by the *net.OpError around it, which every failed
	// dial and every broken read is: read that way, a server behind a VPN
	// that was down, and one that reset the connection mid-command, were each
	// "nothing is listening", about a port that may have been fine. The name
	// before either, since a dial that could not resolve its host is a
	// *net.OpError too.
	var netErr *stdnet.OpError
	var dnsErr *stdnet.DNSError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return view.Errorf("redis.timeout", "%s did not answer in time", addr).
			WithHint("a server blocked on a long command, or a firewall that drops rather than refuses, looks exactly like this")
	case errors.As(err, &dnsErr):
		return view.Errorf("redis.host.unknown", "no address for %q", hostOnly(addr)).
			WithHint(sf.DNSHint(hostOnly(addr)))
	case plugin.DialUnroutable(err):
		why := err
		if errors.As(err, &netErr) {
			why = netErr.Err
		}
		return view.Errorf("redis.conn.unreachable", "%s cannot be reached from this machine: %v", addr, why).
			WithHint("no route leads there from here — a VPN or tunnel the server sits behind that is down " +
				"looks exactly like this, and so does " + sf.SettingName("address") + " naming an address on a " +
				"network this machine is not on")
	case plugin.DialRefused(err):
		return view.Errorf("redis.conn.refused", "nothing is listening on %s", addr).
			WithHint("redis listens on 6379 by default; a server bound to localhost only answers from its own host")
	}
	// **Only a certificate plugin.CertUntrusted reads as an unknown issuer's,**
	// Go's x509.UnknownAuthorityError among them, and on macOS, where the
	// system's verifier answers whenever no ca-file is named, the one untyped
	// verdict known to mean the same. Every other verdict of the system's is
	// a reason of its own, a revoked certificate among them, and keeps its
	// words in redis.conn.failed: a CA file is no cure for one but a way
	// round it, since naming one replaces the system's checks with Go's
	// verifier and that CA alone, which is what the hint says (CAHint). A
	// ca-file already named that did not issue the certificate is said to be
	// that, by classifyDial, which has the request to name it.
	if plugin.CertUntrusted(err) {
		return view.Errorf("redis.tls.untrusted", "%s presented a certificate nothing here trusts", addr).
			WithHint(sf.CAHint("ca-file"))
	}
	if errors.Is(err, io.EOF) {
		return view.Errorf("redis.conn.closed", "%s closed the connection", addr).
			WithHint("a TLS server answers a plaintext client by hanging up — try " + sf.SettingTo("tls", true))
	}
	return view.Errorf("redis.conn.failed", "could not reach %s: %v", addr, err).
		WithHint(sf.SettingsHint("redis.overview"))
}

// classifyDial is classify for the dial, its handshake and the commands
// connect sends before it hands the connection over: the steps that can fail
// on the server's certificate, or on a TLS server hanging up on plaintext,
// and the ones with the request to hand — so the ones that can say a ca-file
// named is not the CA that issued it, rather than send the reader to name
// the CA in the setting that already names one, and that a forward is what
// turned TLS off.
func classifyDial(err error, addr string, req plugin.Request) *view.Error {
	// A certificate that is not for the end of a forward the host opened is
	// no fault of the server's, and not one the address can fix: through a
	// forward the host fills the address with 127.0.0.1 and a port of its
	// own, which a service's certificate names only by luck. So the refusal
	// names the forward and the name the certificate is for, and sends the
	// reader to tls-server-name, which checks that name in 127.0.0.1's place
	// — never to anything that checks less. Only when tls-server-name is not
	// set: a name given and not matched is the certificate's to explain.
	var nameErr x509.HostnameError
	if errors.As(err, &nameErr) {
		if req.Tunnel() != plugin.TunnelNone && serverName(req) == "" {
			return forwardName(req, nameErr)
		}
		return nameRefusal(addr, nameErr, req)
	}
	// **Through a forward the way to TLS is ca-file or tls-server-name, never
	// tls.** The forward turns tls off, and given by the caller tls is an
	// input the forward fills: the host then opens no forward at all, and the
	// call that followed "try --tls" went to the address config or the
	// default names, and was answered "nothing is listening" about a server
	// that had just hung up on plaintext. Either of the two turns TLS on and
	// leaves the forward open, and the name is what the certificate is then
	// checked for, in place of the forward's end.
	if req.Tunnel() != plugin.TunnelNone && errors.Is(err, io.EOF) {
		return view.Errorf("redis.conn.closed", "%s closed the connection", addr).
			WithHint("a TLS server answers a plaintext client by hanging up, and plaintext is what " +
				req.Reached(addr) + " asks for — " +
				req.Surface().SettingName("ca-file", "tls-server-name") + " each turn TLS on over the forward, " +
				"and the name is the one the certificate is checked for, since the forward ends at " + hostOnly(addr))
	}
	// **A port on this machine with nothing on it is a forward that exited**
	// far more often than a server that is down, and "a server bound to
	// localhost only answers from its own host", classify's hint for a
	// refusal, is no question to ask about one. Through a forward the host
	// opened, the port was that forward's end, opened for this call and gone
	// before the call reached it; without one, a loopback address is a server
	// on this machine or a port-forward the operator runs, as pg's refusal
	// says.
	if plugin.DialRefused(err) {
		switch {
		case req.Tunnel() != plugin.TunnelNone:
			behind := "the pod behind it restarts"
			if req.Tunnel() == plugin.TunnelSSH {
				behind = "the SSH connection it rides drops"
			}
			return view.Errorf("redis.conn.refused", "nothing is listening on %s, the end of %s",
				addr, req.Reached(addr)).
				WithHint("a local port with nothing on it is a port-forward that exited — this one was opened for " +
					"this call and was gone before the call reached it, as a forward is when " + behind)
		case loopback(hostOnly(addr)):
			return view.Errorf("redis.conn.refused", "nothing is listening on %s", addr).
				WithHint("redis listens on 6379 by default, and a local port with nothing on it is a server not " +
					"running on this machine or a port-forward that exited — check the terminal running it")
		}
	}
	if ca := plugin.ExpandHome(req.String("ca-file")); ca != "" && plugin.CertUntrusted(err) {
		return view.Errorf("redis.tls.untrusted", "%s presented a certificate nothing here trusts", addr).
			WithHint(ca + ", which " + req.Surface().SettingName("ca-file") + " names, does not hold the CA that " +
				"issued it — a self-signed certificate is its own CA")
	}
	// Every other verdict on a certificate is its own reason, quoted in the
	// verifier's words — the system's, for one macOS gives untyped — and
	// never "could not reach", which sent its reader to the page of every
	// input about a server that had answered with a certificate. The one
	// classify reads as an issuer nothing vouches for is left to it.
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) && !plugin.CertUntrusted(err) {
		checked := "the host in " + req.Surface().SettingName("address")
		if serverName(req) != "" {
			checked = "the name in " + req.Surface().SettingName("tls-server-name")
		}
		rejected := view.Errorf("redis.tls.rejected", "%s presented a certificate that does not verify: %v", addr, verifyErr.Err)
		// A rule of macOS's own, which the verdict's words do not name: a
		// ten-year certificate, the usual one for a server of one's own, is
		// "not standards compliant" there, and the hint below would have
		// sent its reader to check dates that were fine.
		if hint := plugin.CertPolicyHint(err); hint != "" {
			return rejected.WithHint(hint)
		}
		return rejected.WithHint("a certificate is checked for " + checked +
			", its dates and the use it was issued for, as well as for who issued it")
	}
	return classify(err, addr, req.Reached(addr), req.Surface())
}

// nameRefusal is redis.tls.name: the certificate presented at addr refused
// for the name it was checked for, which it does not carry.
//
// The server answered, and "could not reach" misnamed it, with the page of
// every input for a hint. What the reader needs is the names the
// certificate does carry, and the setting the name checked came from — the
// host in address, or tls-server-name when one is given — and a ca-file
// cures nothing here: Go checks the name before it builds a chain, so this
// says nothing about the CA either way. No mode here checks the chain alone,
// so the ways on are the name and the certificate.
func nameRefusal(addr string, nameErr x509.HostnameError, req plugin.Request) *view.Error {
	sf := req.Surface()
	checked := nameErr.Host
	if checked == "" {
		checked = hostOnly(addr)
	}
	cert := nameErr.Certificate
	if cert == nil || len(cert.DNSNames)+len(cert.IPAddresses) == 0 {
		return view.Errorf("redis.tls.name", "%s presented a certificate that names no host, %s or any other",
			addr, checked).
			WithHint("a certificate with no subject alternative names verifies as no host at all — it reaches " +
				"the server once it is reissued with " + checked + " among them")
	}
	refusal := view.Errorf("redis.tls.name", "%s presented a certificate for %s, not %s",
		addr, plugin.CertNames(cert), checked)
	if serverName(req) != "" {
		return refusal.WithHint(sf.SettingName("tls-server-name") + " is the name the certificate is checked " +
			"against — name one it carries, or have it reissued with " + checked + " among its subject " +
			"alternative names")
	}
	return refusal.WithHint(sf.SettingName("address") + " is the name the certificate is checked against — reach " +
		"the server by one it carries, or have it reissued with " + checked + " among its subject alternative names")
}

// reachArgs points a call this one hands its reader at the server it reached,
// as the calls s3's and qdrant's messages name are: the profile it came
// through whenever there was one, since the password it used may be the
// profile's and no other layer holds it, and the address only when the host
// opened no forward (Request.Tunnel) — through one, the address was
// 127.0.0.1 and a port that closed with the call, and the profile is what
// reaches the same server again. Reached directly, the address stays, since
// it may be one typed over the profile's, and with it how it was reached when
// that was protected — tls when on, the certificate paths and
// tls-server-name when named — and as whom and where: the ACL user, whose
// rules decide which keys it may see, and the database, since a key in
// database 3 is in no listing of database 0.
//
// **Without them, the call named reached another server.** Pasted, it ran
// against whatever address the configuration there named, and a key "not
// found" was looked for again somewhere it was never going to be. Over MCP
// the call gives the profile alone: the rest are Local, and the bridge drops
// one an agent sends.
func reachArgs(req plugin.Request) []plugin.Arg {
	if req.Surface() == plugin.SurfaceMCP {
		return req.ReachArgs()
	}
	args := req.ReachArgs(plugin.Arg{Name: "address", Value: req.String("address")})
	if req.Bool("tls") {
		args = append(args, plugin.Arg{Name: "tls", Value: true})
	}
	for _, name := range []string{"ca-file", "tls-server-name", "cert-file", "key-file", "username"} {
		if v := strings.TrimSpace(req.String(name)); v != "" {
			args = append(args, plugin.Arg{Name: name, Value: v})
		}
	}
	if db := req.Int("db"); db != 0 {
		args = append(args, plugin.Arg{Name: "db", Value: db})
	}
	return args
}

// serverName is the name the certificate is checked for in place of the
// address's host, or "" for the host.
func serverName(req plugin.Request) string { return strings.TrimSpace(req.String("tls-server-name")) }

// forwardName is redis.tls.forward: the refusal for a certificate checked
// for the end of a forward the host opened — 127.0.0.1 — and not for the
// name the server answers as, which the certificate names instead.
//
// **The way through is tls-server-name, never tls set to false.** The
// forward has set tls to false already, and given by the caller it is an
// input the forward fills: the host then opens no forward at all, and the
// call goes to the address config or the default names. Nor anything that
// checks less: this plugin has no mode that checks the chain alone, which
// would accept any certificate the CA ever signed.
func forwardName(req plugin.Request, nameErr x509.HostnameError) *view.Error {
	return view.Errorf("redis.tls.forward", "the certificate behind %s is for %s, not for %s, "+
		"where the forward ends", req.Reached(req.String("address")), plugin.CertNames(nameErr.Certificate), nameErr.Host).
		WithHint("a forward always ends at 127.0.0.1, so the certificate is checked for the name the server " +
			"answers as instead: " + req.Surface().SettingName("tls-server-name") + ", which the profile can " +
			"hold beside its forward, names it — one the certificate is for — and it is checked as strictly " +
			"as the host it replaces")
}

// loopback reports whether host names this machine, by parse and by name:
// an operator writes "localhost" about as often as "127.0.0.1".
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := stdnet.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hostOnly(addr string) string {
	host, _, err := stdnet.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
