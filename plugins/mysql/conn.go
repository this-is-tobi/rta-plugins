package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	stdnet "net"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// connFields are the inputs every capability here shares.
//
// Every one is Local, and that is the security property rather than a detail.
// Together they name which server this call reaches and as whom, and an MCP
// caller may not choose that: an input a plugin declares is published in the
// tool schema and accepted from a caller, and caller values resolve last —
// above config, above the host's own environment — so an agent that could set
// `host` would point rta at a database of its own and have the host supply
// $RTA_MYSQL_PASSWORD beside it. Local closes that without changing anything
// for the two callers who should choose: config still fills these, and a
// person at a terminal still passes them as ordinary flags.
//
// password differs only in also declaring EnvFallback, and the distinction is
// the point: EnvFallback is for values that genuinely are credentials. A field
// that merely chooses a destination must come from an explicit caller or from
// config, never from an ambient variable the MCP server happened to inherit.
func connFields() []plugin.Field {
	return []plugin.Field{
		// host and port carry the endpoint roles, so a profile naming a
		// cluster reaches this database through a port-forward the host opens
		// and closes. This plugin never learns a forward was there, which is
		// why none of the code below changes to gain it.
		{Name: "host", Type: plugin.String, Default: "localhost", Config: "host",
			Local: true, Endpoint: plugin.EndpointHost, Help: "database host"},
		{Name: "port", Type: plugin.Int, Default: 3306, Config: "port",
			Local: true, Endpoint: plugin.EndpointPort, Min: 1, Max: 65535, Help: "database port"},
		{Name: "user", Type: plugin.String, Default: "root", Config: "user",
			Local: true, Help: "user to connect as"},
		// Empty by default rather than a guessed name. MySQL will connect with
		// no default database selected, and every capability here that needs
		// one qualifies its own tables — so the zero-config case reaches a
		// server and can still describe it, instead of failing on a database
		// name this plugin invented.
		{Name: "database", Type: plugin.String, Default: "", Config: "database",
			Local: true, Help: "database to select (optional — the server is reachable without one)"},
		// Local for a different reason than the four above: it does not change
		// where the call goes, it changes whether the transport is protected.
		// An agent that could set it could ask for `false` and downgrade a
		// connection the operator configured as verified.
		//
		// `preferred` is the default for the reason pg defaults to `prefer`:
		// it uses TLS when the server offers it and does not fail against the
		// local container somebody is trying this against first.
		//
		// verify-ca is pg's verify-ca and MySQL's own VERIFY_CA: the chain
		// checked against ca-file, and not the name. It is the one verified
		// way to the certificate a server generates for itself, which names no
		// host for true to check, so without it that server could be reached
		// only by skip-verify, which checks nothing at all. The go-sql-driver
		// has no word for it; tlsConfig builds it, and refuses it without a
		// ca-file to check against.
		{Name: "tls", Type: plugin.String, Default: "preferred", Config: "tls",
			Local:    true,
			Endpoint: plugin.EndpointTLS,
			Options:  []string{"false", "preferred", "true", "skip-verify", "verify-ca"},
			Help:     "TLS negotiation mode — verify-ca checks the chain against ca-file and not the name"},
		// The CA true and verify-ca verify against. Without it a server
		// whose certificate a private CA issued — an operator's own root, a
		// cluster's issuer — could be reached only by skip-verify, which
		// encrypts and checks nothing. For a certificate a server generated
		// for itself, the CA it was generated with, or the certificate when
		// it is self-signed, and verify-ca: such a certificate names no host
		// for true to verify it as, which classify's mysql.tls.name says.
		//
		// Local for the reason every ca-file in these plugins is: it names a
		// file on this machine that rta then reads, and a path a caller could
		// choose would be a file-read primitive. Not a Secret: a CA
		// certificate is the public half, the one handed out so anyone can
		// verify what it signed.
		//
		// **Read by true and verify-ca alone, and never a reason for tls to
		// change.** preferred and skip-verify negotiate TLS and verify
		// nothing, so a CA named beside either would read as a verified
		// connection and be none; tlsConfig refuses the pair rather than
		// elevate the mode on the operator's behalf, which would be a second,
		// unwritten way tls gets its value. false is left to stand: it
		// negotiates nothing a CA could verify, and it is what a tunnel forces.
		//
		// **Over a kube: or ssh: forward it is one of the two things that can
		// ask for TLS.** The host forces tls to false there (EndpointTLS) and
		// refuses a caller's own, so the elevation declined above is not the
		// operator's to decline: nothing else on the line says TLS. A CA, or a
		// name to check (tls-server-name), turns it on at true (tlsMode), as
		// plugins/pg's sslrootcert and tls-server-name do, and so this is not
		// TLSAdjacent: a profile may hold it beside its forward.
		{Name: "ca-file", Type: plugin.String, Default: "", Config: "ca-file",
			Local: true,
			Help: "PEM bundle to verify the server against — read when tls is true or verify-ca; under a " +
				"kube:/ssh: forward it turns TLS on, at true"},
		// The name the certificate is checked for when it is not the host
		// dialled — above all through a kube: or ssh: forward, whose end is
		// 127.0.0.1 whatever the server is called, and which its certificate
		// names only by luck. Checked as strictly as the host would have been:
		// it moves the check, never loosens it, so it is read at true alone
		// (checkServerName). Local for the reason tls is: what a certificate
		// has to prove is the operator's to say.
		//
		// Not TLSAdjacent, for ca-file's reason: over a forward it is what turns
		// TLS on.
		{Name: "tls-server-name", Type: plugin.String, Default: "", Config: "tls-server-name",
			Local: true,
			Help: "name to check the server's certificate for, in place of the host's — tls true only; under a " +
				"kube:/ssh: forward it turns TLS on, at true"},
		{Name: "password", Type: plugin.Secret, Local: true, EnvFallback: true,
			Help: "password for the user"},
	}
}

