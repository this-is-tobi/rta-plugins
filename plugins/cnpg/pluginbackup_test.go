package main

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Asking for a backup of a cluster that archives through a WAL-archiver plugin.
//
// The method was refused for as long as rta built no pluginConfiguration, and
// is offered now that it copies the plugin's name from the cluster. What these
// hold is the document that results, who chose what, and the refusals that stand
// where the operator would have accepted a Backup that fails minutes later.

// aCreatedBackup is what the API server answers a create with.
func aCreatedBackup() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "app-db-rta-20261013000000", "namespace": "prod"},
		"status":   map[string]any{"phase": "started"},
	}
}

// pluginStatusOf adds a status.pluginStatus entry to a cluster document.
func pluginStatusOf(name string, backup ...string) func(map[string]any) {
	return func(c map[string]any) {
		status := c["status"].(map[string]any)
		entries, _ := status["pluginStatus"].([]any)
		caps := make([]any, len(backup))
		for i, b := range backup {
			caps[i] = b
		}
		status["pluginStatus"] = append(entries, map[string]any{
			"name": name, "version": "0.9.0", "backupCapabilities": caps})
	}
}

func requestAs(t *testing.T, sf plugin.Surface, dry bool, values map[string]any) error {
	t.Helper()
	all := map[string]any{"cluster": "app-db", "namespace": "prod"}
	for k, v := range values {
		all[k] = v
	}
	_, err := runBackupRequest(context.Background(),
		plugin.NewRequest(all, dry, false).WithSurface(sf))
	return err
}

func receiptOf(t *testing.T, dry bool, values map[string]any) string {
	t.Helper()
	all := map[string]any{"cluster": "app-db", "namespace": "prod"}
	for k, v := range values {
		all[k] = v
	}
	v, err := runBackupRequest(context.Background(), plugin.NewRequest(all, dry, false))
	if err != nil {
		t.Fatal(err)
	}
	return renderPairs(t, v)
}

func sentSpec(t *testing.T, raw string) map[string]any {
	t.Helper()
	var sent map[string]any
	if err := json.Unmarshal([]byte(raw), &sent); err != nil {
		t.Fatalf("what was sent is not JSON: %v\n%s", err, raw)
	}
	return sent["spec"].(map[string]any)
}

// A call whose pre-flight read a Secret would be a plugin that reaches further
// than it says; a request is a get or two and a create, and nothing else.
func noSecretsInARequest(t *testing.T, calls []string) {
	t.Helper()
	for _, call := range calls {
		if strings.Contains(strings.ToLower(call), "secret") {
			t.Errorf("kubectl was asked %q: nothing here reads a Secret", call)
		}
		if verb, _, _ := strings.Cut(call, " "); verb != "get" && verb != "create" {
			t.Errorf("kubectl was asked %q: a request is a read and at most one create", call)
		}
	}
}

// The document is the one the barman-cloud plugin's own documentation shows for
// a Backup — the method, and the plugin's name alone — with the name copied from
// the cluster. Nothing else of the cluster's plugin entry travels: its parameters
// configure the WAL archiving, and a plugin's parameters can be anything.
func TestThePluginMethodBuildsTheDocumentThePluginsOwnDocumentationShows(t *testing.T) {
	if !slices.Contains(backupMethods, "plugin") {
		t.Fatal("plugin is not offered")
	}
	method, _ := inputOf(capabilityOf(t, "cnpg.backup.request"), "method")
	if !slices.Equal(method.Options, backupMethods) {
		t.Errorf("the method input offers %v, want the list the check holds a value to: %v", method.Options, backupMethods)
	}

	calls, stdin := routedKubectlWithStdin(t, answers{
		clusterCRD:     {body: pluginCluster(pluginStatusOf(barmanCloudPlugin, "RPC_TYPE_BACKUP"))},
		objectStoreCRD: {body: anObjectStore()},
		"create":       {body: aCreatedBackup()},
	})
	if err := requestAs(t, plugin.SurfaceCLI, false, map[string]any{"method": "plugin"}); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"cluster":             map[string]any{"name": "app-db"},
		"method":              "plugin",
		"pluginConfiguration": map[string]any{"name": barmanCloudPlugin},
	}
	if got := sentSpec(t, stdin()); !reflect.DeepEqual(got, want) {
		t.Errorf("spec = %v\nwant %v", got, want)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(stdin()), &sent)
	if labels := sent["metadata"].(map[string]any)["labels"].(map[string]any); labels[requestedBy] != "rta" {
		t.Errorf("labels = %v", labels)
	}
	noSecretsInARequest(t, calls())
}

