package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// connFields are the inputs every capability here shares.
//
// Each one declares a Config key, which is the whole reason config keys exist:
// an operator states the connection once, in their own file, and never types
// it again. The handler reads req.String("host") and cannot tell whether that
// came from a flag, the config file or the declared default — which is the
// point, not an accident of the API.
//
// **Every one of them is also Local, and that is a security property rather
// than a detail**. Together they name *which server this
// call reaches and as whom*, and an MCP caller may not choose that. They
// were ordinary inputs until a design review found what that meant: an
// input a plugin declares is published in the MCP tool schema and accepted
// from a caller, and plugin.Resolve applies caller values last — above
// config, above the host's own environment — so an agent could name any
// database it liked and have rta fill $RTA_PG_PASSWORD in beside it,
// pointing a real credential at a machine the agent chose. Local closes it
// with no contract change: config still fills these, a person at a terminal
// still passes them as ordinary flags, and the one surface that must not
// choose them no longer can.
//
// The password differs only in also declaring EnvFallback, and that
// distinction is deliberate: EnvFallback is for values that genuinely
// are credentials, so the host resolves it from $RTA_PG_PASSWORD and
// `rta explain` prints that variable name. A field that merely chooses a
// destination must come from an explicit caller or from config, never from
// an ambient variable the MCP server happened to inherit.
func connFields() []plugin.Field {
	return []plugin.Field{
		// host and port carry the endpoint roles, so a profile naming a
		// cluster reaches this database through a port-forward the host opens
		// and closes: pg never learns a forward was there, which is the tunnel
		// contract and the reason none of the code below changes.
		{Name: "host", Type: plugin.String, Default: "localhost", Config: "host",
			Local: true, Endpoint: plugin.EndpointHost, Help: "database host"},
		{Name: "port", Type: plugin.Int, Default: 5432, Config: "port",
			Local: true, Endpoint: plugin.EndpointPort, Min: 1, Max: 65535, Help: "database port"},
		{Name: "user", Type: plugin.String, Default: "postgres", Config: "user",
			Local: true, Help: "role to connect as"},
		{Name: "database", Type: plugin.String, Default: "postgres", Config: "database",
			Local: true, Help: "database to connect to"},
		// Local for a slightly different reason than the four above: it does
		// not change *where* the call goes, it changes whether the transport
		// is protected. An agent that could set it could ask for `disable`
		// and downgrade a connection the operator configured as verify-full.
		// The tls role, and it is not a downgrade to argue about — it is
		// measured. Through a port-forward, `prefer` kills the forward on the
		// *clean disconnect*: PostgreSQL closes, the TLS layer's trailing
		// close_notify arrives at a socket that is already gone, the pod-side
		// read resets, and kubectl exits. The next call gets "connection
		// refused" on a local port and nothing connects the two. It buys
		// nothing either, since the forward is loopback and the hop that leaves
		// the machine is already inside the API server's TLS.
		//
		// Only when a tunnel is actually open. Every other call keeps `prefer`,
		// and a caller who says otherwise still wins.
		{Name: "sslmode", Type: plugin.String, Default: "prefer", Config: "sslmode",
			Local:    true,
			Endpoint: plugin.EndpointTLS,
			Options:  []string{"disable", "prefer", "require", "verify-ca", "verify-full"},
			Help:     "TLS negotiation mode"},
		{Name: "password", Type: plugin.Secret, Local: true, EnvFallback: true,
			Help: "password for the role"},
		// Local for the same reason plugins/etcd's own ca-file is: it is read
		// off this machine's disk. Named sslrootcert rather than etcd's
		// ca-file on purpose — unlike vault, which invented no libpq-shaped
		// word to mirror, sslmode above already commits this plugin to
		// libpq's own vocabulary, and sslrootcert is libpq's own keyword for
		// exactly this (jackc/pgx's pgconn.configTLS reads it directly,
		// alongside sslcert/sslkey for a client pair this plugin does not
		// expose). Not a plugin.Secret: a CA certificate is the public half
		// of a key pair, the half a CA hands out for wide distribution so
		// anyone can verify what it signed.
		//
		// **Read by verify-ca and verify-full alone, and never a reason for
		// sslmode to change on a direct connection.** checkRootCert refuses
		// it beside the two modes below them rather than elevate either on
		// the operator's behalf, which would be a second, unwritten way
		// sslmode gets its value — the rule plugins/mysql's ca-file keeps
		// beside its own tls. verify-ca needs one as a file: named nowhere,
		// pgx checks the chain against this machine's own store instead,
		// which checkRootCert refuses as it refuses system beside verify-ca.
		// disable is left to stand: it negotiates nothing a CA could verify.
		//
		// **Over a kube: or ssh: forward it is the one thing that can ask for
		// TLS.** The host forces sslmode to disable there (EndpointTLS) and
		// refuses a caller's own, so the elevation declined above is not the
		// operator's to decline: nothing else on the line says TLS. A CA, or
		// a name to check (tls-server-name), turns it on at verify-full, as
		// plugins/etcd's ca-file turns on its tls, and so this is not
		// TLSAdjacent: a profile may hold it beside its forward.
		{Name: "sslrootcert", Type: plugin.String, Default: "", Config: "sslrootcert",
			Local: true,
			Help: "CA bundle to verify the server against, or system for this machine's own store — " +
				"needed by verify-ca and read by verify-full (system by verify-full alone), refused beside prefer " +
				"and require; under a kube:/ssh: forward it turns TLS on, at verify-full"},
		// The name the certificate is checked for when it is not the host
		// dialled — above all through a kube: or ssh: forward, whose end is
		// 127.0.0.1 whatever the server is called, and which its certificate
		// names only by luck. Checked as strictly as the host would have been:
		// it moves the check, never loosens it, so it is read at verify-full
		// alone (checkServerName). Local for the reason sslmode is: what a
		// certificate has to prove is the operator's to say.
		//
		// Not TLSAdjacent, for sslrootcert's reason: over a forward it is what
		// turns TLS on. A kube: forward ends the forward at the first clean
		// disconnect of a TLS connection (the sslmode note above), so a call
		// that connects twice, as a dump does for its pre-flight and then its
		// child, finds the forward gone for the second.
		{Name: "tls-server-name", Type: plugin.String, Default: "", Config: "tls-server-name",
			Local: true,
			Help: "name to check the server's certificate for, in place of the host's — verify-full only, and " +
				"the host an address; under a kube:/ssh: forward it turns TLS on, at verify-full"},
		// The client certificate a server asks for (pg_hba.conf's clientcert,
		// or the cert method), and its key. libpq's own keywords, as
		// sslrootcert is, and Local for the same reason: they name files on
		// this machine that rta then reads, and a path a caller could choose
		// would be a file-read primitive. Not Secret: the certificate is the
		// public half, and the key is a path, not a value that crosses the
		// wire — its file is held to libpq's rule, readable by its owner
		// alone (checkClientPair), and never one a passphrase protects, which
		// nothing here could be asked for.
		//
		// **Named, or not read.** Neither pgx nor libpq is left to find the
		// pair under ~/.postgresql: the driver is handed the files in its
		// connection string and every child in its environment, and a child
		// with no file named is pointed at one that does not exist
		// (transport). ssl-home below asks for libpq's search back.
		//
		// Not TLSAdjacent, for sslrootcert's reason: over a forward a client
		// certificate asks for TLS as the CA does, at verify-full.
		{Name: "sslcert", Type: plugin.String, Default: "", Config: "sslcert",
			Local: true,
			Help: "client certificate to present to the server, PEM, with sslkey — for a server whose " +
				"pg_hba.conf asks for one; under a kube:/ssh: forward it turns TLS on, at verify-full"},
		{Name: "sslkey", Type: plugin.String, Default: "", Config: "sslkey",
			Local: true,
			Help:  "private key for sslcert, PEM, without a passphrase and readable by its owner alone (mode 600)"},
		// The one switch for what libpq does unasked: look for the client
		// certificate, its key and the root certificate under ~/.postgresql
		// (postgresql.crt, postgresql.key, root.crt) when no setting names
		// them. Off, because that search is two clients doing it two ways
		// (transport), and a file nobody named changes what a connection
		// proves: a root.crt there turns sslmode=require into a verifying
		// connection on pgx and on libpq, and a postgresql.crt presents a
		// certificate to a server the operator never pointed it at. On, rta
		// does the search once and hands the result to the driver and to every
		// child, so they still agree, and a setting that names a file wins
		// over the one found for it. Local, for the reason the files are.
		{Name: "ssl-home", Type: plugin.Bool, Default: false, Config: "ssl-home",
			Local: true,
			Help: "also use the client certificate, key and root certificate libpq looks for under ~/.postgresql " +
				"when sslcert, sslkey and sslrootcert do not name them — off, so no file is read that a setting " +
				"did not name; rta resolves them once and hands both the driver and libpq's tools the same ones"},
	}
}

