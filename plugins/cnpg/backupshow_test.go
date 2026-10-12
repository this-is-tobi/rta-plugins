package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// cnpg.backup.show: how one cluster's backups are arranged, read from the
// Cluster and from the resources beside it, each of which fails on its own.

// answers is what a routed kubectl says to each resource it is asked for: a
// document to print, or a failure to print on stderr and exit with.
type answers map[string]answer

type answer struct {
	body   any
	stderr string
}

// routedKubectl is a kubectl that answers by the resource its get names — the
// second word of the call — and keeps every call it was given, so a test can
// assert on what was asked as well as on what the page says.
func routedKubectl(t *testing.T, routes answers) (calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	var script strings.Builder
	script.WriteString("echo \"$@\" >> " + log + "\ncase \"$2\" in\n")
	i := 0
	for resource, a := range routes {
		i++
		if a.stderr != "" {
			stderr := filepath.Join(dir, fmt.Sprintf("stderr-%d", i))
			writeFile(t, stderr, a.stderr)
			script.WriteString(resource + ") cat " + stderr + " >&2; exit 1 ;;\n")
			continue
		}
		body := filepath.Join(dir, fmt.Sprintf("body-%d.json", i))
		writeFile(t, body, mustJSON(t, a.body))
		script.WriteString(resource + ") cat " + body + " ;;\n")
	}
	script.WriteString("*) echo \"nothing answers for $2\" >&2; exit 1 ;;\nesac\n")
	fakeKubectl(t, script.String())
	return func() []string {
		raw, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
}

// noSecretReads fails if any call this test's kubectl was given asked for a
// Secret, in any spelling, or for anything but a get.
func noSecretReads(t *testing.T, calls []string) {
	t.Helper()
	for _, call := range calls {
		if strings.Contains(strings.ToLower(call), "secret") {
			t.Errorf("kubectl was asked %q: nothing here reads a Secret", call)
		}
		if !strings.HasPrefix(call, "get ") {
			t.Errorf("kubectl was asked %q: nothing here is anything but a get", call)
		}
	}
}

// flatten is every word a view says, for an assertion about what a page does
// and does not contain.
func flatten(v view.View) string {
	var b strings.Builder
	var walk func(view.View)
	walk = func(v view.View) {
		switch got := v.(type) {
		case view.Text:
			b.WriteString(got.Body + "\n")
		case view.KeyValue:
			for _, p := range got.Pairs {
				b.WriteString(p.Key + ": " + p.Value + "\n")
			}
		case view.Table:
			for _, c := range got.Columns {
				b.WriteString(c.Name + " | ")
			}
			b.WriteString("\n")
			for _, row := range got.Rows {
				b.WriteString(strings.Join(row, " | ") + "\n")
			}
		case view.Sections:
			for _, s := range got.Items {
				b.WriteString("## " + s.Title + "\n")
				walk(s.View)
			}
			for _, w := range got.Warnings {
				b.WriteString("WARNING " + w.Code + ": " + w.Message + " " + w.Hint + "\n")
			}
		}
	}
	walk(v)
	return b.String()
}

func sectionOf(t *testing.T, v view.View, id string) (view.View, bool) {
	t.Helper()
	for _, s := range v.(view.Sections).Items {
		if s.ID == id {
			return s.View, true
		}
	}
	return nil, false
}

// pluginCluster is a cluster archived by the barman-cloud plugin, with a
// parameter beside the two this reads and conditions on its status.
func pluginCluster(mutate ...func(map[string]any)) map[string]any {
	return aCluster("app-db", "prod", append([]func(map[string]any){func(c map[string]any) {
		spec := c["spec"].(map[string]any)
		delete(spec, "backup")
		spec["plugins"] = []any{map[string]any{
			"name": barmanCloudPlugin, "isWALArchiver": true,
			"parameters": map[string]any{
				"barmanObjectName": "s3-store", "serverName": "app-db-old",
				"sidecarImage": "registry.example.org/sidecar:token-in-a-parameter",
			},
		}}
		c["status"].(map[string]any)["conditions"] = []any{
			map[string]any{"type": "Ready", "status": "True"},
			map[string]any{"type": "ContinuousArchiving", "status": "True", "reason": "ContinuousArchivingSuccess"},
			map[string]any{"type": "LastBackupSucceeded", "status": "False", "reason": "LastBackupFailed",
				"message": "the last backup failed\nsecond line of barman's stderr"},
		}
	}}, mutate...)...)
}

func anObjectStore() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "s3-store", "namespace": "prod"},
		"spec": map[string]any{
			"configuration": map[string]any{
				"destinationPath": "s3://user:pw@bucket/path?X-Amz-Signature=abc",
				"endpointURL":     "https://key:hunter2@minio.internal:9000/?sig=xyz#frag",
				"s3Credentials": map[string]any{
					"accessKeyId":     map[string]any{"name": "object-creds", "key": "ACCESS_KEY_ID"},
					"secretAccessKey": map[string]any{"name": "object-creds", "key": "SECRET_ACCESS_KEY"},
					"region":          map[string]any{"name": "region-config", "key": "REGION"},
				},
				"endpointCA": map[string]any{"name": "ca-bundle", "key": "ca.crt"},
			},
			"retentionPolicy": "30d",
		},
		"status": map[string]any{"serverRecoveryWindow": map[string]any{
			"app-db-old": map[string]any{
				"firstRecoverabilityPoint": time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339),
				"lastSuccessfulBackupTime": time.Now().Add(-5 * time.Hour).UTC().Format(time.RFC3339),
				"lastFailedBackupTime":     time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
			},
			"another-cluster": map[string]any{"firstRecoverabilityPoint": "2020-01-01T00:00:00Z"},
		}},
	}
}

