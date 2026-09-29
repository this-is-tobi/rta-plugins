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
	if req.Bool("tls") || req.String("ca-file") != "" || req.String("cert-file") != "" {
		cfg, verr := tlsConfig(req)
		if verr != nil {
			return nil, verr
		}
		if host, _, splitErr := stdnet.SplitHostPort(addr); splitErr == nil {
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
	c := &client{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn), addr: addr, sf: req.Surface()}

	if pw := req.String("password"); pw != "" {
		args := []string{"AUTH", pw}
		if user := req.String("username"); user != "" {
			args = []string{"AUTH", user, pw}
		}
		if _, err := c.do(ctx, args...); err != nil {
			c.Close()
			return nil, classify(err, addr, req.Surface())
		}
	}
	if db := req.Int("db"); db != 0 {
		if _, err := c.do(ctx, "SELECT", strconv.Itoa(db)); err != nil {
			c.Close()
			return nil, classify(err, addr, req.Surface())
		}
	}
	// One PING, so that a server that requires a password nobody supplied is
	// reported as exactly that, here, rather than as a NOAUTH on whichever
	// command a capability happens to send first.
	if _, err := c.do(ctx, "PING"); err != nil {
		c.Close()
		return nil, classify(err, addr, req.Surface())
	}
	return c, nil
}

