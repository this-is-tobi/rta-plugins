package main

import (
	"net"
	"strings"
	"testing"
)

// A port nothing listens on is answered with what a person asks next: which
// settings to change, and which port the product serves on. The hint named the
// settings and left its reader to know the port.
func TestARefusedConnectionNamesTheSettingsAndThePortTheServerServesOn(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	_, dialErr := net.Dial("tcp", addr)
	if dialErr == nil {
		t.Fatal("a port nothing listens on accepted a connection")
	}
	verr := classify(dialErr, req(t, "mariadb.overview", map[string]any{}))
	if verr.Code != "mariadb.conn.refused" ||
		!strings.Contains(verr.Hint, "is the server up, and are --host and --port right? MariaDB listens on 3306") {
		t.Errorf("%s %q, want mariadb.conn.refused naming the settings and the port", verr.Code, verr.Hint)
	}
}