func scheduledItems(items ...map[string]any) map[string]any { return backupItems(items...) }

func aSchedule(name, cluster string, spec, status map[string]any) map[string]any {
	s := map[string]any{"cluster": map[string]any{"name": cluster}, "schedule": "0 0 2 * * *", "method": "plugin",
		"pluginConfiguration": map[string]any{"name": barmanCloudPlugin}}
	for k, v := range spec {
		s[k] = v
	}
	return map[string]any{"metadata": map[string]any{"name": name}, "spec": s, "status": status}
}

func showOf(t *testing.T, values map[string]any) (view.View, error) {
	t.Helper()
	all := map[string]any{"cluster": "app-db", "namespace": "prod"}
	for k, v := range values {
		all[k] = v
	}
	return runBackupShow(context.Background(), req(all).WithSurface(plugin.SurfaceCLI))
}

// The page of a cluster that archives through the plugin: what the Cluster
// says, the ObjectStore it names, the window that store reports for the cluster,
// the schedules, and the conditions — read through exactly the three gets this
// needs and never a Secret.
func TestThePluginClusterPageReadsTheStoreTheSchedulesAndTheConditions(t *testing.T) {
	calls := routedKubectl(t, answers{
		clusterCRD:     {body: pluginCluster()},
		objectStoreCRD: {body: anObjectStore()},
		scheduledBackupCRD: {body: scheduledItems(
			aSchedule("nightly", "app-db", nil, map[string]any{
				"lastScheduleTime": time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339),
				"nextScheduleTime": time.Now().Add(21 * time.Hour).UTC().Format(time.RFC3339)}),
			aSchedule("weekly", "app-db", map[string]any{"suspend": true}, nil),
			aSchedule("someone-elses", "another-cluster", nil, nil)),
		},
	})
	v, err := showOf(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	page := flatten(v)

	for _, want := range []string{
		"Taken by: the barman-cloud.cloudnative-pg.io plugin, which also archives the WAL",
		"Object store: prod/s3-store",
		"Server name: app-db-old\n",
		"Destination: s3://bucket/path",
		"Endpoint: https://minio.internal:9000/",
		"Retention: 30d",
		"Credentials: S3, from Secrets object-creds, region-config",
		"CA bundle: Secret ca-bundle",
		"app-db-old | ", "(30d ago)", "(5h ago)", "(1h ago)",
		"nightly | ok | 0 0 2 * * * | plugin (barman-cloud.cloudnative-pg.io) | 3h ago | in 20h",
		"weekly | warn | 0 0 2 * * * | plugin (barman-cloud.cloudnative-pg.io) | never | suspended",
		"ContinuousArchiving | ok | ContinuousArchivingSuccess",
		"LastBackupSucceeded | fail | LastBackupFailed | the last backup failed",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not say %q:\n%s", want, page)
		}
	}
	for _, leaked := range []string{
		"pw", "user:", "hunter2", "key:", "X-Amz-Signature", "sig=xyz", "#frag", // a URL's userinfo, query and fragment
		"ACCESS_KEY_ID", "SECRET_ACCESS_KEY", "REGION", "ca.crt", // the keys inside a Secret
		"token-in-a-parameter",    // a plugin parameter nobody asked for
		"another-cluster", "2020", // a neighbour's window, another cluster's schedule
		"someone-elses", "second line of barman",
	} {
		if strings.Contains(page, leaked) {
			t.Errorf("the page carries %q:\n%s", leaked, page)
		}
	}
	if strings.Count(page, "object-creds") != 1 {
		t.Errorf("a Secret named twice is listed %d times", strings.Count(page, "object-creds"))
	}
	if got := calls(); len(got) != 3 {
		t.Errorf("kubectl was called %d times: %q", len(got), got)
	}
	noSecretReads(t, calls())
	if w := v.(view.Sections).Warnings; len(w) != 0 {
		t.Errorf("a complete page carries warnings: %v", w)
	}
}

