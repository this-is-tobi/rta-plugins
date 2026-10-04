package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A name a stranger chose is shown as it is when it reads as itself, and in
// quotes with each character a reader would not see written out when it does
// not. The odd one holds an escape sequence and a newline; the ordinary one a
// space and an accent, which is a name and not a hazard.
const (
	oddName   = "esc\x1b[31mred\nbreak"
	oddShown  = `"esc\x1b[31mred\nbreak"`
	plainName = "my key é"
)

func noRawControls(t *testing.T, where string, cells ...string) {
	t.Helper()
	for _, cell := range cells {
		if strings.ContainsAny(cell, "\x1b\n") {
			t.Errorf("%s = %q, want the escape and the newline written out", where, cell)
		}
	}
}

func TestAKeyListingShowsAnOddKeyQuotedAndAPlainOneAsItIs(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"SCAN 0 MATCH * COUNT 200": array(bulk("0"), array(bulk(plainName), bulk(oddName))),
		"TYPE " + oddName:          "+string\r\n", "TTL " + oddName: ":-1\r\n",
		"TYPE " + plainName: "+hash\r\n", "TTL " + plainName: ":-1\r\n",
	})
	v, err := run(t, "redis.key.list", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := v.(view.Table).Rows
	if len(rows) != 2 || rows[0][0] != oddShown || rows[1][0] != plainName {
		t.Fatalf("keys = %q, want %s and %s", []string{rows[0][0], rows[1][0]}, oddShown, plainName)
	}
	noRawControls(t, "key", rows[0][0], rows[1][0])
}

func TestAKeyTreeLabelsAnOddSegmentQuotedAndKeepsTheSeparatorOutside(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"SCAN 0 MATCH * COUNT 200": array(bulk("0"), array(
			bulk(oddName+":tail\x1b[0m"), bulk(plainName+":é k"))),
	})
	v, err := run(t, "redis.key.tree", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := v.(view.Tree).Roots[0]
	if len(root.Children) != 2 {
		t.Fatalf("children = %+v", root.Children)
	}
	odd, plain := root.Children[0], root.Children[1]
	if odd.Label != oddShown+":" || len(odd.Children) != 1 || odd.Children[0].Label != `"tail\x1b[0m"` {
		t.Errorf("odd folder = %q with %+v, want %s: holding \"tail\\x1b[0m\"", odd.Label, odd.Children, oddShown)
	}
	if plain.Label != plainName+":" || len(plain.Children) != 1 || plain.Children[0].Label != "é k" {
		t.Errorf("plain folder = %q with %+v, want it as it was", plain.Label, plain.Children)
	}
}

// Redacted is matched against a pair's key, so a hash field named one way in
// the pair and another in the mask would reach the screen in the clear: the
// odd field is masked under the name it is shown by.
func TestAHashFieldAndTheKeyAreShownQuotedAndItsValueStaysMasked(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"TYPE " + oddName: "+hash\r\n", "TTL " + oddName: ":-1\r\n",
		"HGETALL " + oddName: array(bulk(oddName), bulk("s3cret"), bulk(plainName), bulk("other-s3cret")),
	})
	v, err := run(t, "redis.key.get", srv, map[string]any{"key": oddName})
	if err != nil {
		t.Fatal(err)
	}
	kv := v.(view.KeyValue)
	if got := pairValue(kv, "key"); got != oddShown {
		t.Errorf("key = %q, want %s", got, oddShown)
	}
	for _, field := range []string{"field " + oddShown, "field " + plainName} {
		if !kv.IsRedacted(field) {
			t.Errorf("%q is not redacted: %v", field, kv.Redacted)
		}
	}
	masked := fmt.Sprint(view.Redact(kv))
	for _, secret := range []string{"s3cret", "other-s3cret"} {
		if strings.Contains(masked, secret) {
			t.Errorf("%s reached the redacted output in the clear: %s", secret, masked)
		}
	}
}

func TestAKeyThatIsNotThereOrNotRenderedIsNamedQuoted(t *testing.T) {
	blank := "prod" + string(rune(0x2800))
	srv := newFakeServer(t, map[string]string{
		"TYPE " + oddName: "+none\r\n", "TTL " + oddName: ":-2\r\n",
		"TYPE " + plainName: "+none\r\n", "TTL " + plainName: ":-2\r\n",
		"TYPE " + blank: "+none\r\n", "TTL " + blank: ":-2\r\n",
		"TYPE stream:" + oddName: "+stream\r\n", "TTL stream:" + oddName: ":-1\r\n",
	})
	for _, tc := range []struct{ key, want string }{
		{oddName, "no key " + oddShown + " on "},
		{plainName, `no key "` + plainName + `" on `},
		{blank, `no key "prod\u2800" on `},
		{"stream:" + oddName, `"stream:esc\x1b[31mred\nbreak" is a stream`},
	} {
		_, err := run(t, "redis.key.get", srv, map[string]any{"key": tc.key})
		ve := view.AsError(err, "x")
		if !strings.Contains(ve.Message, tc.want) {
			t.Errorf("%q: message = %q, want %q in it", tc.key, ve.Message, tc.want)
		}
		noRawControls(t, "message", ve.Message)
	}
}

