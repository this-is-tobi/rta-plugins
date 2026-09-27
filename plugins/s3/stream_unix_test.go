//go:build unix

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/view"
)

// Unix only, for mkfifo(2) and /dev/null: the streams --file has to take
// are a Unix shell's, `--file <(pg_dump app)` and `--file /dev/null`.

// storedS3 is an S3 that stores what it is sent, in one PUT or in parts, so
// a test can compare the object that arrived with the bytes that left. It
// speaks the four requests an upload makes: a PUT of the whole object, and a
// multipart upload's start, parts and completion.
type storedS3 struct {
	*httptest.Server
	mu      sync.Mutex
	whole   map[string]string // key → body of a single PUT
	parts   []int             // the size of each part, in the order they arrived
	joined  string            // a completed multipart object, its parts in order
	aborted bool              // a multipart upload was abandoned
}

func newStoredS3(t *testing.T) *storedS3 {
	t.Helper()
	s := &storedS3{whole: map[string]string{}}
	pending := map[string]string{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
		q := r.URL.Query()
		body, _ := io.ReadAll(r.Body)
		if strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
			body = decodeAWSChunks(t, body)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && q.Has("uploads"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><Bucket>test-bucket</Bucket>` +
				`<Key>` + key + `</Key><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`))
		case r.Method == http.MethodPut && q.Has("partNumber"):
			s.parts = append(s.parts, len(body))
			pending[q.Get("partNumber")] = string(body)
			w.Header().Set("ETag", `"part-`+q.Get("partNumber")+`"`)
		case r.Method == http.MethodPost && q.Has("uploadId"):
			for n := 1; ; n++ {
				part, ok := pending[strconv.Itoa(n)]
				if !ok {
					break
				}
				s.joined += part
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<CompleteMultipartUploadResult><Bucket>test-bucket</Bucket>` +
				`<Key>` + key + `</Key><ETag>"whole"</ETag></CompleteMultipartUploadResult>`))
		case r.Method == http.MethodDelete && q.Has("uploadId"):
			s.aborted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut:
			s.whole[key] = string(body)
			w.Header().Set("ETag", `"e"`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// The readers below take the lock the handler writes under: the race
// detector cannot see the response a handler wrote as ordering its writes
// before the test's reads.
func (s *storedS3) object(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.whole[key]
	return body, ok
}

func (s *storedS3) sentParts() ([]int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.parts), s.joined
}

func (s *storedS3) abandoned() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.aborted
}

// fifoWith makes a named pipe and writes content into it once something
// opens it to read, the way `--file <(printf ...)` hands the command one.
func fifoWith(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		// A reader that gave up early closes its end, and the write fails
		// with EPIPE: the test that gave up already says why.
		_, _ = f.Write(content)
		_ = f.Close()
	}()
	t.Cleanup(func() {
		// Unblocks the writer if the handler never opened the pipe.
		if f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
		<-done
	})
	return path
}

func setFrom(t *testing.T, srv *storedS3, file string) (view.View, error) {
	t.Helper()
	return runObjectSet(t.Context(), reqFor(t, "s3.object.set", endpointOf(t, srv.Server),
		map[string]any{"bucket": "test-bucket", "key": "some/key", "file": file}))
}

// A named pipe has no size and cannot be rewound, and minio-go seeks an
// *os.File back to its start before it sends it. So `--file <(cmd)` failed
// before a byte left, as "could not reach <endpoint>: seek …: illegal seek"
// — naming the network for a local file it never started to read.
func TestAPipeIsUploadedWhole(t *testing.T) {
	srv := newStoredS3(t)
	v, err := setFrom(t, srv, fifoWith(t, []byte("hello from a pipe")))
	if err != nil {
		t.Fatalf("a pipe was not uploaded: %v", err)
	}
	if got, _ := srv.object("some/key"); got != "hello from a pipe" {
		t.Errorf("stored %q, want what went into the pipe", got)
	}
	if got, want := v.(view.Text).Body, "set test-bucket/some/key (17 B)"; got != want {
		t.Errorf("receipt = %q, want %q", got, want)
	}
}

// /dev/null is how an empty object is made — the value argument refuses ""
// — and it has to stay one plain PUT of nothing, the request it has always
// been, rather than a multipart upload of one empty part, which a server is
// entitled to refuse.
func TestDevNullStillMakesAnEmptyObjectInOneRequest(t *testing.T) {
	srv := newStoredS3(t)
	v, err := setFrom(t, srv, os.DevNull)
	if err != nil {
		t.Fatalf("/dev/null was not uploaded: %v", err)
	}
	if got, ok := srv.object("some/key"); !ok || got != "" {
		t.Errorf("stored %q (present %v), want one empty PUT", got, ok)
	}
	if parts, _ := srv.sentParts(); len(parts) > 0 {
		t.Errorf("sent as %d parts, want a single PUT", len(parts))
	}
	if got, want := v.(view.Text).Body, "set test-bucket/some/key (0 B)"; got != want {
		t.Errorf("receipt = %q, want %q", got, want)
	}
}

