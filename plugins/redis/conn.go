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
			Local: true, Help: "private key for --cert-file"},
		// Redis 6 ACLs name a user; before that, and on most servers still,
		// AUTH takes a bare password and the user is "default". Empty means
		// the latter, which is why this has no default of its own.
		{Name: "username", Type: plugin.String, Default: "", Config: "username",
			Local: true, Help: "ACL user to authenticate as (Redis 6+); empty for the default user"},
		{Name: "password", Type: plugin.Secret, Local: true, EnvFallback: true,
			Help: "password, or the ACL user's password"},
		{Name: "db", Type: plugin.Int, Default: 0, Config: "db", Min: 0, Max: 15,
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
		conn, err = tls.DialWithDialer(&dialer, "tcp", addr, cfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, classify(err, addr)
	}
	c := &client{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn), addr: addr}

	if pw := req.String("password"); pw != "" {
		args := []string{"AUTH", pw}
		if user := req.String("username"); user != "" {
			args = []string{"AUTH", user, pw}
		}
		if _, err := c.do(ctx, args...); err != nil {
			c.Close()
			return nil, classify(err, addr)
		}
	}
	if db := req.Int("db"); db != 0 {
		if _, err := c.do(ctx, "SELECT", strconv.Itoa(db)); err != nil {
			c.Close()
			return nil, classify(err, addr)
		}
	}
	// One PING, so that a server that requires a password nobody supplied is
	// reported as exactly that, here, rather than as a NOAUTH on whichever
	// command a capability happens to send first.
	if _, err := c.do(ctx, "PING"); err != nil {
		c.Close()
		return nil, classify(err, addr)
	}
	return c, nil
}

func tlsConfig(req plugin.Request) (*tls.Config, *view.Error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if ca := req.String("ca-file"); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, view.Errorf("redis.tls.ca.unreadable", "%v", err).
				WithHint("--ca-file is a path on this machine, read by rta rather than by the server")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, view.Errorf("redis.tls.ca.invalid", "%s holds no PEM certificate", ca).
				WithHint("this wants the CA bundle, not the client certificate")
		}
		cfg.RootCAs = pool
	}
	cert, key := req.String("cert-file"), req.String("key-file")
	switch {
	case cert != "" && key == "":
		return nil, view.Errorf("redis.tls.key.missing", "--cert-file given without --key-file").
			WithHint("a client certificate is unusable without its private key")
	case key != "" && cert == "":
		return nil, view.Errorf("redis.tls.cert.missing", "--key-file given without --cert-file").
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
func classify(err error, addr string) *view.Error {
	var already *view.Error
	if errors.As(err, &already) {
		return already
	}
	var srv *serverError
	if errors.As(err, &srv) {
		code, _, _ := strings.Cut(srv.msg, " ")
		switch code {
		case "NOAUTH":
			return view.Errorf("redis.auth.required", "%s requires a password", addr).
				WithHint("set $" + plugin.LocalEnvVar("redis.overview", "password") + " or pass --password")
		case "WRONGPASS":
			return view.Errorf("redis.auth.failed", "%s rejected the credentials", addr).
				WithHint("check the password, and --username if the server uses ACLs")
		case "NOPERM":
			return view.Errorf("redis.denied", "%s: %s", addr, srv.msg).
				WithHint("the ACL user is valid but not allowed this command or key")
		case "LOADING":
			return view.Errorf("redis.loading", "%s is still loading its dataset", addr).
				WithHint("a server restoring a large RDB or AOF answers this until it is done — try again shortly")
		case "MOVED", "ASK":
			return view.Errorf("redis.cluster.redirect", "%s: %s", addr, srv.msg).
				WithHint("this is a cluster and that key lives on another node — `rta redis cluster` lists them; point --address at the one named")
		case "ERR":
			if strings.Contains(srv.msg, "unknown command") {
				return view.Errorf("redis.unsupported", "%s: %s", addr, srv.msg).
					WithHint("the server is older than the command, or a proxy in front of it does not pass it through")
			}
			if strings.Contains(srv.msg, "AUTH") && strings.Contains(srv.msg, "no password") {
				return view.Errorf("redis.auth.unneeded", "%s has no password set, and one was given", addr).
					WithHint("drop --password (or the environment variable) for this server")
			}
		}
		return view.Errorf("redis.server.error", "%s: %s", addr, srv.msg)
	}

	var netErr *stdnet.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return view.Errorf("redis.timeout", "%s did not answer in time", addr).
			WithHint("a server blocked on a long command, or a firewall that drops rather than refuses, looks exactly like this")
	case errors.As(err, &netErr) || strings.Contains(err.Error(), "connection refused"):
		var dnsErr *stdnet.DNSError
		if errors.As(err, &dnsErr) {
			return view.Errorf("redis.host.unknown", "no address for %q", addr).
				WithHint("`rta net dns " + hostOnly(addr) + "` shows what DNS returns")
		}
		return view.Errorf("redis.conn.refused", "nothing is listening on %s", addr).
			WithHint("redis listens on 6379 by default; a server bound to localhost only answers from its own host")
	}
	var authErr x509.UnknownAuthorityError
	if errors.As(err, &authErr) {
		return view.Errorf("redis.tls.untrusted", "%s presented a certificate nothing here trusts", addr).
			WithHint("pass the CA that issued it with --ca-file")
	}
	if errors.Is(err, io.EOF) {
		return view.Errorf("redis.conn.closed", "%s closed the connection", addr).
			WithHint("a TLS server answers a plaintext client by hanging up — try --tls")
	}
	return view.Errorf("redis.conn.failed", "could not reach %s: %v", addr, err).
		WithHint("`rta explain redis.overview` lists every input and where each one can come from")
}

func hostOnly(addr string) string {
	host, _, err := stdnet.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