// The name of the plugin is the cluster's and nobody else's: there is no input
// through which a caller could give another, and a value a caller sends for one
// that does not exist is not read.
func TestNoCallerCanNameThePlugin(t *testing.T) {
	c := capabilityOf(t, "cnpg.backup.request")
	var names []string
	for _, f := range c.Inputs {
		names = append(names, f.Name)
		if strings.Contains(f.Name, "plugin") {
			t.Errorf("input %q lets a caller name a plugin", f.Name)
		}
	}
	slices.Sort(names)
	want := []string{"cluster", "context", "method", "namespace", "online", "target"}
	if !slices.Equal(names, want) {
		t.Errorf("inputs = %v, want %v", names, want)
	}

	_, stdin := routedKubectlWithStdin(t, answers{
		clusterCRD:     {body: pluginCluster(pluginStatusOf(barmanCloudPlugin, "RPC_TYPE_BACKUP"))},
		objectStoreCRD: {body: anObjectStore()},
		"create":       {body: aCreatedBackup()},
	})
	if err := requestAs(t, plugin.SurfaceCLI, false, map[string]any{
		"method": "plugin", "pluginConfiguration": "evil.example.org", "plugin": "evil.example.org",
		"plugin-name": "evil.example.org"}); err != nil {
		t.Fatal(err)
	}
	if got := sentSpec(t, stdin())["pluginConfiguration"].(map[string]any)["name"]; got != barmanCloudPlugin {
		t.Errorf("the plugin asked is %v, want the cluster's own", got)
	}
}

// An unstated method on a cluster set up for one mechanism resolves to it, and
// every word the receipt says about it says rta chose it — not the cluster, and
// not the caller. And where the backup goes is the ObjectStore's destination,
// without anything in it that could be a credential.
func TestAnUnstatedMethodOnAPluginClusterAsksThePluginAndTheReceiptSaysRtaChoseIt(t *testing.T) {
	for _, dry := range []bool{true, false} {
		calls, stdin := routedKubectlWithStdin(t, answers{
			clusterCRD:     {body: pluginCluster(pluginStatusOf(barmanCloudPlugin, "RPC_TYPE_BACKUP"))},
			objectStoreCRD: {body: anObjectStore()},
			"create":       {body: aCreatedBackup()},
		})
		text := receiptOf(t, dry, nil)

		for _, want := range []string{
			"how: plugin (barman-cloud.cloudnative-pg.io) — chosen by rta: it is the only mechanism this cluster is set up for",
			"where: s3://bucket/path (ObjectStore s3-store, server app-db-old)",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("dry=%v: the receipt does not say %q:\n%s", dry, want, text)
			}
		}
		for _, leaked := range []string{"pw", "X-Amz-Signature", "user:"} {
			if strings.Contains(text, leaked) {
				t.Errorf("dry=%v: the receipt carries %q:\n%s", dry, leaked, text)
			}
		}
		if dry {
			if !strings.Contains(text, `"method":"plugin"`) || !strings.Contains(text, `"pluginConfiguration":{"name":"barman-cloud.cloudnative-pg.io"}`) {
				t.Errorf("the dry run does not show the document it would send:\n%s", text)
			}
			if stdin() != "" {
				t.Errorf("a dry run sent %q", stdin())
			}
			for _, call := range calls() {
				if strings.HasPrefix(call, "create") {
					t.Errorf("a dry run created: %q", call)
				}
			}
		} else if got := sentSpec(t, stdin())["method"]; got != "plugin" {
			t.Errorf("method sent = %v", got)
		}
		noSecretsInARequest(t, calls())
	}
}

// A method the caller named is said to be theirs.
func TestAStatedPluginMethodIsReportedAsStated(t *testing.T) {
	routedKubectlWithStdin(t, answers{
		clusterCRD:     {body: pluginCluster()},
		objectStoreCRD: {body: anObjectStore()},
		"create":       {body: aCreatedBackup()},
	})
	text := receiptOf(t, false, map[string]any{"method": "plugin"})
	if !strings.Contains(text, "how: plugin (barman-cloud.cloudnative-pg.io) (stated for this backup)") {
		t.Errorf("receipt:\n%s", text)
	}
}

