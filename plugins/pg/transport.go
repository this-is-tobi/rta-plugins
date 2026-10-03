package main

import (
	"net"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// How a call connects over TLS, decided once for the in-process connection
// and for every libpq child (pg_dump, psql, pg_restore) a dump or a restore
// runs after it: one place, so they cannot read different settings.
type transport struct {
	// mode is the sslmode the connection and every child use: the setting's,
	// except over a forward, where the host forces disable and one of the
	// settings below asks for TLS all the same.
	mode string
	// rootCert is "", "system", or a path.
	rootCert string
	// serverName is the name the certificate is checked for in place of the
	// host's, or "".
	serverName string
}

// transportOf resolves the TLS side of req's connection.
func transportOf(req plugin.Request) transport {
	t := transport{
		mode:       req.String("sslmode"),
		rootCert:   rootCert(req),
		serverName: serverName(req),
	}
	// **A kube: or ssh: forward turns sslmode off, and these turn TLS back on
	// over it.** The host forces sslmode to disable beside a forward (it is
	// the TLS role), and refuses a caller's own, so the plugin reads what the
	// operator can still say: a CA to verify against, a name to check. Each is
	// a request for TLS that nothing else answers, as etcd's ca-file and
	// tls-server-name are, and what it asks for is verify-full: the one mode
	// that checks the name, which a forward's end (127.0.0.1) never is, so the
	// refusal for a certificate that is for another name (forwardName) has the
	// setting that cures it.
	if req.Tunnel() != plugin.TunnelNone && (t.serverName != "" || t.rootCert != "") {
		t.mode = "verify-full"
	}
	return t
}

// serverName is the name the certificate is checked for in place of the
// host's, or "" for the host.
func serverName(req plugin.Request) string { return strings.TrimSpace(req.String("tls-server-name")) }

// childHost is what a libpq child is told to connect to: the host as it is,
// and no hostaddr, or, when a name is given to check the certificate for, that
// name as its host and the address as its hostaddr. libpq connects to the
// hostaddr and checks the certificate for the host it was given beside it,
// which is the one way to say "dial this address, check that name" to it, and
// the in-process connection does the same thing by setting the name on its TLS
// configuration (checkAs). checkServerName has refused a host that is not an
// address.
func childHost(req plugin.Request) (host, hostaddr string) {
	host = req.String("host")
	if name := serverName(req); name != "" {
		return name, strings.Trim(host, "[]")
	}
	return host, ""
}

// ipLiteral reports whether host is an address and not a name: the one
// kind libpq's hostaddr takes, and what a forward's end always is.
func ipLiteral(host string) bool { return net.ParseIP(strings.Trim(host, "[]")) != nil }
