package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A name a stranger can choose — a secret's path or key, a policy, a mount, a
// transit key — is shown the way a person reads it in a list of names: as it
// is when it reads as itself, spaces and accents included, and otherwise in
// double quotes with each character they would not see written out. Without
// that an escape sequence in a key came out as another, ordinary name, and a
// newline split a row. Each of these runs a capability against a Vault that
// holds one such name and one ordinary name, and reads what comes back as the
// reader would, in the table, the tree, the pairs or the sentence.

const (
	// oddName holds an escape sequence and a newline, which no renderer may
	// draw as the name it would be without them.
	oddName = "a\x1b[31mb\nc"
	wantOdd = `"a\x1b[31mb\nc"`
	// plainName is one that reads as itself: a space and an accent.
	plainName = "mon secret é"
)

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// drawsAsItself fails when s holds a byte a renderer would act on, which is
// what a name shown as it is would have carried to the terminal.
func drawsAsItself(t *testing.T, what, s string) {
	t.Helper()
	if strings.ContainsAny(s, "\x1b\n") {
		t.Errorf("%s = %q holds a character that does not draw as itself", what, s)
	}
}

func sameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	got, want = slices.Clone(got), slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
	for _, g := range got {
		drawsAsItself(t, what, g)
	}
}

func firstColumn(t *testing.T, v view.View) []string {
	t.Helper()
	tbl, ok := v.(view.Table)
	if !ok {
		t.Fatalf("view is %s, want a Table", view.TypeOf(v))
	}
	var out []string
	for _, row := range tbl.Rows {
		out = append(out, row[0])
	}
	return out
}

func pairsOf(t *testing.T, v view.View) map[string]string {
	t.Helper()
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("view is %s, want a KeyValue", view.TypeOf(v))
	}
	out := map[string]string{}
	for _, p := range kv.Pairs {
		out[p.Key] = p.Value
		drawsAsItself(t, "key "+p.Key, p.Key)
		drawsAsItself(t, "value of "+p.Key, p.Value)
	}
	return out
}