// A cluster set up for several mechanisms is a choice between destinations and
// costs, and a backup is a full copy: it is refused with the list until one is
// named, on every surface in its own words.
func TestSeveralMechanismsAreRefusedWithTheListAndNothingIsSent(t *testing.T) {
	both := aCluster("app-db", "prod", alsoSnapshots)
	pluginAndSnapshots := pluginCluster(func(c map[string]any) {
		c["spec"].(map[string]any)["backup"] = map[string]any{"volumeSnapshot": map[string]any{}}
	})
	for _, tc := range []struct {
		name    string
		cluster map[string]any
		list    string
	}{
		{"an object store and snapshots", both, "barmanObjectStore or volumeSnapshot"},
		{"snapshots and a plugin", pluginAndSnapshots, "volumeSnapshot or plugin"},
	} {
		for sf, hint := range map[plugin.Surface]string{
			plugin.SurfaceCLI: "name one with --method, which takes",
			plugin.SurfaceMCP: `name one with the "method" argument, which takes`,
		} {
			calls, stdin := routedKubectlWithStdin(t, answers{clusterCRD: {body: tc.cluster}, "create": {body: aCreatedBackup()}})
			verr := asViewError(t, requestAs(t, sf, false, nil))
			if verr.Code != "cnpg.backup.method.ambiguous" {
				t.Fatalf("%s: code = %q", tc.name, verr.Code)
			}
			if !strings.Contains(verr.Message, tc.list) || !strings.Contains(verr.Hint, hint) {
				t.Errorf("%s/%s: %q / %q, want the list %q and %q", tc.name, sf, verr.Message, verr.Hint, tc.list, hint)
			}
			if stdin() != "" || len(calls()) != 1 {
				t.Errorf("%s: something followed the refusal: %q %q", tc.name, calls(), stdin())
			}
		}
	}

	// Naming one is the way out, and it is sent as named.
	_, stdin := routedKubectlWithStdin(t, answers{clusterCRD: {body: both}, "create": {body: aCreatedBackup()}})
	if err := requestAs(t, plugin.SurfaceCLI, false, map[string]any{"method": "volumeSnapshot"}); err != nil {
		t.Fatal(err)
	}
	if got := sentSpec(t, stdin()); got["method"] != "volumeSnapshot" {
		t.Errorf("spec = %v", got)
	}
}

// A cluster set up for volume snapshots alone is sent a volumeSnapshot Backup,
// and the online flag that only that method takes is available to it without
// being told the method twice.
func TestAClusterSetUpForSnapshotsAloneIsSentThatMethod(t *testing.T) {
	snapshotsOnly := aCluster("app-db", "prod", func(c map[string]any) {
		backup := c["spec"].(map[string]any)["backup"].(map[string]any)
		delete(backup, "barmanObjectStore")
		backup["volumeSnapshot"] = map[string]any{}
	})
	_, stdin := routedKubectlWithStdin(t, answers{clusterCRD: {body: snapshotsOnly}, "create": {body: aCreatedBackup()}})
	text := receiptOf(t, false, map[string]any{"online": "true"})
	spec := sentSpec(t, stdin())
	if spec["method"] != "volumeSnapshot" || spec["online"] != true {
		t.Errorf("spec = %v", spec)
	}
	if !strings.Contains(text, "volumeSnapshot — chosen by rta") || !strings.Contains(text, "volume snapshots of this cluster's volumes") {
		t.Errorf("receipt:\n%s", text)
	}
}

// Asking for the plugin of a cluster that has none to copy a name from is
// refused by rta, with what the cluster does configure.
func TestThePluginMethodIsRefusedForAClusterWithNoPluginEntry(t *testing.T) {
	calls, stdin := routedKubectlWithStdin(t, answers{clusterCRD: {body: aCluster("app-db", "prod")}, "create": {body: aCreatedBackup()}})
	verr := asViewError(t, requestAs(t, plugin.SurfaceCLI, false, map[string]any{"method": "plugin"}))
	if verr.Code != "cnpg.backup.method.unconfigured" || !strings.Contains(verr.Message, "plugin backup") ||
		!strings.Contains(verr.Hint, "pass --method barmanObjectStore") {
		t.Errorf("got %q %q %q", verr.Code, verr.Message, verr.Hint)
	}
	if stdin() != "" || len(calls()) != 1 {
		t.Errorf("something followed the refusal: %q %q", calls(), stdin())
	}
}

// The in-tree method asked of a cluster that archives through a plugin points
// at the method that works, in the words of the surface that asked.
func TestTheInTreeMethodOnAPluginClusterPointsAtThePluginMethod(t *testing.T) {
	for sf, hint := range map[plugin.Surface]string{
		plugin.SurfaceCLI: "pass --method plugin, which asks that plugin to take it",
		plugin.SurfaceMCP: `pass the "method" argument set to "plugin", which asks that plugin to take it`,
	} {
		_, stdin := routedKubectlWithStdin(t, answers{clusterCRD: {body: pluginCluster()}, "create": {body: aCreatedBackup()}})
		verr := asViewError(t, requestAs(t, sf, false, map[string]any{"method": "barmanObjectStore"}))
		if verr.Code != "cnpg.backup.method.unconfigured" || !strings.Contains(verr.Hint, hint) ||
			!strings.Contains(verr.Hint, barmanCloudPlugin) {
			t.Errorf("%s: got %q %q", sf, verr.Code, verr.Hint)
		}
		if stdin() != "" {
			t.Errorf("%s: a backup the cluster cannot perform was sent: %q", sf, stdin())
		}
	}
}

