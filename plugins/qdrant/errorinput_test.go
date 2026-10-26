package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// fileHolding writes body to a file nothing else uses and returns its path.
func fileHolding(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// shut is a file that is there and that this process may not read.
func shut(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("mode bits do not stop this reader")
	}
	path := fileHolding(t, "shut", "something\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	return path
}

// aSnapshottingInstance holds docs, snapshots it when asked and hands the
// snapshot down: a dump against it gets as far as writing its file.
func aSnapshottingInstance(t *testing.T) string {
	t.Helper()
	srv, _ := newSnapshotServer(t, map[string]http.HandlerFunc{
		"GET /collections/docs": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`{"points_count": 1, "segments_count": 1}`)))
		},
		"GET /": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"version":"1.12.0"}`))
		},
		"POST /collections/docs/snapshots": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`{"name":"s.snapshot","size":5}`)))
		},
		"GET /collections/docs/snapshots/s.snapshot": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("bytes"))
		},
		"DELETE /collections/docs/snapshots/s.snapshot": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(envelope(`true`)))
		},
	})
	return strings.TrimPrefix(srv.URL, "http://")
}

// A refusal of the one input a call got wrong names it (view.Error.Input), by
// the name the capability that refused it declares: the form the call came
// from opens again on that box, and an agent is told which argument to send
// again. A name the capability does not declare is ignored by every surface,
// so each one here is also held to the declaration (ErrorInput).
//
// Beside them, the refusals that name none on purpose: a connection refused, a
// key the instance rejects, a 404 that is no collection's, a file that is
// there and may not be read, a collection a restore may only replace with
// replace given or go round under another name, and the refusal of an agent.
// No one box of the call holds the fix to those.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	instance := func(t *testing.T) string { return newFakeQdrant(t, instanceRoutes()).endpoint() }
	for _, tc := range []struct {
		name    string
		cap     string
		surface plugin.Surface
		given   func(t *testing.T) map[string]any
		code    string
		input   string
	}{
		{name: "a collection the instance does not hold", cap: "qdrant.collection.show",
			code: "qdrant.notfound", input: "collection",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "ghost"}
			}},
		{name: "a collection the instance does not hold", cap: "qdrant.points.count",
			code: "qdrant.notfound", input: "collection",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "ghost"}
			}},
		{name: "a collection the instance does not hold", cap: "qdrant.points.scroll",
			code: "qdrant.notfound", input: "collection",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "ghost"}
			}},
		{name: "an offset that is no point id", cap: "qdrant.points.scroll",
			code: "qdrant.points.offset", input: "offset",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs", "offset": "page-2"}
			}},
		{name: "a collection the instance does not hold, to an agent", cap: "qdrant.points.scroll",
			surface: plugin.SurfaceMCP, code: "qdrant.notfound", input: "collection",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "ghost"}
			}},
		{name: "the one collection named, which the instance does not hold", cap: "qdrant.overview",
			code: "qdrant.notfound", input: "collection",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": []string{"ghost"}}
			}},
		{name: "the one collection named, which the instance does not hold", cap: "qdrant.collection.list",
			code: "qdrant.notfound", input: "collection",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": []string{"ghost"}}
			}},
		{name: "a collection to dump the instance does not hold", cap: "qdrant.dump",
			code: "qdrant.notfound", input: "collection",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "ghost",
					"out": filepath.Join(t.TempDir(), "ghost.snapshot")}
			}},
		{name: "more collections than a listing reads", cap: "qdrant.overview",
			code: "qdrant.collection.toomany", input: "collection",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": namesFrom(0, maxNamedCollections+1)}
			}},
		{name: "more collections than a listing reads", cap: "qdrant.collection.list",
			code: "qdrant.collection.toomany", input: "collection",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": namesFrom(0, maxNamedCollections+1)}
			}},
		{name: "an address that is no host and port", cap: "qdrant.collection.show",
			code: "qdrant.endpoint.invalid", input: "address",
			given: func(*testing.T) map[string]any {
				return map[string]any{"address": "bad host:6333", "collection": "docs"}
			}},
		{name: "a CA file that is not there", cap: "qdrant.collection.show",
			code: "qdrant.tls.ca.unreadable", input: "ca-file",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs",
					"ca-file": filepath.Join(t.TempDir(), "absent.pem")}
			}},
		{name: "a CA file that is a directory", cap: "qdrant.points.count",
			code: "qdrant.tls.ca.unreadable", input: "ca-file",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs", "ca-file": t.TempDir()}
			}},
		{name: "a CA file that holds no certificate", cap: "qdrant.overview",
			code: "qdrant.tls.ca.invalid", input: "ca-file",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "ca-file": fileHolding(t, "ca.pem", "not a certificate\n")}
			}},
		{name: "no file to write the snapshot to", cap: "qdrant.dump",
			code: "qdrant.dump.nooutput", input: "out",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs"}
			}},
		{name: "a file to write the snapshot to that is there already", cap: "qdrant.dump",
			code: "qdrant.dump.exists", input: "out",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs",
					"out": fileHolding(t, "docs.snapshot", "an older dump\n")}
			}},
		{name: "a file to write the snapshot to in a directory that is not there", cap: "qdrant.dump",
			code: "qdrant.dump.create", input: "out",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": aSnapshottingInstance(t), "collection": "docs",
					"out": filepath.Join(t.TempDir(), "absent", "docs.snapshot")}
			}},
		{name: "a snapshot that is not there", cap: "qdrant.restore",
			code: "qdrant.restore.missing", input: "file",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs",
					"file": filepath.Join(t.TempDir(), "absent.snapshot")}
			}},
		{name: "a snapshot under a file, which is no directory", cap: "qdrant.restore",
			code: "qdrant.restore.missing", input: "file",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs",
					"file": filepath.Join(fileHolding(t, "blocker", "x"), "docs.snapshot")}
			}},
		{name: "a snapshot that is a directory", cap: "qdrant.restore",
			code: "qdrant.restore.notafile", input: "file",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs", "file": t.TempDir()}
			}},
		{name: "a snapshot that is empty", cap: "qdrant.restore",
			code: "qdrant.restore.empty", input: "file",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs",
					"file": fileHolding(t, "docs.snapshot", "")}
			}},

		{name: "a connection nothing answers", cap: "qdrant.collection.show",
			code: "qdrant.conn.refused",
			given: func(t *testing.T) map[string]any {
				addr, _ := refusedDial(t)
				return map[string]any{"address": addr, "collection": "docs"}
			}},
		{name: "a key the instance rejects", cap: "qdrant.collection.show",
			code: "qdrant.auth.failed",
			given: func(t *testing.T) map[string]any {
				f := answering(t, instanceRoutes(), map[string]int{"/collections/docs": http.StatusUnauthorized})
				return map[string]any{"address": f.endpoint(), "collection": "docs"}
			}},
		{name: "a 404 of the instance's root, which is no collection's", cap: "qdrant.overview",
			code: "qdrant.notfound",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": newFakeQdrant(t, map[string]string{}).endpoint()}
			}},
		{name: "a collection a restore would replace, with replace not given", cap: "qdrant.restore",
			code: "qdrant.restore.notempty",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs",
					"file": fileHolding(t, "docs.snapshot", "bytes")}
			}},
		{name: "a dump asked for by an agent", cap: "qdrant.dump", surface: plugin.SurfaceMCP,
			code: "qdrant.human",
			given: func(t *testing.T) map[string]any {
				return map[string]any{"address": instance(t), "collection": "docs",
					"out": filepath.Join(t.TempDir(), "docs.snapshot")}
			}},
	} {
		t.Run(tc.cap+": "+tc.name, func(t *testing.T) {
			c := capabilityOf(t, tc.cap)
			surface := tc.surface
			if surface == "" {
				surface = plugin.SurfaceCLI
			}
			r := plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: tc.given(t)}), false, false).WithSurface(surface)
			_, err := c.Run(t.Context(), r)
			verr := view.AsError(err, "qdrant.test.none")
			if verr == nil || verr.Code != tc.code {
				t.Fatalf("%s answered %+v, want %s", tc.cap, verr, tc.code)
			}
			if verr.Input != tc.input {
				t.Errorf("%s names the input %q, want %q: %s", verr.Code, verr.Input, tc.input, verr.Message)
			}
			if _, declared := c.ErrorInput(verr); tc.input != "" && !declared {
				t.Errorf("%s: %s names %q, which it does not declare", tc.cap, verr.Code, verr.Input)
			}
		})
	}
}

// A CA file and a snapshot that are there, and that this process may not
// read, name no input: the path is right, and the fix is a chmod, not the box.
func TestAFileThatCannotBeReadNamesNoInput(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{})
	for _, tc := range []struct {
		cap    string
		values map[string]any
		code   string
	}{
		{"qdrant.collection.show", map[string]any{"collection": "docs", "ca-file": shut(t)},
			"qdrant.tls.ca.unreadable"},
		{"qdrant.restore", map[string]any{"collection": "docs", "file": shut(t)},
			"qdrant.restore.unreadable"},
	} {
		c := capabilityOf(t, tc.cap)
		_, err := c.Run(t.Context(), reqAt(t, f, tc.cap, tc.values).WithSurface(plugin.SurfaceCLI))
		if verr := view.AsError(err, "qdrant.test.none"); verr == nil || verr.Code != tc.code || verr.Input != "" {
			t.Errorf("%s: %+v, want %s naming none", tc.cap, verr, tc.code)
		}
	}
}

// Qdrant reads a UUID in four forms, and an id is an unsigned integer or one of
// them; anything else is no point id.
func TestAPointIDIsAnUnsignedIntegerOrAUUID(t *testing.T) {
	for id, want := range map[string]bool{
		"6b9e5f0e-2d1c-4a8b-9f3e-1a2b3c4d5e6f":          true,
		"6B9E5F0E2D1C4A8B9F3E1A2B3C4D5E6F":              true,
		"{6b9e5f0e-2d1c-4a8b-9f3e-1a2b3c4d5e6f}":        true,
		"urn:uuid:6b9e5f0e-2d1c-4a8b-9f3e-1a2b3c4d5e6f": true,
		"6b9e5f0e-2d1c-4a8b-9f3e-1a2b3c4d5e6":           false,
		"page-2":                                        false,
		"-1":                                            false,
	} {
		if got := isUUID(id); got != want {
			t.Errorf("isUUID(%q) = %v, want %v", id, got, want)
		}
	}
}
