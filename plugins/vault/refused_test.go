package main

import (
	"net"
	"strings"
	"testing"
)

// A port nothing listens on is answered with what a person asks next: which
// setting to change, and which port the product serves on. The hint named the
// setting and left its reader to know that Vault listens on 8200.
func TestARefusedConnectionNamesTheSettingAndThePortVaultServesOn(t *testing.T) {
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
	verr := classify(dialErr, req(t, "vault.seal.status", map[string]any{"address": "http://" + addr}))
	if verr.Code != "vault.conn.refused" ||
		!strings.Contains(verr.Hint, "is the server up, and is --address right? Vault listens on 8200") {
		t.Errorf("%s %q, want vault.conn.refused naming the setting and the port", verr.Code, verr.Hint)
	}
}