// connectTimeout bounds a connection's coming up, the handshake and the
// login with it — as long as etcd's and mysql's plugins give theirs, and far
// past what a server on the other end of a port-forward takes.
const connectTimeout = 10 * time.Second

// dsn builds a connection string from the resolved inputs.
//
// Assembled as key=value with each value quoted rather than as a URL, because
// a password containing '@' or '/' silently produces a different connection
// string under URL parsing — and the failure is an authentication error that
// names nothing.
func dsn(req plugin.Request) string {
	quote := func(s string) string {
		return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
	}
	t := transportOf(req)
	parts := []string{
		"host=" + quote(req.String("host")),
		fmt.Sprintf("port=%d", req.Int("port")),
		"user=" + quote(req.String("user")),
		"dbname=" + quote(req.String("database")),
		"sslmode=" + quote(t.mode),
		// Emitted always, and empty on purpose. Leaving the key out does not
		// mean "no passfile" — pgconn's defaultSettings fills it with
		// $HOME/.pgpass and ParseConfig then loads a password from it whenever
		// none was supplied, so an operator who configured a host and a user
		// and deliberately no password got authenticated with whatever
		// credential their own interactive psql keeps for that host. That is
		// the exact shape connFields' Local-everywhere rule exists to prevent,
		// arriving one layer below it: not a value a caller chose, but a value
		// nobody chose, read out of the ambient environment.
		//
		// An empty passfile fails the open and pgconn skips the lookup, so
		// this fails closed. Note it is not interchangeable with the fix
		// pg.dump needs: libpq treats an empty PGPASSFILE as "use the default"
		// and reads ~/.pgpass anyway, which is why backup.go names a path
		// instead.
		"passfile=''",
		// The connect is bounded, and nothing after it. Without one it
		// waited on the operating system's own connect timeout, more than
		// a minute, and twice over under sslmode's default, prefer, which
		// tries TLS and then plaintext: a server behind a firewall that
		// drops packets was answered two and a half minutes in. The seconds
		// are pgconn's unit; each address a name resolves to gets them.
		fmt.Sprintf("connect_timeout=%d", int(connectTimeout/time.Second)),
	}
	if pw := req.String("password"); pw != "" {
		parts = append(parts, "password="+quote(pw))
	}
	// The three files, always, and empty where no setting named one — for
	// passfile's reason, one layer along. pgconn's defaultSettings fills
	// sslrootcert, sslcert and sslkey from ~/.postgresql whenever the files
	// are there, and a key the connection string leaves out keeps that value:
	// a root.crt nobody named made require verify, and a postgresql.crt nobody
	// named was presented to a server the operator never pointed it at. An
	// empty value is a value, and overrides it. (pgconn reads PGSSLROOTCERT
	// and its siblings too, which the host's allowlist keeps out of a plugin
	// it starts: HOME is the channel that is open, and a binary run by hand
	// has both.)
	//
	// Left empty under disable, which reads no file: pgx reads sslrootcert
	// before it looks at the mode, so a CA that was not there failed a
	// connection that would never have used it, and given system it turns
	// disable into verify-full. disable is what a forward forces for a call
	// that names nothing, beside whatever the config names for connecting
	// directly. sslpassword is closed the same way: a key that needs a
	// passphrase is refused (checkClientPair), not asked for.
	root, cert, key := t.rootCert, t.clientCert, t.clientKey
	if t.mode == "disable" {
		root, cert, key = "", "", ""
	}
	parts = append(parts, "sslrootcert="+quote(root), "sslcert="+quote(cert), "sslkey="+quote(key), "sslpassword=''")
	return strings.Join(parts, " ")
}

