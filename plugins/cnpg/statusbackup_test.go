package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What the status page says about backups, for each way a cluster can be
// backed up.
//
// The CRD documents every backup field of a Cluster's status as "not set for
// backup plugins" — the single timestamps and the by-method maps alike — so a
// cluster archived through a WAL-archiver plugin is a cluster whose record is
// empty however well its backups work. These tests hold the page to saying so
// instead of reading the emptiness as "none ever succeeded".

// backupRow is the one row of the problems table that is about backups, and
// whether there was one.
func backupRow(t *testing.T, c cluster, p pointers) (status, detail string, found bool) {
	t.Helper()
	for _, row := range problemTable(c, p).Rows {
		if row[0] == "backup" {
			return row[1], row[2], true
		}
	}
	return "", "", false
}

func rfc3339Ago(d time.Duration) string {
	return time.Now().Add(-d).UTC().Format(time.RFC3339)
}

func withStatus(fields map[string]any) func(map[string]any) {
	return func(c map[string]any) {
		status := c["status"].(map[string]any)
		for k, v := range fields {
			status[k] = v
		}
	}
}

// The false alarm itself: a healthy cluster backed up through the plugin has
// an empty record, and the page said no backup had ever succeeded.
func TestAPluginArchivedClusterIsNotToldNoBackupEverSucceeded(t *testing.T) {
	c := decodeCluster(t, aCluster("shop", "prod", archivedByPlugin))
	call := "`rta cnpg backup list --cluster shop --namespace prod`"

	status, detail, found := backupRow(t, c, pointers{backups: call})
	if !found {
		t.Fatal("the page says nothing about backups on a cluster whose record is empty")
	}
	if status != "info" {
		t.Errorf("graded %q — nothing is known to be wrong, so this is information and not a warning", status)
	}
	if strings.Contains(detail, "no backup has ever succeeded") {
		t.Errorf("the page still claims a negative from a record that cannot hold it: %q", detail)
	}
	for _, want := range []string{"cannot say", "barman-cloud.cloudnative-pg.io", call, "ground truth"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to carry %q", detail, want)
		}
	}

	line := backupLine(c, pointers{backups: call})
	if strings.Contains(line, "none recorded") {
		t.Errorf("overview says %q, which reads as a statement about the cluster", line)
	}
	if !strings.Contains(line, call) || !strings.Contains(line, "not recorded") {
		t.Errorf("overview = %q, want it to say the record is absent and where to look", line)
	}
}

// A cluster whose record could have held an answer and holds none is still the
// finding it always was: the correction must not have switched it off.
func TestAClassicClusterWithAnEmptyRecordIsStillTold(t *testing.T) {
	c := decodeCluster(t, aCluster("shop", "prod"))
	status, detail, found := backupRow(t, c, pointers{backups: "`x`"})
	if !found || status != "warn" || !strings.Contains(detail, "no backup has ever succeeded") {
		t.Errorf("row = %q %q (found %v), want the warning", status, detail, found)
	}
	if got := backupLine(c, pointers{}); got != "none recorded" {
		t.Errorf("overview = %q", got)
	}

	// And with a recorded failure beside the empty record, as before.
	failing := decodeCluster(t, aCluster("shop", "prod", withStatus(map[string]any{
		"lastFailedBackup": rfc3339Ago(30 * time.Minute)})))
	if _, detail, _ := backupRow(t, failing, pointers{}); !strings.Contains(detail, "last attempt failed") {
		t.Errorf("detail = %q", detail)
	}
}

