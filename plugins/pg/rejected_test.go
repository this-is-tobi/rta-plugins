package main

import (
	"context"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/this-is-tobi/rta/pkg/view"
)

// serveStartupError is a server that answers the startup of every connection with a
// fatal error of the given SQLSTATE, which is where PostgreSQL turns a password or a
// certificate down. It returns the port.
func serveStartupError(t *testing.T, code, message string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				backend := pgproto3.NewBackend(conn, conn)
				if _, err := backend.ReceiveStartupMessage(); err != nil {
					return
				}
				backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: code, Message: message})
				_ = backend.Flush()
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// A dashboard stops asking a tile whose credential was refused twice, reading that off the
// error a call returns (view.Error.CredentialRefused). The overview opens its connection
// before it reads anything and each figure it reads is a statement on that connection, so
// a password the server turns down is the connection's error and never a row that could not
// be read; what a statement can fail with, a privilege the role lacks, is a row, and
// pg.denied is the row's code. A call whose credential is refused returns the error
// whichever form of the page it asked for.
func TestAnOverviewWhoseCredentialTheServerRefusedIsTheRefusalAndNotAPage(t *testing.T) {
	port := serveStartupError(t, "28P01", `password authentication failed for user "rta"`)
	for _, id := range []string{"pg.overview", "pg.status"} {
		for _, detail := range []bool{false, true} {
			if id == "pg.status" && detail {
				continue
			}
			values := map[string]any{"host": "127.0.0.1", "port": port, "user": "rta", "password": "wrong",
				"database": "app", "sslmode": "disable"}
			if id == "pg.overview" {
				values["detail"] = detail
			}
			c := findCapability(t, id)
			v, err := c.Run(context.Background(), reqFor(t, id, values))
			verr := view.AsError(err, "")
			if verr == nil || verr.Code != "pg.auth.failed" || !verr.CredentialRefused() {
				t.Errorf("%s (detail %v): err = %v and the page %#v, want pg.auth.failed refused", id, detail, err, v)
			}
		}
	}
}
