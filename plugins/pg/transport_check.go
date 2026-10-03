package main

import (
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// checkTransport refuses, before anything dials, the TLS settings that
// cannot mean what they say: the CA's (checkRootCert) and the name's.
func checkTransport(req plugin.Request) *view.Error {
	if verr := checkRootCert(req); verr != nil {
		return verr
	}
	return checkServerName(req)
}

// checkServerName refuses tls-server-name beside a connection that would not
// check it. The name is what a certificate is verified for, so it means
// something at verify-full alone: beside disable the call goes in the clear
// with the name unread, and beside prefer, require and verify-ca no name is
// checked at all, so a name given was an operator expecting a check that
// never ran. Over a forward the mode is verify-full already (transportOf), and
// the forward's end is always an address.
//
// **And the host has to be an address.** libpq has no setting for the name a
// certificate is checked for; the one way to make it check a name other than
// the host it dials is hostaddr, the address to connect to beside a host that
// names it. The dump and restore children are given the name as their host
// and the address as hostaddr, which libpq reads as an address and not as a
// name to resolve. A host that is itself a name is its own certificate's name,
// and a second one beside it checks the connection against something the
// children cannot be told to.
func checkServerName(req plugin.Request) *view.Error {
	name := serverName(req)
	if name == "" {
		return nil
	}
	t, sf := transportOf(req), req.Surface()
	switch t.mode {
	case "verify-full":
	case "disable":
		return view.Errorf("pg.tls.name.plaintext", "%s names a certificate to check, and this call would reach "+
			"the server without TLS", sf.SettingName("tls-server-name")).
			WithHint(sf.SettingTo("sslmode", "verify-full") + " checks the certificate for that name")
	default:
		return view.Errorf("pg.tls.name.unchecked", "%s names a certificate to check, and %s never checks a name",
			sf.SettingName("tls-server-name"), sf.SettingTo("sslmode", t.mode)).
			WithHint(sf.SettingTo("sslmode", "verify-full") + " checks the certificate for that name, and " +
				"nothing weaker does")
	}
	if host := req.String("host"); !ipLiteral(host) {
		return view.Errorf("pg.tls.name.host", "%s names a certificate to check beside %s, which is a name and "+
			"not an address", sf.SettingName("tls-server-name"), sf.SettingTo("host", host)).
			WithHint("a host that is a name is the name its certificate is checked for. " +
				sf.SettingName("tls-server-name") + " is for a server reached by address, as through a kube: or ssh: " +
				"forward, whose certificate is for a name the address does not spell")
	}
	return nil
}
