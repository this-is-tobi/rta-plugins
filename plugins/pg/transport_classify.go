package main

import (
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What a server says when the client certificate is the trouble, and what the
// children say about the files they were handed.
//
// **The server asks for a client certificate, and refuses without a good one,
// in words that read as a password.** pg_hba.conf's clientcert (or the cert
// method) answers a connection with no certificate, and one whose certificate
// no CA the server trusts issued, with the same FATAL 28000, "connection
// requires a valid client certificate", and the classifier turned every 28000
// into a rejected password with a hint to check one that had not been looked
// at. A certificate that was accepted but names another role fails the cert
// method with "certificate authentication failed for user", which is a
// different fix. Each is named, with the setting that changes it.

// clientCertRequired is pg.tls.client.required: the server at where wants a
// client certificate and the call did not present one it accepts.
func clientCertRequired(where string, req plugin.Request) *view.Error {
	t, sf := transportOf(req), req.Surface()
	if t.clientCert == "" {
		return view.Errorf("pg.tls.client.required", "%s requires a client certificate, and none was presented", where).
			WithHint(sf.SettingName("sslcert", "sslkey") + " name the certificate and its key, issued by a CA the " +
				"server trusts for clients (its ssl_ca_file), and " + sf.SettingName("ssl-home") +
				" reads the pair libpq looks for under ~/.postgresql")
	}
	return view.Errorf("pg.tls.client.required", "%s refused the client certificate %s", where, t.clientCert).
		WithHint("it requires one issued by a CA in its ssl_ca_file, still valid, and named for the role it " +
			"logs in — " + t.clientSetting(sf, "sslcert") + " names this one")
}

// clientCertRole is pg.tls.client.role: the certificate was accepted, and it
// is not for the role the call connects as.
func clientCertRole(where string, req plugin.Request) *view.Error {
	sf := req.Surface()
	return view.Errorf("pg.tls.client.role", "%s accepted the client certificate and not as role %q", where, req.String("user")).
		WithHint("with the cert method the certificate's common name is the role it logs in as, unless a " +
			"pg_ident.conf map says otherwise — " + sf.SettingName("user") + " is the role the call asks for")
}

// tlsAlert reads a TLS alert the server sent about the client's certificate,
// as Go's TLS reports it, or nil when text holds none.
func tlsAlert(text, where string, req plugin.Request) *view.Error {
	for _, alert := range []string{"tls: certificate required", "tls: unknown certificate authority",
		"tls: bad certificate", "tls: certificate unknown", "tls: unsupported certificate",
		"tls: expired certificate", "tls: revoked certificate"} {
		if strings.Contains(text, "remote error: "+alert) {
			return clientCertRequired(where, req)
		}
	}
	return nil
}

// childTLS names a failure of the TLS files a libpq child was handed, read
// from its stderr, which is pinned to the C locale (childEnv) so the words
// are the ones below. nil when stderr says nothing about them.
//
// The files were checked before the child ran (checkTransport), so these are
// the ones the check cannot know: the server's own verdict on a certificate
// the child presented, a libpq older than 16 that does not know the word
// system, and a file that changed between the two.
func childTLS(stderr string, req plugin.Request) *view.Error {
	t, sf := transportOf(req), req.Surface()
	where := req.Reached(address(req))
	line := func(needles ...string) string {
		if l := lineMatching(stderr, needles...); l != "" {
			return l
		}
		return lastLine(stderr)
	}
	switch {
	case strings.Contains(stderr, `root certificate file "system" does not exist`):
		return view.Errorf("pg.tls.ca.system", "%s", line("root certificate file")).
			WithHint("this libpq is older than 16, which does not know the word system for this machine's own " +
				"CAs: install a newer client, or name the CA that issued the server's certificate in " +
				sf.SettingName("sslrootcert"))
	case strings.Contains(stderr, "root certificate file") && strings.Contains(stderr, "does not exist"):
		return view.Errorf("pg.tls.ca.unreadable", "%s", line("root certificate file")).
			WithHint(t.rootSetting(sf) + " names a file on this machine, read by the tool rta runs as well as by rta, " +
				"holding the CA's certificate in PEM")
	case strings.Contains(stderr, "has group or world access"):
		return view.Errorf("pg.tls.client.key.perms", "%s", line("has group or world access")).
			WithHint("libpq refuses a private key that group or others can read: `chmod 600` on the file that " +
				t.clientSetting(sf, "sslkey") + " names")
	case strings.Contains(stderr, "could not load private key file"), strings.Contains(stderr, "could not read private key file"):
		return view.Errorf("pg.tls.client.invalid", "%s", line("private key file")).
			WithHint(t.clientSetting(sf, "sslkey") + " wants the PEM private key the certificate in " +
				t.clientSetting(sf, "sslcert") + " was issued for, without a passphrase")
	case strings.Contains(stderr, "could not open certificate file"), strings.Contains(stderr, "could not read certificate file"):
		return view.Errorf("pg.tls.client.unreadable", "%s", line("certificate file")).
			WithHint(t.clientSetting(sf, "sslcert") + " names a file on this machine holding the client certificate in PEM")
	case strings.Contains(stderr, "requires a valid client certificate"), strings.Contains(stderr, "alert certificate required"),
		strings.Contains(stderr, "alert unknown ca"), strings.Contains(stderr, "alert bad certificate"):
		return clientCertRequired(where, req)
	case strings.Contains(stderr, "certificate authentication failed"):
		return clientCertRole(where, req)
	case strings.Contains(stderr, "does not match host name"):
		return view.Errorf("pg.tls.name", "%s", line("does not match host name")).
			WithHint(sf.SettingName("tls-server-name") + " names the certificate's name, which both the connection and the " +
				"tool check; " + sf.SettingName("host") + " is what they check when no name is given")
	case strings.Contains(stderr, "certificate verify failed"):
		return view.Errorf("pg.tls.untrusted", "%s", line("certificate verify failed")).
			WithHint("the tool did not trust the certificate the server presented — " + sf.CAHint("sslrootcert"))
	}
	return nil
}
