package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/view"
)

// sdktest is the definition of "a correct plugin" and pg gets no exemption
// from it.
//
// The declaration half needs no database. The dry-run half needs inputs, and
// without them two of pg's four mutating capabilities were never driven at
// all — the suite reported a pass for capabilities it had not run.
func TestConformance(t *testing.T) {
	sdktest.Check(t, Plugin(), sdktest.WithInputs(conformanceInputs), sdktest.WithSource("."))
}

// conformanceInputs points the mutating capabilities at a port nothing is
// listening on, so a dry run that stops being dry fails as a refused
// connection rather than as a query against somebody's database.
func conformanceInputs(dir string) map[string]map[string]any {
	conn := func(m map[string]any) map[string]any {
		m["host"], m["port"] = "127.0.0.1", 1
		m["user"], m["database"] = "conformance", "conformance"
		return m
	}
	// A real plain-SQL fixture, because pg.restore reads the format off the
	// bytes before its dry run says anything — the supported shape per
	// sdktest.WithInputs' own doc. The suite's snapshot is taken after this
	// runs, so a dry run that touched the fixture would still be caught.
	fixture := filepath.Join(dir, "conformance.sql")
	_ = os.WriteFile(fixture, []byte("select 1;\n"), 0o600)
	return map[string]map[string]any{
		"pg.query":      conn(map[string]any{"sql": "select 1"}),
		"pg.table.dump": conn(map[string]any{"table": "public.conformance"}),
		"pg.dump":       conn(map[string]any{"out": filepath.Join(dir, "pg.dump")}),
		"pg.restore":    conn(map[string]any{"file": fixture}),
	}
}

// req builds a resolved request the way the host would, so these test the
// values a handler actually sees rather than a hand-made map.
func req(t *testing.T, values map[string]any) plugin.Request {
	t.Helper()
	c := Plugin().Capabilities[0]
	return plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false)
}

// A password containing '@' or '/' produces a different connection string
// under URL parsing, and the failure is an authentication error naming
// nothing. Key=value with quoting is why this is not built as a URL.
func TestDSNSurvivesAwkwardPasswords(t *testing.T) {
	for _, pw := range []string{"p@ss/word", "with 'quote'", `back\slash`, "sp ace"} {
		got := dsn(req(t, map[string]any{"password": pw, "host": "db.internal"}))
		if !strings.Contains(got, "password=") {
			t.Errorf("password missing for %q: %s", pw, got)
		}
		// The host must still parse as its own field: an unescaped quote
		// would run the password into the next key.
		if !strings.Contains(got, "host='db.internal'") {
			t.Errorf("password %q corrupted the rest of the DSN: %s", pw, got)
		}
	}
}

// An empty password is absent rather than empty: libpq treats `password=”`
// as "authenticate with the empty string", which fails differently from
// "no password offered" on a trust-configured server.
func TestAnEmptyPasswordIsOmitted(t *testing.T) {
	if got := dsn(req(t, map[string]any{})); strings.Contains(got, "password=") {
		t.Errorf("an unset password was sent as empty: %s", got)
	}
}

// sslrootcert rides the DSN exactly where libpq expects to find it — pgx's
// own ParseConfig reads this key directly, which is the whole reason it is
// spelled this and not something rta invented.
func TestDSNCarriesSSLRootCert(t *testing.T) {
	got := dsn(req(t, map[string]any{"sslrootcert": "/etc/rta/pg-ca.crt"}))
	if !strings.Contains(got, "sslrootcert='/etc/rta/pg-ca.crt'") {
		t.Errorf("sslrootcert missing or unquoted: %s", got)
	}
}

// pgx's own defaults fill passfile with $HOME/.pgpass and load a password
// from it whenever the connection string names none — so omitting the key
// does not mean "no passfile", it means "the operator's own". An operator who
// configured a host and a user and deliberately no password was being
// authenticated with whatever credential their interactive psql keeps for
// that host, and the auth-failure hint told them this could not happen.
//
// Empty rather than absent, and always rather than only when the password is
// blank: an empty passfile fails the open, which is what makes pgconn skip
// the lookup.
func TestTheDSNAlwaysRefusesTheAmbientPassfile(t *testing.T) {
	for _, password := range []string{"", "hunter2"} {
		got := dsn(req(t, map[string]any{
			"host": "db.internal", "port": 5432, "user": "postgres",
			"database": "postgres", "sslmode": "prefer", "password": password,
		}))
		if !strings.Contains(got, "passfile=''") {
			t.Errorf("password=%q: dsn does not refuse the ambient passfile: %s", password, got)
		}
	}
}