// The by-method maps are read, and a success recorded there is a success: a
// cluster whose only record is the map was reported as never backed up.
func TestTheByMethodMapsAreReadWhenThatIsAllThereIs(t *testing.T) {
	c := decodeCluster(t, aCluster("shop", "prod", withStatus(map[string]any{
		"lastSuccessfulBackupByMethod": map[string]any{
			"barmanObjectStore": rfc3339Ago(5 * time.Hour),
			"volumeSnapshot":    rfc3339Ago(2 * time.Hour),
		},
		"firstRecoverabilityPointByMethod": map[string]any{
			"barmanObjectStore": rfc3339Ago(72 * time.Hour),
			"volumeSnapshot":    rfc3339Ago(48 * time.Hour),
		},
	})))

	if _, _, found := backupRow(t, c, pointers{}); found {
		t.Error("a cluster with a recorded success was given a backup finding")
	}
	last, ever := c.lastSuccess()
	if !ever || last.method != "volumeSnapshot" {
		t.Fatalf("lastSuccess = %+v %v, want the newest of the two methods", last, ever)
	}
	if got := backupLine(c, pointers{}); got != "last succeeded 2h ago" {
		t.Errorf("overview = %q", got)
	}
	if from := c.recoverableFrom(); from != c.Status.FirstRecoverabilityPointByMethod["barmanObjectStore"] {
		t.Errorf("recoverable from %q, want the earliest of the two windows", from)
	}
	if got := overviewOf(t, c)["Recoverable from"]; got != c.Status.FirstRecoverabilityPointByMethod["barmanObjectStore"] {
		t.Errorf("Recoverable from = %q", got)
	}
}

// An operator that predates the map writes the timestamp alone, and one that
// has both writes the timestamp calculated from the map: the answer is the
// newest of the two and carries the method when the map said which.
func TestTheLegacyTimestampAndTheMapAgreeOnWhichIsNewest(t *testing.T) {
	at := rfc3339Ago(3 * time.Hour)
	both := decodeCluster(t, aCluster("shop", "prod", withStatus(map[string]any{
		"lastSuccessfulBackup":         at,
		"lastSuccessfulBackupByMethod": map[string]any{"barmanObjectStore": at},
	})))
	if last, _ := both.lastSuccess(); last.method != "barmanObjectStore" {
		t.Errorf("method = %q, want the map's attribution when both name one instant", last.method)
	}

	legacy := decodeCluster(t, aCluster("shop", "prod", withStatus(map[string]any{"lastSuccessfulBackup": at})))
	if last, ok := legacy.lastSuccess(); !ok || last.method != "" {
		t.Errorf("legacy only = %+v %v", last, ok)
	}

	newer := decodeCluster(t, aCluster("shop", "prod", withStatus(map[string]any{
		"lastSuccessfulBackup":         rfc3339Ago(10 * time.Hour),
		"lastSuccessfulBackupByMethod": map[string]any{"volumeSnapshot": rfc3339Ago(1 * time.Hour)},
	})))
	if last, _ := newer.lastSuccess(); last.method != "volumeSnapshot" {
		t.Errorf("method = %q, want the newer entry whichever field held it", last.method)
	}
}

// A success recorded for an in-tree method on a cluster that also archives
// through a plugin is a real success and not all of the story, and the page
// says both.
func TestARecordedSuccessOnAPluginClusterIsNotTakenForTheWholeStory(t *testing.T) {
	c := decodeCluster(t, aCluster("shop", "prod", archivedByPlugin, withStatus(map[string]any{
		"lastSuccessfulBackupByMethod": map[string]any{"volumeSnapshot": rfc3339Ago(2 * time.Hour)},
	})))
	line := backupLine(c, pointers{})
	for _, want := range []string{"last succeeded 2h ago by volumeSnapshot", "not recorded in the Cluster resource"} {
		if !strings.Contains(line, want) {
			t.Errorf("overview = %q, want it to carry %q", line, want)
		}
	}
	if _, _, found := backupRow(t, c, pointers{}); found {
		t.Error("a recorded success was given a finding")
	}
}

// What a recorded failure still says on a plugin cluster: the record is not
// proof of success, but a failure it holds is real.
func TestAFailureRecordedOnAPluginClusterIsStillMentioned(t *testing.T) {
	c := decodeCluster(t, aCluster("shop", "prod", archivedByPlugin, withStatus(map[string]any{
		"lastFailedBackup": rfc3339Ago(90 * time.Minute)})))
	status, detail, found := backupRow(t, c, pointers{})
	if !found || status != "info" || !strings.Contains(detail, "one failure it does record is 1h old") {
		t.Errorf("row = %q %q (found %v)", status, detail, found)
	}
}