// rootCert is sslrootcert with a leading ~ resolved and made absolute, or ""
// when it names nothing. One resolution for the three places the path goes —
// the driver's connection string, the child's PGSSLROOTCERT, and the restore
// line a dump's receipt prints — so they cannot name different files, and a
// line pasted in another directory still names this one. Passed through as
// typed, as it once was, ~/ca.pem reached pgx and libpq alike as a path under
// a directory named ~, and a CA sitting in the operator's home was answered
// "no such file or directory".
//
// system is left as it is: libpq's word for this machine's trust store, not
// a file, which made absolute would become one named system here.
func rootCert(req plugin.Request) string {
	ca := req.String("sslrootcert")
	if ca == "" || ca == "system" {
		return ca
	}
	if abs, err := expandHome(ca); err == nil {
		return abs
	}
	return plugin.ExpandHome(ca)
}

// checkRootCert refuses sslrootcert beside an sslmode that does not name
// verification, prefer and require, and a file named beside one that does
// when it cannot be read or holds no certificate. Refused before anything
// dials, and before a dump or a restore's dry run describes a child that
// would carry it — the same place plugins/mysql refuses its ca-file.
//
// **Refused rather than applied, because neither mode can be made to mean
// verified.** prefer never verifies in pgx, whatever CA it is given, so the
// connection this plugin makes was TLS that nothing checked. libpq, which
// pg_dump, psql and pg_restore run on, does verify under prefer when a root
// certificate is there — and prefer then retries in plaintext when TLS
// fails, a certificate that did not verify included. Measured against a
// server whose certificate the named CA did not issue: psql connected, over
// no TLS at all. Applied as libpq applies it, the CA would read as a
// verified connection and turn an impostor into a cleartext session.
//
// require verifies, in both, and that is the trouble: libpq keeps require
// with a root certificate as verify-ca only for compatibility, and its own
// documentation asks nobody to rely on it. The verification lives in the
// presence of a file, not in the mode — drop the file, or have a config
// layer empty it, and require stops verifying without a word, while the
// config and a dump's restore line still say what they said. verify-ca is
// the same check with its name on it, and it fails closed without a CA.
// Which is also plugins/mysql's rule: a CA beside a mode that does not
// itself verify is refused, never taken as a reason for the mode to change.
//
// system, libpq's word for this machine's own trust store rather than a
// file, is taken beside verify-full alone, as libpq takes it. Every public
// CA in that store issues certificates to anyone for a name they control, so
// a check that skips the name accepts all of them; libpq refuses the pair
// outright, and pgx turns any mode beside it into verify-full unasked.
// Between a child that refused and a connection that changed mode, the
// refusal is the one both can keep. disable stands beside it as beside a
// file, since a tunnel forces disable whatever the config names for direct
// connections, and dsn leaves the CA out of a connection that reads none.
//
// verify-ca with no file named is refused too, for system's reason: pgx then
// checks the chain against this machine's own store and reads no name, and a
// server whose certificate a CA in that store issued for another name
// connected, measured. plugins/mysql refuses its own verify-ca without a
// ca-file for the same reason. libpq never falls back to the store — it
// reads ~/.postgresql/root.crt or refuses — and pgx reads that file too when
// it is there, which made verify-ca's CA one nobody named: it is named here,
// as every other file this plugin reads is.
func checkRootCert(req plugin.Request) *view.Error {
	t := transportOf(req)
	ca := t.rootCert
	if ca == "" {
		if mode := t.mode; mode == "verify-ca" {
			sf := req.Surface()
			return view.Errorf("pg.tls.ca.missing", "%s checks the server's chain against the CA %s names, "+
				"and it names none", sf.SettingTo("sslmode", mode), sf.SettingName("sslrootcert")).
				WithHint(sf.SettingName("sslrootcert") + " names it: the CA that issued the server's certificate, or " +
					"the certificate itself when it is self-signed. " + sf.SettingTo("sslmode", "verify-full") +
					" checks against this machine's own CAs instead, the server's name included")
		}
		return nil
	}
	sf := req.Surface()
	if mode := t.mode; ca == "system" && mode != "verify-full" && mode != "disable" {
		return view.Errorf("pg.tls.ca.system", "%s trusts every CA this machine does, and is taken beside %s alone",
			sf.SettingTo("sslrootcert", "system"), sf.SettingTo("sslmode", "verify-full")).
			WithHint("a certificate from any of them is had for any name its holder controls, so only the " +
				"mode that checks the name makes it mean anything. A CA of the server's own belongs in " +
				sf.SettingName("sslrootcert") + " as a file, which verify-ca reads too")
	}
	switch mode := t.mode; mode {
	case "prefer":
		return view.Errorf("pg.tls.ca.unused", "%s names a CA, and %s never verifies against one",
			t.rootSetting(sf), sf.SettingTo("sslmode", mode)).
			WithHint(sf.SettingTo("sslmode", "verify-full") + " verifies the server against it, its name " +
				"included, and " + sf.SettingTo("sslmode", "verify-ca") + " its chain alone")
	case "require":
		return view.Errorf("pg.tls.ca.implied", "%s names a CA, and %s verifies against one only because it is there",
			t.rootSetting(sf), sf.SettingTo("sslmode", mode)).
			WithHint(sf.SettingTo("sslmode", "verify-ca") + " is that same check under its own name, which does " +
				"not stop the day the CA is dropped, and " + sf.SettingTo("sslmode", "verify-full") +
				" checks the server's name as well")
	case "disable":
		return nil
	}
	if ca == "system" {
		return nil
	}
	// The file read here too, for what it has to hold, before pgx reads it
	// again: left to the driver, a file that was not there, or held a key
	// instead of a certificate, came back as pg.conn.failed "could not
	// connect", quoting the whole connection string, from a failure that
	// never reached the network. The dry runs refuse it the same way.
	path := ca
	pem, err := os.ReadFile(path)
	if err != nil {
		return view.Errorf("pg.tls.ca.unreadable", "%v", err).
			WithHint(t.rootSetting(sf) + " names a file on this machine, read by rta rather than by the " +
				"server, holding the CA's certificate in PEM")
	}
	if !x509.NewCertPool().AppendCertsFromPEM(pem) {
		return view.Errorf("pg.tls.ca.invalid", "%s holds no PEM certificate", path).
			WithHint(t.rootSetting(sf) + " wants a PEM certificate — the CA's, or a self-signed " +
				"server's own — and a private key or a DER-encoded certificate is not one")
	}
	return nil
}

