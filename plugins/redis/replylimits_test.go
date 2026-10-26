package main

import (
	"bufio"
	"runtime"
	"strings"
	"testing"
)

func clientOver(wire string) *client {
	return &client{r: bufio.NewReader(strings.NewReader(wire))}
}

// What a reply says of its own size is the server's word, and nothing is made
// to the size of it before the bytes are there: a bulk string "of" 9 GB, an
// array of a billion items and a line with no end each took the plugin down by
// what they asked for, not by anything they sent.
func TestARepliesClaimsAreHeldBeforeAnythingIsSizedFromThem(t *testing.T) {
	for name, wire := range map[string]string{
		"a bulk string past what Redis sends":               "$9999999999\r\n",
		"a bulk length that overflows":                      "$9223372036854775807\r\n",
		"a bulk string announced and not sent":              "$500000000\r\nabc",
		"an array of a billion that never comes":            "*1000000000\r\n",
		"an array inside an array, a hundred thousand deep": strings.Repeat("*1\r\n", 100000),
		"a status line that never ends":                     "+" + strings.Repeat("x", 10<<20),
	} {
		t.Run(name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			if _, err := clientOver(wire).read(); err == nil {
				t.Fatal("a reply that cannot be what it says was read")
			}
			runtime.ReadMemStats(&after)
			if grown := after.TotalAlloc - before.TotalAlloc; grown > 64<<20 {
				t.Errorf("refusing it allocated %d MiB: something was sized from the claim", grown>>20)
			}
		})
	}
}

// And the replies a server really sends are read as they were.
func TestOrdinaryRepliesAreStillRead(t *testing.T) {
	cases := []struct {
		wire string
		kind byte
		text string
		n    int
	}{
		{"+OK\r\n", '+', "OK", 0},
		{":42\r\n", ':', "42", 0},
		{"$5\r\nhello\r\n", '$', "hello", 0},
		{"$0\r\n\r\n", '$', "", 0},
		{"*2\r\n$1\r\na\r\n:7\r\n", '*', "", 2},
		{"*0\r\n", '*', "", 0},
		{"*2\r\n*2\r\n:1\r\n:2\r\n*1\r\n$1\r\nz\r\n", '*', "", 2},
	}
	for _, tc := range cases {
		r, err := clientOver(tc.wire).read()
		if err != nil {
			t.Errorf("%q: %v", tc.wire, err)
			continue
		}
		if r.kind != tc.kind || r.text() != tc.text || len(r.items) != tc.n {
			t.Errorf("%q = kind %q text %q items %d, want %q %q %d", tc.wire, r.kind, r.text(), len(r.items), tc.kind, tc.text, tc.n)
		}
	}
	if r, err := clientOver("$-1\r\n").read(); err != nil || !r.null {
		t.Errorf("a null bulk string was read as %+v, %v", r, err)
	}
	if r, err := clientOver("*-1\r\n").read(); err != nil || !r.null {
		t.Errorf("a null array was read as %+v, %v", r, err)
	}
	long := strings.Repeat("x", 1<<20)
	if r, err := clientOver("$1048576\r\n" + long + "\r\n").read(); err != nil || r.str != long {
		t.Errorf("a bulk string of a megabyte was not read whole (%d bytes, %v)", len(r.str), err)
	}
}
