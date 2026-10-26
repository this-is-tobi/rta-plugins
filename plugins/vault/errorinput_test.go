package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// nothingListens is a Vault no call here is answered by: the refusals driven
// against it are made before anything is sent, and the one that dials it is
// refused by the loopback interface at once.
const nothingListens = "http://127.0.0.1:1"

// vaultRefusing is a Vault that answers every request with status and errs,
// the shape its refusals arrive in.
func vaultRefusing(status int, errs ...string) func(*testing.T) string {
	return func(t *testing.T) string {
		srv, _ := recordingVaultWith(t, func(w http.ResponseWriter, _ *http.Request) {
			body, err := json.Marshal(map[string][]string{"errors": errs})
			if err != nil {
				t.Error(err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
		})
		return srv.URL
	}
}

// vaultHolding is recordingVault as a table entry's Vault: routes answered,
// and anything else the 404 Vault gives a path nothing is at.
func vaultHolding(routes map[string]string) func(*testing.T) string {
	return func(t *testing.T) string {
		srv, _ := recordingVault(t, routes)
		return srv.URL
	}
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
	verr := view.AsError(err, "vault.test.uncoded")
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

// A refusal of the one input a call got wrong names it (view.Error.Input), by
// the name the capability that refused it declares, so the form the call came
// from opens again on that box and an agent is told which argument to send
// again: a path, a mount, a policy, a lease or a wrapping token that is not
// there, a field or a version that does not parse, a file or an out that is
// wrong, a CA file that cannot be used.
//
// Beside them, the refusals that name none on purpose: the connection and the
// token, a version asked for that the path could equally be missing, a write's
// 404, which is the mount's and no input the caller gave, and the ones two
// inputs could each answer — a CA file over plain HTTP, a snapshot from
// another cluster, a ciphertext the key could as well be wrong for.
func TestARefusalOfOneInputNamesIt(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent")
	notes := fileIn(t, "notes.txt", "not a directory\n")
	notACA := fileIn(t, "ca.pem", "not a certificate\n")
	snap := fileIn(t, "vault.snap", "archive")
	empty := fileIn(t, "empty.snap", "")
	mounts := map[string]string{"/v1/sys/mounts": mountsBody}

	for _, tc := range []struct {
		name   string
		id     string
		vault  func(*testing.T) string
		values map[string]any
		dryRun bool
		code   string
		input  string
	}{
		{name: "a secret the path does not hold", id: "vault.kv.get", vault: vaultHolding(nil),
			values: map[string]any{"path": "app/absent"},
			code:   "vault.notfound", input: "path"},
		{name: "a secret the path does not hold", id: "vault.kv.history", vault: vaultHolding(nil),
			values: map[string]any{"path": "app/absent"},
			code:   "vault.notfound", input: "path"},
		{name: "a mount that is not there", id: "vault.kv.list", vault: vaultHolding(mounts),
			values: map[string]any{"mount": "homleab"},
			code:   "vault.kv.mount.unknown", input: "mount"},
		{name: "a mount that is not there", id: "vault.kv.tree", vault: vaultHolding(mounts),
			values: map[string]any{"mount": "homleab"},
			code:   "vault.kv.mount.unknown", input: "mount"},
		{name: "a policy that is not there", id: "vault.policy.get", vault: vaultHolding(nil),
			values: map[string]any{"name": "absent"},
			code:   "vault.policy.notfound", input: "name"},
		{name: "a lease Vault does not hold", id: "vault.lease.show",
			vault:  vaultRefusing(http.StatusBadRequest, "invalid lease"),
			values: map[string]any{"id": "database/creds/app/abc"},
			code:   "vault.badrequest", input: "id"},
		{name: "a wrapping token that is spent", id: "vault.wrap.get",
			vault:  vaultRefusing(http.StatusBadRequest, "wrapping token is not valid or does not exist"),
			values: map[string]any{"wrapping-token": "hvs.spent"},
			code:   "vault.badrequest", input: "wrapping-token"},
		{name: "a wrapping token that is spent, looked up by a dry run", id: "vault.wrap.get",
			vault:  vaultRefusing(http.StatusBadRequest, "wrapping token is not valid or does not exist"),
			values: map[string]any{"wrapping-token": "hvs.spent"}, dryRun: true,
			code: "vault.badrequest", input: "wrapping-token"},
		{name: "a field that is not key=value", id: "vault.kv.set",
			values: map[string]any{"path": "app/db", "data": []string{"user=app", "s3cret"}},
			code:   "vault.data.invalid", input: "data"},
		{name: "a field that is not key=value", id: "vault.wrap.set",
			values: map[string]any{"data": []string{"s3cret"}},
			code:   "vault.data.invalid", input: "data"},
		{name: "a version that is not a number", id: "vault.kv.delete",
			values: map[string]any{"path": "app/db", "versions": []string{"2", "three"}},
			code:   "vault.kv.version.invalid", input: "versions"},
		{name: "a version that is not a number", id: "vault.kv.undelete",
			values: map[string]any{"path": "app/db", "versions": []string{"0"}},
			code:   "vault.kv.version.invalid", input: "versions"},
		{name: "a version that is not a number", id: "vault.kv.destroy",
			values: map[string]any{"path": "app/db", "versions": []string{"latest"}},
			code:   "vault.kv.version.invalid", input: "versions"},
		{name: "nowhere to write", id: "vault.snapshot",
			values: map[string]any{"out": ""},
			code:   "vault.snapshot.nooutput", input: "out"},
		{name: "an out that is there already", id: "vault.snapshot",
			values: map[string]any{"out": snap},
			code:   "vault.snapshot.exists", input: "out"},
		{name: "an out in a directory that is not there", id: "vault.snapshot",
			values: map[string]any{"out": filepath.Join(absent, "vault.snap")},
			code:   "vault.snapshot.create", input: "out"},
		{name: "a file that is not there", id: "vault.restore",
			values: map[string]any{"file": absent},
			code:   "vault.restore.missing", input: "file"},
		{name: "a file under a file", id: "vault.restore",
			values: map[string]any{"file": filepath.Join(notes, "vault.snap")},
			code:   "vault.restore.missing", input: "file"},
		{name: "a directory for a file", id: "vault.restore",
			values: map[string]any{"file": filepath.Dir(notes)},
			code:   "vault.restore.notafile", input: "file"},
		{name: "a file holding nothing", id: "vault.restore",
			values: map[string]any{"file": empty},
			code:   "vault.restore.empty", input: "file"},
		{name: "a CA file that is not there", id: "vault.kv.list",
			vault:  func(*testing.T) string { return "https://127.0.0.1:1" },
			values: map[string]any{"ca-file": absent},
			code:   "vault.tls.ca.unreadable", input: "ca-file"},
		{name: "a CA file holding no certificate", id: "vault.kv.list",
			vault:  func(*testing.T) string { return "https://127.0.0.1:1" },
			values: map[string]any{"ca-file": notACA},
			code:   "vault.tls.ca.invalid", input: "ca-file"},

		{name: "a version the path could as well be missing", id: "vault.kv.get", vault: vaultHolding(nil),
			values: map[string]any{"path": "app/db", "version": 3},
			code:   "vault.notfound"},
		{name: "a write's 404, which is the mount's", id: "vault.kv.set", vault: vaultHolding(nil),
			values: map[string]any{"path": "app/db", "data": []string{"user=app"}},
			code:   "vault.notfound"},
		{name: "a token the Vault refuses", id: "vault.kv.get",
			vault:  vaultRefusing(http.StatusForbidden, "permission denied"),
			values: map[string]any{"path": "app/db"},
			code:   "vault.denied"},
		{name: "a connection nothing answers", id: "vault.seal.status",
			values: map[string]any{},
			code:   "vault.conn.refused"},
		{name: "a CA file over plain HTTP, which the url answers as well", id: "vault.kv.list",
			values: map[string]any{"ca-file": notACA},
			code:   "vault.tls.ca.unused"},
		{name: "a ciphertext the key could as well be wrong for", id: "vault.transit.decrypt",
			vault:  vaultRefusing(http.StatusBadRequest, "invalid ciphertext: no prefix"),
			values: map[string]any{"key": "app", "ciphertext": "not-a-ciphertext"},
			code:   "vault.badrequest"},
		{name: "a snapshot from another cluster, which force answers as well", id: "vault.restore",
			vault: vaultRefusing(http.StatusBadRequest,
				"could not verify hash file, possibly the snapshot is using a different set of unseal keys"),
			values: map[string]any{"file": snap},
			code:   "vault.restore.mismatch"},
	} {
		t.Run(tc.id+": "+tc.name, func(t *testing.T) {
			address := nothingListens
			if tc.vault != nil {
				address = tc.vault(t)
			}
			values := make(map[string]any, len(tc.values))
			for k, v := range tc.values {
				values[k] = v
			}
			r := mountReq(t, tc.id, address, values)
			if tc.dryRun {
				r = dryReq(t, tc.id, address, values)
			}
			verr := refusedInput(t, tc.id, r)
			if verr.Code != tc.code || verr.Input != tc.input {
				t.Errorf("%s naming %q, want %s naming %q (%s)", verr.Code, verr.Input, tc.code, tc.input, verr.Message)
			}
		})
	}
}

// A file a CA or a snapshot is read from that is there and that this process
// may not read names no input: the path is right, and the fix is a chmod, not
// the box.
func TestAFileThatCannotBeReadNamesNoInput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("mode bits do not stop this reader")
	}
	shut := fileIn(t, "shut", "archive")
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id      string
		address string
		values  map[string]any
		code    string
	}{
		{"vault.kv.list", "https://127.0.0.1:1", map[string]any{"ca-file": shut}, "vault.tls.ca.unreadable"},
		{"vault.restore", nothingListens, map[string]any{"file": shut}, "vault.restore.unreadable"},
	} {
		verr := refusedInput(t, tc.id, mountReq(t, tc.id, tc.address, tc.values))
		if verr.Code != tc.code || verr.Input != "" {
			t.Errorf("%s: %s naming %q, want %s naming none", tc.id, verr.Code, verr.Input, tc.code)
		}
	}
}