// tlsConfig is the TLS the three certificate paths describe, each with a
// leading ~ resolved as every other path a plugin reads is. Opened as typed,
// ~/ca.pem was a path under a directory named ~, and a CA sitting in the
// operator's home was answered as no such file.
func tlsConfig(req plugin.Request) (*tls.Config, *view.Error) {
	sf := req.Surface()
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
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
func classify(err error, addr string, sf plugin.Surface) *view.Error {
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
			return view.Errorf("redis.auth.required", "%s requires a password", addr).
				WithHint("the password belongs in $" + plugin.LocalEnvVar("redis.overview", "password") +
					" or " + sf.SettingName("password"))
		case "WRONGPASS":
			return view.Errorf("redis.auth.failed", "%s rejected the credentials", addr).
				WithHint("check the password, and " + sf.SettingName("username") + " if the server uses ACLs")
		case "NOPERM":
			return view.Errorf("redis.denied", "%s: %s", addr, srv.msg).
				WithHint("the ACL user is valid but not allowed this command or key")
		case "LOADING":
			return view.Errorf("redis.loading", "%s is still loading its dataset", addr).
				WithHint("a server restoring a large RDB or AOF answers this until it is done — try again shortly")
		case "MOVED", "ASK":
			return view.Errorf("redis.cluster.redirect", "%s: %s", addr, srv.msg).
				WithHint("this is a cluster and that key lives on another node — " + sf.CapabilityName("redis.cluster") +
					" lists them; point " + sf.SettingName("address") + " at the one named")
		case "ERR":
			if strings.Contains(srv.msg, "unknown command") {
				return view.Errorf("redis.unsupported", "%s: %s", addr, srv.msg).
					WithHint("the server is older than the command, or a proxy in front of it does not pass it through")
			}
			if strings.Contains(srv.msg, "DB index is out of range") {
				return view.Errorf("redis.db.range", "%s has no database with that index", addr).
					WithHint("the server's `databases` setting counts them from 0 (16 unless raised) — pick " + sf.SettingName("db") + " below it")
			}
			if strings.Contains(srv.msg, "AUTH") && strings.Contains(srv.msg, "no password") {
				return view.Errorf("redis.auth.unneeded", "%s has no password set, and one was given", addr).
					WithHint("drop " + sf.SettingName("password") + " (or the environment variable) for this server")
			}
		}
		return view.Errorf("redis.server.error", "%s: %s", addr, srv.msg)
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

// classifyDial is classify for the dial and its handshake: the one step that
// can fail on the server's certificate, and the one with the request to hand,
// so the one that can say a ca-file named is not the CA that issued it —
// rather than send the reader to name the CA in the setting that already
// names one.
func classifyDial(err error, addr string, req plugin.Request) *view.Error {
	var nameErr x509.HostnameError
	if errors.As(err, &nameErr) {
		if req.Tunnel() != plugin.TunnelNone {
			return forwardRefusal(addr, nameErr.Certificate, req)
		}
		return nameRefusal(addr, nameErr.Certificate, req.Surface())
	}
	if ca := plugin.ExpandHome(req.String("ca-file")); ca != "" && plugin.CertUntrusted(err) {
		return view.Errorf("redis.tls.untrusted", "%s presented a certificate nothing here trusts", addr).
			WithHint(ca + ", which " + req.Surface().SettingName("ca-file") + " names, does not hold the CA that " +
				"issued it — a self-signed certificate is its own CA")
	}
	return classify(err, addr, req.Surface())
}

// nameRefusal is redis.tls.name: the certificate cert, presented at addr,
// refused for a host it does not name.
//
// The server answered, and "could not reach" misnamed it, with the page of
// every input for a hint. What the reader needs is the names the
// certificate does carry, since the host in address is the one it is
// checked against, and a ca-file cures nothing here: Go checks the name
// before it builds a chain, so this says nothing about the CA either way.
// No mode here checks the chain alone, so the ways on are the address and
// the certificate.
func nameRefusal(addr string, cert *x509.Certificate, sf plugin.Surface) *view.Error {
	host := hostOnly(addr)
	names := certNames(cert)
	if len(names) == 0 {
		return view.Errorf("redis.tls.name", "%s presented a certificate that names no host, %s or any other",
			addr, host).
			WithHint("a certificate with no subject alternative names verifies as no host at all — it reaches " +
				"the server once it is reissued with " + host + " among them")
	}
	return view.Errorf("redis.tls.name", "%s presented a certificate for %s, not %s",
		addr, strings.Join(names, ", "), host).
		WithHint(sf.SettingName("address") + " is the name the certificate is checked against — reach the " +
			"server by one it carries, or have it reissued with " + host + " among its subject alternative names")
}

// certNames is the names cert is for, as a refusal lists them: its subject
// alternative names, DNS and address, since those are all a verifier
// reads. Four at most: a certificate for a fleet can carry dozens, and the
// reader needs to see the one checked is not among them, not the whole
// list.
func certNames(cert *x509.Certificate) []string {
	if cert == nil {
		return nil
	}
	names := append([]string(nil), cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) > 4 {
		names = append(names[:4:4], fmt.Sprintf("%d more", len(names)-4))
	}
	return names
}

// forwardRefusal is redis.tls.forward: the certificate cert, presented at
// addr, refused for a name that was the local end of the forward the call
// came through.
//
// **Through a forward, TLS checks the certificate against the forward's own
// address.** connect takes the name to verify from the address it dials, and
// a kube: or ssh: profile hands it 127.0.0.1, which a certificate issued for
// the server does not name. The forward turns tls off, but ca-file and
// cert-file turn TLS on whatever tls says, so a profile naming a CA beside a
// kube: coordinate failed on every call, as "could not reach" a server that
// had answered. The name the server goes by is the profile's coordinate,
// which the host never tells a plugin, so it cannot be checked in its place,
// and neither is the check turned off: this plugin has no mode that checks
// the chain alone, so the refusal says what does get through — the forward's
// plaintext, and a certificate that names the address too.
func forwardRefusal(addr string, cert *x509.Certificate, req plugin.Request) *view.Error {
	sf := req.Surface()
	names := "no host"
	if sans := certNames(cert); len(sans) > 0 {
		names = strings.Join(sans, ", ")
	}
	// Which input turned TLS on, since the forward had turned it off.
	on := sf.SettingTo("tls", true)
	switch {
	case req.String("ca-file") != "":
		on = sf.SettingName("ca-file")
	case req.String("cert-file") != "":
		on = sf.SettingName("cert-file")
	}
	// What the hop off this machine already runs inside, which is why the
	// forward turns TLS off in the first place (plugin.EndpointTLS).
	carrier := "the SSH connection it rides"
	if req.Tunnel() == plugin.TunnelKube {
		carrier = "the API server's TLS"
	}
	// **The way through is ca-file and cert-file left out, never tls set to
	// false.** The forward has set tls to false already, and given by the
	// caller it is an input the forward fills: the host then opens no forward
	// at all, and the call goes to the address config or the default names.
	return view.Errorf("redis.tls.forward", "%s presented a certificate for %s; the TLS %s turns on checked it "+
		"against %s, the local end of the %s: forward profile %s opened", addr, names, on, hostOnly(addr),
		req.Tunnel(), req.Profile()).
		WithHint("through a forward the name a certificate is checked against is the forward's own address, never " +
			"the server's, and this plugin has no mode that checks the chain alone — without " +
			sf.SettingName("ca-file", "cert-file") + ", which turn TLS on though the forward turns it off, the " +
			"connection runs over the forward in the clear, the hop off this machine inside " + carrier + "; a " +
			"server that takes TLS alone is reached through a forward only by a certificate that names " +
			hostOnly(addr) + " too")
}

func hostOnly(addr string) string {
	host, _, err := stdnet.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
