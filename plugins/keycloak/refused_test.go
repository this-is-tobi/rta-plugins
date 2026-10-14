package main

import (
	"net"
	"strings"
	"testing"
)

// A port nothing listens on is answered with what a person asks next: which
// setting to change, and which ports the product serves on. The hint named the
// setting and left its reader to know that Keycloak listens on 8080 and 8443.
func TestARefusedConnectionNamesTheSettingAndThePortsKeycloakServesOn(t *testing.T) {
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
	s := &session{req: req(t, "keycloak.overview", map[string]any{"url": "http://" + addr}), base: "http://" + addr}
	verr := s.classifyTransport(dialErr)
	if verr.Code != "keycloak.conn.refused" ||
		!strings.Contains(verr.Hint, "is the server up, and is --url right? Keycloak listens on 8080 for HTTP and 8443 for HTTPS") {
		t.Errorf("%s %q, want keycloak.conn.refused naming the setting and the ports", verr.Code, verr.Hint)
	}
}