// canTake: every method rta can name is a fact about the cluster, and one it
// cannot name is the API server's to judge.
func TestWhichMethodsAClusterCanTake(t *testing.T) {
	plugged := decodeCluster(t, pluginCluster())
	barman := decodeCluster(t, aCluster("app-db", "prod"))
	for _, tc := range []struct {
		name   string
		c      cluster
		method string
		want   bool
	}{
		{"plugin on a plugin cluster", plugged, "plugin", true},
		{"plugin on an object store cluster", barman, "plugin", false},
		{"the object store on an object store cluster", barman, "barmanObjectStore", true},
		{"the object store on a plugin cluster", plugged, "barmanObjectStore", false},
		{"snapshots on an object store cluster", barman, "volumeSnapshot", false},
		{"a method a later CRD adds", plugged, "somethingNewInTheCRD", true},
		{"a method a later CRD adds, on the other arrangement", barman, "somethingNewInTheCRD", true},
	} {
		if got := tc.c.canTake(tc.method); got != tc.want {
			t.Errorf("%s: canTake(%q) = %v, want %v", tc.name, tc.method, got, tc.want)
		}
	}
}

// The plugin has to have said it can back up. A status that names it and lists
// no such capability is a Backup the operator will refuse to hand over, so the
// request is refused now; one that does not name it at all is the operator
// saying nothing, and is let through.
func TestAPluginThatDeclaresItCannotBackUpIsRefusedBeforeAnythingIsSent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(map[string]any)
		refused bool
	}{
		{"names it with no backup capability", pluginStatusOf(barmanCloudPlugin, "RPC_TYPE_RESTORE"), true},
		{"names it with no capability at all", pluginStatusOf(barmanCloudPlugin), true},
		{"names it with the backup capability", pluginStatusOf(barmanCloudPlugin, "RPC_TYPE_BACKUP"), false},
		{"does not name it", func(map[string]any) {}, false},
		{"names only another plugin", pluginStatusOf("snapshots.example.org"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, stdin := routedKubectlWithStdin(t, answers{
				clusterCRD:     {body: pluginCluster(tc.mutate)},
				objectStoreCRD: {body: anObjectStore()},
				"create":       {body: aCreatedBackup()},
			})
			err := requestAs(t, plugin.SurfaceCLI, false, nil)
			if !tc.refused {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			verr := asViewError(t, err)
			if verr.Code != "cnpg.backup.plugin.unsupported" || !strings.Contains(verr.Hint, backupCapability) {
				t.Errorf("got %q %q", verr.Code, verr.Hint)
			}
			if stdin() != "" || len(calls()) != 1 {
				t.Errorf("something followed the refusal: %q %q", calls(), stdin())
			}
		})
	}
}

