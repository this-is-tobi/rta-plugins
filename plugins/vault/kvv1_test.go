package main

import (
	"context"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A KV v1 mount keeps its names where they are listed from: LIST legacy/ and
// LIST legacy/dir/, with no metadata/ in between. Only v2 has that. Both
// listings asked every mount for <mount>/metadata/, which a v1 mount answers
// with a 404, and the client turns a 404 into an empty result with no error —
// so a mount holding ten secrets read as "0 secrets" and an empty table, and
// the check for a mount that does not exist did not fire because the mount
// does. What the mount is, is the mount's to say (the vault CLI asks the same
// question), so each listing asks before it lists.

const (
	// A v1 mount reports its options as null, or without a version.
	legacyMount = `{"data":{"path":"legacy/","type":"kv","options":null}}`
	v1Explicit  = `{"data":{"path":"legacy/","type":"kv","options":{"version":"1"}}}`
	v2Mount     = `{"data":{"path":"secret/","type":"kv","options":{"version":"2"}}}`
)

// legacyFixture is a v1 mount shaped like the v2 one the tree tests use, with
// a folder named metadata among its keys: the v1 mount where a guess at
// <mount>/metadata/ would be answered, with the wrong names.
var legacyFixture = map[string]string{
	"/v1/sys/internal/ui/mounts/legacy": legacyMount,
	"/v1/legacy":                        `{"data":{"keys":["apps/","metadata/","root-token"]}}`,
	"/v1/legacy/apps":                   `{"data":{"keys":["db","web"]}}`,
	"/v1/legacy/metadata":               `{"data":{"keys":["owner"]}}`,
}

func TestAKVv1MountIsListedFromItsRootNotFromMetadata(t *testing.T) {
	for name, mount := range map[string]string{"options null": legacyMount, "version 1": v1Explicit} {
		t.Run(name, func(t *testing.T) {
			routes := map[string]string{}
			for k, v := range legacyFixture {
				routes[k] = v
			}
			routes["/v1/sys/internal/ui/mounts/legacy"] = mount
			srv, asked := recordingVault(t, routes)

			v, err := runKVList(context.Background(),
				mountReq(t, "vault.kv.list", srv.URL, map[string]any{"mount": "legacy"}))
			if err != nil {
				t.Fatal(err)
			}
			tbl := v.(view.Table)
			var got []string
			for _, r := range tbl.Rows {
				got = append(got, r[0])
			}
			if want := "apps/ metadata/ root-token"; strings.Join(got, " ") != want {
				t.Errorf("names = %q, want %q", strings.Join(got, " "), want)
			}
			readOnly(t, asked())
		})
	}
}

func TestAKVv1MountIsMappedByTheTree(t *testing.T) {
	srv, asked := recordingVault(t, legacyFixture)
	tree := treeOf(t, srv, map[string]any{"mount": "legacy"})

	got := strings.Join(paths(tree.Roots[0].Children, ""), " ")
	for _, want := range []string{"apps/db", "apps/web", "metadata/owner", "root-token"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is not in the tree: %s", want, got)
		}
	}
	if d := tree.Roots[0].Detail; !strings.Contains(d, "4 secrets") {
		t.Errorf("detail = %q, want 4 secrets", d)
	}
	readOnly(t, asked())
}

func TestAKVv1MountsPathsComplete(t *testing.T) {
	srv, _ := recordingVault(t, legacyFixture)
	got := suggestPaths(context.Background(), mountReq(t, "vault.kv.list", srv.URL,
		map[string]any{"mount": "legacy", "path": "apps/"}))
	if strings.Join(got, " ") != "apps/db apps/web" {
		t.Errorf("suggestions = %q, want the names under apps/", got)
	}
}

// v2 is unchanged: its names are under metadata/, and the question is asked
// once for the listing and once for the whole walk.
func TestAKVv2MountIsStillListedFromMetadataAndAskedOnce(t *testing.T) {
	routes := map[string]string{
		"/v1/sys/internal/ui/mounts/secret": v2Mount,
	}
	for k, v := range mountFixture {
		routes[k] = v
	}
	srv, asked := recordingVault(t, routes)
	tree := treeOf(t, srv, map[string]any{})
	if d := tree.Roots[0].Detail; !strings.Contains(d, "5 secrets") {
		t.Errorf("detail = %q, want 5 secrets", d)
	}
	preflights := 0
	for _, line := range asked() {
		if strings.Contains(line, "/v1/sys/internal/ui/mounts/") {
			preflights++
		}
	}
	if preflights != 1 {
		t.Errorf("the mount was asked about %d times in a walk of several folders, want once: %v", preflights, asked())
	}
}

// A token that may list a mount and not ask what it is — or an older Vault
// without the endpoint — is told nothing, and gets the listing it always had.
func TestAMountThatCannotBeAskedAboutIsListedAsV2(t *testing.T) {
	srv, _ := recordingVault(t, mountFixture) // the mount's own question is a 404
	tree := treeOf(t, srv, map[string]any{})
	if d := tree.Roots[0].Detail; !strings.Contains(d, "5 secrets") {
		t.Errorf("detail = %q, want the v2 listing", d)
	}
}