// connectTimeout bounds a connection's coming up, the handshake and the login
// with it: the pre-flight's ping here, and the restore child's
// --connect-timeout, in the seconds the client counts in.
const connectTimeout = 10 * time.Second

// driverConfig is go-sql-driver's configuration for the resolved inputs.
//
// Handed to the driver as a mysql.Config, through mysql.NewConnector, and
// never as a DSN string. The string was built through this same Config's
// FormatDSN, which escaped what hand assembly got wrong — a password
// containing '@' or '/' made a different DSN, and an authentication error
// that named nothing — but a CA has no spelling in a DSN at all: the driver
// takes one there only by the name of a tls.Config registered with
// RegisterTLSConfig, a registry global to the process. Passed as the Config's
// own TLS, it belongs to this call alone.
func driverConfig(req plugin.Request) (*mysql.Config, *view.Error) {
	c := mysql.NewConfig()
	c.Net = "tcp"
	c.Addr = address(req)
	c.User = req.String("user")
	c.Passwd = req.String("password")
	c.DBName = req.String("database")
	c.TLSConfig = tlsMode(req)
	// Nil, and the driver builds the tls.Config TLSConfig spells, unless
	// ca-file names a CA. Set, it outranks TLSConfig, and the driver still
	// takes the name to verify from the address, as it does for true. Always
	// set for verify-ca, a word the driver would refuse as a config name it
	// does not know: tlsConfig refuses verify-ca with no CA before this.
	tlsCfg, verr := tlsConfig(req)
	if verr != nil {
		return nil, verr
	}
	c.TLS = tlsCfg
	// Timestamps come back as time.Time rather than []byte, so a column of
	// them formats the same way everywhere instead of once per call site.
	c.ParseTime = true
	// Without this the driver reports a lost connection as a bare
	// "invalid connection" with nothing to classify. Named errors are what
	// classify below turns into something an operator can act on.
	c.CheckConnLiveness = true
	return c, nil
}

// address is host and port as one address to dial, an IPv6 literal
// bracketed. Joined with a colon, as it once was, ::1 became ::1:3306, which
// the driver took for a name to look up, and the refusal said there was no
// address for ::1 — a message that sent the reader to DNS for an address.
func address(req plugin.Request) string {
	return stdnet.JoinHostPort(req.String("host"), strconv.Itoa(req.Int("port")))
}