// The connect is bounded: without connect_timeout pgconn waits on the
// operating system's own, more than a minute and twice under prefer.
func TestTheDSNBoundsTheConnect(t *testing.T) {
	if got := dsn(req(t, map[string]any{})); !strings.Contains(got, "connect_timeout=10") {
		t.Errorf("the connection string carries no connect bound: %s", got)
	}
}

// A dial that timed out or found no route reached nothing that could refuse
// it, and is not a port nobody is on; one the host refused still is.
func TestAHostNoRouteReachesIsNotAPortNobodyIsOn(t *testing.T) {
	r := req(t, map[string]any{"host": "10.0.0.9", "port": 5432})
	for errno, want := range map[syscall.Errno]string{
		syscall.ENETUNREACH:  "pg.conn.unreachable",
		syscall.EHOSTUNREACH: "pg.conn.unreachable",
		syscall.EHOSTDOWN:    "pg.conn.unreachable",
		syscall.ETIMEDOUT:    "pg.conn.timeout",
		syscall.ECONNREFUSED: "pg.conn.refused",
	} {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
		got := classify(err, r)
		if got.Code != want || !strings.Contains(got.Message, "10.0.0.9:5432") {
			t.Errorf("%v: %s %q, want %s naming the server", errno, got.Code, got.Message, want)
		}
		if want == "pg.conn.unreachable" && !strings.Contains(got.Message, errno.Error()) {
			t.Errorf("%v: %q does not say why", errno, got.Message)
		}
	}
}

// A failed dial is read by the operating system's own error, never by the
// *net.OpError around it, which every broken socket call is: a server that
// reset the handshake was answered "nothing is listening". Text a driver
// flattened is read by the words the error had, and a name nothing resolved
// stays that, whatever the resolver's own failed exchange with its server said.
func TestADialIsReadByItsOwnErrorNotByTheWrapper(t *testing.T) {
	r := req(t, map[string]any{"host": "db.internal", "port": 5432})
	dial := func(errno syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
	}
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"a handshake the server reset", &net.OpError{Op: "read", Net: "tcp",
			Err: os.NewSyscallError("read", syscall.ECONNRESET)}, "pg.conn.failed"},
		{"a refusal flattened to text", errors.New("dial tcp 10.0.0.9:5432: connect: connection refused"),
			"pg.conn.refused"},
		{"no route flattened to text", errors.New("dial tcp 10.0.0.9:5432: connect: no route to host"),
			"pg.conn.unreachable"},
		{"one address with no route and one refused", errors.Join(dial(syscall.EHOSTUNREACH),
			dial(syscall.ECONNREFUSED)), "pg.conn.refused"},
		{"a name whose DNS server refused the resolver", &net.OpError{Op: "dial", Net: "tcp",
			Err: &net.DNSError{Err: "dial udp 10.0.0.53:53: connect: connection refused", Name: "db.internal"}},
			"pg.host.unknown"},
	} {
		if got := classify(tc.err, r); got.Code != tc.code {
			t.Errorf("%s: %s %q, want %s", tc.name, got.Code, got.Message, tc.code)
		}
	}
}

// A CA beside a mode that does not verify by its own name is refused, never
// applied: prefer never reads it in pgx, and in libpq it falls back to
// plaintext when the certificate does not verify; require verifies only for
// as long as the file is named. disable stands, since a tunnel forces it, and
// the two verify modes are what read it.
func TestSSLRootCertIsRefusedBesideAModeThatDoesNotVerify(t *testing.T) {
	ca := testCA(t)
	for mode, want := range map[string]string{
		"prefer": "pg.tls.ca.unused", "require": "pg.tls.ca.implied",
		"disable": "", "verify-ca": "", "verify-full": "",
	} {
		got := checkRootCert(req(t, map[string]any{"sslmode": mode, "sslrootcert": ca}))
		switch {
		case want == "" && got != nil:
			t.Errorf("sslmode %s: refused a CA it reads or never negotiates for: %s", mode, got.Code)
		case want != "" && (got == nil || got.Code != want):
			t.Errorf("sslmode %s: %v, want %s", mode, got, want)
		case want != "" && !strings.Contains(got.Hint, "--sslmode verify-"):
			t.Errorf("sslmode %s: the hint %q names no mode that verifies against it", mode, got.Hint)
		}
		if got := checkRootCert(req(t, map[string]any{"sslmode": mode})); got != nil && mode != "verify-ca" {
			t.Errorf("sslmode %s with no CA: refused as %s", mode, got.Code)
		}
	}
}

