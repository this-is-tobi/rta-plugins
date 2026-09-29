package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// verify-ca's child, in words MariaDB's client has.
//
// MySQL's client has VERIFY_CA; MariaDB's has no mode that checks a chain and
// leaves the name alone, and what its flags mean moved under them. Measured
// against a server whose certificate the CA in ca-file issued for another
// name: MariaDB 10.11's client, given --ssl-ca and
// --skip-ssl-verify-server-cert, checks the chain and not the name — verify-ca
// exactly, and the spelling tlsArgs gives it. From 11.4 the client checks the
// name whenever it is given a CA, the skip notwithstanding, and refused the
// very certificate verify-ca exists for with "Hostname verification failed".
//
// That client takes --ssl-fp instead: the fingerprint of the one certificate
// to accept, and nothing else. So a client that takes it is handed the
// SHA-256 of the certificate the pre-flight connection verified against
// ca-file a moment before — the server the pre-flight checked and no other,
// which is more than a chain check says and never less. One whose certificate
// changed in between is refused, and classifyDump says so.

// pinPending stands in a dry run's argv for the fingerprint a real run learns
// from its pre-flight connection, which a dry run never makes.
const pinPending = "<the certificate the pre-flight verifies against ca-file>"

// takesPin reports whether the client at tool takes --ssl-fp, read off its
// own --help rather than off a version string: the client answers to two
// names, and to MySQL's tools' names on some hosts, and what it lists is what
// it takes.
func takesPin(ctx context.Context, tool string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, "--no-defaults", "--help")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	out, err := cmd.Output()
	return err == nil && bytes.Contains(out, []byte("--ssl-fp"))
}

// pinned is args with verify-ca's chain-check spelling replaced by the pin:
// fp, the SHA-256 fingerprint of the certificate to accept. The CA goes with
// it, since a client given one checks the name.
func pinned(args []string, fp string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--ssl-ca="), arg == "--skip-ssl-verify-server-cert":
		case arg == "--ssl":
			out = append(out, arg, "--ssl-fp="+fp)
		default:
			out = append(out, arg)
		}
	}
	return out
}

// connectPinned is connect, and for verify-ca the SHA-256 fingerprint of the
// certificate whose chain it verified against ca-file — "" for any other
// mode. Recorded where the chain is checked, so it is the certificate that
// passed, and under a lock, since the pool may open its connection on a
// goroutine of its own.
func connectPinned(ctx context.Context, req plugin.Request) (*sql.DB, string, *view.Error) {
	cfg, verr := driverConfig(req)
	if verr != nil {
		return nil, "", verr
	}
	var mu sync.Mutex
	var fp string
	if cfg.TLS != nil && cfg.TLS.VerifyConnection != nil {
		verify := cfg.TLS.VerifyConnection
		cfg.TLS.VerifyConnection = func(cs tls.ConnectionState) error {
			if err := verify(cs); err != nil {
				return err
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			mu.Lock()
			fp = hex.EncodeToString(sum[:])
			mu.Unlock()
			return nil
		}
	}
	db, verr := open(ctx, req, cfg)
	if verr != nil {
		return nil, "", verr
	}
	mu.Lock()
	defer mu.Unlock()
	return db, fp, nil
}

// pinRefusal answers a child that refused the certificate it was pinned to:
// the server presented another than the one the pre-flight verified against
// ca-file a moment before. Run again, the pre-flight says which it was — a
// certificate reissued in between verifies and is pinned in turn, and one the
// CA did not issue is refused as untrusted.
func pinRefusal(sf plugin.Surface, line string) *view.Error {
	return view.Errorf("mariadb.tls.changed", "%s", line).
		WithHint("the server presented another certificate than the one rta verified against " +
			setting(sf, "ca-file") + " a moment before — reissued in between, or something else answering " +
			"in its place. Run it again: the check before the child says which")
}