// connect opens a pool and proves it works before handing it back.
//
// sql.OpenDB never dials — it returns a lazy pool — so without the ping
// here, every capability would discover an unreachable server at its own
// first query and each would have to classify the same failure separately.
func connect(ctx context.Context, req plugin.Request) (*sql.DB, *view.Error) {
	cfg, verr := driverConfig(req)
	if verr != nil {
		return nil, verr
	}
	return open(ctx, req, cfg)
}

// open is connect for a configuration already built, for a caller that has
// something to add to it before it dials: a record of the certificate a
// verify-ca connection verified, for one.
func open(ctx context.Context, req plugin.Request, cfg *mysql.Config) (*sql.DB, *view.Error) {
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, view.Errorf("mysql.conn.invalid", "%v", err).
			WithHint(req.Surface().SettingsHint("mysql.overview"))
	}
	db := sql.OpenDB(connector)
	// One connection, because a capability here runs one query and exits. A
	// pool that outlives the call would hold a socket open against somebody
	// else's server for nothing.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, classify(err, req)
	}
	return db, nil
}

// tlsConfig is the TLS that verifies the server against ca-file's CA, or nil
// when ca-file names none, or tls negotiates nothing for one to verify.
//
// Refused before anything dials, and before a dump or a restore's dry run
// describes a child that would be refused the same way: a file that cannot
// be read or holds no certificate, a CA named beside a mode that would never
// read it, and verify-ca with none named.
//
// verify-ca with no ca-file is refused rather than checked against this
// machine's own store. Every public CA in it issues certificates to anyone
// for a name they control, so a chain that ends there and a name nobody
// checks accept all of them — a check that could pass for any server at all.
func tlsConfig(req plugin.Request) (*tls.Config, *view.Error) {
	path, sf, mode := caFile(req), req.Surface(), tlsMode(req)
	if verr := checkServerName(req); verr != nil {
		return nil, verr
	}
	if path == "" {
		if name := serverName(req); name != "" {
			return &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}, nil
		}
		if mode == "verify-ca" {
			return nil, view.Errorf("mysql.tls.ca.missing", "%s checks the server's chain against the CA %s names, "+
				"and it names none", sf.SettingTo("tls", mode), sf.SettingName("ca-file")).
				WithHint(sf.SettingName("ca-file") + " names it: the CA that issued the server's certificate, or the " +
					"certificate itself when it is self-signed. " + sf.SettingTo("tls", "true") +
					" checks against this machine's own CAs instead, the server's name included")
		}
		return nil, nil
	}
	switch mode {
	case "false":
		return nil, nil
	case "preferred", "skip-verify":
		return nil, view.Errorf("mysql.tls.ca.unused", "%s names a CA, and %s never verifies against one",
			sf.SettingName("ca-file"), sf.SettingTo("tls", mode)).
			WithHint(sf.SettingTo("tls", "true") + " verifies the server against it, its name included, and " +
				sf.SettingTo("tls", "verify-ca") + " its chain alone")
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, view.Errorf("mysql.tls.ca.unreadable", "%v", err).
			WithHint(sf.SettingName("ca-file") + " names a file on this machine, read by rta rather than " +
				"by the server, holding the CA's certificate in PEM")
	}
	// What the file has to hold, rather than a guess at what it held
	// instead: only a file with no PEM certificate in it reaches this, a
	// private key or a DER-encoded certificate most often. A self-signed
	// server's own certificate is exactly what belongs here — it is its own
	// CA, and the untrusted-certificate hint in classify sends the reader
	// here with it.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, view.Errorf("mysql.tls.ca.invalid", "%s holds no PEM certificate", path).
			WithHint(sf.SettingName("ca-file") + " wants a PEM certificate — the CA's, or a self-signed " +
				"server's own — and a private key or a DER-encoded certificate is not one")
	}
	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12, ServerName: serverName(req)}
	if mode == "verify-ca" {
		// Go's verifier checks the name whenever it verifies, so it is
		// turned off and the chain checked here instead, as pgx builds its
		// own verify-ca. Nothing else goes unchecked: a chain that does not
		// end at ca-file's CA fails the handshake, as an
		// x509.UnknownAuthorityError that classify names as untrusted.
		// VerifyConnection rather than VerifyPeerCertificate, because it runs
		// on a resumed session too, which the other would let through.
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = verifyChain(pool)
	}
	return cfg, nil
}