// An IPv6 host is bracketed wherever a message names the server by address:
// joined with a bare colon, ::1 read ::1:5432, a port no reader could tell
// from the address.
func TestAnIPv6HostIsNamedBracketed(t *testing.T) {
	values := map[string]any{"host": "::1", "port": 5432, "database": "app"}
	said := map[string]string{
		"a refused connection": classify(&net.OpError{Op: "dial", Net: "tcp",
			Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, req(t, values)).Message,
		"a schema dump's header": renderDDL(reqFor(t, "pg.schema.dump", values), "public", nil, dropped{}),
	}
	for what, text := range said {
		if !strings.Contains(text, "[::1]:5432") || strings.Contains(text, "::1:5432") {
			t.Errorf("%s: %q, want the server named [::1]:5432", what, text)
		}
	}
}

// testCA writes a self-signed CA certificate in PEM and returns its path.
func testCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rta test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A CA file the verify modes cannot use is refused as that, before anything
// dials: left to pgx, it came back as a connection that failed, quoting the
// whole connection string, from a failure that never reached the network.
// Beside disable it is never read, as a tunnel forces disable.
func TestACAFileThatCannotBeUsedIsNamedAsThat(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "server.key")
	if err := os.WriteFile(key, []byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(dir, "absent.pem"): "pg.tls.ca.unreadable",
		key:                              "pg.tls.ca.invalid",
	} {
		values := map[string]any{"host": "127.0.0.1", "port": 1, "sslmode": "verify-full", "sslrootcert": path}
		if _, got := connect(context.Background(), req(t, values)); got == nil || got.Code != want ||
			!strings.Contains(got.Hint, "--sslrootcert") {
			t.Errorf("%s: %v, want %s naming --sslrootcert", filepath.Base(path), got, want)
		}
		values["sslmode"] = "disable"
		if got := checkRootCert(req(t, values)); got != nil {
			t.Errorf("%s beside disable: refused as %s, though nothing reads it", filepath.Base(path), got.Code)
		}
	}
}

// system is this machine's whole trust store, taken beside verify-full alone:
// libpq refuses it beside anything weaker, and pgx turns the weaker mode into
// verify-full unasked. disable stands, as beside a file, since a tunnel
// forces it; TestDisableLeavesTheCAOutOfTheConnection covers what follows.
func TestTheSystemStoreIsTakenBesideVerifyFullAlone(t *testing.T) {
	for _, mode := range []string{"prefer", "require", "verify-ca"} {
		got := checkRootCert(req(t, map[string]any{"sslmode": mode, "sslrootcert": "system"}))
		if got == nil || got.Code != "pg.tls.ca.system" || !strings.Contains(got.Message, "--sslmode verify-full") {
			t.Errorf("sslmode %s beside the system store: %v, want pg.tls.ca.system naming verify-full", mode, got)
		}
	}
	if got := checkRootCert(req(t, map[string]any{"sslmode": "verify-full", "sslrootcert": "system"})); got != nil {
		t.Errorf("verify-full beside the system store: refused as %s", got.Code)
	}
	got := classify(x509.UnknownAuthorityError{},
		req(t, map[string]any{"sslmode": "verify-full", "sslrootcert": "system"}))
	if !strings.Contains(got.Hint, "no CA in this machine's trust store") {
		t.Errorf("an untrusted certificate beside the system store: %q, want it named as the store", got.Hint)
	}
}

// disable reads no CA, and is what a tunnel forces beside whatever the config
// names for direct connections, so the CA stays out of the connection: pgx
// reads a named file before it looks at the mode, and turns disable into
// verify-full beside system, and libpq refuses system beside disable.
func TestDisableLeavesTheCAOutOfTheConnection(t *testing.T) {
	for _, ca := range []string{"system", "/nonexistent/ca.pem"} {
		values := map[string]any{"sslmode": "disable", "sslrootcert": ca}
		if got := checkRootCert(req(t, values)); got != nil {
			t.Errorf("%s beside disable: refused as %s", ca, got.Code)
		}
		if got := dsn(req(t, values)); strings.Contains(got, "sslrootcert") {
			t.Errorf("%s beside disable reached the driver: %s", ca, got)
		}
		if env := childEnv(reqFor(t, "pg.dump", values)); slices.ContainsFunc(env, func(kv string) bool {
			return strings.HasPrefix(kv, "PGSSLROOTCERT=")
		}) {
			t.Errorf("%s beside disable reached the child: %v", ca, env)
		}
	}
}

