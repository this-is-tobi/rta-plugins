package main

import (
	"net"
	"strings"
	"testing"
)

// refusedDial is the error a dial to a port nothing listens on returns.
func refusedDial(t *testing.T) (addr string, err error) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = lis.Addr().String()
	_ = lis.Close()
	if _, err = net.Dial("tcp", addr); err == nil {
		t.Fatal("a port nothing listens on accepted a connection")
	}
	return addr, err
}

// A port nothing listens on is answered with what a person asks next: which
// setting to change, and which ports the product serves on. Qdrant named its
// ports and never the setting, and sent its reader to guess which one held the
// address.
func TestARefusedConnectionNamesTheSettingAndThePortsQdrantServesOn(t *testing.T) {
	addr, dialErr := refusedDial(t)
	verr := classify(dialErr, req(t, "qdrant.overview", map[string]any{"address": addr}))
	if verr.Code != "qdrant.conn.refused" {
		t.Fatalf("code = %q, want qdrant.conn.refused", verr.Code)
	}
	for _, want := range []string{"is the server up, and is --address right?", "REST on 6333", "gRPC on 6334"} {
		if !strings.Contains(verr.Hint, want) {
			t.Errorf("hint %q does not say %q", verr.Hint, want)
		}
	}
}
