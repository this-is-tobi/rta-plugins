package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A chain of three, as Vault's metadata endpoint reports it: the first
// destroyed, the second deleted, the third current. deletion_time is the
// empty string on a live version, which is the shape the client has to
// tolerate rather than the zero time a Go fixture would write.
const metadataBody = `{"data":{"current_version":3,"oldest_version":1,"max_versions":0,` +
	`"created_time":"2026-01-01T00:00:00Z","updated_time":"2026-03-01T00:00:00Z","versions":{` +
	`"1":{"created_time":"2026-01-01T00:00:00Z","deletion_time":"","destroyed":true},` +
	`"2":{"created_time":"2026-02-01T00:00:00Z","deletion_time":"2026-02-15T00:00:00Z","destroyed":false},` +
	`"3":{"created_time":"2026-03-01T00:00:00Z","deletion_time":"","destroyed":false}}}}`

func vaultReq(t *testing.T, capID, address string, values map[string]any) plugin.Request {
	t.Helper()
	values["address"] = address
	values["token"] = "test"
	return req(t, capID, values)
}

func metaPair(kv view.KeyValue, key string) string {
	for _, p := range kv.Pairs {
		if p.Key == key {
			return p.Value
		}
	}
	return ""
}

// The chain in order, one state per link — destroyed is final, deleted comes
// back, current is the head — and no version's data fetched to say so.
func TestHistoryListsEveryVersionWithItsState(t *testing.T) {
	srv, asked := recordingVault(t, map[string]string{"/v1/secret/metadata/app/db": metadataBody})
	v, err := runKVHistory(context.Background(),
		vaultReq(t, "vault.kv.history", srv.URL, map[string]any{"path": "app/db"}))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := v.(view.Sections)
	if !ok || len(s.Items) != 2 {
		t.Fatalf("view = %T %+v, want the versions and the metadata", v, v)
	}
	tbl, ok := s.Items[0].View.(view.Table)
	if !ok || len(tbl.Rows) != 3 || tbl.Total != 3 {
		t.Fatalf("versions = %+v, want three rows", s.Items[0].View)
	}
	want := [][2]string{{"1", "destroyed"}, {"2", "deleted 2026-02-15T00:00:00Z"}, {"3", "current"}}
	for i, w := range want {
		if tbl.Rows[i][0] != w[0] || tbl.Rows[i][2] != w[1] {
			t.Errorf("row %d = %v, want version %s %s", i, tbl.Rows[i], w[0], w[1])
		}
	}
	about, ok := s.Items[1].View.(view.KeyValue)
	if !ok || metaPair(about, "versions") != "3" || metaPair(about, "current") != "3" {
		t.Errorf("metadata = %+v, want 3 versions with 3 current", s.Items[1].View)
	}
	for _, line := range asked() {
		if strings.Contains(line, "/data/") {
			t.Errorf("history fetched a version's data: %s", line)
		}
	}
}

// --version reaches back along the chain: the request names the version and
// nothing else changes.
func TestGetReachesBackToTheVersionNamed(t *testing.T) {
	srv, asked := recordingVault(t, map[string]string{
		"/v1/secret/data/app/db": `{"data":{"data":{"password":"older"},"metadata":{"version":2}}}`,
	})
	v, err := runKVGet(context.Background(),
		vaultReq(t, "vault.kv.get", srv.URL, map[string]any{"path": "app/db", "version": 2}))
	if err != nil {
		t.Fatal(err)
	}
	if kv, ok := v.(view.KeyValue); !ok || metaPair(kv, "password") != "older" {
		t.Errorf("get --version 2 = %+v, want the older value", v)
	}
	if !slices.Contains(asked(), "GET /v1/secret/data/app/db?version=2") {
		t.Errorf("asked %v, want the version in the request", asked())
	}
}

// Each operation on the chain reaches the endpoint it names and no other:
// delete with no versions is the engine's own "delete the current version",
// the rest name theirs.
func TestVersionOperationsReachTheEndpointsTheyName(t *testing.T) {
	srv, asked := recordingVault(t, map[string]string{
		"/v1/secret/data/app/db":     `{}`,
		"/v1/secret/delete/app/db":   `{}`,
		"/v1/secret/undelete/app/db": `{}`,
		"/v1/secret/destroy/app/db":  `{}`,
	})
	steps := []struct {
		id     string
		run    func(context.Context, plugin.Request) (view.View, error)
		values map[string]any
		want   string
	}{
		{"vault.kv.delete", runKVDelete, map[string]any{"path": "app/db"}, "DELETE /v1/secret/data/app/db"},
		{"vault.kv.delete", runKVDelete, map[string]any{"path": "app/db", "versions": []string{"1,2"}}, "PUT /v1/secret/delete/app/db"},
		{"vault.kv.undelete", runKVUndelete, map[string]any{"path": "app/db", "versions": []string{"2"}}, "PUT /v1/secret/undelete/app/db"},
		{"vault.kv.destroy", runKVDestroy, map[string]any{"path": "app/db", "versions": []string{"1"}}, "PUT /v1/secret/destroy/app/db"},
	}
	for _, s := range steps {
		before := len(asked())
		v, err := s.run(context.Background(), vaultReq(t, s.id, srv.URL, s.values))
		if err != nil {
			t.Fatalf("%s: %v", s.id, err)
		}
		if lines := asked()[before:]; !slices.Contains(lines, s.want) {
			t.Errorf("%s asked %v, want %s", s.id, lines, s.want)
		}
		if txt, ok := v.(view.Text); !ok || txt.Body == "" {
			t.Errorf("%s answered %+v, want a receipt", s.id, v)
		}
	}
}

// Destroy is for the person at the terminal: an agent is refused before any
// call, by the declaration and by the handler both.
func TestDestroyRefusesAnAgent(t *testing.T) {
	srv, asked := recordingVault(t, nil)
	_, err := runKVDestroy(context.Background(),
		vaultReq(t, "vault.kv.destroy", srv.URL, map[string]any{"path": "app/db", "versions": []string{"1"}}).
			WithSurface(plugin.SurfaceMCP))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "vault.human" {
		t.Fatalf("err = %v, want vault.human", err)
	}
	if len(asked()) != 0 {
		t.Errorf("the refusal reached Vault: %v", asked())
	}
}

// A version that is not a number is refused before any call, naming the
// capability that numbers them.
func TestAVersionThatIsNotANumberIsRefusedBeforeAnyCall(t *testing.T) {
	srv, asked := recordingVault(t, nil)
	_, err := runKVUndelete(context.Background(),
		vaultReq(t, "vault.kv.undelete", srv.URL, map[string]any{"path": "app/db", "versions": []string{"latest"}}))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "vault.kv.version.invalid" {
		t.Fatalf("err = %v, want vault.kv.version.invalid", err)
	}
	if len(asked()) != 0 {
		t.Errorf("a bad version reached Vault: %v", asked())
	}
}