// verify-ca with no file named is refused, before anything dials and before a
// dry run: pgx would check the chain against this machine's own store and no
// name, which any certificate a public CA issued passes. verify-full with no
// file keeps the store, since it checks the name as well.
func TestVerifyCAWithoutASSLRootCertIsRefused(t *testing.T) {
	values := func(extra map[string]any) map[string]any {
		extra["host"], extra["port"], extra["sslmode"] = "127.0.0.1", 1, "verify-ca"
		return extra
	}
	_, verr := connect(context.Background(), req(t, values(map[string]any{})))
	if verr == nil || verr.Code != "pg.tls.ca.missing" || !strings.Contains(verr.Hint, "--sslrootcert names it") {
		t.Fatalf("connect: %v, want pg.tls.ca.missing naming --sslrootcert", verr)
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "app.sql")
	if err := os.WriteFile(fixture, []byte("select 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for id, run := range map[string]func(context.Context, plugin.Request) (view.View, error){
		"pg.dump":    runFullDump,
		"pg.restore": runRestore,
	} {
		extra := map[string]any{"out": filepath.Join(dir, "out.sql")}
		if id == "pg.restore" {
			extra = map[string]any{"file": fixture}
		}
		_, err := run(context.Background(), dryRunReqFor(t, id, values(extra)))
		if !errors.As(err, &verr) || verr.Code != "pg.tls.ca.missing" {
			t.Errorf("%s dry run: %v, want pg.tls.ca.missing", id, err)
		}
	}
	if got := checkRootCert(req(t, map[string]any{"sslmode": "verify-full"})); got != nil {
		t.Errorf("verify-full with no file: refused as %s, though it checks the name", got.Code)
	}
}

// Refused before anything dials, and before a dry run describes a child that
// would carry the pair. Nothing listens on port 1, so a refusal that came from
// a dial would name the port rather than the CA.
func TestTheUnverifiedCAIsRefusedBeforeAnythingRuns(t *testing.T) {
	values := func(extra map[string]any) map[string]any {
		extra["host"], extra["port"] = "127.0.0.1", 1
		extra["sslmode"], extra["sslrootcert"] = "prefer", "/etc/rta/pg-ca.crt"
		return extra
	}
	if _, verr := connect(context.Background(), req(t, values(map[string]any{}))); verr == nil ||
		verr.Code != "pg.tls.ca.unused" {
		t.Errorf("connect: %v, want pg.tls.ca.unused", verr)
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "app.sql")
	if err := os.WriteFile(fixture, []byte("select 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for id, extra := range map[string]map[string]any{
		"pg.dump":    {"out": filepath.Join(dir, "out.sql")},
		"pg.restore": {"file": fixture},
	} {
		run := runFullDump
		if id == "pg.restore" {
			run = runRestore
		}
		_, err := run(context.Background(), dryRunReqFor(t, id, values(extra)))
		var verr *view.Error
		if !errors.As(err, &verr) || verr.Code != "pg.tls.ca.unused" {
			t.Errorf("%s dry run: %v, want pg.tls.ca.unused", id, err)
		}
	}
}

// With the CA named, the certificate nothing here trusts is one that CA did
// not issue, and the hint says so of the file rather than asking for it.
func TestAnUntrustedCertificateNamesTheCAThatDidNotIssueIt(t *testing.T) {
	got := classify(x509.UnknownAuthorityError{},
		req(t, map[string]any{"sslmode": "verify-full", "sslrootcert": "/etc/rta/pg-ca.crt"}))
	if got.Code != "pg.tls.untrusted" || !strings.Contains(got.Hint, "/etc/rta/pg-ca.crt, which --sslrootcert names") {
		t.Errorf("%s: %q, want the hint to name the CA file that did not vouch for it", got.Code, got.Hint)
	}
}

// sslrootcert is resolved as every other path this plugin reads, a leading ~
// to the home directory and the rest made absolute, and the one resolution
// goes wherever the connection does: the driver's connection string, the
// child's PGSSLROOTCERT, and the restore line a dump's receipt prints. system
// is libpq's word for the trust store, and stays that word.
func TestSSLRootCertIsResolvedWhereverItGoes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, "ca.pem")
	values := map[string]any{"host": "db.internal", "sslmode": "verify-full", "sslrootcert": "~/ca.pem"}
	if got := dsn(req(t, values)); !strings.Contains(got, "sslrootcert='"+want+"'") {
		t.Errorf("dsn = %s, want sslrootcert='%s'", got, want)
	}
	if env := childEnv(reqFor(t, "pg.dump", values)); !slices.Contains(env, "PGSSLROOTCERT="+want) {
		t.Errorf("the child's env = %v, want PGSSLROOTCERT=%s", env, want)
	}
	if line := restoreCommand(reqFor(t, "pg.dump", values), "/backups/app.dump"); !strings.Contains(line,
		"--sslrootcert "+want) {
		t.Errorf("restore line = %q, want --sslrootcert %s", line, want)
	}
	values["sslrootcert"] = "system"
	if got := dsn(req(t, values)); !strings.Contains(got, "sslrootcert='system'") {
		t.Errorf("dsn = %s, want the trust store named as libpq names it", got)
	}
}