// A stream longer than one part goes up in parts of streamPartSize, and in
// order. minio-go sizes the parts of an upload of unknown length for the
// largest object S3 allows, and holds one in memory: 528 MiB, for a pipe
// carrying a few kilobytes.
func TestALongStreamIsUploadedInModestParts(t *testing.T) {
	content := []byte(strings.Repeat("0123456789abcdef", streamPartSize/16) + "tail")
	srv := newStoredS3(t)
	if _, err := setFrom(t, srv, fifoWith(t, content)); err != nil {
		t.Fatalf("a long pipe was not uploaded: %v", err)
	}
	parts, joined := srv.sentParts()
	if want := []int{streamPartSize, 4}; !slices.Equal(parts, want) {
		t.Errorf("parts = %v, want %v", parts, want)
	}
	if joined != string(content) {
		t.Errorf("the stored object is %d bytes and not what was sent (%d bytes)", len(joined), len(content))
	}
}

// A directory opens, and its size reads as a few dozen bytes, so it went up
// the way a file does and failed on the first read as a connection error.
func TestADirectoryIsRefusedBeforeAnythingIsSent(t *testing.T) {
	srv, asked := recordingS3(t, "")
	dir := t.TempDir()
	_, err := runObjectSet(t.Context(), reqFor(t, "s3.object.set", endpointOf(t, srv),
		map[string]any{"bucket": "test-bucket", "key": "some/key", "file": dir}))
	if err == nil {
		t.Fatal("a directory given as --file was uploaded")
	}
	verr := view.AsError(err, "none")
	if verr.Code != "s3.file.directory" || verr.Hint == "" {
		t.Errorf("a directory given as --file: %+v, want a hinted s3.file.directory", verr)
	}
	if hits := asked(); len(hits) > 0 {
		t.Errorf("a refused upload reached the server: %v", hits)
	}
}

// failingAfter reads as n zero bytes and then fails, as a device can, or a
// pipe on a network mount, partway through a stream.
type failingAfter struct {
	n   int
	err error
}

func (f *failingAfter) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, f.err
	}
	m := min(len(p), f.n)
	clear(p[:m])
	f.n -= m
	return m, nil
}

// A stream that fails partway through is the file's failure, and says so.
// minio-go hands a reader's error back as its own, and classify would have
// named the endpoint for it: "could not reach …" for a local read that went
// wrong. The upload it had begun is abandoned rather than completed with the
// parts that did arrive.
//
// No path fails a read on demand, so this drives the three steps
// runObjectSet takes for a stream — streamed, PutObject in streamPartSize
// parts, uploadFailure — against the same server the tests above upload to.
func TestAStreamThatFailsPartwayIsReportedAsTheFiles(t *testing.T) {
	srv := newStoredS3(t)
	req := reqFor(t, "s3.object.set", endpointOf(t, srv.Server),
		map[string]any{"bucket": "test-bucket", "key": "some/key", "file": "/dev/fd/63"})
	_, err := withClient(t.Context(), req, func(ctx context.Context, client *minio.Client) (view.View, error) {
		body, size, err := streamed(&failingAfter{n: streamPartSize + 1, err: errors.New("input/output error")})
		if err != nil {
			t.Fatalf("the first part was readable and was refused: %v", err)
		}
		_, err = client.PutObject(ctx, "test-bucket", "some/key", body, size,
			minio.PutObjectOptions{PartSize: streamPartSize})
		if err != nil {
			return nil, uploadFailure(err, body, req)
		}
		return nil, nil
	})
	verr := view.AsError(err, "none")
	if verr == nil || verr.Code != "s3.file.unreadable" || !strings.Contains(verr.Message, "/dev/fd/63") ||
		!strings.Contains(verr.Message, "input/output error") {
		t.Fatalf("a stream that failed partway: %+v, want s3.file.unreadable naming the file and why", verr)
	}
	if _, joined := srv.sentParts(); joined != "" {
		t.Errorf("a %d-byte object was completed from a stream that failed", len(joined))
	}
	if !srv.abandoned() {
		t.Error("the multipart upload was left open rather than abandoned")
	}
}

// lowerStreamMost sets the longest stream an upload holds to parts parts, for
// one test: the real bound is 156 GiB, which no test streams through a pipe.
func lowerStreamMost(t *testing.T, parts int64) {
	t.Helper()
	was := streamMost
	streamMost = parts * streamPartSize
	t.Cleanup(func() { streamMost = was })
}

