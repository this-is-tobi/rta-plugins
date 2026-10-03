package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// newSnapshotServer routes by "METHOD /path" and records every call in
// order. fakeQdrant cannot drive these handlers: a snapshot download is
// binary, an upload is multipart, and both sides of the transfer care about
// the method, which a body-per-path map cannot express.
func newSnapshotServer(t *testing.T,
	routes map[string]http.HandlerFunc) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		calls = append(calls, key)
		if h, ok := routes[key]; ok {
			h(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":{"error":"Not found"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// envelope wraps a result the way Qdrant's REST API does.
func envelope(result string) string {
	return `{"result":` + result + `,"status":"ok","time":0}`
}

func dryReq(t *testing.T, capID string, values map[string]any) plugin.Request {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == capID {
			return plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), true, false)
		}
	}
	t.Fatalf("no capability %q", capID)
	return plugin.Request{}
}

func pairValue(t *testing.T, v view.View, key string) string {
	t.Helper()
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("view is %T, want KeyValue", v)
	}
	for _, p := range kv.Pairs {
		if p.Key == key {
			return p.Value
		}
	}
	t.Fatalf("no %q pair in %v", key, kv.Pairs)
	return ""
}

// An agent must never be able to pull a whole collection — payloads and
// vectors both — through the surface grants exist to gate. The refusal is
// marked as a refusal, so the ledger files it under policy rather than
// under "the work broke".
func TestDumpRefusesMCP(t *testing.T) {
	r := req(t, "qdrant.dump", map[string]any{
		"collection": "docs", "out": filepath.Join(t.TempDir(), "d.snapshot"),
	}).WithSurface(plugin.SurfaceMCP)
	_, err := runDump(t.Context(), r)
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "qdrant.human" {
		t.Fatalf("err = %v, want qdrant.human", err)
	}
	if !verr.Refusal {
		t.Error("the MCP gate is not marked as a refusal — the ledger would file it as a failure")
	}
	if !strings.Contains(verr.Hint, "the `qdrant_points_scroll` tool") {
		t.Errorf("the hint does not name the bounded alternative as the agent calls it: %q", verr.Hint)
	}
}

func TestDumpRequiresAnOutput(t *testing.T) {
	_, err := runDump(t.Context(), req(t, "qdrant.dump", map[string]any{"collection": "docs"}))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "qdrant.dump.nooutput" {
		t.Fatalf("err = %v, want qdrant.dump.nooutput", err)
	}
}