// The ObjectStore the cluster names has to be there, or the plugin has nowhere
// to archive to and the Backup is admitted and fails. What cannot be told is not
// refused: an identity that may not read ObjectStores is not an ObjectStore that
// is missing, and the receipt says the destination was not read. And a read that
// failed for any other reason is the call's failure, since nothing should be
// written after a question that went unanswered.
func TestTheObjectStoreIsCheckedBeforeABackupIsAskedOfThePlugin(t *testing.T) {
	cl := pluginCluster(pluginStatusOf(barmanCloudPlugin, "RPC_TYPE_BACKUP"))
	for _, tc := range []struct {
		name   string
		store  answer
		code   string
		sent   bool
		inText []string
	}{
		{"it does not exist", answer{stderr: `Error from server (NotFound): objectstores.barmancloud.cnpg.io "s3-store" not found`},
			"cnpg.backup.objectstore.missing", false, []string{"there is none by that name in namespace prod"}},
		{"its type is not installed", answer{stderr: `error: the server doesn't have a resource type "objectstores"`},
			"cnpg.backup.objectstore.missing", false, []string{"has no ObjectStore resource type"}},
		{"it may not be read", answer{stderr: `Error from server (Forbidden): objectstores.barmancloud.cnpg.io is forbidden: User "u" ` +
			`cannot get resource "objectstores" in API group "barmancloud.cnpg.io" in the namespace "prod"`},
			"", true, []string{"where: ObjectStore s3-store (not read: not allowed to get objectstores.barmancloud.cnpg.io in namespace prod)"}},
		{"the cluster does not answer", answer{stderr: "Unable to connect to the server: dial tcp 10.0.0.1:6443: connect: connection refused"},
			"cnpg.unreachable", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, stdin := routedKubectlWithStdin(t, answers{
				clusterCRD: {body: cl}, objectStoreCRD: tc.store, "create": {body: aCreatedBackup()}})
			v, err := runBackupRequest(context.Background(),
				plugin.NewRequest(map[string]any{"cluster": "app-db", "namespace": "prod"}, false, false))
			if tc.code != "" {
				verr := asViewError(t, err)
				if verr.Code != tc.code {
					t.Fatalf("code = %q, want %q (%s)", verr.Code, tc.code, verr.Message)
				}
				for _, want := range tc.inText {
					if !strings.Contains(verr.Message+verr.Hint, want) {
						t.Errorf("%q / %q do not say %q", verr.Message, verr.Hint, want)
					}
				}
				if stdin() != "" {
					t.Errorf("a Backup was created after the refusal: %q", stdin())
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				text := renderPairs(t, v)
				for _, want := range tc.inText {
					if !strings.Contains(text, want) {
						t.Errorf("the receipt does not say %q:\n%s", want, text)
					}
				}
				if stdin() == "" {
					t.Error("nothing was sent")
				}
			}
			noSecretsInARequest(t, calls())
		})
	}
}

// A plugin that is not the barman-cloud one has no ObjectStore to look for: its
// parameters are its own, and where it sends a backup is the plugin's to say.
func TestAnotherPluginIsAskedAndNoObjectStoreIsLooked(t *testing.T) {
	other := pluginCluster(pluginStatusOf("snapshots.example.org", "RPC_TYPE_BACKUP"), func(c map[string]any) {
		c["spec"].(map[string]any)["plugins"].([]any)[0].(map[string]any)["name"] = "snapshots.example.org"
	})
	calls, stdin := routedKubectlWithStdin(t, answers{clusterCRD: {body: other}, "create": {body: aCreatedBackup()}})
	text := receiptOf(t, false, nil)
	if !strings.Contains(text, "where: wherever the snapshots.example.org plugin this cluster names sends it") {
		t.Errorf("receipt:\n%s", text)
	}
	if got := sentSpec(t, stdin())["pluginConfiguration"].(map[string]any)["name"]; got != "snapshots.example.org" {
		t.Errorf("plugin asked = %v", got)
	}
	for _, call := range calls() {
		if strings.Contains(call, objectStoreCRD) {
			t.Errorf("an ObjectStore was read for another plugin: %q", call)
		}
	}
}

// The in-tree destination is shown with what could be a credential taken out
// of it, where the receipt names where the bytes go.
func TestTheInTreeDestinationIsNamedWithoutUserInfo(t *testing.T) {
	cl := aCluster("app-db", "prod", func(c map[string]any) {
		c["spec"].(map[string]any)["backup"].(map[string]any)["barmanObjectStore"] = map[string]any{
			"destinationPath": "s3://key:token@bucket/prefix?sig=1"}
	})
	routedKubectlWithStdin(t, answers{clusterCRD: {body: cl}, "create": {body: aCreatedBackup()}})
	text := receiptOf(t, true, nil)
	if !strings.Contains(text, "where: wherever this cluster's `.spec.backup` sends it (s3://bucket/prefix)") {
		t.Errorf("receipt:\n%s", text)
	}
	if strings.Contains(text, "token") || strings.Contains(text, "sig=1") {
		t.Errorf("receipt carries what could be a credential:\n%s", text)
	}
}

// pluginDeclaresBackup, at the edges.
func TestWhatTheStatusSaysOfThePluginsBackupCapability(t *testing.T) {
	c := decodeCluster(t, pluginCluster(pluginStatusOf(barmanCloudPlugin, "RPC_TYPE_BACKUP", "RPC_TYPE_RESTORE")))
	if declared, known := c.pluginDeclaresBackup(); !declared || !known {
		t.Errorf("declared %v known %v", declared, known)
	}
	c = decodeCluster(t, pluginCluster())
	if declared, known := c.pluginDeclaresBackup(); declared || known {
		t.Errorf("a status that says nothing: declared %v known %v", declared, known)
	}
	c = decodeCluster(t, aCluster("app-db", "prod"))
	if declared, known := c.pluginDeclaresBackup(); declared || known {
		t.Errorf("a cluster with no plugin: declared %v known %v", declared, known)
	}
}
