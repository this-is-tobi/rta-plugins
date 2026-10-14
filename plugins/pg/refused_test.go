package main

import (
	"net"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A port on this machine with nothing on it is what a call nobody pointed
// anywhere reaches — localhost:5432, the defaults — as much as it is a
// port-forward that exited. The hint opened with the forward, and told somebody
// who had just installed this to check a terminal they never opened; it opens
// with whether the server runs here and which settings say where, and the
// forward follows. Through a forward the host opened the port was its own end,
// and no terminal is the one to check.
func TestARefusedPortOnThisMachineAsksAboutTheServerBeforeTheForward(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()
	_, dialErr := net.Dial("tcp", lis.Addr().String())
	if dialErr == nil {
		t.Fatal("a port nothing listens on accepted a connection")
	}
	values := map[string]any{"host": "localhost", "port": port}

	verr := classify(dialErr, req(t, values))
	if verr.Code != "pg.conn.refused" {
		t.Fatalf("code = %q, want pg.conn.refused", verr.Code)
	}
	ask := "is the server up on this machine, and are --host and --port right?"
	if !strings.HasPrefix(verr.Hint, ask) || !strings.Contains(verr.Hint, "port-forward that exited") {
		t.Errorf("hint %q, want it to open with %q and keep the forward after it", verr.Hint, ask)
	}

	forwarded := classify(dialErr, req(t, values).WithProfile("lab", plugin.TunnelSSH))
	if strings.Contains(forwarded.Hint, ask) || !strings.Contains(forwarded.Hint, "port-forward that exited") {
		t.Errorf("through a forward the host opened: %q, want the forward alone", forwarded.Hint)
	}
}
