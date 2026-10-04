package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A name a stranger chose is shown as a list of names shows one: as it is when
// it reads as itself, and quoted with its characters written out when it does
// not. escKey holds the two a renderer cannot be trusted with — an escape
// sequence, which it strips, leaving another ordinary name, and a newline,
// which splits a row — and plainKey holds the two a quoting rule must not
// touch, a space and an accent.
const (
	escKey   = "esc\x1b[31mred\nline"
	escShown = `"esc\x1b[31mred\nline"`
	plainKey = "reports/café été.txt"
)

// Two more that draw as nothing or as something else, spelled by their bytes
// and, as a name is shown, by their code points.
const (
	brailleBlank             = "\xe2\xa0\x80"
	brailleBlankShown        = `\` + "u2800"
	rightToLeftOverride      = "\xe2\x80\xae"
	rightToLeftOverrideShown = `\` + "u202e"
)

// escAddress and plainAddress are the objects of "test-bucket" these keys are,
// as a message names one.
const (
	escAddress   = `"test-bucket/esc\x1b[31mred\nline"`
	plainAddress = "test-bucket/" + plainKey
)

// bucketContents is what serveNames holds, and how it fails.
type bucketContents struct {
	buckets                 []string // the buckets a listing of them names
	keys                    []string // listed as objects
	prefixes                []string // listed as common prefixes
	object                  []byte   // what a GET of any object answers
	taken                   bool     // a HEAD finds the object
	refusePut, refuseDelete bool
}

// serveNames is an S3 holding names that cannot cross a plain listing, which
// is how a real one carries them: minio-go asks for encoding-type=url on every
// listing and decodes what comes back, so a key with an escape byte in it
// travels percent-encoded, and the XML a bucket's name travels in could not
// carry that byte at all.
func serveNames(t *testing.T, c bucketContents) *httptest.Server {
	t.Helper()
	escaped := func(s string) string {
		var b strings.Builder
		if err := xml.EscapeText(&b, []byte(s)); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			var b strings.Builder
			for _, name := range c.buckets {
				b.WriteString("<Bucket><Name>" + escaped(name) + "</Name>" +
					"<CreationDate>2024-03-01T00:00:00.000Z</CreationDate></Bucket>")
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<ListAllMyBucketsResult><Owner><ID>me</ID></Owner><Buckets>` + b.String() +
				`</Buckets></ListAllMyBucketsResult>`))
		case r.Method == http.MethodGet && q.Has("list-type"):
			var b strings.Builder
			for _, key := range c.keys {
				b.WriteString("<Contents><Key>" + url.QueryEscape(key) + "</Key><Size>10</Size>" +
					`<LastModified>2026-08-01T10:00:00.000Z</LastModified><ETag>"e"</ETag></Contents>`)
			}
			for _, prefix := range c.prefixes {
				b.WriteString("<CommonPrefixes><Prefix>" + url.QueryEscape(prefix) + "</Prefix></CommonPrefixes>")
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<ListBucketResult><Name>test-bucket</Name><EncodingType>url</EncodingType>` +
				`<IsTruncated>false</IsTruncated>` + b.String() + `</ListBucketResult>`))
		case r.Method == http.MethodGet:
			w.Header().Set("Last-Modified", "Mon, 01 Sep 2026 10:00:00 GMT")
			_, _ = w.Write(c.object)
		case r.Method == http.MethodHead:
			if !c.taken {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Last-Modified", "Mon, 01 Sep 2026 10:00:00 GMT")
			w.Header().Set("ETag", `"e"`)
			w.Header().Set("Content-Length", "5")
		case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<CopyObjectResult><LastModified>2026-08-01T10:00:00.000Z</LastModified>` +
				`<ETag>"e"</ETag></CopyObjectResult>`))
		case r.Method == http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			if c.refusePut {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("ETag", `"e"`)
		case r.Method == http.MethodDelete:
			if c.refuseDelete {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTheHelpersShowAPlainNameAsItWasAndAnOddOneQuoted(t *testing.T) {
	// A name that reads as itself is what %q wrote for it, byte for byte, even
	// where it holds a quote or a backslash.
	for _, name := range []string{plainKey, `say "hi" \o/`, "", "a b"} {
		if got, want := quoted(name), strconv.Quote(name); got != want {
			t.Errorf("quoted(%q) = %s, want what %%q wrote: %s", name, got, want)
		}
	}
	if got := quoted(escKey); got != escShown {
		t.Errorf("quoted(an escape sequence and a newline) = %s, want %s", got, escShown)
	}
	// What %q leaves raw inside its quotes: a Braille blank, which draws as
	// nothing, so the name with it and the name without read alike.
	if got, want := quoted("prod/db"+brailleBlank), `"prod/db`+brailleBlankShown+`"`; got != want {
		t.Errorf("quoted(a name ending in a Braille blank) = %s, want %s", got, want)
	}

	if got, want := address("shop", plainKey), "shop/"+plainKey; got != want {
		t.Errorf("address(plain) = %q, want %q", got, want)
	}
	if got, want := address("shop", escKey), `"shop/esc\x1b[31mred\nline"`; got != want {
		t.Errorf("address(esc) = %s, want %s", got, want)
	}

	odd := &fs.PathError{Op: "mkdir", Path: "/out/" + escKey, Err: errors.New("not a directory")}
	if got, want := fsReason(odd), `mkdir "/out/esc\x1b[31mred\nline": not a directory`; got != want {
		t.Errorf("fsReason(odd path) = %s, want %s", got, want)
	}
	// Wrapped, the words around it stay and the path is still shown as a name.
	if got, want := fsReason(fmt.Errorf("walking: %w", odd)),
		`walking: mkdir "/out/esc\x1b[31mred\nline": not a directory`; got != want {
		t.Errorf("fsReason(wrapped odd path) = %s, want %s", got, want)
	}
	plain := &fs.PathError{Op: "open", Path: "/out/" + plainKey, Err: errors.New("permission denied")}
	if got := fsReason(plain); got != plain.Error() {
		t.Errorf("fsReason(plain path) = %q, want what the error said: %q", got, plain.Error())
	}
	if got := fsReason(errors.New("short write")); got != "short write" {
		t.Errorf("fsReason(not a path error) = %q", got)
	}
}

// A bucket's name reaches the listing as the server holds it, and a lenient
// server holds more than AWS does: a newline splits the row it is in, and a
// right-to-left override reorders what follows it. The XML a name travels in
// cannot carry an escape byte, so that one is tried on the table itself.
func TestABucketNameThatDoesNotDrawAsItselfIsListedQuoted(t *testing.T) {
	odd := "bucket\nwith" + rightToLeftOverride + "line"
	const oddShown = `"bucket\nwith` + rightToLeftOverrideShown + `line"`
	srv := serveNames(t, bucketContents{buckets: []string{"bücher 2026", odd}})
	want := []string{oddShown, "bücher 2026"}
	names := func(tbl view.Table) []string {
		var out []string
		for _, row := range tbl.Rows {
			out = append(out, row[0])
		}
		return out
	}

	v, err := runBucketList(context.Background(), reqFor(t, "s3.bucket.list", endpointOf(t, srv), map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := names(v.(view.Table)); !slices.Equal(got, want) {
		t.Errorf("s3.bucket.list names = %q, want %q", got, want)
	}

	v, err = runOverview(context.Background(),
		reqFor(t, "s3.overview", endpointOf(t, srv), map[string]any{"detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, section := range v.(view.Sections).Items {
		if tbl, ok := section.View.(view.Table); ok {
			found = true
			if got := names(tbl); !slices.Equal(got, want) {
				t.Errorf("s3.overview names = %q, want %q", got, want)
			}
		}
	}
	if !found {
		t.Error("s3.overview detail carries no table of buckets")
	}

	rows := bucketTable([]minio.BucketInfo{{Name: escKey}}).Rows
	if len(rows) != 1 || rows[0][0] != escShown {
		t.Errorf("a bucket named with an escape sequence and a newline is listed %q, want %s", rows, escShown)
	}
}

func TestAnObjectKeyThatDoesNotDrawAsItselfIsListedQuoted(t *testing.T) {
	srv := serveNames(t, bucketContents{
		keys:     []string{plainKey, escKey},
		prefixes: []string{"dir\x1b[2J/", "café été/"},
	})
	tbl := listTable(t, srv, map[string]any{})
	var got []string
	for _, row := range tbl.Rows {
		got = append(got, row[0])
	}
	slices.Sort(got)
	want := []string{`"dir\x1b[2J/"`, escShown, "café été/", plainKey}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("keys listed as %q, want %q", got, want)
	}
}

// What the next page starts after is a value the server is given back, so it
// stays the key as it is; the sentence that says the listing stopped shows it
// as a name, and the hint's argument is quoted for the surface by Call.
func TestAStoppedListingKeepsTheCursorAKeyAndShowsTheKeyQuoted(t *testing.T) {
	srv := serveNames(t, bucketContents{keys: []string{escKey, "zzz"}})
	tbl := listTable(t, srv, map[string]any{"limit": 1})

	if tbl.Page == nil || tbl.Page.Next != escKey {
		t.Fatalf("cursor = %+v, want the key as the server holds it, to be passed back as it is", tbl.Page)
	}
	if len(tbl.Rows) != 1 || tbl.Rows[0][0] != escShown {
		t.Errorf("rows = %q, want the one key shown quoted", tbl.Rows)
	}
	if len(tbl.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want the one that says it stopped", tbl.Warnings)
	}
	w := tbl.Warnings[0]
	if want := "stopped after 1 object; more keys follow " + escShown; w.Message != want {
		t.Errorf("message = %q, want %q", w.Message, want)
	}
	if strings.ContainsAny(w.Hint, "\x1b\n") || !strings.Contains(w.Hint, "--after") {
		t.Errorf("hint = %q, want the argument that continues it, with nothing a terminal would act on", w.Hint)
	}
}

// The sentence that says a listing stopped quoted its key with %q, which wrote
// an escape sequence out and left a Braille blank raw: the key it stopped at,
// and the same key without the blank, read alike in it.
func TestAStoppedListingNamesAKeyEndingInABlankByItsCodePoint(t *testing.T) {
	blank := "prod/db" + brailleBlank
	srv := serveNames(t, bucketContents{keys: []string{blank, "zzz"}})
	tbl := listTable(t, srv, map[string]any{"limit": 1})

	if tbl.Page == nil || tbl.Page.Next != blank {
		t.Fatalf("cursor = %+v, want the key as the server holds it", tbl.Page)
	}
	if len(tbl.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want the one that says it stopped", tbl.Warnings)
	}
	if want := `stopped after 1 object; more keys follow "prod/db` + brailleBlankShown + `"`; tbl.Warnings[0].Message != want {
		t.Errorf("message = %q, want %q", tbl.Warnings[0].Message, want)
	}
}

func TestATreeLabelsAKeyThatDoesNotDrawAsItselfQuoted(t *testing.T) {
	srv := serveNames(t, bucketContents{keys: []string{
		"dir\x1b[31m/leaf\nname", "plain dir é/file é.txt", "pre\x1b[2J/a.txt",
	}})
	root := treeOf(t, srv, map[string]any{}).Roots[0]
	if root.Label != "test-bucket" {
		t.Errorf("root label = %q, want the bucket as it is", root.Label)
	}
	find(t, root.Children, `"dir\x1b[31m"/`, `"leaf\nname"`)
	find(t, root.Children, "plain dir é/", "file é.txt")
	find(t, root.Children, `"pre\x1b[2J"/`, "a.txt")

	// Rooted at a prefix somebody typed, which is a name as much as a key is.
	tree := treeOf(t, srv, map[string]any{"prefix": "pre\x1b[2J/"})
	if got, want := tree.Roots[0].Label, `"test-bucket/pre\x1b[2J"`; got != want {
		t.Errorf("root label = %q, want %q", got, want)
	}
	tree = treeOf(t, srv, map[string]any{"prefix": "plain dir é/"})
	if got, want := tree.Roots[0].Label, "test-bucket/plain dir é"; got != want {
		t.Errorf("root label = %q, want %q", got, want)
	}
}

// A refusal names the object or the bucket it is about, which the server's own
// answer may give in its own words: quoted as these have always been, and now
// with what does not draw written out, and with nothing changed for a name
// that does.
func TestAMissingNameIsShownInARefusalAsAListingWouldShowIt(t *testing.T) {
	const oddBucket = escKey
	r := req(t, "s3.object.get", map[string]any{"bucket": "shop", "key": escKey})
	for _, tc := range []struct {
		name string
		err  minio.ErrorResponse
		want string
	}{
		{"an object the call named", minio.ErrorResponse{Code: minio.NoSuchKey},
			"no object " + escShown + ` in "shop"`},
		{"an object the server named", minio.ErrorResponse{Code: minio.NoSuchKey, Key: "k\x1b[2J", BucketName: oddBucket},
			`no object "k\x1b[2J" in ` + escShown},
		{"a plain object", minio.ErrorResponse{Code: minio.NoSuchKey, Key: plainKey, BucketName: "shop"},
			`no object "` + plainKey + `" in "shop"`},
		{"a name ending in a character that draws as nothing", minio.ErrorResponse{Code: minio.NoSuchKey,
			Key: "prod/db" + brailleBlank, BucketName: "shop"},
			`no object "prod/db` + brailleBlankShown + `" in "shop"`},
		{"a bucket that is not there", minio.ErrorResponse{Code: minio.NoSuchBucket, BucketName: oddBucket},
			"127.0.0.1:9000 has no bucket " + escShown},
		{"a bucket that exists", minio.ErrorResponse{Code: minio.BucketAlreadyExists, BucketName: oddBucket},
			escShown + " already exists"},
		{"a plain bucket that exists", minio.ErrorResponse{Code: minio.BucketAlreadyExists, BucketName: "shop"},
			`"shop" already exists`},
		{"a bucket with no policy", minio.ErrorResponse{Code: minio.NoSuchBucketPolicy, BucketName: oddBucket},
			escShown + " has no bucket policy set"},
		// %q wrote the escape sequence and the newline above out already, so those
		// cases hold without the helper; a bucket ending in a character that draws
		// as nothing is what the helper changed, in each of the four refusals.
		{"an object in a bucket ending in a character that draws as nothing",
			minio.ErrorResponse{Code: minio.NoSuchKey, Key: "k", BucketName: "shop" + brailleBlank},
			`no object "k" in "shop` + brailleBlankShown + `"`},
		{"a bucket that is not there, ending in one", minio.ErrorResponse{Code: minio.NoSuchBucket,
			BucketName: "shop" + brailleBlank},
			`127.0.0.1:9000 has no bucket "shop` + brailleBlankShown + `"`},
		{"a bucket that exists, ending in one", minio.ErrorResponse{Code: minio.BucketAlreadyExists,
			BucketName: "shop" + brailleBlank},
			`"shop` + brailleBlankShown + `" already exists`},
		{"a bucket with no policy, ending in one", minio.ErrorResponse{Code: minio.NoSuchBucketPolicy,
			BucketName: "shop" + brailleBlank},
			`"shop` + brailleBlankShown + `" has no bucket policy set`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.err, r); got.Message != tc.want {
				t.Errorf("message = %q, want %q", got.Message, tc.want)
			}
		})
	}
}

// Every preview and every receipt names the object it is about by its address,
// and an address holding a name that does not draw as itself is quoted whole.
func TestAnObjectIsNamedByAnAddressWhoseKeyIsQuotedWhenItNeedsIt(t *testing.T) {
	big := make([]byte, maxInline+1)
	for _, name := range []struct{ key, address string }{{escKey, escAddress}, {plainKey, plainAddress}} {
		for _, tc := range []struct {
			name   string
			capID  string
			run    plugin.Handler
			dry    bool
			values map[string]any
			bucket bucketContents
			want   string
			isErr  bool
		}{
			{"a preview of set", "s3.object.set", runObjectSet, true, map[string]any{"value": "x"},
				bucketContents{}, "would set " + name.address + " (1 B, text/plain; charset=utf-8)", false},
			{"set", "s3.object.set", runObjectSet, false, map[string]any{"value": "x"},
				bucketContents{}, "set " + name.address + " (1 B)", false},
			{"a preview of rm", "s3.object.rm", runObjectRemove, true, map[string]any{},
				bucketContents{}, "would remove " + name.address, false},
			{"rm", "s3.object.rm", runObjectRemove, false, map[string]any{},
				bucketContents{}, "removed " + name.address, false},
			{"a preview of get", "s3.object.get", runObjectGet, true, map[string]any{"out": "got.bin"},
				bucketContents{}, "would write " + name.address + " to got.bin", false},
			{"get of an object too large to print", "s3.object.get", runObjectGet, false, map[string]any{},
				bucketContents{object: big}, name.address + " is larger than 1048576 bytes", true},
			{"a preview of copy", "s3.object.copy", runObjectCopy, true, map[string]any{"dest-key": "copy"},
				bucketContents{}, "would copy " + name.address + " to test-bucket/copy", false},
			{"copy", "s3.object.copy", runObjectCopy, false, map[string]any{"dest-key": "copy"},
				bucketContents{}, "copied " + name.address + " to test-bucket/copy", false},
			{"copy onto an object that is there", "s3.object.copy", runObjectCopy, false,
				map[string]any{"dest-key": name.key}, bucketContents{taken: true},
				name.address + " already exists", true},
			{"a preview of rename", "s3.object.rename", runObjectRename, true, map[string]any{"dest-key": "moved"},
				bucketContents{}, "would move " + name.address + " to test-bucket/moved", false},
			{"rename", "s3.object.rename", runObjectRename, false, map[string]any{"dest-key": "moved"},
				bucketContents{}, "moved " + name.address + " to test-bucket/moved", false},
			{"rename that could not remove its source", "s3.object.rename", runObjectRename, false,
				map[string]any{"dest-key": "moved"}, bucketContents{refuseDelete: true},
				"copied to test-bucket/moved but could not remove the source " + name.address + ": ", true},
		} {
			t.Run(tc.name+" "+strconv.Quote(name.key), func(t *testing.T) {
				srv := serveNames(t, tc.bucket)
				values := map[string]any{"bucket": "test-bucket", "key": name.key}
				for k, v := range tc.values {
					values[k] = v
				}
				var r plugin.Request
				if tc.dry {
					r = dryReq(t, tc.capID, endpointOf(t, srv), values)
				} else {
					r = reqFor(t, tc.capID, endpointOf(t, srv), values)
				}
				v, err := tc.run(context.Background(), r)
				var got string
				switch {
				case tc.isErr:
					var verr *view.Error
					if !errors.As(err, &verr) {
						t.Fatalf("err = %v, want a refusal", err)
					}
					got = verr.Message
				case err != nil:
					t.Fatal(err)
				default:
					got = v.(view.Text).Body
				}
				if tc.isErr {
					if !strings.HasPrefix(got, tc.want) {
						t.Errorf("message = %q, want it to open with %q", got, tc.want)
					}
				} else if got != tc.want {
					t.Errorf("answered %q, want %q", got, tc.want)
				}
			})
		}
	}
}

// A copy or a move names two objects, and the key it lands on is the caller's
// to choose as much as the one it starts from: the destination is an address
// too, in the preview, the receipt and the refusal that says the source stayed.
func TestADestinationIsNamedByAnAddressAsTheSourceIs(t *testing.T) {
	for _, name := range []struct{ key, address string }{{escKey, escAddress}, {plainKey, plainAddress}} {
		for _, tc := range []struct {
			name   string
			capID  string
			run    plugin.Handler
			dry    bool
			bucket bucketContents
			want   string
			isErr  bool
		}{
			{"a preview of copy", "s3.object.copy", runObjectCopy, true, bucketContents{},
				"would copy test-bucket/src to " + name.address, false},
			{"copy", "s3.object.copy", runObjectCopy, false, bucketContents{},
				"copied test-bucket/src to " + name.address, false},
			{"a preview of rename", "s3.object.rename", runObjectRename, true, bucketContents{},
				"would move test-bucket/src to " + name.address, false},
			{"rename", "s3.object.rename", runObjectRename, false, bucketContents{},
				"moved test-bucket/src to " + name.address, false},
			{"rename that could not remove its source", "s3.object.rename", runObjectRename, false,
				bucketContents{refuseDelete: true},
				"copied to " + name.address + " but could not remove the source test-bucket/src: ", true},
		} {
			t.Run(tc.name+" "+strconv.Quote(name.key), func(t *testing.T) {
				srv := serveNames(t, tc.bucket)
				values := map[string]any{"bucket": "test-bucket", "key": "src", "dest-key": name.key}
				r := reqFor(t, tc.capID, endpointOf(t, srv), values)
				if tc.dry {
					r = dryReq(t, tc.capID, endpointOf(t, srv), values)
				}
				v, err := tc.run(context.Background(), r)
				var got string
				var verr *view.Error
				switch {
				case tc.isErr:
					if !errors.As(err, &verr) {
						t.Fatalf("err = %v, want a refusal", err)
					}
					got = verr.Message
				case err != nil:
					t.Fatal(err)
				default:
					got = v.(view.Text).Body
				}
				if tc.isErr && !strings.HasPrefix(got, tc.want) || !tc.isErr && got != tc.want {
					t.Errorf("answered %q, want %q", got, tc.want)
				}
			})
		}
	}
}

// A copy that refuses to write over an object offers the removal that clears
// it, which is a command line a reader pastes: its key is quoted for the
// shell by the surface's own spelling, never shown as a name.
func TestTheRemovalAHintOffersStillQuotesTheKeyForTheShell(t *testing.T) {
	srv := serveNames(t, bucketContents{taken: true})
	_, err := runObjectCopy(context.Background(), reqFor(t, "s3.object.copy", endpointOf(t, srv),
		map[string]any{"bucket": "test-bucket", "key": "src", "dest-key": escKey}))
	var verr *view.Error
	if !errors.As(err, &verr) || verr.Code != "s3.copy.taken" {
		t.Fatalf("err = %v, want s3.copy.taken", err)
	}
	if strings.ContainsAny(verr.Hint, "\x1b\n") || !strings.Contains(verr.Hint, "rta s3 object rm") {
		t.Errorf("hint = %q, want a removal line with the key spelled for a shell", verr.Hint)
	}
}

func TestAPresignedPreviewNamesTheObjectByItsAddress(t *testing.T) {
	srv := serveNames(t, bucketContents{})
	for key, want := range map[string]string{escKey: escAddress, plainKey: plainAddress} {
		v, err := runObjectPresign(context.Background(), dryReq(t, "s3.object.presign", endpointOf(t, srv),
			map[string]any{"bucket": "test-bucket", "key": key}))
		if err != nil {
			t.Fatal(err)
		}
		if got := v.(view.KeyValue).Pairs[0]; got.Key != "would presign" || got.Value != want {
			t.Errorf("%q: %s = %q, want %q", key, got.Key, got.Value, want)
		}
	}
}

func downloadRefusal(t *testing.T, keys []string, values map[string]any) *view.Error {
	t.Helper()
	srv := serveNames(t, bucketContents{keys: keys, object: []byte("body")})
	values["out"] = filepath.Join(t.TempDir(), "backup")
	values["parallel"] = 1
	_, err := runBucketDownload(context.Background(), downloadReq(t, srv, values))
	var verr *view.Error
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	return verr
}

// A key becomes a filename, and the key is the server's: the one that would
// escape the destination is named in the refusal, which is where a name that
// does not draw as itself is the attack and not an accident.
func TestADownloadRefusesAKeyThatDoesNotDrawAsItselfByNamingItQuoted(t *testing.T) {
	verr := downloadRefusal(t, []string{"../x\x1b[31m\n", plainKey, "../" + plainKey, "../blank" + brailleBlank},
		map[string]any{})
	if verr.Code != "s3.download.unsafekey" {
		t.Fatalf("code = %s, want s3.download.unsafekey: %s", verr.Code, verr.Message)
	}
	for _, want := range []string{
		`"../x\x1b[31m\n" (escapes the destination directory)`,
		`"../` + plainKey + `" (escapes the destination directory)`,
		`"../blank` + brailleBlankShown + `" (escapes the destination directory)`,
	} {
		if !strings.Contains(verr.Message, want) {
			t.Errorf("message = %q, want %q in it", verr.Message, want)
		}
	}
	if strings.ContainsAny(verr.Message, "\x1b\n") {
		t.Errorf("message = %q, holds a character a terminal would act on", verr.Message)
	}
}

// Two keys that resolve to one local file, and a key whose directory is a file
// the download wrote a moment before: each names the local path they came to,
// which is built from the key and is the key's name as much as the key is.
func TestADownloadNamesTheLocalFileItCouldNotWriteAsAName(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		code string
		want func(root string) string
	}{
		{"two keys that are one file", []string{"dir\x1b[31m/file", "dir\x1b[31m//file"}, "s3.download.collision",
			func(root string) string {
				return `two object keys resolve to the same local file: "` + root + `/dir\x1b[31m/file"`
			}},
		{"a directory that is a file", []string{"a\x1b", "a\x1b/b"}, "s3.download.create",
			func(root string) string {
				return `creating "` + root + `/a\x1b": mkdir "` + root + `/a\x1b": not a directory`
			}},
		{"two plain keys that are one file", []string{plainKey, "reports//café été.txt"}, "s3.download.collision",
			func(root string) string {
				return "two object keys resolve to the same local file: " + root + "/" + plainKey
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveNames(t, bucketContents{keys: tc.keys, object: []byte("body")})
			root := filepath.Join(t.TempDir(), "backup")
			_, err := runBucketDownload(context.Background(),
				downloadReq(t, srv, map[string]any{"out": root, "parallel": 1}))
			var verr *view.Error
			if !errors.As(err, &verr) || verr.Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
			if want := tc.want(root); verr.Message != want {
				t.Errorf("message = %q, want %q", verr.Message, want)
			}
		})
	}
}

// The file a key resolves to is one the system may refuse to create for a
// reason of its own, and its error words the path as the key spelled it.
func TestADownloadNamesTheFileTheSystemWouldNotCreateAsAName(t *testing.T) {
	const tail = "x\x1b"
	long := tail + strings.Repeat("a", 300)
	srv := serveNames(t, bucketContents{keys: []string{"dir\x1b/" + long}, object: []byte("body")})
	root := filepath.Join(t.TempDir(), "backup")
	_, err := runBucketDownload(context.Background(),
		downloadReq(t, srv, map[string]any{"out": root, "parallel": 1}))
	var verr *view.Error
	if !errors.As(err, &verr) || verr.Code != "s3.download.create" {
		t.Fatalf("err = %v, want s3.download.create", err)
	}
	shown := `"` + root + `/dir\x1b/x\x1b` + strings.Repeat("a", 300) + `"`
	if want := "creating " + shown + ": open " + shown + ": file name too long"; verr.Message != want {
		t.Errorf("message = %q, want %q", verr.Message, want)
	}
}

func TestADownloadReportsWhereItCameFromByAName(t *testing.T) {
	for prefix, want := range map[string]string{
		escKey:              escAddress,
		"reports/café été/": "test-bucket/reports/café été/",
		"":                  "test-bucket/",
		"reports/ ":         `"test-bucket/reports/ "`,
	} {
		srv := serveNames(t, bucketContents{keys: []string{"a.txt"}, object: []byte("body")})
		v, err := runBucketDownload(context.Background(), downloadReq(t, srv,
			map[string]any{"out": filepath.Join(t.TempDir(), "backup"), "prefix": prefix}))
		if err != nil {
			t.Fatal(err)
		}
		var from string
		for _, p := range v.(view.KeyValue).Pairs {
			if p.Key == "from" {
				from = p.Value
			}
		}
		if from != want {
			t.Errorf("prefix %q: from = %q, want %q", prefix, from, want)
		}
	}
}

func TestAnUploadNamesWhatItRefusesAndWhereItWritesAsNames(t *testing.T) {
	t.Run("a symlink named with an escape sequence and a newline", func(t *testing.T) {
		dir := dirWithFiles(t, map[string]string{"a.txt": "x"})
		for _, name := range []string{"link\x1b[31m\nname", "link" + brailleBlank} {
			if err := os.Symlink("a.txt", filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		srv := serveNames(t, bucketContents{})
		_, err := runBucketUpload(context.Background(), uploadReq(t, srv, map[string]any{"dir": dir}))
		var verr *view.Error
		if !errors.As(err, &verr) || verr.Code != "s3.upload.notregular" {
			t.Fatalf("err = %v, want s3.upload.notregular", err)
		}
		for _, want := range []string{
			`"` + dir + `/link\x1b[31m\nname" (a symlink)`,
			`"` + dir + `/link` + brailleBlankShown + `" (a symlink)`,
		} {
			if !strings.Contains(verr.Message, want) {
				t.Errorf("message = %q, want %q in it", verr.Message, want)
			}
		}
	})

	t.Run("a directory it could not read", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		dir := dirWithFiles(t, map[string]string{"a.txt": "x"})
		unreadable := filepath.Join(dir, "sub\x1b[2J")
		if err := os.Mkdir(unreadable, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(unreadable, 0o700) })
		srv := serveNames(t, bucketContents{})
		_, err := runBucketUpload(context.Background(), uploadReq(t, srv, map[string]any{"dir": dir}))
		var verr *view.Error
		if !errors.As(err, &verr) || verr.Code != "s3.upload.walk" {
			t.Fatalf("err = %v, want s3.upload.walk", err)
		}
		if want := "reading " + dir + `: open "` + dir + `/sub\x1b[2J": permission denied`; verr.Message != want {
			t.Errorf("message = %q, want %q", verr.Message, want)
		}
	})

	t.Run("a destination that already holds an object", func(t *testing.T) {
		for key, want := range map[string]string{escKey: escShown, plainKey: plainKey} {
			srv := serveNames(t, bucketContents{keys: []string{key}})
			dir := dirWithFiles(t, map[string]string{"a.txt": "x"})
			_, err := runBucketUpload(context.Background(),
				uploadReq(t, srv, map[string]any{"dir": dir, "prefix": "backup"}))
			var verr *view.Error
			if !errors.As(err, &verr) || verr.Code != "s3.upload.notempty" {
				t.Fatalf("err = %v, want s3.upload.notempty", err)
			}
			if wantMessage := "test-bucket/backup/ already holds objects (" + want + ", and possibly more)"; verr.Message != wantMessage {
				t.Errorf("message = %q, want %q", verr.Message, wantMessage)
			}
		}
	})

	t.Run("a destination named by a prefix", func(t *testing.T) {
		for prefix, want := range map[string]string{
			escKey:              `"test-bucket/esc\x1b[31mred\nline/"`,
			"reports/café été/": "test-bucket/reports/café été/",
		} {
			srv := serveNames(t, bucketContents{keys: []string{"k"}})
			dir := dirWithFiles(t, map[string]string{"a.txt": "x"})
			_, err := runBucketUpload(context.Background(),
				uploadReq(t, srv, map[string]any{"dir": dir, "prefix": prefix}))
			var verr *view.Error
			if !errors.As(err, &verr) || verr.Code != "s3.upload.notempty" {
				t.Fatalf("err = %v, want s3.upload.notempty", err)
			}
			if wantMessage := want + " already holds objects (k, and possibly more)"; verr.Message != wantMessage {
				t.Errorf("message = %q, want %q", verr.Message, wantMessage)
			}
		}
	})

	for prefix, want := range map[string]string{
		escKey:              `"test-bucket/esc\x1b[31mred\nline/"`,
		"reports/café été/": "test-bucket/reports/café été/",
		"":                  "test-bucket/",
	} {
		t.Run("under the prefix "+prefix, func(t *testing.T) {
			dir := dirWithFiles(t, map[string]string{"a.txt": "x"})
			values := func() map[string]any {
				return map[string]any{"dir": dir, "prefix": prefix, "overwrite": true}
			}

			srv := serveNames(t, bucketContents{})
			v, err := runBucketUpload(context.Background(), uploadReq(t, srv, values()))
			if err != nil {
				t.Fatal(err)
			}
			var to string
			for _, p := range v.(view.KeyValue).Pairs {
				if p.Key == "to" {
					to = p.Value
				}
			}
			if to != want {
				t.Errorf("to = %q, want %q", to, want)
			}

			previewValues := values()
			previewValues["bucket"] = "test-bucket"
			preview, err := runBucketUpload(context.Background(),
				dryReq(t, "s3.bucket.upload", endpointOf(t, srv), previewValues))
			if err != nil {
				t.Fatal(err)
			}
			if got, wantText := preview.(view.Text).Body, "would upload 1 file (1 B) from "+dir+" into "+want; got != wantText {
				t.Errorf("preview = %q, want %q", got, wantText)
			}

			refused := serveNames(t, bucketContents{refusePut: true})
			_, err = runBucketUpload(context.Background(), uploadReq(t, refused, values()))
			var verr *view.Error
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want the upload's failure", err)
			}
			if !strings.Contains(verr.Hint, want+" may hold a partial upload") {
				t.Errorf("hint = %q, want it to name %s as the prefix that may hold a partial upload", verr.Hint, want)
			}
		})
	}
}