// classify turns a driver error into something an operator can act on.
//
// This is the capability the design brief singles out — "Error with hints for
// the classic connection failures" — and it is why pg is the plugin that
// proves the contract: every one of these is a sentence somebody has stared
// at without knowing what to do next.
func classify(err error, req plugin.Request) *view.Error {
	// **An error that is already a view.Error has already been classified,
	// and classifying it twice loses it.** Every capability here runs its
	// work inside a closure — withConn, and readOnly inside that — and a
	// closure can only report a refusal by returning an error, so a
	// handler's own view.Error arrives back at exactly the same place a
	// driver failure does. Falling through the switches below, it matched
	// nothing and came out as `pg.conn.failed: could not connect to
	// 127.0.0.1:55432: the query returned more than 1.0 MiB` — a connection
	// error naming a hint about an input, for a connection that was fine.
	//
	// Found by running pg.query against a real server rather than by a test:
	// the row bound had unit tests either side of this function and none
	// through it, so the refusal was correct, reached, and then thrown away
	// one frame later.
	var already *view.Error
	if errors.As(err, &already) {
		return already
	}

	where, sf := address(req), req.Surface()

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "28P01", "28000": // invalid_password, invalid_authorization
			// **A pg_hba.conf that takes this connection only over TLS is not
			// a password refused.** It answers 28000 as a rejected password
			// does, and read as one the reader was sent to check a password
			// nothing had checked — through a forward, whose disable is every
			// call's, on every call. By the server's own words for it, which
			// end "no encryption" in the English a server speaks unless its
			// lc_messages says otherwise; in another language it is read as
			// it always was.
			if pgErr.Code == "28000" && strings.HasSuffix(pgErr.Message, "no encryption") {
				return tlsRequired(where, req)
			}
			// **A server that wants a client certificate answers a connection
			// without a good one in the same code**, 28000, and read as a
			// rejected password the reader was sent to check one that nothing
			// had looked at. By the server's own words for it, as the case
			// above is.
			if pgErr.Code == "28000" && strings.Contains(pgErr.Message, "requires a valid client certificate") {
				return clientCertRequired(req.Reached(where), req)
			}
			if pgErr.Code == "28000" && strings.Contains(pgErr.Message, "certificate authentication failed") {
				return clientCertRole(req.Reached(where), req)
			}
			// Where the password comes from rather than a verb telling the
			// reader to set one: pg.status and the reads beside it answer an
			// agent too, which has no host environment to set and no password
			// argument, since the bridge drops a Local input given.
			return view.Errorf("pg.auth.failed", "%s rejected the credentials for %q",
				req.Reached(where), req.String("user")).
				WithHint("the password is read from $" + plugin.LocalEnvVar("pg.status", "password") +
					" or " + sf.SettingName("password") + " — check it, and " + sf.SettingName("user") +
					": rta only ever uses the password it is given, never ~/.pgpass")
		case "3D000": // invalid_catalog_name
			return view.Errorf("pg.database.missing", "%s has no database named %q",
				req.Reached(where), req.String("database")).
				WithHint(nextCall(req, "pg.database.list") + " shows what is there")
		case "42501": // insufficient_privilege
			return view.Errorf("pg.denied", "%q may not do that on %s",
				req.String("user"), req.String("database")).
				WithHint("this is the database refusing, not rta")
		}
		return view.Errorf("pg.query.failed", "%s", pgErr.Message).
			WithHint("SQLSTATE " + pgErr.Code)
	}

	// Short of the host, before the port: a dial that timed out, or found no
	// way there, reached nothing that could refuse it. Read as refused, a
	// server behind a VPN that is down, or at an address of another
	// network's, was "nothing is listening" about a port no packet reached.
	// The bound's own end arrives as a deadline, and the operating system's
	// connect timeout as a dial that timed out.
	//
	// The other two by the operating system's own error, as plugin.DialRefused
	// and DialUnroutable read it, and never by the *net.OpError around it,
	// which every failed dial and every broken read is: read that way, a
	// server that reset the handshake was "nothing is listening" too. They
	// read Windows' socket errors as well, which the errnos asked here before
	// them did not.
	var netErr *net.OpError
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return view.Errorf("pg.conn.timeout", "%s did not answer in time", where).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	if plugin.DialUnroutable(err) {
		why := err
		if errors.As(err, &netErr) {
			why = netErr.Err
		}
		return view.Errorf("pg.conn.unreachable", "%s cannot be reached from this machine: %v", where, why).
			WithHint("no route leads there from here — a VPN or tunnel the server sits behind that is " +
				"down looks exactly like this, and so does " + sf.SettingName("host") + " naming an address " +
				"on a network this machine is not on")
	}
	if plugin.DialRefused(err) {
		refused := view.Errorf("pg.conn.refused", "nothing is listening on %s", where)
		// TLS the settings asked for over a kube: forward, which that forward
		// does not survive a first connection of: a call that connects twice,
		// as a dump does for its pre-flight and again for its child, finds the
		// forward gone for the second. Said as that, since the port the
		// general hint below blames is the forward's own.
		if req.Tunnel() == plugin.TunnelKube && transportOf(req).mode != "disable" {
			return refused.WithHint("a kubectl port-forward carrying TLS ends at the first clean disconnect — " +
				"PostgreSQL closes, the TLS layer's trailing close_notify reaches a socket already gone, and " +
				"kubectl exits — so a call that connects twice finds it gone the second time. " +
				sf.SettingName("sslrootcert", "tls-server-name") + " are what turned TLS on here; one connection " +
				"per call (a query, a status) does not meet it")
		}
		if loopback(req.String("host")) {
			// "Is the server up?" is the wrong question about a port on this
			// machine, and it is the question the general hint asks. A
			// loopback port with nothing on it is almost always a forward
			// that exited — and the reason it exited is worth naming,
			// because it is not obvious and it is caused by the default.
			return refused.WithHint("a local port with nothing on it is usually a " +
				"port-forward that exited — check the terminal running it. PostgreSQL TLS " +
				"through `kubectl port-forward` kills the forward on the first clean " +
				"disconnect, so `sslmode: disable` is what survives; that hop is already " +
				"inside the API server's TLS")
		}
		return refused.WithHint("is the server up, and is the port right? `" + sf.Call("net.port",
			plugin.Arg{Name: "host", Value: req.String("host"), Positional: true},
			plugin.Arg{Name: "ports", Value: req.Int("port")}) + "` answers the second")
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return view.Errorf("pg.host.unknown", "no address for %q", req.String("host")).
			WithHint(sf.DNSHint(req.String("host")))
	}
	if strings.Contains(err.Error(), "SSL is not enabled") ||
		strings.Contains(err.Error(), "server does not support SSL") {
		return view.Errorf("pg.tls.unsupported", "%s does not offer TLS", where).
			WithHint(sf.SettingTo("sslmode", "disable") + " if that is expected on this network")
	}
	// Only verify-ca and verify-full get here: prefer and require without a
	// CA verify nothing, and checkRootCert refuses either beside one, and
	// verify-ca without one. So the hint names the CA, and never a mode —
	// the one it once pointed at, require, is refused beside sslrootcert now.
	//
	// **Only a certificate plugin.CertUntrusted reads as an unknown issuer's,
	// never every one the handshake refused.** With no CA file, macOS asks
	// its own verifier, which gives most of its verdicts untyped, and each
	// was read here as untrusted: a revoked certificate was answered with the
	// CA to name, and naming one replaces the system's checks, revocation
	// among them, with Go's verifier and that CA alone — the operator who
	// followed the hint reached the server the check had caught. A verdict
	// CertUntrusted does not read keeps the system's words, in
	// pg.conn.failed, and the hint that sends somebody to name a CA says what
	// naming one costs (CAHint).
	if plugin.CertUntrusted(err) {
		refused := view.Errorf("pg.tls.untrusted", "%s presented a certificate nothing here trusts", where)
		t := transportOf(req)
		switch ca := t.rootCert; ca {
		case "":
			return refused.WithHint("a PostgreSQL an operator or a cluster runs commonly has a CA of its own — " +
				sf.CAHint("sslrootcert"))
		case "system":
			return refused.WithHint("no CA in this machine's trust store, which " + sf.SettingTo("sslrootcert", "system") +
				" names, issued it — " + sf.CAHint("sslrootcert"))
		default:
			return refused.WithHint(ca + ", which " + t.rootSetting(sf) + " names, does not hold " +
				"the CA that issued it — a self-signed certificate is its own CA")
		}
	}
	// A certificate for another name than the one checked. Through a forward
	// the host dialled is 127.0.0.1 and a port of the host's own, which a
	// server's certificate names only by luck, so the refusal names the
	// forward and what the certificate is for and sends the reader to
	// tls-server-name, which checks that name in the forward's end's place —
	// never to anything that checks less. A name already given and not matched
	// is the certificate's to explain, with the setting that gave it.
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		if req.Tunnel() != plugin.TunnelNone && serverName(req) == "" {
			return forwardName(req, hostErr)
		}
		return nameRefusal(where, hostErr, req)
	}
	// An alert about the client's certificate, as Go's TLS reports one the
	// server sent: the server saw the certificate and did not take it.
	if alert := tlsAlert(err.Error(), req.Reached(where), req); alert != nil {
		return alert
	}
	// Every other verdict on a certificate is its own reason, quoted in the
	// verifier's words — the system's, for one macOS gives untyped, a revoked
	// certificate among them — and never "could not connect": the server was
	// reached, and answered with a certificate. No setting is offered that
	// checks less.
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		rejected := view.Errorf("pg.tls.rejected", "%s presented a certificate that does not verify: %v",
			where, verifyErr.Err)
		// A rule of macOS's own, which the verdict's words do not name: a
		// ten-year certificate, the usual one for a server of one's own, is
		// "not standards compliant" there.
		if hint := plugin.CertPolicyHint(err); hint != "" {
			return rejected.WithHint(hint)
		}
		return rejected.WithHint("a certificate is checked for its dates and the use it was issued for, as " +
			"well as for who issued it, and with " + sf.SettingTo("sslmode", "verify-full") + " for the host in " +
			sf.SettingName("host") + " too")
	}
	return view.Errorf("pg.conn.failed", "could not connect to %s: %v", where, err).
		WithHint(sf.SettingsHint("pg.status"))
}