// Unset, sslrootcert is left out of the connection string rather than sent
// empty, because its two readers disagree about empty. pgx takes it as no CA
// at all, overriding the ~/.postgresql/root.crt it otherwise defaults to;
// libpq, which pg_dump, psql and pg_restore run on, takes it as unset and
// reads that same file. Left out, both read the file when it is there, and
// the connection checked before a dump trusts what the dump's child does.
func TestAnEmptySSLRootCertIsOmitted(t *testing.T) {
	if got := dsn(req(t, map[string]any{})); strings.Contains(got, "sslrootcert=") {
		t.Errorf("an unset sslrootcert was sent as empty: %s", got)
	}
}

// A pg_hba.conf that takes a connection only over TLS answers 28000, as a
// rejected password does, and the reader was sent to check a password
// nothing had checked. It is named as what it is; through a forward, whose
// disable is every call's, the forward is named as what carries no TLS, and
// no sslmode is offered, since given by the caller one opens no forward.
func TestAConnectionTakenOnlyOverTLSIsNotAPasswordRefused(t *testing.T) {
	hba := &pgconn.PgError{Code: "28000",
		Message: `no pg_hba.conf entry for host "172.17.0.1", user "app", database "app", no encryption`}
	values := map[string]any{"host": "127.0.0.1", "port": 54321, "sslmode": "disable"}

	direct := classify(hba, req(t, values))
	if direct.Code != "pg.tls.required" || !strings.Contains(direct.Hint, "--sslmode verify-full connects over it") {
		t.Errorf("directly: %s %q, want pg.tls.required naming verify-full", direct.Code, direct.Hint)
	}
	forwarded := classify(hba, req(t, values).WithProfile("prod", plugin.TunnelKube))
	if forwarded.Code != "pg.tls.required" ||
		!strings.Contains(forwarded.Message, "profile prod (through its kube: forward) carries none") {
		t.Errorf("through a forward: %s %q, want the forward named", forwarded.Code, forwarded.Message)
	}
	if !strings.Contains(forwarded.Hint, "--sslrootcert and --tls-server-name each turn TLS on over it") ||
		strings.Contains(forwarded.Hint, "--sslmode") {
		t.Errorf("hint = %q, want what turns TLS on over a forward named and no sslmode to give", forwarded.Hint)
	}

	for _, other := range []*pgconn.PgError{{Code: "28000", Message: "role \"app\" is not permitted to log in"},
		{Code: "28P01", Message: "password authentication failed for user \"app\""}} {
		if verr := classify(other, req(t, values)); verr.Code != "pg.auth.failed" {
			t.Errorf("%s %q = %s, want pg.auth.failed as before", other.Code, other.Message, verr.Code)
		}
	}
}

