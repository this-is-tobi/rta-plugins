package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// nothingListens is an endpoint no call here is answered by: the refusals
// driven against it are made before anything is dialled, and the one that
// dials it is refused by the loopback interface at once.
const nothingListens = "127.0.0.1:1"

// refusingS3 is an S3 that refuses what a call asks for: a HEAD, which has no
// body to carry a document in, with a bare not found, and every other request
// with status and the error document code, naming bucket and key as a
// server's own answer does.
func refusingS3(t *testing.T, status int, code, bucket, key string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code>`+
			`<Message>refused</Message><BucketName>%s</BucketName><Key>%s</Key></Error>`, code, bucket, key)
	}))
	t.Cleanup(srv.Close)
	return endpointOf(t, srv)
}

// goneOnceListed lists one key in test-bucket and answers its GET with
// NoSuchKey: an object a download's listing found and that was removed before
// it was fetched.
func goneOnceListed(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Has("list-type") {
			_, _ = w.Write([]byte(`<ListBucketResult><Name>test-bucket</Name><IsTruncated>false</IsTruncated>` +
				`<Contents><Key>a.txt</Key><Size>1</Size><LastModified>2026-08-01T10:00:00.000Z</LastModified>` +
				`<ETag>"e"</ETag></Contents></ListBucketResult>`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>gone</Message>` +
			`<BucketName>test-bucket</BucketName><Key>a.txt</Key></Error>`))
	}))
	t.Cleanup(srv.Close)
	return endpointOf(t, srv)
}

// served is serveNames as an endpoint, for a table entry.
func served(c bucketContents) func(*testing.T) string {
	return func(t *testing.T) string { return endpointOf(t, serveNames(t, c)) }
}

// refusing is refusingS3 as an endpoint, for a table entry.
func refusing(status int, code, bucket, key string) func(*testing.T) string {
	return func(t *testing.T) string { return refusingS3(t, status, code, bucket, key) }
}

// refusedInput runs capability id on r and returns the refusal it answered
// with, held to id's declaration: a name id does not declare is one no
// surface acts on (plugin.Capability.ErrorInput), so it fails here rather
// than passing as a stamp that does nothing.
func refusedInput(t *testing.T, id string, r plugin.Request) *view.Error {
	t.Helper()
	c := capabilityByID(t, id)
	_, err := c.Run(context.Background(), r)
	if err == nil {
		t.Fatalf("%s was answered, not refused", id)
	}
	verr := view.AsError(err, "s3.test.uncoded")
	if _, declared := c.ErrorInput(verr); verr.Input != "" && !declared {
		t.Errorf("%s: %s names the input %q, which %s does not declare", id, verr.Code, verr.Input, id)
	}
	return verr
}

