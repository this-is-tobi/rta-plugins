package main

import (
	"context"
	stdnet "net"
	"testing"
	"time"
)

// A TLS connection is abandoned when the caller stops waiting, exactly as a
// plain one already is.
//
// connect dials one of two ways and only one of them was reachable by a
// context: the plain branch calls dialer.DialContext, the TLS branch called
// tls.DialWithDialer, which takes a deadline and no context at all. So a
// caller that gave up — an MCP client that closed, an operator's ^C, a
// deadline three layers up — kept a goroutine sitting on a handshake with a
// server that had accepted the TCP connection and then said nothing, for the
// whole of dialTimeout, with nobody left to hand the answer to.
//
// The listener here accepts and never speaks, which is what a half-open
// middlebox looks like from this side and is the only case where the
// difference is observable: with a server that answers, the handshake
// finishes long before anyone notices which dial was used.
func TestATLSConnectStopsWhenTheCallerHasStoppedWaiting(t *testing.T) {
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Held, not closed: a closed connection would fail the handshake
			// on its own and prove nothing about the context.
			defer conn.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, verr := connect(ctx, req(t, "redis.overview", map[string]any{
			"address": ln.Addr().String(),
			"tls":     true,
		})); verr == nil {
			t.Error("connect succeeded against a server that never completed a handshake")
		}
	}()

	// Generous next to dialTimeout and tight next to the failure: the whole
	// point is that this returns on the cancellation rather than on the
	// deadline.
	select {
	case <-done:
	case <-time.After(dialTimeout / 2):
		t.Fatalf("connect was still dialing %s after the context was cancelled — it is waiting "+
			"out dialTimeout (%s) instead", dialTimeout/2, dialTimeout)
	}
}