// Every classified failure has to say what to do next. These are the errors
// people stare at without knowing the next move, which is the whole reason
// pg is the plugin that proves the contract.
func TestEveryClassifiedFailureNamesTheNextStep(t *testing.T) {
	r := req(t, map[string]any{"host": "db.internal", "port": 5432, "database": "app"})
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"bad password", &pgconn.PgError{Code: "28P01"}, "pg.auth.failed"},
		{"no such database", &pgconn.PgError{Code: "3D000"}, "pg.database.missing"},
		{"not permitted", &pgconn.PgError{Code: "42501"}, "pg.denied"},
		{"refused", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, "pg.conn.refused"},
		{"unknown host", &net.DNSError{Err: "no such host", Name: "db.internal"}, "pg.host.unknown"},
		{"timed out", context.DeadlineExceeded, "pg.conn.timeout"},
		{"no TLS", errors.New("server does not support SSL"), "pg.tls.unsupported"},
		{"untrusted CA", x509.UnknownAuthorityError{}, "pg.tls.untrusted"},
		{"anything else", errors.New("something unexpected"), "pg.conn.failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verr := classify(tc.err, r)
			if verr.Code != tc.code {
				t.Errorf("code = %q, want %q", verr.Code, tc.code)
			}
			if verr.Hint == "" {
				t.Error("no hint: this is exactly the error somebody is stuck on")
			}
			if verr.Message == "" {
				t.Error("no message")
			}
		})
	}
}

// A certificate is answered with the CA to name only when it is an unknown
// issuer's. macOS's own verifier, asked whenever no sslrootcert is named,
// gives most of its verdicts untyped, and every one was read as untrusted: a
// revoked certificate was answered with the CA file that turns the system's
// revocation check off. Such a verdict keeps the system's words, and the one
// hint that does send the reader to a CA file says what naming one costs.
func TestOnlyAnUnknownIssuerIsAnsweredWithTheCA(t *testing.T) {
	r := req(t, map[string]any{"host": "db.internal", "port": 5432})
	open, closing := string(rune(0x201c)), string(rune(0x201d))
	verdict := func(words string) error {
		return &tls.CertificateVerificationError{
			Err: errors.New("x509: " + open + "db.internal" + closing + " " + words)}
	}
	// The system's words for a chain to no anchor it holds are read as
	// untrusted where the system gave them, and are nobody's words elsewhere.
	notTrusted := "pg.tls.rejected"
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		notTrusted = "pg.tls.untrusted"
	}
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"Go's verifier, an issuer in no pool", x509.UnknownAuthorityError{}, "pg.tls.untrusted"},
		{"no pool to read at all", x509.SystemRootsError{}, "pg.tls.untrusted"},
		{"the same, inside the driver's error", fmt.Errorf("tls error: %w",
			&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}), "pg.tls.untrusted"},
		{"macOS, a chain to no anchor it holds", verdict("certificate is not trusted"), notTrusted},
		{"macOS, a revoked certificate", verdict("certificate is revoked"), "pg.tls.rejected"},
		{"macOS, a policy it will not pass", verdict("certificate is not standards compliant"), "pg.tls.rejected"},
		{"a revoked certificate named to look untrusted",
			verdict("certificate is not trusted" + closing + " certificate is revoked"), "pg.tls.rejected"},
		{"a signature algorithm Go's verifier refuses", &tls.CertificateVerificationError{
			Err: x509.InsecureAlgorithmError(x509.SHA1WithRSA)}, "pg.tls.rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verr := classify(tc.err, r)
			if verr.Code != tc.code {
				t.Fatalf("code = %q, want %q", verr.Code, tc.code)
			}
			if tc.code != "pg.tls.untrusted" {
				words := tc.err.Error()
				var handshake *tls.CertificateVerificationError
				if errors.As(tc.err, &handshake) {
					words = handshake.Err.Error()
				}
				if !strings.Contains(verr.Message, words) {
					t.Errorf("message = %q, want the verifier's own words in it", verr.Message)
				}
				if strings.Contains(verr.Hint, "sslrootcert") {
					t.Errorf("hint = %q sends the reader to a CA for a reason no CA cures", verr.Hint)
				}
				return
			}
			if !strings.Contains(verr.Hint, "--sslrootcert") || !strings.Contains(verr.Hint, "replaces the system's") {
				t.Errorf("hint = %q, want the CA file named and what naming one replaces", verr.Hint)
			}
		})
	}
}