// forwardName is pg.tls.forward: the refusal for a certificate checked for
// the end of a forward the host opened, 127.0.0.1, and not for the name the
// server answers as, which the certificate names instead.
//
// **The way through is tls-server-name, never sslmode.** The forward has set
// sslmode to disable already and the host refuses one given beside it, so a
// hint naming sslmode sent its reader to a setting that opens no forward at
// all. Nor anything that checks less: tls-server-name moves the check to a
// name the certificate is for, and verify-ca, which skips the name, would
// accept any certificate the CA ever signed.
func forwardName(req plugin.Request, hostErr x509.HostnameError) *view.Error {
	return view.Errorf("pg.tls.forward", "the certificate behind %s is for %s, not for %s, where the forward ends",
		req.Reached(address(req)), plugin.CertNames(hostErr.Certificate), hostErr.Host).
		WithHint("a forward always ends at 127.0.0.1, so the certificate is checked for the name the server " +
			"answers as instead: " + req.Surface().SettingName("tls-server-name") + ", which the profile can " +
			"hold beside its forward, names it — one the certificate is for — and it is checked as strictly " +
			"as the host it replaces")
}

// nameRefusal is pg.tls.name: the certificate presented at where refused for
// the name it was checked for, which it does not carry.
//
// The server answered, and "could not connect" misnamed it, with the page of
// every input for a hint. What the reader needs is the names the certificate
// does carry and the setting the name came from: tls-server-name when one was
// given, otherwise the host. A host that is an address can be told to check
// another name, and a host that is a name is itself the one to change. No
// mode here checks the chain alone and keeps this check, so the ways on are
// the name and the certificate.
func nameRefusal(where string, hostErr x509.HostnameError, req plugin.Request) *view.Error {
	sf := req.Surface()
	checked := hostErr.Host
	cert := hostErr.Certificate
	if cert == nil || len(cert.DNSNames)+len(cert.IPAddresses) == 0 {
		return view.Errorf("pg.tls.name", "%s presented a certificate that names no host, %s or any other", where, checked).
			WithHint("a certificate with no subject alternative names verifies as no host at all — it reaches " +
				"the server once it is reissued with " + checked + " among them")
	}
	refusal := view.Errorf("pg.tls.name", "%s presented a certificate for %s, not %s",
		where, plugin.CertNames(cert), checked)
	switch {
	case serverName(req) != "":
		return refusal.WithHint(sf.SettingName("tls-server-name") + " is the name the certificate is checked " +
			"against — name one it carries, or have it reissued with " + checked + " among its subject " +
			"alternative names")
	case ipLiteral(req.String("host")):
		return refusal.WithHint(sf.SettingName("tls-server-name") + " checks the certificate for a name it " +
			"carries instead of the address in " + sf.SettingName("host") + ", or have it reissued with " +
			checked + " among its subject alternative names")
	}
	return refusal.WithHint(sf.SettingName("host") + " is the name the certificate is checked against — reach " +
		"the server by one it carries, or have it reissued with " + checked + " among its subject alternative names")
}