// verifyChain checks that the certificate a server presented chains to a CA
// in roots — through the intermediates it sent beside it — for serving TLS,
// and when, and nothing about the name it is for.
func verifyChain(roots *x509.CertPool) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		certs := cs.PeerCertificates
		if len(certs) == 0 {
			return errors.New("the server presented no certificate")
		}
		opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
		for _, cert := range certs[1:] {
			opts.Intermediates.AddCert(cert)
		}
		_, err := certs[0].Verify(opts)
		return err
	}
}

// caFile is ca-file with a leading ~ resolved and made absolute, or "" when
// it names nothing. One resolution for the three places the path goes — this
// process's read, the child's --ssl-ca, and the restore line a dump's receipt
// prints — so they cannot name different files, and a restore line pasted
// in another directory still names this one.
func caFile(req plugin.Request) string {
	ca := req.String("ca-file")
	if ca == "" {
		return ""
	}
	if abs, err := expandHome(ca); err == nil {
		return abs
	}
	return plugin.ExpandHome(ca)
}

// classify turns a driver error into something an operator can act on.
//
// Every branch here is a sentence somebody has stared at without knowing what
// to do next. The MySQL error numbers are the stable part of the protocol —
// the text beside them is localized and version-dependent, so switching on the
// number is the only version of this that keeps working.
func classify(err error, req plugin.Request) *view.Error {
	// An error that is already a view.Error has been classified by whatever
	// raised it, and re-wrapping would bury a specific answer under a generic
	// one.
	var already *view.Error
	if errors.As(err, &already) {
		return already
	}

	where := address(req)

	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		switch myErr.Number {
		case 1045: // ER_ACCESS_DENIED_ERROR
			// Where the password comes from rather than a verb telling the
			// reader to set one: the reads here answer an agent too, which
			// has no host environment to set and no password argument, since
			// the bridge drops a Local input given.
			return view.Errorf("mysql.auth.failed", "%s rejected user %q", req.Reached(where), req.String("user")).
				WithHint("the password is read from $" + plugin.LocalEnvVar("mysql.overview", "password") + " or " +
					req.Surface().SettingName("password") + " — check it, and " + req.Surface().SettingName("user"))
		case 1044: // ER_DBACCESS_DENIED_ERROR
			return view.Errorf("mysql.database.denied", "%q may not use database %q",
				req.String("user"), req.String("database")).
				WithHint("the credentials are valid but not granted on this database — check SHOW GRANTS")
		case 1049: // ER_BAD_DB_ERROR
			return view.Errorf("mysql.database.notfound", "%s has no database %q", req.Reached(where), req.String("database")).
				WithHint(nextCall(req, "mysql.database.list") + " shows what is there")
		case 1046: // ER_NO_DB_ERROR
			return noDatabase("no database is selected for this statement", req,
				"name the table as database.table in the statement")
		case 1792: // ER_CANT_EXECUTE_IN_READ_ONLY_TRANSACTION
			// Said as the design it is. This is the one refusal an agent is
			// sure to meet, because it will try a write, and the generic
			// answer below sent it to the operator to change a setting.
			return view.Errorf("mysql.query.readonly", "%s", myErr.Message).
				WithHint("every statement here runs in a READ ONLY transaction, so the server refuses " +
					"one that writes — by design, whatever the account may do")
		case 1146: // ER_NO_SUCH_TABLE
			return view.Errorf("mysql.table.notfound", "%s", myErr.Message).
				WithHint(nextCall(req, "mysql.table.list") + " shows what is there")
		case 1142, 1143: // ER_TABLEACCESS_DENIED_ERROR, ER_COLUMNACCESS_DENIED_ERROR
			return view.Errorf("mysql.denied", "%s", myErr.Message).
				WithHint("the credentials are valid but not authorized for this — check SHOW GRANTS")
		case 1130: // ER_HOST_NOT_PRIVILEGED
			return view.Errorf("mysql.host.denied", "%s will not accept connections from this machine", req.Reached(where)).
				WithHint("MySQL authorizes on user@host — the grant has to name where you are connecting from")
		case 1290: // ER_OPTION_PREVENTS_STATEMENT
			return view.Errorf("mysql.readonly", "%s", myErr.Message).
				WithHint("the server is running with read_only on; this is a replica or was set that way deliberately")
		case 3159: // ER_SECURE_TRANSPORT_REQUIRED
			// The server refusing a connection without TLS, which is not a
			// query that failed: nothing was queried, and the answer is a
			// setting, not the page of every input. false is what a tunnel
			// forces, so this is where a server started with
			// require_secure_transport meets one (tlsThroughForward).
			if req.Tunnel() != plugin.TunnelNone {
				return tlsThroughForward(req)
			}
			return view.Errorf("mysql.tls.required", "%s accepts connections over TLS only", req.Reached(where)).
				WithHint(req.Surface().SettingTo("tls", "true") + " connects over it, with " +
					req.Surface().SettingName("ca-file") + " naming the CA if the server's certificate is from one of its own")
		}
		return view.Errorf("mysql.query.failed", "%d: %s", myErr.Number, myErr.Message).
			WithHint(req.Surface().SettingsHint("mysql.overview"))
	}

	// true or skip-verify against a server that offers no TLS. The driver's
	// own sentence for it came out as "could not reach", which the server
	// was not: it answered, without the TLS that was asked for.
	if errors.Is(err, mysql.ErrNoTLS) {
		return view.Errorf("mysql.tls.unsupported", "%s does not offer TLS", req.Reached(where)).
			WithHint(req.Surface().SettingTo("tls", "false") + " if that is expected on this network")
	}

	// A certificate for another name than the one dialled, or for none.
	// "could not reach" misnamed it — the server answered — and ca-file
	// cannot cure it: Go checks the name before it builds a chain, so this
	// says nothing about the CA either way. What the reader needs is the
	// names the certificate does carry. The one MySQL generates for itself
	// carries none, only a CN, which no verifier reads, so true refuses it
	// whatever ca-file holds. Ahead of untrusted for that reason: its hint
	// sends the reader to ca-file, a detour that would only end here.
	//
	// **Never a certificate its issuer revoked (plugin.CertRevoked).** On
	// macOS, with no ca-file, the system's verifier answers, and says
	// "revoked" untyped: read by what it lacks, a revoked certificate with no
	// names was answered with verify-ca, which runs Go's verifier in the
	// system's place and checks no revocation at all — the operator who
	// followed the hint connected to the server the check had caught. It
	// keeps the system's words below, with no way round.
	//
	// A forward's refusal only for a certificate that has names: tls-server-name
	// can be set to one of them, and one with none is cured by reissuing it,
	// which nameRefusal says and plugin.ForwardNameRefusal's hint would not.
	if cert, ok := misnamed(err); ok && !plugin.CertRevoked(err) {
		var hostErr x509.HostnameError
		if req.Tunnel() != plugin.TunnelNone && serverName(req) == "" && errors.As(err, &hostErr) &&
			cert != nil && len(cert.DNSNames)+len(cert.IPAddresses) > 0 {
			return plugin.ForwardNameRefusal(req, "mysql.tls.forward", where, "server", hostErr)
		}
		return nameRefusal(req.Reached(where), cert, req)
	}

	// The CA named as where it belongs, and as what to use instead of
	// skip-verify: that is the mode a reader reaches for next, and it
	// connects by checking nothing. Only true and verify-ca verify, so only
	// they get here, and verify-ca never without a ca-file. Over MCP ca-file
	// is the operator's setting, Local, and an agent told to pass it has no
	// such argument to give.
	//
	// **Only a certificate plugin.CertUntrusted reads as an unknown issuer's,
	// never every one the handshake refused.** With no ca-file, macOS asks
	// its own verifier, which gives most of its verdicts untyped, and each
	// was read here as untrusted: a revoked certificate was answered with the
	// CA to name, and naming one replaces the system's checks, revocation
	// among them, with Go's verifier and that CA alone — the operator who
	// followed the hint reached the server the check had caught. A verdict
	// CertUntrusted does not read keeps the system's words, in
	// mysql.conn.failed, and the hint that sends somebody to name a CA says
	// what naming one costs (CAHint).
	if plugin.CertUntrusted(err) {
		refused := view.Errorf("mysql.tls.untrusted", "%s presented a certificate nothing here trusts", req.Reached(where))
		if ca := caFile(req); ca != "" {
			return refused.WithHint(ca + ", which " + req.Surface().SettingName("ca-file") + " names, does " +
				"not hold the CA that issued it — a self-signed certificate is its own CA")
		}
		return refused.WithHint("a server with a CA of its own wants that CA named rather than " +
			req.Surface().SettingTo("tls", "skip-verify") + ", which turns verification off — " +
			req.Surface().CAHint("ca-file"))
	}

	// Every other verdict on a certificate is its own reason, quoted in the
	// verifier's words — the system's, for one macOS gives untyped, a revoked
	// certificate among them — and never "could not reach": the server was
	// reached, and answered with a certificate. No setting is offered that
	// checks less, which is what skip-verify and a CA file replacing the
	// system's verifier would be.
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		rejected := view.Errorf("mysql.tls.rejected", "%s presented a certificate that does not verify: %v",
			req.Reached(where), verifyErr.Err)
		// A rule of macOS's own, which the verdict's words do not name: a
		// ten-year certificate, the usual one for a server of one's own, is
		// "not standards compliant" there.
		if hint := plugin.CertPolicyHint(err); hint != "" {
			return rejected.WithHint(hint)
		}
		return rejected.WithHint("a certificate is checked for its dates and the use it was issued for, as " +
			"well as for who issued it, and with " + req.Surface().SettingTo("tls", "true") + " for the host in " +
			req.Surface().SettingName("host") + " too")
	}

	// The name first: a dial that could not resolve its host fails with a
	// *net.OpError wrapping the *net.DNSError, and read the other way round
	// every name nothing resolves was reported as a port nothing listens on.
	var dnsErr *stdnet.DNSError
	if errors.As(err, &dnsErr) {
		return view.Errorf("mysql.host.unknown", "no address for %q", req.String("host")).
			WithHint(req.Surface().DNSHint(req.String("host")))
	}
	// A dial that found no way to the host, and one the host refused, by the
	// operating system's own error, as plugin.DialUnroutable and DialRefused
	// read it, and never by the *net.OpError around it, which every failed
	// dial is: read that way, a server behind a VPN that was down, a dial
	// that timed out and a handshake the server reset were each "nothing is
	// listening", about a port that may have been fine.
	var netErr *stdnet.OpError
	if plugin.DialUnroutable(err) {
		why := err
		if errors.As(err, &netErr) {
			why = netErr.Err
		}
		return view.Errorf("mysql.conn.unreachable", "%s cannot be reached from this machine: %v", where, why).
			WithHint("no route leads there from here — a VPN or tunnel the server sits behind that is down " +
				"looks exactly like this, and so does " + req.Surface().SettingName("host") + " naming an address " +
				"on a network this machine is not on")
	}
	if plugin.DialRefused(err) {
		return view.Errorf("mysql.conn.refused", "nothing is listening on %s", where).
			WithHint(reachHint(req.Surface()))
	}
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return view.Errorf("mysql.conn.timeout", "%s did not answer in time", where).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return view.Errorf("mysql.conn.timeout", "%s did not answer in time", where).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	return view.Errorf("mysql.conn.failed", "could not reach %s: %v", where, err).
		WithHint(req.Surface().SettingsHint("mysql.overview"))
}