// A cluster whose object store is its own stanza reads nothing but the
// Cluster and its schedules, and says what the Cluster says.
func TestAnInTreeClusterIsShownFromWhatItStatesAboutItself(t *testing.T) {
	cl := aCluster("app-db", "prod", func(c map[string]any) {
		backup := c["spec"].(map[string]any)["backup"].(map[string]any)
		backup["retentionPolicy"] = "14d"
		backup["barmanObjectStore"] = map[string]any{
			"destinationPath": "s3://bucket:token@store/prefix?sig=1",
			"serverName":      "legacy",
			"s3Credentials":   map[string]any{"accessKeyId": map[string]any{"name": "in-tree-creds", "key": "K"}},
		}
		c["status"].(map[string]any)["lastSuccessfulBackup"] = rfc3339Ago(2 * time.Hour)
		c["status"].(map[string]any)["firstRecoverabilityPoint"] = rfc3339Ago(48 * time.Hour)
	})
	calls := routedKubectl(t, answers{clusterCRD: {body: cl}, scheduledBackupCRD: {body: scheduledItems()}})

	v, err := showOf(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	page := flatten(v)
	for _, want := range []string{"Taken by: barmanObjectStore", "Destination: s3://store/prefix",
		"Server name: legacy\n", "Retention: 14d", "legacy | ", "(2d ago)", "(2h ago)",
		"No ScheduledBackup names this cluster"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not say %q:\n%s", want, page)
		}
	}
	for _, leaked := range []string{"token", "sig=1", "in-tree-creds"} {
		if strings.Contains(page, leaked) {
			t.Errorf("the page carries %q:\n%s", leaked, page)
		}
	}
	if _, has := sectionOf(t, v, "object-store"); has {
		t.Error("an in-tree cluster has a plugin's object store section")
	}
	if got := calls(); len(got) != 2 {
		t.Errorf("kubectl was called %d times: %q, want the Cluster and its schedules", len(got), got)
	}
	noSecretReads(t, calls())
}