// tlsRequired is pg.tls.required: the server at where takes this connection
// only over TLS, and it was made without.
//
// **Through a forward, not the sslmode that would verify it.** The forward
// sets disable, and sslmode given by the caller is an input the forward
// fills, so the host opens no forward at all and the call goes to the host
// config or the default names — measured through a kube: profile, "nothing
// is listening on localhost:5432". What turns TLS on over one is a CA or a
// name to check (transportOf), and a profile holds either beside its
// forward. The name is the one the certificate is for, since the forward ends
// at 127.0.0.1; without it, the refusal for a certificate for another name
// (forwardName) says so.
func tlsRequired(where string, req plugin.Request) *view.Error {
	sf := req.Surface()
	if req.Tunnel() == plugin.TunnelNone {
		return view.Errorf("pg.tls.required", "%s accepts this connection over TLS only", where).
			WithHint(sf.SettingTo("sslmode", "verify-full") + " connects over it, with " + sf.SettingName("sslrootcert") +
				" naming the CA if the server's certificate is from one of its own")
	}
	// What the hop off this machine runs inside, which is why the forward
	// turns TLS off in the first place (plugin.EndpointTLS).
	carrier := "the SSH connection it rides"
	if req.Tunnel() == plugin.TunnelKube {
		carrier = "the API server's TLS"
	}
	return view.Errorf("pg.tls.required", "%s accepts this connection over TLS only, and %s "+
		"carries none", where, req.Reached(where)).
		WithHint("a forward runs the connection in the clear, the hop off this machine inside " + carrier +
			" — " + sf.SettingName("sslrootcert", "tls-server-name") + " each turn TLS on over it, at verify-full, " +
			"with the name the certificate is for in " + sf.SettingName("tls-server-name"))
}