// misnamed is the certificate err refused for its name — or refused for
// anything, the chain included, while naming no host at all, since true
// would refuse that one by name the moment its chain was trusted.
//
// The leaf comes from the handshake's *tls.CertificateVerificationError,
// which carries what the server presented whichever verifier ran: Go's own
// when ca-file is set, and on macOS the platform's otherwise.
func misnamed(err error) (*x509.Certificate, bool) {
	var nameErr x509.HostnameError
	if errors.As(err, &nameErr) {
		return nameErr.Certificate, true
	}
	var verifyErr *tls.CertificateVerificationError
	if !errors.As(err, &verifyErr) || len(verifyErr.UnverifiedCertificates) == 0 {
		return nil, false
	}
	leaf := verifyErr.UnverifiedCertificates[0]
	return leaf, len(leaf.DNSNames) == 0 && len(leaf.IPAddresses) == 0
}

// nameRefusal is mysql.tls.name for cert, presented at where for a host it
// does not name.
//
// The names are the subject alternative names, DNS and address, since those
// are all a verifier reads. Three at most (plugin.CertNames): a certificate for a fleet can
// carry dozens, and the reader needs to see that the one dialled is not
// among them, not the whole list.
func nameRefusal(where string, cert *x509.Certificate, req plugin.Request) *view.Error {
	host, sf := checkedName(req), req.Surface()
	if cert == nil || len(cert.DNSNames)+len(cert.IPAddresses) == 0 {
		refusal := view.Errorf("mysql.tls.name", "%s presented a certificate that names no host, %s or any other",
			where, host)
		if req.Tunnel() != plugin.TunnelNone {
			return refusal.WithHint("a certificate with no subject alternative names, as the one MySQL generates " +
				"for itself has, verifies as no host at all, and nothing a forward can be given checks a chain and " +
				"no name — it reaches the server once it is reissued with " + host + " among them")
		}
		return refusal.WithHint("a certificate with no subject alternative names, as the one MySQL generates for itself " +
			"is, verifies as no host at all — " + sf.SettingTo("tls", "verify-ca") + " checks it against the " +
			"CA in " + sf.SettingName("ca-file") + " without a name, and " + sf.SettingTo("tls", "true") +
			" reaches the server once its certificate is reissued with " + host + " among them")
	}
	return view.Errorf("mysql.tls.name", "%s presented a certificate for %s, not %s",
		where, plugin.CertNames(cert), host).
		WithHint(nameSetting(req) + " is the name the certificate is checked against — reach the " +
			"server by one it carries, or have it reissued with " + host + " among its subject alternative names")
}

