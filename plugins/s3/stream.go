package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A stream is what s3.object.set's --file names when it is not a regular file:
// a named pipe, `<(pg_dump app)`, /dev/null. None has a size to state or a
// start to go back to, and minio-go treats an *os.File as both — it seeks one
// back to its start before sending it, which a pipe refuses, so `--file <(cmd)`
// failed before a byte left, as "could not reach <endpoint>: seek …: illegal
// seek". And handed any reader with no size, minio-go sizes its parts for the
// largest object S3 allows, 5 TiB over 10,000 parts, and allocates a buffer for
// one before it reads a byte: 528 MiB, for a pipe carrying a few kilobytes.
//
// So a stream is read, never sized or seeked: its first part is read before
// anything is sent, and one that ends inside it — /dev/null, which is how an
// empty object is made since the value argument refuses "", and most pipes —
// goes up in one request of known size, exactly as a regular file does. Only
// a longer one becomes a multipart upload, in parts of streamPartSize, which
// is what bounds what it holds in memory: that part, and minio-go's own copy
// of the one it is sending.

// streamPartSize is the part a stream is read and sent in: minio-go's own
// part size for any object it can size under 156 GiB, and so the size it
// already uploads a regular file of that length in.
const streamPartSize = 16 << 20

// maxParts is S3's limit on the parts of one multipart upload.
const maxParts = 10000

// streamMost is the longest stream one upload can hold: maxParts parts of
// streamPartSize, 156.25 GiB. A var rather than a const so a test can lower
// it and reach the bound without streaming 156 GiB through a pipe — an
// unreached bound is one nothing would notice the removal of.
var streamMost int64 = streamPartSize * maxParts

// errStreamTooLong is a stream that ran past streamMost.
var errStreamTooLong = errors.New("the stream is longer than one upload can hold")

// descriptorDirs are the directories a process's own descriptors are listed
// under, each by its number: /dev/fd/0 is standard input, as /dev/stdin is.
var descriptorDirs = []string{"/dev/fd", "/proc/self/fd", "/proc/thread-self/fd"}

// namesStandardInput reports whether --file's path names a process's own
// standard input, which is the one stream a plugin is never handed. rta
// starts each plugin with /dev/null there, and what is piped to rta stays
// with rta, so `pg_dump app | rta s3 object set dump.sql --file /dev/stdin`
// read nothing and stored an empty object in place of whatever the key held,
// under a success message. Refused by name, before anything is opened:
// /dev/null named as itself is the same file and still makes an empty object
// on purpose, so the file cannot be told apart from it, only the name. A
// process substitution is the stream that does arrive — the descriptor is
// the shell's, handed down through rta — and is what the refusal points at
// instead.
//
// The descriptor is read as a number rather than matched as text, and the
// path made absolute first, because the name is all there is to go on and
// the kernel is lenient about it: macOS opens /dev/fd/00 as descriptor 0,
// and a list of spellings let that one store the empty object this refuses.
func namesStandardInput(path string) bool {
	p, err := filepath.Abs(plugin.ExpandHome(path))
	if err != nil {
		p = filepath.Clean(plugin.ExpandHome(path))
	}
	if p == "/dev/stdin" {
		return true
	}
	dir, name := filepath.Split(p)
	fd, err := strconv.Atoi(name)
	return err == nil && fd == 0 && slices.Contains(descriptorDirs, filepath.Clean(dir))
}

// stdinRefusal is the refusal of a --file naming standard input, pointing at
// what does carry a command's output. A process substitution is shell syntax,
// so it is offered only where a shell is what the caller typed into.
func stdinRefusal(req plugin.Request, path string) *view.Error {
	hint := "write it to a file and name that"
	if req.Surface() == plugin.SurfaceCLI {
		hint = "name the command instead, " + req.Surface().InputName("file") + " <(pg_dump app), or " + hint
	}
	return view.Errorf("s3.file.stdin",
		"%s is this plugin's own standard input, which holds nothing: what is piped to rta stays with rta", path).
		WithHint(hint)
}

// streamed reads the first part of r and returns what to upload and its size:
// the part itself and its length when r ended inside it, or the part followed
// by the rest of r and -1, "unknown, send it in parts", when it did not.
func streamed(r io.Reader) (io.Reader, int64, error) {
	head := make([]byte, streamPartSize)
	n, err := io.ReadFull(r, head)
	switch {
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return bytes.NewReader(head[:n]), int64(n), nil
	case err != nil:
		return nil, 0, err
	}
	return &bounded{r: io.MultiReader(bytes.NewReader(head), r), left: streamMost}, -1, nil
}

// bounded hands minio-go at most left bytes of a stream, and fails the upload
// rather than let it end there with more still to come.
//
// The failing is the point. minio-go uploads a stream of unknown length until
// it has sent its last permitted part and then completes the upload with what
// it has — a stream one byte past the bound would be stored as its first
// 156 GiB, under a success message. And the error has to arrive on a read
// that leaves a part short: minio-go reads a part with a readFull of its own
// that drops an error when the same read fills the part, so the read that
// finds the stream going on returns nothing at all rather than the bytes it
// read. The upload is then abandoned before it completes, and the object
// under the key is whatever it was before.
//
// err keeps what reading the stream itself failed with, so a failure on this
// machine is not reported as one on the network: minio-go hands a reader's
// error back as its own, and classify would have named the endpoint.
type bounded struct {
	r    io.Reader
	left int64
	err  error
}

func (b *bounded) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	switch {
	case err == nil && b.left == 0:
		var probe [1]byte
		m, perr := io.ReadFull(b.r, probe[:])
		switch {
		case m > 0:
			b.err = errStreamTooLong
			return 0, b.err
		case !errors.Is(perr, io.EOF):
			b.err = perr
			return 0, b.err
		}
		return n, io.EOF
	case err != nil && !errors.Is(err, io.EOF):
		b.err = err
	}
	return n, err
}

// uploadFailure is classify for an upload, which can also fail on this
// machine, reading the stream it sends. minio-go hands that failure back as
// its own, and classify would have named the endpoint for it.
func uploadFailure(err error, body io.Reader, req plugin.Request) *view.Error {
	b, ok := body.(*bounded)
	if !ok || b.err == nil {
		return classify(err, req)
	}
	file := req.String("file")
	if errors.Is(b.err, errStreamTooLong) {
		return view.Errorf("s3.file.toolarge", "%s is longer than %s, the most one upload of a stream can hold",
			file, format.Bytes(streamMost)).
			WithHint("nothing was stored: the upload was abandoned before it completed. Write it to a " +
				"file and upload that — a file states its size, and its parts are sized to fit it")
	}
	return view.Errorf("s3.file.unreadable", "reading %s: %v", file, b.err).
		WithHint("nothing was stored: the upload was abandoned before it completed")
}