// %q leaves a character that draws as nothing as it is, and a key that ends
// in one reads as the key without it.
func TestAMessageQuotesAKeyThatDrawsAsNothingAsAListingDoes(t *testing.T) {
	for name, want := range map[string]string{
		"prod":          `"prod"`,
		plainName:       `"` + plainName + `"`,
		`say "hi"`:      `"say \"hi\""`,
		"prod\u2800":    `"prod\u2800"`,
		"prod\u200b":    `"prod\u200b"`,
		oddName:         oddShown,
		"":              `""`,
		" padded ":      `" padded "`,
		`"opens quoted`: `"\"opens quoted"`,
	} {
		if got := quotedName(name); got != want {
			t.Errorf("quotedName(%q) = %s, want %s", name, got, want)
		}
	}
}

// The slow log is the one listing where a stranger writes into it without
// being a client of the reader's: whoever runs a slow command chooses its
// arguments, and an admin reads them here.
func TestASlowlogCommandAndClientNameAreShownQuoted(t *testing.T) {
	entry := func(name string) string {
		return array(":1\r\n", ":1700000000\r\n", ":15000\r\n",
			array(bulk("SET"), bulk(oddName), bulk(plainName), bulk("v")),
			bulk("127.0.0.1:5000"), bulk(name))
	}
	for name, tc := range map[string]struct{ client, wantClient string }{
		"an odd client name":  {oddName, `127.0.0.1:5000 (` + oddShown + `)`},
		"a plain client name": {plainName, `127.0.0.1:5000 (` + plainName + `)`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newFakeServer(t, map[string]string{"SLOWLOG GET 25": array(entry(tc.client))})
			v, err := run(t, "redis.slowlog", srv, nil)
			if err != nil {
				t.Fatal(err)
			}
			row := v.(view.Table).Rows[0]
			if row[3] != tc.wantClient {
				t.Errorf("client = %q, want %q", row[3], tc.wantClient)
			}
			if want := "SET " + oddShown + " " + plainName + " v"; row[4] != want {
				t.Errorf("command = %q, want %q", row[4], want)
			}
			noRawControls(t, "row", row...)
		})
	}
}

// A client chooses its own name (CLIENT SETNAME), though a Redis server holds
// it to printable characters and so cannot hold a newline in one: the line a
// client is listed on would be two. What it cannot refuse is a name that
// draws as something else, an escape or a bidirectional override.
func TestAClientNameIsShownQuotedAndAnEmptyOneIsStillADash(t *testing.T) {
	const odd = "esc\x1b[31mred\u202e"
	table := clientTable("id=3 addr=10.0.0.1:5000 name=" + odd + " age=5 idle=1 db=0 cmd=get\n" +
		"id=4 addr=10.0.0.2:5001 name=wörker-1 age=5 idle=1 db=0 cmd=get\n" +
		"id=5 addr=10.0.0.3:5002 name= age=5 idle=1 db=0 cmd=get\n")
	if len(table.Rows) != 3 {
		t.Fatalf("rows = %v", table.Rows)
	}
	for i, want := range []string{`"esc\x1b[31mred\u202e"`, "wörker-1", "-"} {
		if got := table.Rows[i][2]; got != want {
			t.Errorf("name of client %d = %q, want %q", i+3, got, want)
		}
	}
}

// INFO's keys are the server's own, and a script can name one: the key of an
// errorstat line is whatever prefix its error began with.
func TestADetailedInfoKeyIsShownQuoted(t *testing.T) {
	kv := sectionPairs(map[string]string{
		oddName: "1", plainName: "2", "used_memory": "3", "errorstat_ERR": "calls=1",
	})
	got := map[string]string{}
	for _, p := range kv.Pairs {
		got[p.Key] = p.Value
	}
	for key, want := range map[string]string{oddShown: "1", plainName: "2", "used_memory": "3", "errorstat_ERR": "calls=1"} {
		if got[key] != want {
			t.Errorf("pair %q = %q, want %q in %v", key, got[key], want, kv.Pairs)
		}
	}
	for key := range got {
		noRawControls(t, "key", key)
	}

	info := "# Errorstats\r\nerrorstat_\x1b[31mX:count=1\r\nerrorstat_ERR:count=2\r\n"
	srv := newFakeServer(t, map[string]string{"INFO all": bulk(sampleInfo + info)})
	v, err := run(t, "redis.overview", srv, map[string]any{"detail": true})
	if err != nil {
		t.Fatal(err)
	}
	errorstats := sectionOf(t, v.(view.Sections), "info errorstats").(view.KeyValue)
	if pairValue(errorstats, `"errorstat_\x1b[31mX"`) != "count=1" || pairValue(errorstats, "errorstat_ERR") != "count=2" {
		t.Errorf("errorstats = %+v", errorstats.Pairs)
	}
}
