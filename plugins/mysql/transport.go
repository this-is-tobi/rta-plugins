package main

import (
	"crypto/x509"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// tlsMode is the tls a call connects with: the setting's, except over a
// forward, where the host forces tls to false and a CA or a name to check
// asks for TLS all the same.
//
// **A kube: or ssh: forward turns tls off, and these turn it back on over
// it.** The host forces the TLS role to false beside a forward (it is
// EndpointTLS) and refuses a caller's own, so the plugin reads what the
// operator can still say: a CA to verify against, a name to check. Each is a
// request for TLS that nothing else answers, as plugins/pg's sslrootcert and
// tls-server-name are, and what it asks for is true: the one mode that checks
// the name, which a forward's end (127.0.0.1) never is, so the refusal for a
// certificate that is for another name (forwardName) has the setting that
// cures it. Never verify-ca, which checks the chain alone: a name given is a
// name that has to hold, and a forward is no reason to check less.
func tlsMode(req plugin.Request) string {
	if req.Tunnel() != plugin.TunnelNone && (caFile(req) != "" || serverName(req) != "") {
		return "true"
	}
	return req.String("tls")
}

// serverName is the name the certificate is checked for in place of the
// host's, or "" for the host.
func serverName(req plugin.Request) string { return strings.TrimSpace(req.String("tls-server-name")) }

// checkedName is the name the server's certificate is checked for: the one
// tls-server-name gives, or the host.
func checkedName(req plugin.Request) string {
	if name := serverName(req); name != "" {
		return name
	}
	return req.String("host")
}

// nameSetting names the setting a certificate's name came from, for the hint
// that says which to change.
func nameSetting(req plugin.Request) string {
	if serverName(req) != "" {
		return req.Surface().SettingName("tls-server-name")
	}
	return req.Surface().SettingName("host")
}

// checkServerName refuses tls-server-name beside a connection that would not
// check it. The name is what a certificate is verified for, so it means
// something at true alone: beside false the call goes in the clear with the
// name unread, and beside preferred, skip-verify and verify-ca no name is
// checked at all, so a name given was an operator expecting a check that
// never ran. Over a forward the mode is true already (tlsMode).
func checkServerName(req plugin.Request) *view.Error {
	name := serverName(req)
	if name == "" {
		return nil
	}
	sf := req.Surface()
	switch mode := tlsMode(req); mode {
	case "true":
		return nil
	case "false":
		return view.Errorf("mysql.tls.name.plaintext", "%s names a certificate to check, and this call would "+
			"reach the server without TLS", sf.SettingName("tls-server-name")).
			WithHint(sf.SettingTo("tls", "true") + " checks the certificate for that name")
	default:
		return view.Errorf("mysql.tls.name.unchecked", "%s names a certificate to check, and %s never checks a name",
			sf.SettingName("tls-server-name"), sf.SettingTo("tls", mode)).
			WithHint(sf.SettingTo("tls", "true") + " checks the certificate for that name, and nothing weaker does")
	}
}

// checkClientTLS refuses, before anything dials, a dump or a restore whose
// child could not do what the in-process connection does.
//
// **The MySQL client checks a certificate for the host it dials and no other
// name.** It has no setting for one, as the driver rta connects with has
// (tls-server-name), so a child handed a name would check the forward's end
// or nothing, and a pre-flight that verified one name was followed by a child
// that verified another. Said here rather than found out as a connection the
// child lost.
//
// **Not for a kube: forward, which ends at a PostgreSQL TLS connection's close
// and does not at this server's.** Measured against a real kubectl
// port-forward to a MySQL 8.4 that takes TLS only: the same forward served
// call after call, each closing cleanly, so the pre-flight leaves the child
// one. A CA with no name is checked by the pre-flight for the forward's end
// itself, and the child checks the same.
func checkClientTLS(req plugin.Request) *view.Error {
	sf := req.Surface()
	if serverName(req) != "" {
		return view.Errorf("mysql.tls.client.name", "%s names a certificate to check, and the MySQL client a dump "+
			"or a restore runs checks the certificate for the host it dials and no other name",
			sf.SettingName("tls-server-name")).
			WithHint("reach the server by a host its certificate names, as a profile with no kube: or ssh: " +
				"coordinate does, and give no " + sf.SettingName("tls-server-name"))
	}
	return nil
}

// forwardName is mysql.tls.forward: the refusal for a certificate checked for
// the end of a forward the host opened, 127.0.0.1, and not for the name the
// server answers as, which the certificate names instead.
//
// **The way through is tls-server-name, never tls.** The forward has set tls
// to false already, and given by the caller it is an input the forward fills:
// the host then opens no forward at all, and the call goes to the host config
// or the default names. Nor anything that checks less: tls-server-name moves
// the check to a name the certificate is for, and verify-ca, which skips the
// name, would accept any certificate the CA ever signed.
func forwardName(req plugin.Request, cert *x509.Certificate) *view.Error {
	return view.Errorf("mysql.tls.forward", "the certificate behind %s is for %s, not for %s, where the forward ends",
		req.Reached(address(req)), plugin.CertNames(cert), req.String("host")).
		WithHint("a forward always ends at 127.0.0.1, so the certificate is checked for the name the server " +
			"answers as instead: " + req.Surface().SettingName("tls-server-name") + ", which the profile can " +
			"hold beside its forward, names it — one the certificate is for — and it is checked as strictly " +
			"as the host it replaces")
}