// address is host and port as one address, an IPv6 literal bracketed, for
// the messages and receipts that name the server by it. Joined with a bare
// colon, as it once was, ::1 read ::1:5432, which no reader could split into
// an address and a port. Display only: the driver and the children are
// handed host and port apart.
func address(req plugin.Request) string {
	return net.JoinHostPort(req.String("host"), strconv.Itoa(req.Int("port")))
}

// loopback reports whether a host names this machine.
//
// By parse and by name: an operator writes "localhost" about as often as
// "127.0.0.1", and a resolved tunnel hands back whichever kubectl printed.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// connect opens a connection, mapping any failure through classify.
func connect(ctx context.Context, req plugin.Request) (*pgx.Conn, *view.Error) {
	if verr := checkTransport(req); verr != nil {
		return nil, verr
	}
	cfg, err := pgx.ParseConfig(dsn(req))
	if err != nil {
		return nil, classify(err, req)
	}
	checkAs(cfg, serverName(req))
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, classify(err, req)
	}
	return conn, nil
}

// checkAs makes every TLS configuration of cfg check the server's certificate
// for name, when one is given, in place of the host the driver dialled: the
// ServerName is what Go's verifier checks a certificate for, and the SNI the
// handshake sends. A connection string has no word for it, which is why it is
// set on the parsed configuration, on the main one and on each fallback the
// driver built for another host or another mode: left on one, the other
// connected checking the host.
func checkAs(cfg *pgx.ConnConfig, name string) {
	if name == "" {
		return
	}
	if cfg.TLSConfig != nil {
		cfg.TLSConfig.ServerName = name
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.TLSConfig != nil {
			fallback.TLSConfig.ServerName = name
		}
	}
}

