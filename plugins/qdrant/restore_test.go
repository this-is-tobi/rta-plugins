package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func snapshotOnDisk(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docs.snapshot")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRestoreRefusesMCP(t *testing.T) {
	r := req(t, "qdrant.restore", map[string]any{
		"collection": "docs", "file": snapshotOnDisk(t, "bytes"),
	}).WithSurface(plugin.SurfaceMCP)
	_, err := runRestore(t.Context(), r)
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "qdrant.human" {
		t.Fatalf("err = %v, want qdrant.human", err)
	}
	if !verr.Refusal {
		t.Error("the MCP gate is not marked as a refusal — the ledger would file it as a failure")
	}
}

func TestRestoreRefusesAMissingFile(t *testing.T) {
	_, err := runRestore(t.Context(), req(t, "qdrant.restore", map[string]any{
		"collection": "docs", "file": filepath.Join(t.TempDir(), "nope.snapshot"),
	}))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "qdrant.restore.missing" {
		t.Fatalf("err = %v, want qdrant.restore.missing", err)
	}
}

// An empty file restores as nothing; the refusal says what actually
// happened — the dump did not finish — which the server, seeing only a
// malformed upload, cannot.
func TestRestoreRefusesAnEmptyFile(t *testing.T) {
	_, err := runRestore(t.Context(), req(t, "qdrant.restore", map[string]any{
		"collection": "docs", "file": snapshotOnDisk(t, ""),
	}))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "qdrant.restore.empty" {
		t.Fatalf("err = %v, want qdrant.restore.empty", err)
	}
}

func TestRestoreDryRunTouchesNothing(t *testing.T) {
	srv, calls := newSnapshotServer(t, nil)
	v, err := runRestore(t.Context(), dryReq(t, "qdrant.restore", map[string]any{
		"collection": "docs", "file": snapshotOnDisk(t, "bytes"),
		"endpoint": strings.TrimPrefix(srv.URL, "http://"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("dry run reached the server %d times, want 0: %v", len(*calls), *calls)
	}
	if _, ok := v.(view.Text); !ok {
		t.Errorf("dry run answered %T, want a Text description", v)
	}
}

// The dump's no-overwrite rule pointing the other way: a collection holding
// points is somebody's live search, and it is never silently replaced.
func TestRestoreRefusesANonEmptyCollection(t *testing.T) {
	srv, calls := newSnapshotServer(t, map[string]http.HandlerFunc{
		"GET /collections/docs": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`{"points_count": 7, "segments_count": 1}`)))
		},
	})
	_, err := runRestore(t.Context(), req(t, "qdrant.restore", map[string]any{
		"collection": "docs", "file": snapshotOnDisk(t, "bytes"),
		"endpoint": strings.TrimPrefix(srv.URL, "http://"),
	}))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "qdrant.restore.notempty" {
		t.Fatalf("err = %v, want qdrant.restore.notempty", err)
	}
	if !strings.Contains(verr.Message, "7 points") {
		t.Errorf("the refusal does not say what is at stake: %q", verr.Message)
	}
	for _, c := range *calls {
		if strings.Contains(c, "upload") {
			t.Errorf("the upload happened despite the refusal: %v", *calls)
		}
	}
}

func TestRestoreUploadsIntoAMissingCollection(t *testing.T) {
	restoreEndToEnd(t, func(routes map[string]http.HandlerFunc) map[string]any {
		// GET /collections/docs is deliberately absent from routes: the 404
		// is what a fresh target looks like, and recovery creates it.
		return map[string]any{}
	})
}

func TestRestoreReplaceHandsTheCollectionToTheSnapshot(t *testing.T) {
	restoreEndToEnd(t, func(routes map[string]http.HandlerFunc) map[string]any {
		served := false
		routes["GET /collections/docs"] = func(w http.ResponseWriter, _ *http.Request) {
			// First read is the pre-flight (holding points); the second is
			// the read-back after recovery.
			if !served {
				served = true
				_, _ = w.Write([]byte(envelope(`{"points_count": 7, "segments_count": 1}`)))
				return
			}
			_, _ = w.Write([]byte(envelope(`{"points_count": 42, "segments_count": 1}`)))
		}
		return map[string]any{"replace": true}
	})
}