// The early stat comes before any network call, so refusing an existing
// file costs the server nothing — and, more to the point, a dump that will
// be refused never makes the server snapshot a collection first.
func TestDumpRefusesAnExistingFileBeforeTouchingTheServer(t *testing.T) {
	srv, calls := newSnapshotServer(t, nil)
	path := filepath.Join(t.TempDir(), "d.snapshot")
	if err := os.WriteFile(path, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runDump(t.Context(), req(t, "qdrant.dump", map[string]any{
		"collection": "docs", "out": path,
		"endpoint": strings.TrimPrefix(srv.URL, "http://"),
	}))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "qdrant.dump.exists" {
		t.Fatalf("err = %v, want qdrant.dump.exists", err)
	}
	if len(*calls) != 0 {
		t.Errorf("the server was reached %d times before the refusal, want 0: %v", len(*calls), *calls)
	}
}

func TestDumpDryRunTouchesNothing(t *testing.T) {
	srv, calls := newSnapshotServer(t, nil)
	path := filepath.Join(t.TempDir(), "d.snapshot")
	v, err := runDump(t.Context(), dryReq(t, "qdrant.dump", map[string]any{
		"collection": "docs", "out": path,
		"endpoint": strings.TrimPrefix(srv.URL, "http://"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("dry run reached the server %d times, want 0: %v", len(*calls), *calls)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("dry run created the output file")
	}
	if _, ok := v.(view.Text); !ok {
		t.Errorf("dry run answered %T, want a Text description", v)
	}
}

// A dry run names the instance the way the reader reaches it again, which
// through a forward is the profile and not the forward's end.
func TestADryRunNamesTheProfileAndItsForward(t *testing.T) {
	values := func(extra map[string]any) map[string]any {
		extra["endpoint"], extra["collection"] = "127.0.0.1:54321", "docs"
		return extra
	}
	dump, err := runDump(t.Context(), dryReq(t, "qdrant.dump",
		values(map[string]any{"out": filepath.Join(t.TempDir(), "d.snapshot")})).WithProfile("prod", plugin.TunnelKube))
	if err != nil {
		t.Fatal(err)
	}
	if body := dump.(view.Text).Body; !strings.Contains(body, "would ask profile prod (through its kube: forward) to snapshot") {
		t.Errorf("dump dry run = %q", body)
	}
	restore, err := runRestore(t.Context(), dryReq(t, "qdrant.restore",
		values(map[string]any{"file": snapshotOnDisk(t, "bytes")})).WithProfile("prod", plugin.TunnelKube))
	if err != nil {
		t.Fatal(err)
	}
	if body := restore.(view.Text).Body; !strings.Contains(body, "to profile prod (through its kube: forward), recovering") {
		t.Errorf("restore dry run = %q", body)
	}
}

func TestDumpCreatesDownloadsAndDeletes(t *testing.T) {
	snapshot := []byte("binary-snapshot-bytes\x00\x01\x02")
	srv, calls := newSnapshotServer(t, map[string]http.HandlerFunc{
		"GET /collections/docs": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`{"points_count": 42, "segments_count": 1, "status": "green"}`)))
		},
		"GET /": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"title":"qdrant","version":"1.12.0"}`))
		},
		"POST /collections/docs/snapshots": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("wait") != "true" {
				t.Error("snapshot creation was not asked to wait")
			}
			_, _ = w.Write([]byte(envelope(`{"name":"docs-2026.snapshot","size":26}`)))
		},
		"GET /collections/docs/snapshots/docs-2026.snapshot": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(snapshot)
		},
		"DELETE /collections/docs/snapshots/docs-2026.snapshot": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`true`)))
		},
	})

	path := filepath.Join(t.TempDir(), "docs.snapshot")
	v, err := runDump(t.Context(), req(t, "qdrant.dump", map[string]any{
		"collection": "docs", "out": path,
		"endpoint": strings.TrimPrefix(srv.URL, "http://"),
	}))
	if err != nil {
		t.Fatal(err)
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(snapshot) {
		t.Errorf("file holds %q, want the snapshot bytes", got)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600 — this is every payload and vector in the collection", perm)
	}

	// Create before download before delete, with the source described first:
	// the order is the design, not an accident of the implementation.
	want := []string{
		"GET /collections/docs",
		"GET /",
		"POST /collections/docs/snapshots",
		"GET /collections/docs/snapshots/docs-2026.snapshot",
		"DELETE /collections/docs/snapshots/docs-2026.snapshot",
	}
	if fmt.Sprint(*calls) != fmt.Sprint(want) {
		t.Errorf("calls = %v, want %v", *calls, want)
	}

	// A call qdrant.restore takes: the snapshot by its place, the collection
	// and the endpoint as flags. It once gave the collection by place too,
	// which the CLI refuses as an unexpected argument.
	endpoint := strings.TrimPrefix(srv.URL, "http://")
	if restore := pairValue(t, v, "restore with"); restore != "rta qdrant restore "+path+
		" --collection docs --endpoint "+endpoint {
		t.Errorf("the receipt does not name a restore the CLI takes: %q", restore)
	}
	if at := pairValue(t, v, "at rest"); !strings.Contains(at, "unencrypted") {
		t.Errorf("the receipt does not say the file is unencrypted: %q", at)
	}
	if c := pairValue(t, v, "contents"); !strings.Contains(c, "42 points") {
		t.Errorf("the receipt does not report what was dumped: %q", c)
	}
}

// The restore connects as protected as the dump did. A dump over HTTPS
// printed a line with neither tls nor ca-file, which ran over plain HTTP on a
// machine whose config said nothing: the api-key went in the clear. TLS off
// stays off the line, since that is what a tunnel forces for the forward
// alone — and the api-key never goes on it.
func TestTheRestoreConnectsAsProtectedAsTheDump(t *testing.T) {
	for _, tc := range []struct {
		name    string
		values  map[string]any
		want    []string
		without []string
	}{
		{name: "over HTTPS", values: map[string]any{"tls": true}, want: []string{" --tls"}},
		{
			name:    "trusting a CA",
			values:  map[string]any{"ca-file": "/etc/qdrant/ca.pem"},
			want:    []string{"--ca-file /etc/qdrant/ca.pem"},
			without: []string{"--tls"},
		},
		{name: "plain HTTP, as a tunnel forces it", values: map[string]any{"tls": false}, without: []string{"--tls"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.values["endpoint"] = "qdrant.internal:6333"
			tc.values["api-key"] = "hunter2"
			got := restoreCommand(req(t, "qdrant.dump", tc.values), "docs", "/backups/docs.snapshot")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("restore = %q, missing %q", got, w)
				}
			}
			for _, w := range append(tc.without, "hunter2", "api-key") {
				if strings.Contains(got, w) {
					t.Errorf("restore = %q, want no %q in it", got, w)
				}
			}
		})
	}
}

// The restore line reaches the instance the dump came from again. Through a
// profile it names the profile, whose credentials the dump may have used;
// through a forward the host opened on it, the profile alone, since the
// endpoint was the forward's end on 127.0.0.1 and nothing listens there once
// the dump is over; and reached directly, the endpoint too, which may be one
// typed over the profile's.
func TestTheRestoreLineReachesTheSameInstanceAgain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		tunnel  plugin.Tunnel
		want    string
	}{
		{"no profile", "", plugin.TunnelNone,
			"rta qdrant restore /backups/docs.snapshot --collection docs --endpoint qdrant.internal:6333"},
		{"a profile reached directly", "prod", plugin.TunnelNone,
			"rta qdrant restore /backups/docs.snapshot --collection docs --profile prod --endpoint qdrant.internal:6333"},
		{"a profile through a forward", "prod", plugin.TunnelKube,
			"rta qdrant restore /backups/docs.snapshot --collection docs --profile prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := "qdrant.internal:6333"
			if tc.tunnel != plugin.TunnelNone {
				endpoint = "127.0.0.1:54321"
			}
			r := req(t, "qdrant.dump", map[string]any{"endpoint": endpoint}).WithProfile(tc.profile, tc.tunnel)
			if got := restoreCommand(r, "docs", "/backups/docs.snapshot"); got != tc.want {
				t.Errorf("restore = %q, want %q", got, tc.want)
			}
		})
	}
}

// What a receipt says was reached is the instance the reader can reach again:
// never the forward's end, which closed with the call.
func TestAReceiptNamesTheProfileRatherThanAForwardsEnd(t *testing.T) {
	for _, tc := range []struct {
		name, profile, endpoint, want string
		tunnel                        plugin.Tunnel
	}{
		{"no profile", "", "qdrant.internal:6333", "qdrant.internal:6333", plugin.TunnelNone},
		{"a profile reached directly", "prod", "qdrant.internal:6333", "qdrant.internal:6333 (profile prod)",
			plugin.TunnelNone},
		{"a profile through a forward", "prod", "127.0.0.1:54321", "profile prod (through its kube: forward)",
			plugin.TunnelKube},
	} {
		r := req(t, "qdrant.dump", map[string]any{"endpoint": tc.endpoint}).WithProfile(tc.profile, tc.tunnel)
		if got := r.Reached(r.String("endpoint")); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A failed transfer must remove its half-written file — a partial snapshot
// is the one that gets restored six months later — and must still delete the
// server-side copy, or every broken download also eats the server's disk.
func TestDumpFailedDownloadCleansUpBothSides(t *testing.T) {
	srv, calls := newSnapshotServer(t, map[string]http.HandlerFunc{
		"GET /collections/docs": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`{"points_count": 1, "segments_count": 1}`)))
		},
		"GET /": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"version":"1.12.0"}`))
		},
		"POST /collections/docs/snapshots": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`{"name":"s.snapshot","size":1}`)))
		},
		"GET /collections/docs/snapshots/s.snapshot": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":{"error":"storage failure"}}`))
		},
		"DELETE /collections/docs/snapshots/s.snapshot": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`true`)))
		},
	})

	path := filepath.Join(t.TempDir(), "docs.snapshot")
	_, err := runDump(t.Context(), req(t, "qdrant.dump", map[string]any{
		"collection": "docs", "out": path,
		"endpoint": strings.TrimPrefix(srv.URL, "http://"),
	}))
	if err == nil {
		t.Fatal("a failed download reported success")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("the partial file survived the failure")
	}
	deleted := false
	for _, c := range *calls {
		if strings.HasPrefix(c, "DELETE ") {
			deleted = true
		}
	}
	if !deleted {
		t.Errorf("the server-side snapshot was not deleted after the failed download: %v", *calls)
	}
}

