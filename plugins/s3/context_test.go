package main

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The host keeps a plugin process around between calls, so whatever one call
// leaves behind is still there for the next. Two things a handler can leave:
// a call the caller already gave up on, still running to completion because
// the handler never looked at its context; and the client library's own
// listing goroutine, parked forever after a loop that stopped reading before
// the bucket ran out. Neither shows against a small bucket, and an agent
// paging through a large one produces both on every call.

// Every handler that reaches the endpoint, driven with a context already
// cancelled, has to come back with a coded error — and without a round trip,
// which is what "promptly" means here and the thing a fake server can count.
// s3.object.presign is not in the table: signing a URL is local arithmetic
// that touches neither the network nor the context.
func TestACancelledRequestStopsEveryHandlerBeforeItReachesTheEndpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		id     string
		values map[string]any
		run    func(context.Context, plugin.Request) (view.View, error)
	}{
		{"s3.overview", map[string]any{}, runOverview},
		{"s3.bucket.list", map[string]any{}, runBucketList},
		{"s3.policy.get", map[string]any{"bucket": "test-bucket"}, runPolicyGet},
		{"s3.object.list", map[string]any{"bucket": "test-bucket"}, runObjectList},
		{"s3.object.tree", map[string]any{"bucket": "test-bucket"}, runObjectTree},
		{"s3.object.show", map[string]any{"bucket": "test-bucket", "key": "k"}, runObjectShow},
		{"s3.object.get", map[string]any{"bucket": "test-bucket", "key": "k"}, runObjectGet},
		{"s3.object.set", map[string]any{"bucket": "test-bucket", "key": "k", "value": "x"}, runObjectSet},
		{"s3.object.copy", map[string]any{"bucket": "test-bucket", "key": "k", "dest-key": "k2"}, runObjectCopy},
		{"s3.object.rename", map[string]any{"bucket": "test-bucket", "key": "k", "dest-key": "k2"}, runObjectRename},
		{"s3.object.rm", map[string]any{"bucket": "test-bucket", "key": "k"}, runObjectRemove},
		{"s3.bucket.download", map[string]any{"bucket": "test-bucket",
			"out": filepath.Join(t.TempDir(), "copy")}, runBucketDownload},
		{"s3.bucket.upload", map[string]any{"bucket": "test-bucket",
			"dir": dirWithFiles(t, map[string]string{"a.txt": "x"})}, runBucketUpload},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			srv, asked := recordingS3(t, keysXML)
			_, err := tc.run(ctx, reqFor(t, tc.id, endpointOf(t, srv), tc.values))
			var verr *view.Error
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want a *view.Error for a call nobody is waiting on", err)
			}
			if got := asked(); len(got) != 0 {
				t.Errorf("a cancelled call still reached the endpoint: %v", got)
			}
		})
	}
}

// A loop that stops before the listing is exhausted — a page limit, a
// completion cap, a pre-flight that only needs one key — must not leave the
// client library's goroutine behind. The check is on the process rather
// than on a counter: after the handler has returned, nothing may still be
// executing inside minio-go, whatever mechanism it used to feed the loop.
func TestAListingThatStopsEarlyLeavesNoGoroutineBehind(t *testing.T) {
	srv := listingServer(t, 500, 10)
	endpoint := endpointOf(t, srv)

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"s3.object.list at its page limit", func(t *testing.T) {
			r := reqFor(t, "s3.object.list", endpoint, map[string]any{"bucket": "test-bucket", "limit": 10})
			if _, err := runObjectList(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		}},
		{"s3.object.tree at its key limit", func(t *testing.T) {
			r := reqFor(t, "s3.object.tree", endpoint, map[string]any{"bucket": "test-bucket", "limit": 10})
			if _, err := runObjectTree(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		}},
		{"a completion at its cap", func(t *testing.T) {
			r := reqFor(t, "s3.object.list", endpoint, map[string]any{"bucket": "test-bucket"})
			if got := suggestKeys("prefix")(context.Background(), r); len(got) != completionCap {
				t.Fatalf("completion returned %d keys, want the cap of %d", len(got), completionCap)
			}
		}},
		{"the upload pre-flight at its first key", func(t *testing.T) {
			r := reqFor(t, "s3.bucket.upload", endpoint, map[string]any{"bucket": "test-bucket",
				"dir": dirWithFiles(t, map[string]string{"a.txt": "x"})})
			_, err := runBucketUpload(context.Background(), r)
			var verr *view.Error
			if !errors.As(err, &verr) || verr.Code != "s3.upload.notempty" {
				t.Fatalf("err = %v, want s3.upload.notempty", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t)
			if left := goroutinesInside("github.com/minio/minio-go"); len(left) != 0 {
				t.Errorf("%d goroutine(s) still inside minio-go after the handler returned:\n\n%s",
					len(left), strings.Join(left, "\n\n"))
			}
		})
	}
}

// goroutinesInside returns the stack of every goroutine with a frame in the
// named package. The calling goroutine has already returned from whatever it
// ran, so its own stack never matches.
func goroutinesInside(pkg string) []string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var found []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, pkg) {
			found = append(found, g)
		}
	}
	return found
}
