package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Which TLS files and names a call connects with, decided once.
//
// **Two clients read these, and they must read the same ones.** The pgx
// connection every capability opens first, and the libpq children (pg_dump,
// psql, pg_restore) a dump or a restore runs afterwards, each find a client
// certificate, its key and a root certificate under ~/.postgresql when none
// is named, and each find them their own way: pgx under the home directory
// the password database gives, libpq under $HOME, and libpq checks a key's
// permissions where pgx does not and reads a revocation list where pgx has no
// way to. A pre-flight that passed on one set of files was followed by a
// child that connected with another, or was refused for a key the pre-flight
// never questioned, and a ~/.postgresql/root.crt nobody named turned
// sslmode=require into a verifying one on both.
//
// So nothing is found. The files a call uses are the ones its settings name,
// handed to the driver in the connection string and to every child in its
// environment, and where no file is named the child is pointed at a path
// that does not exist, since libpq reads an empty value as "use the default"
// (the passfile's lesson, childEnv). ssl-home is the one switch that asks for
// libpq's own defaults back, and it asks by name: this resolves them here,
// once, and both clients are handed the result.
type transport struct {
	// mode is the sslmode the connection and every child use: the setting's,
	// except over a forward, where the host forces disable and one of the
	// settings below asks for TLS all the same.
	mode string
	// rootCert is "", "system", or a path.
	rootCert string
	// clientCert and clientKey are paths, or both empty.
	clientCert, clientKey string
	// serverName is the name the certificate is checked for in place of the
	// host's, or "".
	serverName string
	// fromHome says which files ssl-home supplied, for a refusal that has to
	// name the setting that put a file there.
	fromHome struct{ root, client bool }
}

// transportOf resolves the TLS side of req's connection.
func transportOf(req plugin.Request) transport {
	t := transport{
		mode:       req.String("sslmode"),
		rootCert:   rootCert(req),
		clientCert: resolvedPath(req.String("sslcert")),
		clientKey:  resolvedPath(req.String("sslkey")),
		serverName: serverName(req),
	}
	// **A kube: or ssh: forward turns sslmode off, and these turn TLS back on
	// over it.** The host forces sslmode to disable beside a forward (it is
	// the TLS role), and refuses a caller's own, so the plugin reads what the
	// operator can still say: a CA to verify against, a name to check, a
	// client certificate to present. Each is a request for TLS that nothing
	// else answers, as etcd's ca-file and tls-server-name are, and what it
	// asks for is verify-full: the one mode that checks the name, which a
	// forward's end (127.0.0.1) never is, so the refusal for a certificate
	// that is for another name (forwardName) has the setting that cures it.
	if req.Tunnel() != plugin.TunnelNone &&
		(t.serverName != "" || t.rootCert != "" || t.clientCert != "" || t.clientKey != "") {
		t.mode = "verify-full"
	}
	if req.Bool("ssl-home") && t.mode != "disable" {
		t.addHomeDefaults()
	}
	return t
}

// addHomeDefaults fills in, from libpq's own directory, what no setting
// named: the root certificate when the file is there, and the client pair
// when both files are. The rule is pgx's and libpq's, a client certificate
// without its key being no certificate at all, and a file named explicitly
// is never mixed with one found: the client pair is the settings' when either
// is named.
func (t *transport) addHomeDefaults() {
	dir := libpqDir()
	if dir == "" {
		return
	}
	if t.rootCert == "" {
		if root := filepath.Join(dir, "root.crt"); exists(root) {
			t.rootCert, t.fromHome.root = root, true
		}
	}
	if t.clientCert == "" && t.clientKey == "" {
		cert, key := filepath.Join(dir, "postgresql.crt"), filepath.Join(dir, "postgresql.key")
		if exists(cert) && exists(key) {
			t.clientCert, t.clientKey, t.fromHome.client = cert, key, true
		}
	}
}

// libpqDir is where libpq looks for its files: ~/.postgresql, and on Windows
// %APPDATA%\postgresql. "" when the home directory is not known.
func libpqDir() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "postgresql")
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".postgresql")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolvedPath is a path setting with a leading ~ resolved and made
// absolute, or "" when it names nothing: rootCert's rule, for the same
// reason. One resolution for every place the path goes, so the driver, the
// children and the restore line a dump prints cannot name different files.
func resolvedPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if abs, err := expandHome(p); err == nil {
		return abs
	}
	return plugin.ExpandHome(p)
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

// pasteEnv is the PG* assignments, each one word of a shell line, that make a
// libpq command pasted into a shell connect the way this call did: the mode
// when it insists on TLS, the files the call used however it found them, and
// the address behind the name its certificate was checked for. For a command
// that has no flag for them, as createdb has none.
func pasteEnv(req plugin.Request) []string {
	t := transportOf(req)
	var env []string
	if verifiesOrRequires(t.mode) {
		env = append(env, "PGSSLMODE="+plugin.ShellWord(t.mode))
	}
	if _, hostaddr := childHost(req); hostaddr != "" {
		env = append(env, "PGHOSTADDR="+plugin.ShellWord(hostaddr))
	}
	if t.mode == "disable" {
		return env
	}
	for _, file := range []struct{ name, path string }{
		{"PGSSLROOTCERT", t.rootCert}, {"PGSSLCERT", t.clientCert}, {"PGSSLKEY", t.clientKey},
	} {
		if file.path != "" {
			env = append(env, file.name+"="+plugin.ShellWord(file.path))
		}
	}
	return env
}

// orUnreadable is the path a child is handed for one of its TLS files: the
// file the call uses, or the path that is not there when it uses none.
func orUnreadable(path string) string {
	if path == "" {
		return unreadable
	}
	return path
}

// ipLiteral reports whether host is an address and not a name: the one
// kind libpq's hostaddr takes, and what a forward's end always is.
func ipLiteral(host string) bool { return net.ParseIP(strings.Trim(host, "[]")) != nil }

// unreadable is the path of a file that does not exist, for the PG* variable
// of a file no setting named. libpq treats an empty value as unset and falls
// back to the default under ~/.postgresql; a path that is not there is
// answered as "no such file", which for a client certificate and a revocation
// list is "none", and for a root certificate is none too under the modes that
// do not verify.
const unreadable = "/nonexistent/rta-refuses-ambient-tls-files"
