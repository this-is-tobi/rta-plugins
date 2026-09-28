package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	stdnet "net"
	"net/url"
	"os"
	"strconv"
	"strings"
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
		{Name: "tls", Type: plugin.String, Default: "preferred", Config: "tls",
			Local:    true,
			Endpoint: plugin.EndpointTLS,
			Options:  []string{"false", "preferred", "true", "skip-verify"},
			Help:     "TLS negotiation mode"},
		// The CA true verifies against. Without it a server whose
		// certificate a private CA issued — an operator's own root, a
		// cluster's issuer — could be reached only by skip-verify, which
		// encrypts and checks nothing. Not the way to the certificate a server
		// generates for itself, which names no host for true to verify it as;
		// classify's mysql.tls.name says so.
		//
		// Local for the reason every ca-file in these plugins is: it names a
		// file on this machine that rta then reads, and a path a caller could
		// choose would be a file-read primitive. Not a Secret: a CA
		// certificate is the public half, the one handed out so anyone can
		// verify what it signed.
		//
		// **Read by true alone, and never a reason for tls to change.**
		// preferred and skip-verify negotiate TLS and verify nothing, so a CA
		// named beside either would read as a verified connection and be
		// none; tlsConfig refuses the pair rather than elevate the mode on
		// the operator's behalf, which would be a second, unwritten way tls
		// gets its value. false is left to stand: it negotiates nothing a CA
		// could verify, and it is what a tunnel forces.
		//
		// TLSAdjacent for that last fact — see pg's sslrootcert, the other
		// input beside a mode a tunnel turns off. The host then refuses a
		// profile that sets this beside a forward instead of letting it sit
		// inert.
		{Name: "ca-file", Type: plugin.String, Default: "", Config: "ca-file",
			Local: true, TLSAdjacent: true,
			Help: "PEM bundle to verify the server against — read when tls is true, and " +
				"overridden along with tls under a kube:/ssh: tunnel"},
		{Name: "password", Type: plugin.Secret, Local: true, EnvFallback: true,
			Help: "password for the user"},
	}
}

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
	c.TLSConfig = req.String("tls")
	// Nil, and the driver builds the tls.Config TLSConfig spells, unless
	// ca-file names a CA. Set, it outranks TLSConfig, and the driver still
	// takes the name to verify from the address, as it does for true.
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
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, view.Errorf("mysql.conn.invalid", "%v", err).
			WithHint(explainHint(req.Surface(), "mysql.overview"))
	}
	db := sql.OpenDB(connector)
	// One connection, because a capability here runs one query and exits. A
	// pool that outlives the call would hold a socket open against somebody
	// else's server for nothing.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
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
// be read or holds no certificate, and a CA named beside a mode that would
// never read it.
func tlsConfig(req plugin.Request) (*tls.Config, *view.Error) {
	path := caFile(req)
	if path == "" {
		return nil, nil
	}
	sf := req.Surface()
	switch mode := req.String("tls"); mode {
	case "false":
		return nil, nil
	case "preferred", "skip-verify":
		return nil, view.Errorf("mysql.tls.ca.unused", "%s names a CA, and %s never verifies against one",
			setting(sf, "ca-file"), settingTo(sf, "tls", mode)).
			WithHint(settingTo(sf, "tls", "true") + " verifies the server against it")
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, view.Errorf("mysql.tls.ca.unreadable", "%v", err).
			WithHint(setting(sf, "ca-file") + " names a file on this machine, read by rta rather than " +
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
			WithHint(setting(sf, "ca-file") + " wants a PEM certificate — the CA's, or a self-signed " +
				"server's own — and a private key or a DER-encoded certificate is not one")
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
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
			return view.Errorf("mysql.auth.failed", "%s rejected user %q", where, req.String("user")).
				WithHint("the password is read from $" + plugin.LocalEnvVar("mysql.overview", "password") + " or " +
					setting(req.Surface(), "password") + " — check it, and " + setting(req.Surface(), "user"))
		case 1044: // ER_DBACCESS_DENIED_ERROR
			return view.Errorf("mysql.database.denied", "%q may not use database %q",
				req.String("user"), req.String("database")).
				WithHint("the credentials are valid but not granted on this database — check SHOW GRANTS")
		case 1049: // ER_BAD_DB_ERROR
			return view.Errorf("mysql.database.notfound", "%s has no database %q", where, req.String("database")).
				WithHint(req.Surface().CapabilityName("mysql.database.list") + " shows what is there")
		case 1146: // ER_NO_SUCH_TABLE
			return view.Errorf("mysql.table.notfound", "%s", myErr.Message).
				WithHint(req.Surface().CapabilityName("mysql.table.list") + " shows what is there")
		case 1142, 1143: // ER_TABLEACCESS_DENIED_ERROR, ER_COLUMNACCESS_DENIED_ERROR
			return view.Errorf("mysql.denied", "%s", myErr.Message).
				WithHint("the credentials are valid but not authorized for this — check SHOW GRANTS")
		case 1130: // ER_HOST_NOT_PRIVILEGED
			return view.Errorf("mysql.host.denied", "%s will not accept connections from this machine", where).
				WithHint("MySQL authorizes on user@host — the grant has to name where you are connecting from")
		case 1290: // ER_OPTION_PREVENTS_STATEMENT
			return view.Errorf("mysql.readonly", "%s", myErr.Message).
				WithHint("the server is running with read_only on; this is a replica or was set that way deliberately")
		case 3159: // ER_SECURE_TRANSPORT_REQUIRED
			// The server refusing a connection without TLS, which is not a
			// query that failed: nothing was queried, and the answer is a
			// setting, not the page of every input. false is what a tunnel
			// forces, so this is where a server started with
			// require_secure_transport meets one.
			return view.Errorf("mysql.tls.required", "%s accepts connections over TLS only", where).
				WithHint(settingTo(req.Surface(), "tls", "true") + " connects over it, with " +
					setting(req.Surface(), "ca-file") + " naming the CA if the server's certificate is from one of its own")
		}
		return view.Errorf("mysql.query.failed", "%d: %s", myErr.Number, myErr.Message).
			WithHint(explainHint(req.Surface(), "mysql.overview"))
	}

	// true or skip-verify against a server that offers no TLS. The driver's
	// own sentence for it came out as "could not reach", which the server
	// was not: it answered, without the TLS that was asked for.
	if errors.Is(err, mysql.ErrNoTLS) {
		return view.Errorf("mysql.tls.unsupported", "%s does not offer TLS", where).
			WithHint(settingTo(req.Surface(), "tls", "false") + " if that is expected on this network")
	}

	// A certificate for another name than the one dialled, or for none.
	// "could not reach" misnamed it — the server answered — and ca-file
	// cannot cure it: Go checks the name before it builds a chain, so this
	// says nothing about the CA either way. What the reader needs is the
	// names the certificate does carry. The one MySQL generates for itself
	// carries none, only a CN, which no verifier reads, so true refuses it
	// whatever ca-file holds. Ahead of untrusted for that reason: its hint
	// sends the reader to ca-file, a detour that would only end here.
	if cert, ok := misnamed(err); ok {
		return nameRefusal(where, cert, req)
	}

	// The CA named as where it belongs, and as what to use instead of
	// skip-verify: that is the mode a reader reaches for next, and it
	// connects by checking nothing. Only true verifies, so only true gets
	// here. Over MCP ca-file is the operator's setting, Local, and an agent
	// told to pass it has no such argument to give.
	if untrusted(err) {
		refused := view.Errorf("mysql.tls.untrusted", "%s presented a certificate nothing here trusts", where)
		if ca := caFile(req); ca != "" {
			return refused.WithHint(ca + ", which " + setting(req.Surface(), "ca-file") + " names, does " +
				"not hold the CA that issued it — a self-signed certificate is its own CA")
		}
		return refused.WithHint("a server with a CA of its own wants that CA in " +
			setting(req.Surface(), "ca-file") + " rather than " + settingTo(req.Surface(), "tls", "skip-verify") +
			", which turns verification off — a self-signed certificate is its own CA")
	}

	// The name first: a dial that could not resolve its host fails with a
	// *net.OpError wrapping the *net.DNSError, and read the other way round
	// every name nothing resolves was reported as a port nothing listens on.
	var dnsErr *stdnet.DNSError
	if errors.As(err, &dnsErr) {
		return view.Errorf("mysql.host.unknown", "no address for %q", req.String("host")).
			WithHint(dnsHint(req.Surface(), req.String("host")))
	}
	var netErr *stdnet.OpError
	if errors.As(err, &netErr) || strings.Contains(err.Error(), "connection refused") {
		return view.Errorf("mysql.conn.refused", "nothing is listening on %s", where).
			WithHint(reachHint(req.Surface()))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return view.Errorf("mysql.conn.timeout", "%s did not answer in time", where).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return view.Errorf("mysql.conn.timeout", "%s did not answer in time", where).
			WithHint("a firewall that drops rather than refuses looks exactly like this")
	}
	return view.Errorf("mysql.conn.failed", "could not reach %s: %v", where, err).
		WithHint(explainHint(req.Surface(), "mysql.overview"))
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
// are all a verifier reads. Four at most: a certificate for a fleet can
// carry dozens, and the reader needs to see that the one dialled is not
// among them, not the whole list.
func nameRefusal(where string, cert *x509.Certificate, req plugin.Request) *view.Error {
	host, sf := req.String("host"), req.Surface()
	var names []string
	if cert != nil {
		names = append(names, cert.DNSNames...)
		for _, ip := range cert.IPAddresses {
			names = append(names, ip.String())
		}
	}
	if len(names) == 0 {
		return view.Errorf("mysql.tls.name", "%s presented a certificate that names no host, %s or any other",
			where, host).
			WithHint("a certificate with no subject alternative names, as the one MySQL generates for itself " +
				"is, verifies as no host at all — " + settingTo(sf, "tls", "true") + " reaches the server once " +
				"its certificate is reissued with " + host + " among them")
	}
	if len(names) > 4 {
		names = append(names[:4:4], fmt.Sprintf("%d more", len(names)-4))
	}
	return view.Errorf("mysql.tls.name", "%s presented a certificate for %s, not %s",
		where, strings.Join(names, ", "), host).
		WithHint(setting(sf, "host") + " is the name the certificate is checked against — reach the " +
			"server by one it carries, or have it reissued with " + host + " among its subject alternative names")
}

// untrusted reports whether err is a certificate that nothing here vouches
// for. Go's own verifier says so as an x509.UnknownAuthorityError, and it is
// the one that runs whenever ca-file is set. Without one, on macOS, the
// system's trust store is consulted through the platform's verifier, and an
// untrusted chain comes back from it as a bare error inside the handshake's
// *tls.CertificateVerificationError — as does a self-signed certificate
// valid for longer than Apple's policy allows — so a verification failure
// the platform answers untyped is read as one too.
//
// Untyped, and not merely not UnknownAuthorityError: Go's verifier types
// every failure it names — a host the certificate is not for, a date or a
// use it is not valid for, a signature algorithm it will not accept, a
// critical extension it does not handle — and each of those is a reason of
// its own, which no CA in ca-file would cure: the host is mysql.tls.name,
// and mysql.conn.failed quotes the rest.
// The same reading as pg's, etcd's and keycloak's.
func untrusted(err error) bool {
	var authErr x509.UnknownAuthorityError
	if errors.As(err, &authErr) {
		return true
	}
	var verifyErr *tls.CertificateVerificationError
	if !errors.As(err, &verifyErr) {
		return false
	}
	for _, reason := range []any{new(x509.HostnameError), new(x509.CertificateInvalidError),
		new(x509.InsecureAlgorithmError), new(x509.UnhandledCriticalExtension),
		new(x509.ConstraintViolationError)} {
		if errors.As(verifyErr.Err, reason) {
			return false
		}
	}
	return true
}

// setting names connection input name in a message the way its reader
// changes it: the flag on the CLI, the box in a TUI form. Not the argument
// over MCP, as plugin.Surface.InputName would: every connection input is
// Local, so the tool's schema hides it and the bridge drops one given, and an
// agent told to check the "user" argument would pass one that is thrown
// away and read the same refusal again. It is named there as the declaration
// names it, `user` — a setting of the operator's, which the agent can
// report and cannot change.
func setting(sf plugin.Surface, name string) string {
	if sf == plugin.SurfaceMCP {
		return "`" + name + "`"
	}
	return sf.InputName(name)
}

// settingTo is setting with the value to give it: "--tls true" on the CLI,
// as a command line takes it, and elsewhere the setting with the value
// beside it.
func settingTo(sf plugin.Surface, name, value string) string {
	if sf == plugin.SurfaceMCP || sf == plugin.SurfaceTUI {
		return setting(sf, name) + " set to " + value
	}
	return sf.InputName(name) + " " + value
}

// explainHint sends the reader to the page listing every input and where each
// one can come from. That page is `rta explain`, a terminal's command with no
// capability behind it, and what it answers here is where the connection
// inputs come from — the operator's to set — so over MCP it is the operator
// who is asked to read it.
func explainHint(sf plugin.Surface, id string) string {
	if sf == plugin.SurfaceMCP {
		return plugin.AskOperator("explain "+id) + ", which lists every input and where each one can come from"
	}
	return "`rta explain " + id + "` lists every input and where each one can come from"
}

// dnsHint is the call that shows what DNS returns for host, spelled for the
// surface that will make it.
func dnsHint(sf plugin.Surface, host string) string {
	return "`" + sf.Call("net.dns", plugin.Arg{Name: "name", Value: host, Positional: true}) + "` shows what DNS returns"
}

// reachHint asks whether the server is up and its address right, naming the
// two connection inputs the address is made of the way the reader sets them.
func reachHint(sf plugin.Surface) string {
	if sf == plugin.SurfaceMCP || sf == plugin.SurfaceTUI {
		return "is the server up, and are " + setting(sf, "host") + " and " + setting(sf, "port") + " right?"
	}
	return "is the server up, and is " + sf.InputName("host") + "/" + sf.InputName("port") + " right?"
}

// given names input name set to value, as the reader would give it: "--out
// ./shop.sql" on the CLI, and elsewhere the input the surface names, with the
// value beside it.
func given(sf plugin.Surface, name, value string) string {
	if sf == plugin.SurfaceMCP || sf == plugin.SurfaceTUI {
		return sf.InputName(name) + " set to " + value
	}
	return sf.InputName(name) + " " + value
}
