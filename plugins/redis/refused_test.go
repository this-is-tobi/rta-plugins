package main

import (
	"net"
	"strings"
	"testing"
)

// refusedDial is the error a dial to a port nothing listens on returns.
func refusedDial(t *testing.T) error {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	_, err = net.Dial("tcp", addr)
	if err == nil {
		t.Fatal("a port nothing listens on accepted a connection")
	}
	return err
}

// A port nothing listens on is answered with what a person asks next: which
// setting to change, and which port the product serves on. redis named its
// port and never the setting, on a server elsewhere and on this machine alike.
func TestARefusedConnectionNamesTheSettingAndThePortRedisServesOn(t *testing.T) {
	dialErr := refusedDial(t)
	for _, tc := range []struct{ name, addr, want string }{
		{"a server elsewhere", "db.internal:6379", "is the server up, and is --address right? redis listens on 6379"},
		{"this machine", "127.0.0.1:6379", "and that --address names the port it serves"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rq := req(t, "redis.overview", map[string]any{"address": tc.addr})
			verr := classifyDial(dialErr, tc.addr, rq)
			if verr.Code != "redis.conn.refused" || !strings.Contains(verr.Hint, tc.want) {
				t.Errorf("%s %q, want redis.conn.refused with %q in the hint", verr.Code, verr.Hint, tc.want)
			}
		})
	}
}
