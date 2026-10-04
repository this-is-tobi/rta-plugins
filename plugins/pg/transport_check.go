package main

import (
	"crypto/tls"
	"encoding/pem"
	"os"
	"path/filepath"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// checkTransport refuses, before anything dials, the TLS settings that
// cannot mean what they say: the CA's (checkRootCert), the name's and the
// client pair's. Each file is read here for what it has to hold, the way the
// driver will read it and the way the children will, because left to them a
// file that was not there, or whose key a child would refuse for its
// permissions, came back as pg.conn.failed quoting the whole connection
// string, or as a libpq line, from a failure that never reached the network.
func checkTransport(req plugin.Request) *view.Error {
	if verr := checkRootCert(req); verr != nil {
		return verr
	}
	if verr := checkServerName(req); verr != nil {
		return verr
	}
	if verr := checkRevocation(req); verr != nil {
		return verr
	}
	return checkClientPair(req)
}

// checkChildForward refuses a dump or a restore that would run its tool over a
// TLS connection through a kube: forward, because the forward does not outlive
// the first connection.
//
// **Measured against a real kubectl port-forward**, to a PostgreSQL 17 with
// TLS: the first connection works and its clean close ends the forward
// ("lost connection to pod", the pod-side read reset by a server that has
// already gone), so the second finds nothing listening. A dump or a restore
// connects twice, once for the pre-flight that says what the server is and once
// for the tool, and the tool's was refused as "connection refused", which
// rta then passed through unchanged for a reader to wonder at. Said here, before
// either connects. An ssh: forward is a long-lived tunnel and does not do it,
// and one with no TLS on it does not either (the default over a forward).
func checkChildForward(req plugin.Request) *view.Error {
	if req.Tunnel() != plugin.TunnelKube || !transportOf(req).forwardTLS {
		return nil
	}
	return view.Errorf("pg.tls.client.forward", "a dump or a restore over TLS cannot run through %s",
		req.Reached(address(req))).
		WithHint("it connects twice, once to check the server and once for the tool, and a kube: forward ends " +
			"when the first TLS connection closes — run it from where the server is reached directly or from " +
			"inside the cluster. A call that connects once, a query or a schema, is fine through it")
}

// checkRevocation refuses ssl-home beside a revocation list under ~/.postgresql
// when the connection verifies the server, because nothing here would read it.
//
// libpq reads ~/.postgresql/root.crl unasked and refuses a certificate it
// revokes. pgx has no way to read one, and the children are pointed away from
// the file (PGSSLCRL, transport) so that they and the pre-flight judge a
// certificate by the same rules: a list read by one client alone is a child
// that refuses what the pre-flight accepted. ssl-home asks for libpq's own
// files, so an operator who has a list there is relying on it, and a
// connection that accepted a certificate the list revokes, with no word,
// would be the worst way to find out. It fails closed, before anything dials.
func checkRevocation(req plugin.Request) *view.Error {
	t := transportOf(req)
	if !req.Bool("ssl-home") || (t.mode != "verify-ca" && t.mode != "verify-full") {
		return nil
	}
	dir := libpqDir()
	if dir == "" {
		return nil
	}
	list := filepath.Join(dir, "root.crl")
	if !exists(list) {
		return nil
	}
	sf := req.Surface()
	return view.Errorf("pg.tls.crl.unsupported", "%s asks for libpq's own files, and %s is a revocation list that "+
		"nothing here reads", sf.SettingName("ssl-home"), list).
		WithHint("pgx has no way to check a certificate against one, so a certificate it revokes would be " +
			"accepted — move it aside, or drop " + sf.SettingName("ssl-home") + " and name the files with " +
			sf.SettingName("sslrootcert", "sslcert", "sslkey") + ", which are read and nothing else is")
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

// checkClientPair refuses a client certificate that the server could not be
// shown: one without its key, a file that is not there or holds no
// certificate, a key that libpq's children would refuse for being readable
// by others, a key protected by a passphrase, and a pair that is not a pair.
// Each names the setting that put the file there.
//
// **The key's permissions are libpq's rule, applied to the pre-flight too.**
// libpq refuses a private key that group or others can read, and pgx does
// not look, so a pre-flight that connected was followed by a child that was
// refused. Both are held to the stricter rule, here, before either runs.
//
// **A passphrase is refused rather than asked for.** pgx would ask a callback
// the plugin does not give, and libpq's children ask the controlling terminal
// through OpenSSL, past the child's own descriptors: a call that hangs at a
// prompt nobody sees. A key without a passphrase, readable by its owner
// alone, is what the client certificate of an unattended connection is.
func checkClientPair(req plugin.Request) *view.Error {
	t, sf := transportOf(req), req.Surface()
	if t.mode == "disable" || (t.clientCert == "" && t.clientKey == "") {
		return nil
	}
	cert, key := t.clientSetting(sf, "sslcert"), t.clientSetting(sf, "sslkey")
	switch {
	case t.clientCert == "":
		return view.Errorf("pg.tls.client.pair", "%s names a private key and %s names no certificate to go with it",
			key, sf.SettingName("sslcert")).
			WithHint("a client certificate is presented with its key: " + sf.SettingName("sslcert", "sslkey") + " go together")
	case t.clientKey == "":
		return view.Errorf("pg.tls.client.pair", "%s names a client certificate and %s names no key to go with it",
			cert, sf.SettingName("sslkey")).
			WithHint("a client certificate is presented with its key: " + sf.SettingName("sslcert", "sslkey") + " go together")
	}
	certPEM, err := os.ReadFile(t.clientCert)
	if err != nil {
		return view.Errorf("pg.tls.client.unreadable", "%v", err).
			WithHint(cert + " names a file on this machine, read by rta rather than by the server, holding the " +
				"client certificate in PEM")
	}
	if info, err := os.Stat(t.clientKey); err != nil {
		return view.Errorf("pg.tls.client.unreadable", "%v", err).
			WithHint(key + " names a file on this machine, read by rta rather than by the server, holding the " +
				"client certificate's private key in PEM")
	} else if info.Mode().Perm()&0o077 != 0 {
		return view.Errorf("pg.tls.client.key.perms", "%s is readable by others (mode %04o)", t.clientKey, info.Mode().Perm()).
			WithHint("libpq refuses a private key that group or others can read, and rta holds its own " +
				"connection to the same rule: `chmod 600` on the file that " + key + " names")
	}
	keyPEM, err := os.ReadFile(t.clientKey)
	if err != nil {
		return view.Errorf("pg.tls.client.unreadable", "%v", err).
			WithHint(key + " names a file on this machine, read by rta rather than by the server, holding the " +
				"client certificate's private key in PEM")
	}
	if encrypted(keyPEM) {
		return view.Errorf("pg.tls.client.key.encrypted", "%s holds a private key protected by a passphrase", t.clientKey).
			WithHint("rta has nowhere to ask for it, and libpq would ask a terminal it does not own — " +
				key + " wants a key without one: `openssl pkey -in <key> -out <new key>` writes it")
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return view.Errorf("pg.tls.client.invalid", "%s and %s are not a certificate and its key: %v",
			t.clientCert, t.clientKey, err).
			WithHint(cert + " wants a PEM certificate and " + key + " the PEM private key that certificate was " +
				"issued for")
	}
	return nil
}

// rootSetting names the setting that put the root certificate in place:
// sslrootcert, or ssl-home when the file was found under ~/.postgresql.
func (t transport) rootSetting(sf plugin.Surface) string {
	if t.fromHome.root {
		return sf.SettingName("ssl-home")
	}
	return sf.SettingName("sslrootcert")
}

// caSubject is what a refusal about a root certificate begins with: the setting
// that named it, or, when it was found and nothing named it, ssl-home and the
// file it found. "ssl-home names a CA" read as though the setting held one, to
// a reader who had set it for the client pair alone and never saw the file.
func (t transport) caSubject(sf plugin.Surface) string {
	if t.fromHome.root {
		return sf.SettingName("ssl-home") + " found the CA " + t.rootCert
	}
	return t.rootSetting(sf) + " names a CA"
}

// homeWayOut is the sentence a refusal of a root certificate ssl-home found
// ends with: leaving ssl-home off stops the file being read at all, which is
// the way out for a reader who wanted the client pair and not the CA.
func (t transport) homeWayOut(sf plugin.Surface) string {
	if !t.fromHome.root {
		return ""
	}
	return " — or drop " + sf.SettingName("ssl-home") + ", which stops the file being read, and name the client " +
		"pair with " + sf.SettingName("sslcert", "sslkey")
}

// clientSetting names the setting that put the client pair in place: sslcert
// or sslkey, or ssl-home when the files were found under ~/.postgresql.
func (t transport) clientSetting(sf plugin.Surface, setting string) string {
	if t.fromHome.client {
		return sf.SettingName("ssl-home")
	}
	return sf.SettingName(setting)
}

// encrypted reports whether the first PEM block of a private key is one a
// passphrase protects: PKCS#8's own type, and the legacy form that says so in
// a DEK-Info header (what x509.IsEncryptedPEMBlock reads, which is deprecated
// for everything but this).
func encrypted(keyPEM []byte) bool {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return false
	}
	_, legacy := block.Headers["DEK-Info"]
	return block.Type == "ENCRYPTED PRIVATE KEY" || legacy
}