// The cluster states no server name, so its own name is the one its backups
// are filed under, and the page says that is where the name came from.
func TestAServerNameNobodyStatedIsTheClustersOwn(t *testing.T) {
	cl := pluginCluster(func(c map[string]any) {
		c["spec"].(map[string]any)["plugins"].([]any)[0].(map[string]any)["parameters"] = map[string]any{
			"barmanObjectName": "s3-store"}
	})
	store := anObjectStore()
	store["status"] = map[string]any{"serverRecoveryWindow": map[string]any{
		"app-db": map[string]any{"firstRecoverabilityPoint": rfc3339Ago(24 * time.Hour)}}}
	routedKubectl(t, answers{clusterCRD: {body: cl}, objectStoreCRD: {body: store}, scheduledBackupCRD: {body: scheduledItems()}})
	v, err := showOf(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	page := flatten(v)
	if !strings.Contains(page, "Server name: app-db (the cluster's own name)") || !strings.Contains(page, "app-db | ") {
		t.Errorf("page:\n%s", page)
	}
}

// Each read beside the Cluster stands alone. A refusal becomes a note in its
// section and a warning that the page is partial; an absent type or object is
// an answer, and the page is not partial for it; and what was read stands.
func TestEachReadBesideTheClusterDegradesOnItsOwn(t *testing.T) {
	const forbidden = `Error from server (Forbidden): %s is forbidden: User "u" cannot get resource "%s" in API group "%s" in the namespace "prod"`
	for _, tc := range []struct {
		name    string
		routes  answers
		want    []string
		partial bool
		absent  []string
	}{
		{
			name: "the ObjectStore is refused",
			routes: answers{clusterCRD: {body: pluginCluster()},
				objectStoreCRD:     {stderr: fmt.Sprintf(forbidden, objectStoreCRD, "objectstores", "barmancloud.cnpg.io")},
				scheduledBackupCRD: {body: scheduledItems()}},
			want:    []string{"## Object store", "Not read: not allowed to get objectstores.barmancloud.cnpg.io in namespace prod", "## Schedules", "## Backups", "WARNING cnpg.backup.show.partial: the ObjectStore s3-store was not read"},
			partial: true,
			absent:  []string{"## Recovery window"},
		},
		{
			name: "the schedules are refused",
			routes: answers{clusterCRD: {body: pluginCluster()}, objectStoreCRD: {body: anObjectStore()},
				scheduledBackupCRD: {stderr: fmt.Sprintf(forbidden, scheduledBackupCRD, "scheduledbackups", "postgresql.cnpg.io")}},
			want:    []string{"## Object store", "## Recovery window", "## Schedules", "Not read: not allowed to get scheduledbackups.postgresql.cnpg.io", "WARNING cnpg.backup.show.partial: the ScheduledBackups was not read"},
			partial: true,
		},
		{
			name: "the ObjectStore type is not installed",
			routes: answers{clusterCRD: {body: pluginCluster()},
				objectStoreCRD:     {stderr: `error: the server doesn't have a resource type "objectstores"`},
				scheduledBackupCRD: {body: scheduledItems()}},
			want:   []string{"the barman-cloud plugin is not installed", objectStoreCRD + " is not registered", "## Schedules"},
			absent: []string{"WARNING"},
		},
		{
			name: "the ObjectStore the cluster names does not exist",
			routes: answers{clusterCRD: {body: pluginCluster()},
				objectStoreCRD:     {stderr: `Error from server (NotFound): objectstores.barmancloud.cnpg.io "s3-store" not found`},
				scheduledBackupCRD: {body: scheduledItems()}},
			want:   []string{"there is none by that name in namespace prod", "a backup asked of it fails"},
			absent: []string{"WARNING"},
		},
		{
			name: "the ScheduledBackup type is not installed",
			routes: answers{clusterCRD: {body: pluginCluster()}, objectStoreCRD: {body: anObjectStore()},
				scheduledBackupCRD: {stderr: `error: the server doesn't have a resource type "scheduledbackups"`}},
			want:   []string{"has no ScheduledBackup resource type", "## Object store"},
			absent: []string{"WARNING"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := routedKubectl(t, tc.routes)
			v, err := showOf(t, nil)
			if err != nil {
				t.Fatalf("a read beside the Cluster failed the whole page: %v", err)
			}
			page := flatten(v)
			for _, want := range tc.want {
				if !strings.Contains(page, want) {
					t.Errorf("the page does not say %q:\n%s", want, page)
				}
			}
			for _, not := range tc.absent {
				if strings.Contains(page, not) {
					t.Errorf("the page carries %q:\n%s", not, page)
				}
			}
			if got := len(v.(view.Sections).Warnings) > 0; got != tc.partial {
				t.Errorf("partial = %v, want %v", got, tc.partial)
			}
			noSecretReads(t, calls())
		})
	}
}

// The Cluster is the one read whose failure is the page's: with it the
// arrangement is known, and without it nothing is.
func TestARefusedClusterReadFailsThePage(t *testing.T) {
	routedKubectl(t, answers{clusterCRD: {stderr: `Error from server (Forbidden): clusters.postgresql.cnpg.io is forbidden: ` +
		`User "u" cannot get resource "clusters" in API group "postgresql.cnpg.io" in the namespace "prod"`}})
	_, err := showOf(t, nil)
	if verr := asViewError(t, err); verr.Code != "cnpg.denied" || !strings.Contains(verr.Message, "get clusters.postgresql.cnpg.io") {
		t.Errorf("got %q %q", verr.Code, verr.Message)
	}
}

// A cluster that configures nothing says so, and the page does not go looking
// for an object store it has none of.
func TestAClusterWithNoBackupIsShownAsHavingNone(t *testing.T) {
	bare := aCluster("app-db", "prod", func(c map[string]any) { delete(c["spec"].(map[string]any), "backup") })
	calls := routedKubectl(t, answers{clusterCRD: {body: bare}, scheduledBackupCRD: {body: scheduledItems()}})
	v, err := showOf(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page := flatten(v); !strings.Contains(page, "nothing — this cluster configures no backup") {
		t.Errorf("page:\n%s", page)
	}
	if got := calls(); len(got) != 2 {
		t.Errorf("calls = %q", got)
	}

	hollow := aCluster("app-db", "prod", func(c map[string]any) {
		c["spec"].(map[string]any)["backup"] = map[string]any{"retentionPolicy": "30d"}
	})
	routedKubectl(t, answers{clusterCRD: {body: hollow}, scheduledBackupCRD: {body: scheduledItems()}})
	v, _ = showOf(t, nil)
	if page := flatten(v); !strings.Contains(page, "`.spec.backup` names no mechanism") {
		t.Errorf("page:\n%s", page)
	}
}

// A plugin that is not the barman-cloud one is not read for an ObjectStore: a
// parameter of the same spelling is that plugin's own word.
func TestAnotherPluginsParameterIsNotTakenForAnObjectStore(t *testing.T) {
	other := pluginCluster(func(c map[string]any) {
		c["spec"].(map[string]any)["plugins"].([]any)[0].(map[string]any)["name"] = "snapshots.example.org"
	})
	calls := routedKubectl(t, answers{clusterCRD: {body: other}, scheduledBackupCRD: {body: scheduledItems()}})
	v, err := showOf(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	page := flatten(v)
	if !strings.Contains(page, "Plugin configuration: kept in the plugin's own resources, which this does not read") {
		t.Errorf("page:\n%s", page)
	}
	for _, call := range calls() {
		if strings.Contains(call, objectStoreCRD) {
			t.Errorf("an ObjectStore was read for another plugin: %q", call)
		}
	}
}

// No read of a Secret can be made by this plugin at all: the refusal is in the
// one place every get goes through, so a later slip fails closed.
func TestNoGetOfASecretIsEverMade(t *testing.T) {
	for _, resource := range []string{"secrets", "secret", "Secrets", "secret/creds", "secrets.v1", "pods,secrets", "secrets.", " secrets"} {
		argv := []string{"get", resource, "-o", "json"}
		if verr := refusesSecrets(argv); verr == nil || verr.Code != "cnpg.secrets.refused" {
			t.Errorf("a get of %q was not refused", resource)
		}
	}
	for _, args := range [][]string{
		{"get", clusterCRD, "secrets", "-o", "json"}, // a cluster that is called that
		{"get", objectStoreCRD, "s3-store"},
		{"get", "persistentvolumeclaims", "-l", "cnpg.io/cluster"},
		{"config", "get-contexts"},
	} {
		if verr := refusesSecrets(args); verr != nil {
			t.Errorf("%v was refused: %v", args, verr)
		}
	}

	calls := routedKubectl(t, answers{"secrets": {body: map[string]any{"items": []any{}}}})
	var list clusterList
	verr := getResource(context.Background(), selection{namespace: "prod"}, "secrets", "", &list)
	if verr == nil || verr.Code != "cnpg.secrets.refused" {
		t.Errorf("getResource of secrets = %v", verr)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("kubectl was run for a Secret: %q", got)
	}
}

func TestAURLIsShownWithoutAnythingThatCouldBeACredential(t *testing.T) {
	for raw, want := range map[string]string{
		"s3://bucket/path":                           "s3://bucket/path",
		"s3://user:pw@bucket/path":                   "s3://bucket/path",
		"https://key:p@ss@host:9000/a/b":             "https://host:9000/a/b",
		"https://host/path?sv=2020&sig=abc":          "https://host/path",
		"https://u:p@host/path?sig=abc#fragment":     "https://host/path",
		"  https://host/a  ":                         "https://host/a",
		"a hand-written value with no scheme":        "a hand-written value with no scheme",
		"azure://user@account/container":             "azure://account/container",
		"":                                           "",
		"https://host/a@b-is-in-the-path-not-a-user": "https://host/a@b-is-in-the-path-not-a-user",
	} {
		if got := plainURL(raw); got != want {
			t.Errorf("plainURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTheCredentialsSayTheKindAndTheNamesAndNothingElse(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*objectStore)
		want   string
	}{
		"s3 secrets, deduplicated": {func(o *objectStore) {
			o.Spec.Configuration.S3.AccessKeyID.Name = "b"
			o.Spec.Configuration.S3.SecretAccessKey.Name = "b"
			o.Spec.Configuration.S3.Region.Name = "a"
		}, "S3, from Secrets a, b"},
		"one secret": {func(o *objectStore) { o.Spec.Configuration.S3.AccessKeyID.Name = "only" }, "S3, from Secret only"},
		"an IAM role": {func(o *objectStore) { o.Spec.Configuration.S3.InheritFromIAMRole = true },
			"S3, inherited from the pod's IAM role"},
		"azure": {func(o *objectStore) { o.Spec.Configuration.Azure.StorageKey.Name = "az" }, "Azure, from Secret az"},
		"azure ad": {func(o *objectStore) { o.Spec.Configuration.Azure.InheritFromAzureAD = true },
			"Azure, inherited from Azure AD"},
		"google": {func(o *objectStore) { o.Spec.Configuration.Google.ApplicationCredentials.Name = "gcp" }, "Google, from Secret gcp"},
		"gke":    {func(o *objectStore) { o.Spec.Configuration.Google.GKEEnvironment = true }, "Google, inherited from the GKE environment"},
		"none":   {func(*objectStore) {}, "none stated"},
	} {
		var o objectStore
		tc.mutate(&o)
		if got := o.credentials(); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// What the structs that decode a stanza with credentials in it can hold: the
// name of a Secret and no key of one, and from a plugin's parameters, the two
// keys that are read. Asserted on the decode, as the Backup status's is.
func TestTheStructsCannotHoldAKeyOrAParameterNobodyAskedFor(t *testing.T) {
	c := decodeCluster(t, pluginCluster())
	got := fmt.Sprintf("%+v", c.Spec.Plugins)
	if strings.Contains(got, "token-in-a-parameter") {
		t.Errorf("a plugin parameter survived the decode: %s", got)
	}
	if name := c.walArchiver().Parameters.BarmanObjectName; name != "s3-store" {
		t.Errorf("the object store the entry names = %q", name)
	}

	var o objectStore
	raw := mustJSON(t, anObjectStore())
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY", "ca.crt"} {
		if strings.Contains(fmt.Sprintf("%+v", o), leaked) {
			t.Errorf("%q survived the decode: the struct can hold the key inside a Secret", leaked)
		}
	}
}

// The capability is a read, and its example gives a cluster and nothing else.
func TestTheShowCapabilityIsAReadWithAnExampleThatGivesAClusterOnly(t *testing.T) {
	c := capabilityOf(t, "cnpg.backup.show")
	if c.Safety != plugin.Read || !c.Idempotent || c.NeedsGrant {
		t.Errorf("safety %v idempotent %v needs grant %v", c.Safety, c.Idempotent, c.NeedsGrant)
	}
	if len(c.Examples) != 1 || !slices.Equal(mapKeys(c.Examples[0].Inputs), []string{"cluster"}) {
		t.Errorf("examples = %+v, want one that gives a cluster only", c.Examples)
	}
	if field, _ := inputOf(c, "cluster"); !field.Required {
		t.Error("the cluster is optional on a read of one cluster's arrangement")
	}
	if _, wide := inputOf(c, "all-namespaces"); wide {
		t.Error("a read of one cluster has a wide form")
	}
}

func mapKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// What the status page of a plugin-archived cluster points at beside the
// listing: this capability, spelled for the surface that asked.
func TestTheStatusOfAPluginClusterAlsoNamesTheShow(t *testing.T) {
	for _, tc := range []struct {
		sf   plugin.Surface
		want string
	}{
		{plugin.SurfaceCLI, "and `rta cnpg backup show --cluster shop --namespace prod` shows where they go"},
		{plugin.SurfaceMCP, "and `cnpg_backup_show {\"cluster\":\"shop\",\"namespace\":\"prod\"}` shows where they go"},
	} {
		recordingKubectl(t, mustJSON(t, aCluster("shop", "prod", archivedByPlugin)))
		v, err := runStatus(context.Background(),
			req(map[string]any{"cluster": "shop", "namespace": "prod"}).WithSurface(tc.sf))
		if err != nil {
			t.Fatal(err)
		}
		if page := flatten(v); strings.Count(page, tc.want) != 2 {
			t.Errorf("%s: the page does not point at the show twice (overview and finding), want %q:\n%s", tc.sf, tc.want, page)
		}
	}
}