// readOnly runs fn inside a READ ONLY transaction.
//
// This is what lets pg.query declare Safety: Read honestly. The alternative
// is parsing the SQL and refusing anything that looks like a write, which is
// a game nobody wins — `WITH x AS (DELETE ... RETURNING *) SELECT * FROM x`
// is a SELECT statement that deletes rows. PostgreSQL enforces this itself,
// server-side, against the parsed statement rather than against a string, so
// an agent handed pg.query cannot mutate anything whatever it sends.
func readOnly(ctx context.Context, conn *pgx.Conn, fn func(pgx.Tx) error) error {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return fn(tx)
}

// rowsToTable renders a result set, whatever its shape.
// ErrTooManyRows is what a result set larger than the caller allowed comes
// back as, so the handler can say which flag fixes it.
var ErrTooManyRows = errors.New("the result set is larger than the row bound")

// ErrTooLarge is the other half: within the row bound, over the byte one.
var ErrTooLarge = errors.New("the result set is larger than the size bound")

// maxRows bounds a result set rta wrote the SQL for and therefore already
// knows the size of — a ceiling against a catalogue nobody expected to be
// this big, not a limit anybody tunes.
const maxRows = 10000

// maxBytes bounds the same result set by size, because **a row bound is not
// a size bound**: `select body from documents` at two hundred rows is two
// hundred rows of whatever a text column holds, and a bytea column makes
// that arbitrary. The row bound alone was the whole protection here, and it
// counts the wrong thing.
//
// What makes it a correctness bound and not only a courtesy: a plugin's view
// crosses go-plugin's gRPC channel, and nothing configures
// MaxCallRecvMsgSize on either side, so grpc-go's 4 MiB default applies.
// Past it the caller does not get a large answer or a truncated one — the
// transport fails with ResourceExhausted, an error naming gRPC rather than
// the query that caused it, from a layer the operator has no flag for.
// Refusing here costs one comparison and says which flag fixes it.
//
// One mebibyte because that is the ceiling this codebase already applies
// twice for the same reason — builtin/http's response body and plugins/s3's
// inline object — and because the room between it and the transport's limit
// is where the host's own re-encoding lives.
const maxBytes = 1 << 20

// rowsToTable reads a result set into a table, bounded.
//
// **The bound is a parameter because forgetting it is the bug.** Every
// listing capability in this plugin declares a limit and applies it in SQL,
// and pg.query — the one capability whose SQL the *caller* writes — did not:
// `select * from users` streamed every row into a slice in the plugin, then
// through the host, then at a model's context. That is an unbounded
// allocation driven by an argument, which is a denial of service, and a bulk
// read of a table nobody consented to row by row, which is the more
// interesting half.
//
// One past the bound, so a full page and an overflowing one are told apart,
// and the overflow is **refused rather than truncated** — the same rule
// ai.ask's context bound follows, and for the same reason: a silently
// shortened answer is a different answer wearing the right shape. The caller
// gets to decide, by raising the bound or by writing a LIMIT.
func rowsToTable(rows pgx.Rows, bound int) (view.Table, error) {
	var t view.Table
	for _, fd := range rows.FieldDescriptions() {
		t.Columns = append(t.Columns, view.Column{Name: fd.Name})
	}
	if bound <= 0 {
		bound = maxRows
	}
	var size int
	for rows.Next() {
		if len(t.Rows) == bound {
			return t, ErrTooManyRows
		}
		vals, err := rows.Values()
		if err != nil {
			return t, err
		}
		row := make([]string, len(vals))
		for i, v := range vals {
			row[i] = cell(v)
			size += len(row[i])
		}
		// Checked after the row is built rather than before, so the refusal
		// happens on the row that crosses the line instead of one row early
		// on a guess about how big the next one will be.
		if size > maxBytes {
			return t, ErrTooLarge
		}
		t.Rows = append(t.Rows, row)
	}
	return t, rows.Err()
}

// cell renders one value as text.
//
// fmt.Sprint alone is wrong here and the failure is loud: pgx decodes
// `numeric` into a pgtype.Numeric struct, so `select total from orders`
// printed `{5 3 false finite true}` — the struct's fields — in the first
// version of this. Every pgtype scalar implements driver.Valuer and knows how
// to render itself, so ask it before falling back.
func cell(v any) string {
	if v == nil {
		// NULL is not the empty string, but a table cell has nowhere to say
		// so; empty at least reads as absent rather than as a value.
		return ""
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	if valuer, ok := v.(driver.Valuer); ok {
		if dv, err := valuer.Value(); err == nil && dv != nil {
			if b, ok := dv.([]byte); ok {
				return string(b)
			}
			return fmt.Sprint(dv)
		}
	}
	if t, ok := v.(time.Time); ok {
		return t.Format(time.RFC3339)
	}
	return fmt.Sprint(v)
}