// tlsThroughForward is mysql.tls.required for a server reached through
// the forward a kube: or ssh: profile opened.
//
// **Not tls true, the way on for a direct connection.** The forward turns tls
// off, and tls given by the caller is an input the forward fills, so the host
// opens no forward at all and the call goes to the host config or the default
// names. Measured through a kube: profile: the call the hint handed over was
// "nothing is listening on localhost:3306". What turns TLS on over a forward
// is a CA or a name to check (tlsMode), which a profile holds beside it, and
// the name is the one the certificate is for, since the forward ends at
// 127.0.0.1; without it, the refusal for a certificate for another name
// (plugin.ForwardNameRefusal) says so.
func tlsThroughForward(req plugin.Request) *view.Error {
	// What the hop off this machine runs inside, which is why the forward
	// turns TLS off in the first place (plugin.EndpointTLS).
	carrier := "the SSH connection it rides"
	if req.Tunnel() == plugin.TunnelKube {
		carrier = "the API server's TLS"
	}
	return view.Errorf("mysql.tls.required", "%s accepts connections over TLS only, and the forward "+
		"carries none", req.Reached(address(req))).
		WithHint("a forward runs the connection in the clear, the hop off this machine inside " + carrier +
			" — " + req.Surface().SettingName("ca-file", "tls-server-name") + " each turn TLS on over it, with " +
			"the name the certificate is for in " + req.Surface().SettingName("tls-server-name"))
}

// reachHint asks whether the server is up and its address right, naming the
// two connection inputs the address is made of the way the reader sets them.
func reachHint(sf plugin.Surface) string {
	return "is the server up, and are " + sf.SettingName("host", "port") + " right?"
}