// fileIn writes body to a file of its own and returns its path.
func fileIn(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// dirIn makes a directory of its own holding files, and returns its path.
func dirIn(t *testing.T, files ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A refusal of the one input a call got wrong names it (view.Error.Input), by
// the name the capability that refused it declares, so the form the call came
// from opens again on that box and an agent is told which argument to send
// again: a bucket or a key that is not there, a name S3 does not allow, a
// destination that is taken, a path that is wrong, an address or a CA file
// that cannot be used. A copy's missing bucket is the source's or the
// destination's by the name the server gave, and named for that one.
//
// Beside them, the refusals that name none on purpose: the connection and the
// credentials, a bucket's own state, a key a download's listing found rather
// than one the caller gave, and the refusals two inputs could each answer —
// a limit or a prefix, overwrite or a prefix, a value or a file, out or
// nothing to print.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent")
	notes := fileIn(t, "notes.txt", "not a directory\n")
	notACA := fileIn(t, "ca.pem", "not a certificate\n")
	there := fileIn(t, "there.bin", "already here")
	many := make([]string, maxBuckets+1)
	for i := range many {
		many[i] = fmt.Sprintf("bucket-%02d", i)
	}

	for _, tc := range []struct {
		name     string
		id       string
		endpoint func(*testing.T) string
		values   func(*testing.T) map[string]any
		code     string
		input    string
	}{
		{name: "a key the bucket does not hold", id: "s3.object.get",
			endpoint: refusing(http.StatusNotFound, "NoSuchKey", "shop", "absent"),
			values:   given(map[string]any{"bucket": "shop", "key": "absent"}),
			code:     "s3.object.notfound", input: "key"},
		{name: "a key whose metadata is not there", id: "s3.object.show",
			endpoint: refusing(http.StatusNotFound, "NoSuchKey", "shop", "absent"),
			values:   given(map[string]any{"bucket": "shop", "key": "absent"}),
			code:     "s3.object.notfound", input: "key"},
		{name: "a bucket that is not there", id: "s3.object.list",
			endpoint: refusing(http.StatusNotFound, "NoSuchBucket", "nope", ""),
			values:   given(map[string]any{"bucket": "nope"}),
			code:     "s3.bucket.notfound", input: "bucket"},
		{name: "a bucket that is not there", id: "s3.object.rm",
			endpoint: refusing(http.StatusNotFound, "NoSuchBucket", "nope", ""),
			values:   given(map[string]any{"bucket": "nope", "key": "k"}),
			code:     "s3.bucket.notfound", input: "bucket"},
		{name: "a bucket that is not there", id: "s3.overview",
			endpoint: refusing(http.StatusNotFound, "NoSuchBucket", "nope", ""),
			values:   given(map[string]any{"bucket": []string{"nope"}}),
			code:     "s3.bucket.notfound", input: "bucket"},
		{name: "a source key that is not there", id: "s3.object.copy",
			endpoint: refusing(http.StatusNotFound, "NoSuchKey", "shop", "k"),
			values:   given(map[string]any{"bucket": "shop", "key": "k", "dest-key": "k2"}),
			code:     "s3.object.notfound", input: "key"},
		{name: "a source bucket that is not there", id: "s3.object.copy",
			endpoint: refusing(http.StatusNotFound, "NoSuchBucket", "shop", ""),
			values:   given(map[string]any{"bucket": "shop", "key": "k", "dest-key": "k2"}),
			code:     "s3.bucket.notfound", input: "bucket"},
		{name: "a destination bucket that is not there", id: "s3.object.copy",
			endpoint: refusing(http.StatusNotFound, "NoSuchBucket", "elsewhere", ""),
			values:   given(map[string]any{"bucket": "shop", "key": "k", "dest-bucket": "elsewhere", "dest-key": "k2"}),
			code:     "s3.bucket.notfound", input: "dest-bucket"},
		{name: "a destination that is taken", id: "s3.object.copy",
			endpoint: served(bucketContents{taken: true}),
			values:   given(map[string]any{"bucket": "test-bucket", "key": "k", "dest-key": "k2"}),
			code:     "s3.copy.taken", input: "dest-key"},
		{name: "a destination that is taken", id: "s3.object.rename",
			endpoint: served(bucketContents{taken: true}),
			values:   given(map[string]any{"bucket": "test-bucket", "key": "k", "dest-key": "k2"}),
			code:     "s3.rename.taken", input: "dest-key"},
		{name: "a bucket name S3 does not allow", id: "s3.object.list",
			values: given(map[string]any{"bucket": "b1"}),
			code:   "s3.bucket.invalid", input: "bucket"},
		{name: "more buckets than an overview reads", id: "s3.overview",
			values: given(map[string]any{"bucket": many}),
			code:   "s3.bucket.toomany", input: "bucket"},
		{name: "an out that is there already", id: "s3.object.get",
			values: given(map[string]any{"bucket": "shop", "key": "k", "out": there}),
			code:   "s3.object.exists", input: "out"},
		{name: "an out in a directory that is not there", id: "s3.object.get",
			values: given(map[string]any{"bucket": "shop", "key": "k", "out": filepath.Join(absent, "got.bin")}),
			code:   "s3.object.out", input: "out"},
		{name: "a file that is not there", id: "s3.object.set",
			values: given(map[string]any{"bucket": "shop", "key": "k", "file": absent}),
			code:   "s3.file.unreadable", input: "file"},
		{name: "a file under a file", id: "s3.object.set",
			values: given(map[string]any{"bucket": "shop", "key": "k", "file": filepath.Join(notes, "x")}),
			code:   "s3.file.unreadable", input: "file"},
		{name: "a directory for a file", id: "s3.object.set",
			values: func(t *testing.T) map[string]any {
				return map[string]any{"bucket": "shop", "key": "k", "file": dirIn(t)}
			},
			code: "s3.file.directory", input: "file"},
		{name: "standard input for a file", id: "s3.object.set",
			values: given(map[string]any{"bucket": "shop", "key": "k", "file": "/dev/stdin"}),
			code:   "s3.file.stdin", input: "file"},
		{name: "nowhere to write", id: "s3.bucket.download",
			values: given(map[string]any{"bucket": "shop"}),
			code:   "s3.download.nooutput", input: "out"},
		{name: "an out that is there already", id: "s3.bucket.download",
			endpoint: served(bucketContents{keys: []string{"a.txt"}}),
			values: func(t *testing.T) map[string]any {
				return map[string]any{"bucket": "test-bucket", "out": dirIn(t)}
			},
			code: "s3.download.exists", input: "out"},
		{name: "an out under a file", id: "s3.bucket.download",
			endpoint: served(bucketContents{keys: []string{"a.txt"}}),
			values:   given(map[string]any{"bucket": "test-bucket", "out": filepath.Join(notes, "copy")}),
			code:     "s3.download.create", input: "out"},
		{name: "a directory that is not there", id: "s3.bucket.upload",
			values: given(map[string]any{"bucket": "shop", "dir": absent}),
			code:   "s3.upload.missing", input: "dir"},
		{name: "a file for a directory", id: "s3.bucket.upload",
			values: given(map[string]any{"bucket": "shop", "dir": notes}),
			code:   "s3.upload.notadir", input: "dir"},
		{name: "a directory holding nothing", id: "s3.bucket.upload",
			values: func(t *testing.T) map[string]any { return map[string]any{"bucket": "shop", "dir": dirIn(t)} },
			code:   "s3.upload.empty", input: "dir"},
		{name: "an address holding a path", id: "s3.object.list",
			endpoint: func(*testing.T) string { return nothingListens + "/shop" },
			values:   given(map[string]any{"bucket": "shop"}),
			code:     "s3.conn.invalid", input: "address"},
		{name: "a CA file that is not there", id: "s3.object.list",
			values: given(map[string]any{"bucket": "shop", "ca-file": absent}),
			code:   "s3.tls.ca.unreadable", input: "ca-file"},
		{name: "a CA file holding no certificate", id: "s3.object.list",
			values: given(map[string]any{"bucket": "shop", "ca-file": notACA}),
			code:   "s3.tls.ca.invalid", input: "ca-file"},

		{name: "a connection nothing answers", id: "s3.object.list",
			values: given(map[string]any{"bucket": "shop"}),
			code:   "s3.conn.refused"},
		{name: "credentials the server rejects", id: "s3.object.list",
			endpoint: refusing(http.StatusForbidden, "InvalidAccessKeyId", "shop", ""),
			values:   given(map[string]any{"bucket": "shop"}),
			code:     "s3.auth.failed"},
		{name: "a refusal by the credentials' policy", id: "s3.object.get",
			endpoint: refusing(http.StatusForbidden, "AccessDenied", "shop", "k"),
			values:   given(map[string]any{"bucket": "shop", "key": "k"}),
			code:     "s3.denied"},
		{name: "a bucket with no policy, which is its state", id: "s3.policy.get",
			endpoint: refusing(http.StatusNotFound, "NoSuchBucketPolicy", "shop", ""),
			values:   given(map[string]any{"bucket": "shop"}),
			code:     "s3.policy.notfound"},
		{name: "a key a download's listing found, gone before it was fetched", id: "s3.bucket.download",
			endpoint: goneOnceListed,
			values: func(t *testing.T) map[string]any {
				return map[string]any{"bucket": "test-bucket", "out": filepath.Join(t.TempDir(), "copy")}
			},
			code: "s3.object.notfound"},
		{name: "a bucket larger than the limit, which a prefix answers as well", id: "s3.bucket.download",
			endpoint: served(bucketContents{keys: []string{"a.txt", "b.txt"}}),
			values: func(t *testing.T) map[string]any {
				return map[string]any{"bucket": "test-bucket", "out": filepath.Join(t.TempDir(), "copy"), "limit": 1}
			},
			code: "s3.download.toomany"},
		{name: "a prefix holding objects, which overwrite answers as well", id: "s3.bucket.upload",
			endpoint: served(bucketContents{keys: []string{"a.txt"}}),
			values: func(t *testing.T) map[string]any {
				return map[string]any{"bucket": "test-bucket", "dir": dirIn(t, "a.txt")}
			},
			code: "s3.upload.notempty"},
		{name: "neither a value nor a file", id: "s3.object.set",
			values: given(map[string]any{"bucket": "shop", "key": "k"}),
			code:   "s3.set.novalue"},
		{name: "an object too large to print, which out answers", id: "s3.object.get",
			endpoint: served(bucketContents{object: make([]byte, maxInline+1)}),
			values:   given(map[string]any{"bucket": "test-bucket", "key": "big.bin"}),
			code:     "s3.object.toolarge"},
	} {
		t.Run(tc.id+": "+tc.name, func(t *testing.T) {
			endpoint := nothingListens
			if tc.endpoint != nil {
				endpoint = tc.endpoint(t)
			}
			verr := refusedInput(t, tc.id, reqFor(t, tc.id, endpoint, tc.values(t)))
			if verr.Code != tc.code || verr.Input != tc.input {
				t.Errorf("%s naming %q, want %s naming %q (%s)", verr.Code, verr.Input, tc.code, tc.input, verr.Message)
			}
		})
	}
}

// given is a table entry's values, made afresh for each run: reqFor adds the
// connection to the map it is handed.
func given(values map[string]any) func(*testing.T) map[string]any {
	return func(*testing.T) map[string]any {
		out := make(map[string]any, len(values))
		for k, v := range values {
			out[k] = v
		}
		return out
	}
}

// A stream longer than one upload holds is the file input refused — a file
// states its size, and is the fix the refusal gives — where a stream that
// failed partway is a file that is there and could not be read, and names
// nothing. Driven at uploadFailure: reaching the bound through a pipe is
// stream_test.go's, at the cost of streaming the parts.
func TestAStreamTooLongNamesTheFileAndOneThatFailedNamesNone(t *testing.T) {
	r := reqFor(t, "s3.object.set", nothingListens, map[string]any{"bucket": "shop", "key": "k", "file": "/dev/fd/63"})
	set := capabilityByID(t, "s3.object.set")
	for _, tc := range []struct {
		why   string
		read  error
		code  string
		input string
	}{
		{"a stream past the bound", errStreamTooLong, "s3.file.toolarge", "file"},
		{"a stream that failed partway", io.ErrUnexpectedEOF, "s3.file.unreadable", ""},
	} {
		verr := uploadFailure(errors.New("the upload stopped"), &bounded{err: tc.read}, r)
		if verr.Code != tc.code || verr.Input != tc.input {
			t.Errorf("%s: %s naming %q, want %s naming %q", tc.why, verr.Code, verr.Input, tc.code, tc.input)
		}
		if _, declared := set.ErrorInput(verr); verr.Input != "" && !declared {
			t.Errorf("%s: %s names %q, which s3.object.set does not declare", tc.why, verr.Code, verr.Input)
		}
	}
}

// A file a value or a CA is read from that is there and that this process may
// not read names no input: the path is right, and the fix is a chmod, not the
// box.
func TestAFileThatCannotBeReadNamesNoInput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("mode bits do not stop this reader")
	}
	shut := fileIn(t, "shut", "value\n")
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     string
		values map[string]any
		code   string
	}{
		{"s3.object.set", map[string]any{"bucket": "shop", "key": "k", "file": shut}, "s3.file.unreadable"},
		{"s3.object.list", map[string]any{"bucket": "shop", "ca-file": shut}, "s3.tls.ca.unreadable"},
	} {
		if verr := refusedInput(t, tc.id, reqFor(t, tc.id, nothingListens, tc.values)); verr.Code != tc.code || verr.Input != "" {
			t.Errorf("%s: %s naming %q, want %s naming none", tc.id, verr.Code, verr.Input, tc.code)
		}
	}
}