func TestAListedSecretNameThatDoesNotDrawAsItselfIsShownQuoted(t *testing.T) {
	srv, _ := recordingVault(t, map[string]string{
		"/v1/secret/metadata": `{"data":{"keys":` + asJSON(t, []string{oddName, oddName + "/", plainName, plainName + "/"}) + `}}`,
	})
	v, err := runKVList(context.Background(), mountReq(t, "vault.kv.list", srv.URL, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	// A folder keeps the "/" that marks it outside the quotes, since the
	// description tells a reader to look for a name that ends in one.
	sameSet(t, "names", firstColumn(t, v), []string{wantOdd, wantOdd + "/", plainName, plainName + "/"})
}

func TestTheTreeShowsASecretNameThatDoesNotDrawAsItselfQuoted(t *testing.T) {
	srv, _ := recordingVault(t, map[string]string{
		"/v1/secret/metadata":              `{"data":{"keys":` + asJSON(t, []string{oddName, oddName + "/", plainName, plainName + "/"}) + `}}`,
		"/v1/secret/metadata/" + oddName:   `{"data":{"keys":` + asJSON(t, []string{"in" + oddName}) + `}}`,
		"/v1/secret/metadata/" + plainName: `{"data":{"keys":["inside"]}}`,
	})
	tree := treeOf(t, srv, map[string]any{})

	var labels, inner []string
	for _, n := range tree.Roots[0].Children {
		labels = append(labels, n.Label)
		for _, c := range n.Children {
			inner = append(inner, c.Label)
		}
	}
	sameSet(t, "labels", labels, []string{wantOdd, wantOdd + "/", plainName, plainName + "/"})
	// The walk went on into the folder under its real name, which is what
	// the server knows it by.
	sameSet(t, "labels inside the folders", inner, []string{`"ina\x1b[31mb\nc"`, "inside"})
}

func TestTheTreesRootNamesTheStartingPathQuotedWhenItDoesNotDrawAsItself(t *testing.T) {
	srv, _ := recordingVault(t, map[string]string{
		"/v1/secret/metadata/" + oddName:   `{"data":{"keys":["x"]}}`,
		"/v1/secret/metadata/" + plainName: `{"data":{"keys":["x"]}}`,
	})
	for path, want := range map[string]string{
		oddName:   `"secret/a\x1b[31mb\nc"`,
		plainName: "secret/" + plainName,
	} {
		got := treeOf(t, srv, map[string]any{"path": path}).Roots[0].Label
		if got != want {
			t.Errorf("root for %q = %q, want %q", path, got, want)
		}
		drawsAsItself(t, "root", got)
	}
}

func TestARevealedSecretsFieldNamesAreShownQuotedWhenTheyDoNotDrawAsThemselves(t *testing.T) {
	srv, _ := recordingVault(t, map[string]string{
		"/v1/secret/data/app/db": `{"data":{"data":` + asJSON(t, map[string]string{oddName: "v1", plainName: "v2"}) +
			`,"metadata":{"version":1}}}`,
	})
	v, err := runKVGet(context.Background(), mountReq(t, "vault.kv.get", srv.URL, map[string]any{"path": "app/db"}))
	if err != nil {
		t.Fatal(err)
	}
	got := pairsOf(t, v)
	if got[wantOdd] != "v1" || got[plainName] != "v2" || len(got) != 2 {
		t.Errorf("fields = %q, want the odd name quoted and the plain one as it is", got)
	}
}

func TestAWrittenSecretsPathIsShownQuotedWhenItDoesNotDrawAsItself(t *testing.T) {
	srv, _ := recordingVault(t, map[string]string{
		"/v1/secret/data/" + oddName:   `{"data":{"version":2,"created_time":"2024-05-06T07:08:09Z"}}`,
		"/v1/secret/data/" + plainName: `{"data":{"version":2,"created_time":"2024-05-06T07:08:09Z"}}`,
	})
	for path, want := range map[string]string{oddName: wantOdd, plainName: plainName} {
		v, err := runKVSet(context.Background(), mountReq(t, "vault.kv.set", srv.URL,
			map[string]any{"path": path, "data": []string{"k=v"}}))
		if err != nil {
			t.Fatal(err)
		}
		if got := pairsOf(t, v)["path"]; got != want {
			t.Errorf("path for %q = %q, want %q", path, got, want)
		}

		pv, err := runKVSet(context.Background(), dryReq(t, "vault.kv.set", srv.URL,
			map[string]any{"path": path, "data": []string{"k=v"}}))
		if err != nil {
			t.Fatal(err)
		}
		body := pv.(view.Text).Body
		wantWhere := "secret/" + path
		if path == oddName {
			wantWhere = `"secret/a\x1b[31mb\nc"`
		}
		if !strings.HasPrefix(body, "would set "+wantWhere+" with 1 field") {
			t.Errorf("preview for %q = %q, want it to name %s", path, body, wantWhere)
		}
		drawsAsItself(t, "preview", body)
	}
}

// The three that act on a version say where in the same sentence for the
// preview and for what they did.
func TestAVersionCallNamesThePathQuotedWhenItDoesNotDrawAsItself(t *testing.T) {
	routes := map[string]string{}
	for _, name := range []string{oddName, plainName} {
		for _, verb := range []string{"data", "delete", "undelete", "destroy"} {
			routes["/v1/secret/"+verb+"/"+name] = `{}`
		}
	}
	srv, _ := recordingVault(t, routes)
	for _, tc := range []struct {
		id   string
		run  func(context.Context, plugin.Request) (view.View, error)
		verb string
	}{
		{"vault.kv.delete", runKVDelete, "delete"},
		{"vault.kv.undelete", runKVUndelete, "bring back"},
		{"vault.kv.destroy", runKVDestroy, "destroy"},
	} {
		for path, where := range map[string]string{oddName: `"secret/a\x1b[31mb\nc"`, plainName: "secret/" + plainName} {
			values := func() map[string]any { return map[string]any{"path": path, "versions": []string{"1"}} }
			preview, err := tc.run(context.Background(), dryReq(t, tc.id, srv.URL, values()))
			if err != nil {
				t.Fatalf("%s preview: %v", tc.id, err)
			}
			if body := preview.(view.Text).Body; !strings.Contains(body, "version 1 of "+where) {
				t.Errorf("%s preview for %q = %q, want it to name %s", tc.id, path, body, where)
			} else {
				drawsAsItself(t, tc.id+" preview", body)
			}
			done, err := tc.run(context.Background(), mountReq(t, tc.id, srv.URL, values()))
			if err != nil {
				t.Fatalf("%s: %v", tc.id, err)
			}
			if body := done.(view.Text).Body; !strings.Contains(body, "version 1 of "+where) {
				t.Errorf("%s for %q = %q, want it to name %s", tc.id, path, body, where)
			} else {
				drawsAsItself(t, tc.id, body)
			}
		}
	}
}

const policiesRoute = "/v1/sys/policies/acl"

func policyListBody(t *testing.T) string {
	t.Helper()
	return `{"data":{"keys":` + asJSON(t, []string{oddName, plainName, "default"}) + `}}`
}

func TestAPolicyNameThatDoesNotDrawAsItselfIsShownQuoted(t *testing.T) {
	srv, _ := recordingVault(t, map[string]string{policiesRoute: policyListBody(t)})
	want := []string{wantOdd, plainName, "default"}

	v, err := runPolicyList(context.Background(), mountReq(t, "vault.policy.list", srv.URL, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	sameSet(t, "policies", firstColumn(t, v), want)

	// The page lists them in the table the capability does.
	srv, _ = recordingVault(t, map[string]string{
		policiesRoute:                policyListBody(t),
		"/v1/sys/seal-status":        `{"sealed":false,"initialized":true,"version":"1.0.0"}`,
		"/v1/auth/token/lookup-self": `{"data":{"policies":["default"],"ttl":3600}}`,
	})
	pv, err := overviewOf(t, srv, true)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range pv.(view.Sections).Items {
		if s.Key() == "policies" {
			found = true
			sameSet(t, "overview policies", firstColumn(t, s.View), want)
		}
	}
	if !found {
		t.Fatal("the overview has no policies section")
	}
}

func TestATokensPoliciesAndDisplayNameAreShownQuotedWhenTheyDoNotDrawAsThemselves(t *testing.T) {
	for _, tc := range []struct {
		name, display, policies string
	}{
		{"names that do not draw as themselves", oddName, wantOdd + ", " + plainName},
		{"ordinary names", plainName, "default, " + plainName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policies := []string{"default", plainName}
			if tc.display == oddName {
				policies = []string{oddName, plainName}
			}
			srv, _ := recordingVault(t, map[string]string{
				"/v1/auth/token/lookup-self": `{"data":{"display_name":` + asJSON(t, tc.display) +
					`,"policies":` + asJSON(t, policies) + `,"ttl":3600}}`,
				"/v1/sys/seal-status": `{"sealed":false,"initialized":true,"version":"1.0.0"}`,
			})
			wantDisplay := tc.display
			if tc.display == oddName {
				wantDisplay = wantOdd
			}

			v, err := runTokenStatus(context.Background(), mountReq(t, "vault.token.status", srv.URL, map[string]any{}))
			if err != nil {
				t.Fatal(err)
			}
			got := pairsOf(t, v)
			if got["display name"] != wantDisplay || got["policies"] != tc.policies {
				t.Errorf("token status = %q, want display name %q and policies %q", got, wantDisplay, tc.policies)
			}

			ov, err := overviewOf(t, srv, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := pairsOf(t, ov)["token policies"]; got != tc.policies {
				t.Errorf("overview token policies = %q, want %q", got, tc.policies)
			}
		})
	}
}

func TestALeaseIDIsShownQuotedWhenItsRoleDoesNotDrawAsItself(t *testing.T) {
	for id, want := range map[string]string{
		"database/creds/" + oddName + "/x7": `"database/creds/a\x1b[31mb\nc/x7"`,
		"database/creds/" + plainName:       "database/creds/" + plainName,
	} {
		srv, _ := recordingVault(t, map[string]string{
			"/v1/sys/leases/lookup": `{"data":{"id":` + asJSON(t, id) + `,"ttl":60,"renewable":true}}`,
		})
		v, err := runLeaseShow(context.Background(), mountReq(t, "vault.lease.show", srv.URL, map[string]any{"id": id}))
		if err != nil {
			t.Fatal(err)
		}
		got := pairsOf(t, v)
		if got["id"] != want || got["renewable"] != "true" {
			t.Errorf("lease = %q, want id %q", got, want)
		}
	}
}

func TestAWrappedSecretsFieldNamesAndCreationPathAreShownQuotedWhenTheyDoNotDrawAsThemselves(t *testing.T) {
	srv, _ := recordingVault(t, map[string]string{
		"/v1/sys/wrapping/unwrap": `{"data":` + asJSON(t, map[string]string{oddName: "v1", plainName: "v2"}) + `}`,
		"/v1/sys/wrapping/lookup": `{"data":{"creation_path":` + asJSON(t, "secret/data/"+oddName) + `,"creation_ttl":300}}`,
	})
	v, err := runWrapGet(context.Background(), mountReq(t, "vault.wrap.get", srv.URL, map[string]any{"wrapping-token": "hvs.x"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := pairsOf(t, v); got[wantOdd] != "v1" || got[plainName] != "v2" || len(got) != 2 {
		t.Errorf("unwrapped fields = %q, want the odd name quoted and the plain one as it is", got)
	}

	pv, err := runWrapGet(context.Background(), dryReq(t, "vault.wrap.get", srv.URL, map[string]any{"wrapping-token": "hvs.x"}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pairsOf(t, pv)["creation_path"], `"secret/data/a\x1b[31mb\nc"`; got != want {
		t.Errorf("creation_path = %q, want %q", got, want)
	}

	srv, _ = recordingVault(t, map[string]string{
		"/v1/sys/wrapping/lookup": `{"data":{"creation_path":"secret/data/` + plainName + `","creation_ttl":300}}`,
	})
	pv, err = runWrapGet(context.Background(), dryReq(t, "vault.wrap.get", srv.URL, map[string]any{"wrapping-token": "hvs.x"}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pairsOf(t, pv)["creation_path"], "secret/data/"+plainName; got != want {
		t.Errorf("creation_path = %q, want it as it is, %q", got, want)
	}
}

func TestATransitPreviewNamesTheKeyQuotedWhenItDoesNotDrawAsItself(t *testing.T) {
	for key, want := range map[string]string{
		oddName:   `with "transit/encrypt/a\x1b[31mb\nc"`,
		plainName: "with transit/encrypt/" + plainName,
	} {
		v, err := runTransitEncrypt(context.Background(), dryReq(t, "vault.transit.encrypt", "http://127.0.0.1:1",
			map[string]any{"key": key, "plaintext": "hello"}))
		if err != nil {
			t.Fatal(err)
		}
		body := v.(view.Text).Body
		if !strings.HasSuffix(body, want) {
			t.Errorf("preview for %q = %q, want it to end %q", key, body, want)
		}
		drawsAsItself(t, "preview", body)
	}
}

func TestAClusterNameIsShownQuotedWhenItDoesNotDrawAsItself(t *testing.T) {
	for name, want := range map[string]string{oddName: wantOdd, plainName: plainName} {
		srv, _ := recordingVault(t, map[string]string{
			"/v1/sys/seal-status": `{"sealed":false,"initialized":true,"version":"1.0.0","storage_type":"raft","cluster_name":` +
				asJSON(t, name) + `}`,
		})
		r := mountReq(t, "vault.seal.status", srv.URL, map[string]any{})
		v, err := runSealStatus(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if got := pairsOf(t, v)["cluster"]; got != want {
			t.Errorf("cluster = %q, want %q", got, want)
		}

		client, verr := connect(r)
		if verr != nil {
			t.Fatal(verr)
		}
		after := sealStateAfter(context.Background(), client, r)
		if !strings.HasPrefix(after, want+" is unsealed") {
			t.Errorf("read-back for %q = %q, want it to open on %s", name, after, want)
		}
		drawsAsItself(t, "read-back", after)
	}
}

func TestTheMountsAVaultHasAreListedQuotedWhenTheyDoNotDrawAsThemselves(t *testing.T) {
	srv, _ := recordingVault(t, map[string]string{
		"/v1/sys/mounts": `{"data":` + asJSON(t, map[string]any{
			oddName + "/":   map[string]string{"type": "kv"},
			"homelab/":      map[string]string{"type": "kv"},
			plainName + "/": map[string]string{"type": "kv"},
		}) + `}`,
	})
	_, err := runKVList(context.Background(), mountReq(t, "vault.kv.list", srv.URL, map[string]any{"mount": "typo"}))
	verr := view.AsError(err, "")
	if verr == nil || verr.Code != "vault.kv.mount.unknown" {
		t.Fatalf("err = %v, want vault.kv.mount.unknown", err)
	}
	if want := "its KV mounts are: " + wantOdd + ", homelab, " + plainName; verr.Hint != want {
		t.Errorf("hint = %q, want %q", verr.Hint, want)
	}
	drawsAsItself(t, "hint", verr.Hint)
}