// The auth hint has to name the variable the host actually reads, and that
// name is derived rather than written down — so hardcoding it here would let
// the two drift apart silently.
func TestTheAuthHintNamesTheRealEnvironmentVariable(t *testing.T) {
	verr := classify(&pgconn.PgError{Code: "28P01"}, req(t, nil))
	want := plugin.LocalEnvVar("pg.status", "password")
	if !strings.Contains(verr.Hint, want) {
		t.Errorf("hint %q does not name %s", verr.Hint, want)
	}
}

// A hint that names an rta command must name one this plugin declares.
// pg.status's "no database named X" hint pointed at `rta pg database list`
// before that capability existed, which sends somebody to type something that
// fails differently.
func TestHintsOnlyNameCapabilitiesThatExist(t *testing.T) {
	declared := map[string]bool{}
	for _, c := range Plugin().Capabilities {
		declared[c.ID] = true
	}
	r := req(t, map[string]any{"host": "db.internal", "database": "app"})
	for _, err := range []error{
		&pgconn.PgError{Code: "28P01"}, &pgconn.PgError{Code: "3D000"},
		&pgconn.PgError{Code: "42501"}, context.DeadlineExceeded,
	} {
		hint := classify(err, r).Hint
		// Any `rta pg <words>` in a hint has to name a capability this plugin
		// declares. Other namespaces are deliberately not checked here: a
		// hint may legitimately point at `rta net dns`, and this module cannot
		// see the built-in catalogue.
		if i := strings.Index(hint, "rta pg "); i >= 0 {
			tail := hint[i+len("rta pg "):]
			if j := strings.IndexAny(tail, "`\n"); j >= 0 {
				tail = tail[:j]
			}
			id := "pg." + strings.Join(strings.Fields(tail), ".")
			if !declared[id] {
				t.Errorf("a hint names `rta pg %s`, which would be %s — this plugin declares no such capability:\n  %s",
					tail, id, hint)
			}
		}
	}
}

// The doc comment at the top of main.go is an instruction somebody will
// follow, and the first person to follow it hit "main module does not contain
// package": this is its own module, so the build has to cd into it, and the
// comment said `go build ./plugins/pg` from the root — which cannot work.
//
// Checked rather than proof-read, because a command in a comment is a claim
// like any other and this one was wrong for as long as it existed.
func TestTheBuildInstructionIsRunnable(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	doc, _, _ := strings.Cut(string(src), "\npackage main")
	if !strings.Contains(doc, "cd plugins/pg && go build") {
		t.Error("the build instruction no longer cds into this module, so it cannot work " +
			"from the repository root")
	}
	if strings.Contains(doc, "go build -o ~/.local/bin/rta-plugin-pg ./plugins/pg") {
		t.Error("the build instruction is the one that fails: `go build ./plugins/pg` from the " +
			"root cannot see a separate module")
	}
}

// A dead port-forward is the commonest way a developer's `pg` call fails, and
// the general hint asks the wrong question about it: "is the server up" is
// not a question about a port on this machine. Measured against a real
// CloudNativePG cluster through `kubectl port-forward` — PostgreSQL TLS kills
// the forward on the first clean disconnect, and `psql --sslmode=require`
// kills it identically, so this is the transport rather than rta. What is
// rta's is that `prefer` is the default, so it happens on the first call.
func TestARefusedLoopbackPortBlamesTheForwardNotTheServer(t *testing.T) {
	refused := &net.OpError{Op: "dial", Err: errors.New("connect: connection refused")}

	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		req := plugin.NewRequest(map[string]any{"host": host, "port": 15433}, false, false)
		verr := classify(refused, req)
		if verr.Code != "pg.conn.refused" {
			t.Fatalf("%s: code = %s", host, verr.Code)
		}
		if !strings.Contains(verr.Hint, "port-forward") {
			t.Errorf("%s: hint does not mention a port-forward: %s", host, verr.Hint)
		}
		if !strings.Contains(verr.Hint, "sslmode") {
			t.Errorf("%s: hint does not name the setting that survives: %s", host, verr.Hint)
		}
	}

	// A real host keeps the general hint: "check your port-forward" is noise
	// when the operator typed db.internal.
	req := plugin.NewRequest(map[string]any{"host": "db.internal", "port": 5432}, false, false)
	verr := classify(refused, req)
	if strings.Contains(verr.Hint, "port-forward") {
		t.Errorf("a remote host was told to check a port-forward: %s", verr.Hint)
	}
	if !strings.Contains(verr.Hint, "rta net port db.internal") {
		t.Errorf("the remote hint lost its next command: %s", verr.Hint)
	}
}