// A cluster that moved from the in-tree mechanism to a plugin keeps the last
// failure the old arrangement recorded, and nothing the plugin does can clear
// it, so a failure newer than the last recorded success is not a warning there.
func TestAFailureTheOldArrangementLeftOnAPluginClusterIsNotAWarning(t *testing.T) {
	c := decodeCluster(t, aCluster("shop", "prod", archivedByPlugin, withStatus(map[string]any{
		"lastSuccessfulBackup": rfc3339Ago(18 * 24 * time.Hour),
		"lastFailedBackup":     rfc3339Ago(17 * 24 * time.Hour),
	})))
	call := "`rta cnpg backup list --cluster shop --namespace prod`"
	status, detail, found := backupRow(t, c, pointers{backups: call})
	if !found || status != "info" {
		t.Fatalf("row = %q %q (found %v), want information and not a warning", status, detail, found)
	}
	for _, want := range []string{"last recorded attempt failed 17d ago", "barman-cloud.cloudnative-pg.io", call, "ground truth"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to carry %q", detail, want)
		}
	}

	// The same dates on a cluster whose record is the record stay a warning.
	classic := decodeCluster(t, aCluster("shop", "prod", withStatus(map[string]any{
		"lastSuccessfulBackup": rfc3339Ago(18 * 24 * time.Hour),
		"lastFailedBackup":     rfc3339Ago(17 * 24 * time.Hour),
	})))
	if status, detail, _ := backupRow(t, classic, pointers{}); status != "warn" || !strings.Contains(detail, "most recent attempt failed") {
		t.Errorf("classic row = %q %q, want the warning", status, detail)
	}
}

// The whole path, from what kubectl prints to the page: the by-method maps and
// the plugin entry decode from the wire shape, and the pointer is a call the
// surface that asked can make.
func TestThePluginClusterPageNamesTheListingForTheSurfaceThatAsked(t *testing.T) {
	for _, tc := range []struct {
		sf   plugin.Surface
		want string
	}{
		{plugin.SurfaceCLI, "`rta cnpg backup list --cluster shop --namespace prod`"},
		{plugin.SurfaceMCP, "`cnpg_backup_list {\"cluster\":\"shop\",\"namespace\":\"prod\"}`"},
	} {
		recordingKubectl(t, mustJSON(t, aCluster("shop", "prod", archivedByPlugin)))
		v, err := runStatus(context.Background(),
			req(map[string]any{"cluster": "shop", "namespace": "prod"}).WithSurface(tc.sf))
		if err != nil {
			t.Fatalf("%s: %v", tc.sf, err)
		}
		sections := v.(view.Sections)
		overview := sections.Items[0].View.(view.KeyValue)
		var line string
		for _, p := range overview.Pairs {
			if p.Key == "Backup" {
				line = p.Value
			}
		}
		if !strings.Contains(line, tc.want) {
			t.Errorf("%s: Backup row = %q, want %q in it", tc.sf, line, tc.want)
		}
		var problems view.Table
		for _, s := range sections.Items {
			if s.Title == "Needs attention" {
				problems = s.View.(view.Table)
			}
		}
		var sawBackup bool
		for _, row := range problems.Rows {
			if row[0] == "backup" {
				sawBackup = true
				if row[1] != "info" || !strings.Contains(row[2], tc.want) {
					t.Errorf("%s: backup row = %v, want info naming %s", tc.sf, row, tc.want)
				}
			}
		}
		if !sawBackup {
			t.Errorf("%s: no backup row in %v", tc.sf, problems.Rows)
		}
	}
}

// The status page makes exactly one GET however the cluster is backed up: a
// pointer to the listing is a sentence and not a second read, which is the
// property its description promises.
func TestTheStatusPageStaysOneRead(t *testing.T) {
	dir := t.TempDir()
	log := dir + "/calls"
	body := dir + "/body.json"
	writeFile(t, body, mustJSON(t, aCluster("shop", "prod", archivedByPlugin)))
	fakeKubectl(t, "echo \"$@\" >> "+log+"\ncat "+body+"\n")

	if _, err := runStatus(context.Background(), req(map[string]any{"cluster": "shop", "namespace": "prod"})); err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(readFile(t, log)), "\n")
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "get "+clusterCRD+" shop") {
		t.Errorf("kubectl was called %d times: %q", len(calls), calls)
	}
}