// A stream longer than one upload can hold is refused, and the upload
// abandoned before it completes. Left to minio-go, it stops asking for more
// after its last part and completes the upload with what it has: the
// stream's first 156 GiB, stored under the key as if it were the whole of it.
func TestAStreamPastTheLastPartIsRefusedNotCutShort(t *testing.T) {
	lowerStreamMost(t, 2)
	srv := newStoredS3(t)
	_, err := setFrom(t, srv, fifoWith(t, make([]byte, 2*streamPartSize+1)))
	if _, joined := srv.sentParts(); err == nil {
		t.Fatalf("a stream one byte past the bound was stored, as %d bytes", len(joined))
	}
	verr := view.AsError(err, "none")
	if verr.Code != "s3.file.toolarge" || verr.Hint == "" {
		t.Fatalf("a stream one byte past the bound: %+v, want a hinted s3.file.toolarge", verr)
	}
	if _, joined := srv.sentParts(); joined != "" {
		t.Errorf("a %d-byte object was completed from a stream that went on", len(joined))
	}
	if !srv.abandoned() {
		t.Error("the multipart upload was left open rather than abandoned")
	}
}

// And one that ends exactly on the bound is whole, not refused: the read
// that finds the end has to tell the end from more to come.
func TestAStreamEndingOnTheLastPartIsUploadedWhole(t *testing.T) {
	lowerStreamMost(t, 2)
	content := []byte(strings.Repeat("x", 2*streamPartSize))
	srv := newStoredS3(t)
	if _, err := setFrom(t, srv, fifoWith(t, content)); err != nil {
		t.Fatalf("a stream exactly as long as the bound was refused: %v", err)
	}
	if parts, joined := srv.sentParts(); !slices.Equal(parts, []int{streamPartSize, streamPartSize}) ||
		joined != string(content) {
		t.Errorf("parts = %v, stored %d bytes, want two whole parts and all %d bytes", parts, len(joined), len(content))
	}
}

// A stream's size is not known until it has been read, and a preview reads
// nothing, so it has to say so. It printed the unknown size as "0 B",
// predicting an empty object for whatever the pipe was about to carry.
func TestAStreamPreviewSaysItsSizeIsUnknown(t *testing.T) {
	srv, asked := recordingS3(t, "")
	v, err := runObjectSet(t.Context(), dryReq(t, "s3.object.set", endpointOf(t, srv),
		map[string]any{"bucket": "test-bucket", "key": "some/key", "file": fifoWith(t, []byte("hello"))}))
	if err != nil {
		t.Fatal(err)
	}
	body := v.(view.Text).Body
	if !strings.Contains(body, "size unknown") || strings.Contains(body, "0 B") {
		t.Errorf("preview = %q, want it to say the size is unknown", body)
	}
	if hits := asked(); len(hits) > 0 {
		t.Errorf("--dry-run reached the server: %v", hits)
	}
}

// A plugin's standard input is not the caller's: rta starts every plugin with
// /dev/null there. So `pg_dump app | rta s3 object set dump.sql --file
// /dev/stdin` read nothing, and stored an empty object in place of whatever
// the key held under a success message. Each spelling of it is refused
// before anything is opened or sent.
func TestStandardInputIsRefusedRatherThanStoredEmpty(t *testing.T) {
	for _, path := range []string{"/dev/stdin", "/dev/fd/0", "/proc/self/fd/0", "/dev/./stdin",
		"/dev/fd/00", "/proc/thread-self/fd/0"} {
		srv, asked := recordingS3(t, "")
		_, err := runObjectSet(t.Context(), reqFor(t, "s3.object.set", endpointOf(t, srv),
			map[string]any{"bucket": "test-bucket", "key": "some/key", "file": path}))
		if verr := view.AsError(err, "none"); verr == nil || verr.Code != "s3.file.stdin" || verr.Hint == "" {
			t.Errorf("--file %s: %+v, want a hinted s3.file.stdin", path, verr)
		}
		if hits := asked(); len(hits) > 0 {
			t.Errorf("--file %s reached the server: %v", path, hits)
		}
	}
}

// Reading the descriptor as a number must not reach past descriptor 0: a
// process substitution arrives as another descriptor under the same
// directory, and /dev/null named as itself is how an empty object is made.
func TestOnlyStandardInputIsTakenForIt(t *testing.T) {
	for _, path := range []string{os.DevNull, "/dev/fd/63", "/dev/fd/10", "/proc/self/fd/3", "/tmp/fd/0", "/dev/fd"} {
		if namesStandardInput(path) {
			t.Errorf("--file %s was taken for standard input", path)
		}
	}
}