// restoreEndToEnd drives a full restore and asserts the wire-level facts
// both paths share: the multipart body carries the file's bytes, the upload
// asks for priority=snapshot (without it a distributed deployment prefers
// what its replicas already hold — the restore that restores nothing), and
// the receipt's count is read back from the server rather than assumed.
func restoreEndToEnd(t *testing.T, arrange func(map[string]http.HandlerFunc) map[string]any) {
	t.Helper()
	content := "binary-snapshot-bytes\x00\x01"
	var uploaded string
	var query string

	routes := map[string]http.HandlerFunc{
		"POST /collections/docs/snapshots/upload": func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("the upload is not multipart: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f, _, err := r.FormFile("snapshot")
			if err != nil {
				t.Errorf("no \"snapshot\" form file: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			raw, _ := io.ReadAll(f)
			uploaded = string(raw)
			_, _ = w.Write([]byte(envelope(`true`)))
		},
	}
	extra := arrange(routes)
	if _, ok := routes["GET /collections/docs"]; !ok {
		// The fresh-target path still needs the read-back to answer, once,
		// after the upload has happened.
		recovered := false
		routes["GET /collections/docs"] = func(w http.ResponseWriter, _ *http.Request) {
			if !recovered && uploaded == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"status":{"error":"Not found"}}`))
				return
			}
			recovered = true
			_, _ = w.Write([]byte(envelope(`{"points_count": 42, "segments_count": 1}`)))
		}
	}
	srv, _ := newSnapshotServer(t, routes)

	values := map[string]any{
		"collection": "docs", "file": snapshotOnDisk(t, content),
		"endpoint": strings.TrimPrefix(srv.URL, "http://"),
	}
	for k, v := range extra {
		values[k] = v
	}
	v, err := runRestore(t.Context(), req(t, "qdrant.restore", values))
	if err != nil {
		t.Fatal(err)
	}

	if uploaded != content {
		t.Errorf("the server received %q, want the file's bytes", uploaded)
	}
	if !strings.Contains(query, "priority=snapshot") {
		t.Errorf("the upload does not make the snapshot the authority: query = %q", query)
	}
	if !strings.Contains(query, "wait=true") {
		t.Errorf("the upload does not wait for recovery to finish: query = %q", query)
	}
	if holds := pairValue(t, v, "now holds"); !strings.Contains(holds, "42 points") {
		t.Errorf("the receipt's count is not the server's read-back: %q", holds)
	}
}

// **A count Qdrant did not report is not a count of zero, and this guard
// stands in front of a destructive write.**
//
// Qdrant leaves points_count out while a collection is still loading, so a
// collection holding millions of points reports nil until it finishes. The
// check read `PointsCount != nil && *PointsCount > 0`, so nil fell straight
// through to "nothing to lose" and the restore overwrote it without
// --replace ever being typed — the one flag whose whole job is to make that
// intent explicit.
func TestRestoreWillNotOverwriteACollectionWhoseCountWasNotReported(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		// A real Qdrant answer for a collection that is still loading: a
		// status, and no points_count at all.
		"/collections/docs": `{"result":{"status":"yellow","segments_count":2},"status":"ok"}`,
	})
	verr := checkTarget(t.Context(), reqAt(t, f, "qdrant.restore", map[string]any{
		"collection": "docs", "file": snapshotOnDisk(t, "bytes"),
	}), "docs")

	if verr == nil {
		t.Fatal("a collection that did not report its point count was treated as empty and overwritten")
	}
	if !strings.Contains(verr.Hint, "--replace") {
		t.Errorf("the refusal does not name the way through: %+v", verr)
	}
}

// --replace is still the way through, since saying so is the whole point of
// the flag.
func TestReplaceStillOverwritesAnUncountedCollection(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections/docs": `{"result":{"status":"yellow"},"status":"ok"}`,
	})
	if verr := checkTarget(t.Context(), reqAt(t, f, "qdrant.restore", map[string]any{
		"collection": "docs", "replace": true, "file": snapshotOnDisk(t, "bytes"),
	}), "docs"); verr != nil {
		t.Fatalf("--replace was refused: %+v", verr)
	}
}

// And a collection that reports zero really is empty, so recovery into a
// freshly-created collection is not made to pass a flag it does not need.
func TestACollectionReportingZeroPointsStillPasses(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections/docs": `{"result":{"status":"green","points_count":0},"status":"ok"}`,
	})
	if verr := checkTarget(t.Context(), reqAt(t, f, "qdrant.restore", map[string]any{
		"collection": "docs", "file": snapshotOnDisk(t, "bytes"),
	}), "docs"); verr != nil {
		t.Fatalf("an empty collection was refused: %+v", verr)
	}
}