// The leftover is reported, not fatal: the dump is safely local, and
// failing it retroactively would tell the operator their backup does not
// exist when it does.
func TestDumpReportsAnUndeletableServerCopy(t *testing.T) {
	srv, _ := newSnapshotServer(t, map[string]http.HandlerFunc{
		"GET /collections/docs": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`{"points_count": 1, "segments_count": 1}`)))
		},
		"GET /": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"version":"1.12.0"}`))
		},
		"POST /collections/docs/snapshots": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`{"name":"s.snapshot","size":1}`)))
		},
		"GET /collections/docs/snapshots/s.snapshot": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("bytes"))
		},
		"DELETE /collections/docs/snapshots/s.snapshot": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":{"error":"busy"}}`))
		},
	})

	path := filepath.Join(t.TempDir(), "docs.snapshot")
	v, err := runDump(t.Context(), req(t, "qdrant.dump", map[string]any{
		"collection": "docs", "out": path,
		"endpoint": strings.TrimPrefix(srv.URL, "http://"),
	}))
	if err != nil {
		t.Fatalf("an undeletable server copy failed the dump: %v", err)
	}
	if leftover := pairValue(t, v, "leftover"); !strings.Contains(leftover, "s.snapshot") {
		t.Errorf("the receipt does not name the leftover snapshot: %q", leftover)
	}
}
