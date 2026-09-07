package main

import (
	"context"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Vault answers a LIST against a missing mount with a 404, and the client
// turns that into an empty result with no error — indistinguishable from a
// mount holding nothing. These pin the three answers apart: the typo is
// named, the genuinely empty mount stays empty, and a token that cannot
// enumerate the engines gets no invented failure.

const mountsBody = `{"data":{"homelab/":{"type":"kv","options":{"version":"2"}},` +
	`"cubbyhole/":{"type":"cubbyhole"},"transit/":{"type":"transit"}}}`

func mountReq(t *testing.T, capID, address string, values map[string]any) plugin.Request {
	t.Helper()
	values["address"] = address
	values["token"] = "test"
	return req(t, capID, values)
}

func TestAMountThatIsNotThereIsNamedRatherThanShownEmpty(t *testing.T) {
	srv, _ := recordingVault(t, map[string]string{"/v1/sys/mounts": mountsBody})

	_, err := runKVList(context.Background(),
		mountReq(t, "vault.kv.list", srv.URL, map[string]any{"mount": "homleab"}))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "vault.kv.mount.unknown" {
		t.Fatalf("err = %v, want vault.kv.mount.unknown", err)
	}
	if !strings.Contains(verr.Message, "homleab") {
		t.Errorf("message = %q, does not name the mount that was asked for", verr.Message)
	}
	if !strings.Contains(verr.Hint, "homelab") {
		t.Errorf("hint = %q, does not name the mount that is there", verr.Hint)
	}
	// The tree said "0 secrets" for the same typo and has to say what the
	// listing says.
	_, err = runKVTree(context.Background(),
		mountReq(t, "vault.kv.tree", srv.URL, map[string]any{"mount": "homleab"}))
	if verr, ok := err.(*view.Error); !ok || verr.Code != "vault.kv.mount.unknown" {
		t.Fatalf("tree err = %v, want vault.kv.mount.unknown", err)
	}
}

func TestAMountThatIsThereAndEmptyStaysEmpty(t *testing.T) {
	srv, asked := recordingVault(t, map[string]string{"/v1/sys/mounts": mountsBody})

	v, err := runKVList(context.Background(),
		mountReq(t, "vault.kv.list", srv.URL, map[string]any{"mount": "homelab"}))
	if err != nil {
		t.Fatalf("an empty mount that exists was refused: %v", err)
	}
	if tbl, ok := v.(view.Table); !ok || tbl.Total != 0 {
		t.Fatalf("view = %+v, want an empty table", v)
	}
	if !strings.Contains(strings.Join(asked(), " "), "sys/mounts") {
		t.Errorf("the empty listing never asked whether the mount exists: %v", asked())
	}
}

// The extra request is the price of telling the two empties apart, so it is
// paid only when the listing came back empty.
func TestAListingThatAnsweredNeverAsksAboutTheMount(t *testing.T) {
	srv, asked := recordingVault(t, map[string]string{
		"/v1/sys/mounts":            mountsBody,
		"/v1/homelab/metadata/apps": `{"data":{"keys":["db","web"]}}`,
	})
	v, err := runKVList(context.Background(),
		mountReq(t, "vault.kv.list", srv.URL, map[string]any{"mount": "homelab", "path": "apps"}))
	if err != nil {
		t.Fatal(err)
	}
	if tbl, ok := v.(view.Table); !ok || tbl.Total != 2 {
		t.Fatalf("view = %+v, want the two names", v)
	}
	if strings.Contains(strings.Join(asked(), " "), "sys/mounts") {
		t.Errorf("a listing that answered still asked about the mount: %v", asked())
	}
}

// A token allowed to list a mount and not to enumerate the engines is an
// ordinary least-privilege setup, and its honest empty listing must not
// become an invented "no such mount".
func TestATokenThatCannotEnumerateMountsGetsNoInventedFailure(t *testing.T) {
	srv, _ := recordingVault(t, nil) // sys/mounts 404s, as a denied read would

	v, err := runKVList(context.Background(),
		mountReq(t, "vault.kv.list", srv.URL, map[string]any{"mount": "homleab"}))
	if err != nil {
		t.Fatalf("an unreadable sys/mounts turned an empty listing into %v", err)
	}
	if tbl, ok := v.(view.Table); !ok || tbl.Total != 0 {
		t.Fatalf("view = %+v, want the empty table it can honestly give", v)
	}
}
