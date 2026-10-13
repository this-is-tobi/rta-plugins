package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The WAL a Backup records itself as spanning — where its log starts and ends,
// as a position and as the file that position is in — is the one place in this
// plugin a log position comes from, and it comes from the Backup object and
// from nowhere else. These tests are about where it is shown, how whole it is
// shown, and that the listing is left exactly the table it was.

const (
	// The longest a position gets: two hex halves with no padding.
	longestLSN = "FFFFFFFF/FFFFFFFF"
	longerLSN  = "FFFFFFFE/FFFFFF00"
)

// withWAL records a WAL range on a Backup document, leaving out the ends that
// are empty: a backup that is running has the beginning and not the end.
func withWAL(b map[string]any, beginLSN, endLSN, beginWal, endWal string) map[string]any {
	status := b["status"].(map[string]any)
	for key, value := range map[string]string{
		"beginLSN": beginLSN, "endLSN": endLSN, "beginWal": beginWal, "endWal": endWal,
	} {
		if value != "" {
			status[key] = value
		}
	}
	return b
}

func listOf(t *testing.T, values map[string]any, doc any) view.View {
	t.Helper()
	serves(t, doc)
	v, err := runBackupList(context.Background(), req(values))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestABackupsWALIsDecodedAsTheOperatorWroteIt(t *testing.T) {
	raw := `{"items":[{"metadata":{"name":"b1"},"spec":{"cluster":{"name":"shop"}},
	 "status":{"phase":"completed","beginLSN":" 16/B374D848 ","endLSN":"` + longestLSN + `",
	  "beginWal":"0000000100000016000000B3","endWal":"0000000100000016000000B4"}}]}`
	var list backupList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	r := list.Items[0].walRange()
	want := walRange{
		beginLSN: "16/B374D848", endLSN: longestLSN,
		beginWal: "0000000100000016000000B3", endWal: "0000000100000016000000B4",
	}
	if r != want || !r.recorded() {
		t.Errorf("range = %+v, want %+v", r, want)
	}
	var none backupObject
	if none.walRange().recorded() {
		t.Error("a Backup that says nothing of its WAL was read as recording one")
	}
}

// The listing is the table it was whatever the backups record: a table to copy,
// a row to select and one shape for anything that reads it. Every completed
// backup records a range, so a section for it would have turned the common
// read into a page for every healthy cluster; the range is the arrangement
// page's, for the newest completed backup.
func TestTheListingIsTheSameTableWhateverTheBackupsRecord(t *testing.T) {
	done := withWAL(aBackup("shop-1", "prod", "shop", "completed", 2*time.Hour),
		longestLSN, longerLSN, "0000000100000000FFFFFFFE", "0000000100000000FFFFFFFF")
	running := withWAL(aBackup("shop-2", "prod", "shop", "running", time.Hour),
		"0/3000028", "", "000000010000000000000003", "")
	bare := aBackup("shop-3", "prod", "shop", "completed", 3*time.Hour)
	v := listOf(t, nil, backupItems(done, running, bare))

	tbl, isTable := v.(view.Table)
	if !isTable {
		t.Fatalf("view is %T, want the bare table", v)
	}
	if got := strings.Join(columnNames(tbl), ","); got != "Backup,Cluster,Status,Phase,Method,Took,Age,Where" {
		t.Errorf("the listing's columns = %s: the table changed", got)
	}
	if len(tbl.Rows) != 3 {
		t.Errorf("the listing has %d rows, want the three backups", len(tbl.Rows))
	}
}

// A failed backup is still in the failures, which is the one section a listing
// grows: the range it got as far as does not add another.
func TestAFailedBackupIsInTheFailuresAndAddsNoRangeSection(t *testing.T) {
	failed := withWAL(aBackup("shop-1", "prod", "shop", "failed", time.Hour), "0/3000028", "", "000000010000000000000003", "")
	v := listOf(t, nil, backupItems(failed))
	var titles []string
	for _, s := range v.(view.Sections).Items {
		titles = append(titles, s.Title)
	}
	if got := strings.Join(titles, ","); got != "Backups,What failed" {
		t.Errorf("sections = %s, want the table and the failures only", got)
	}
}

// The arrangement page ends its account of the backups with the newest one that
// finished and the WAL it spans — not the newest, which may be running and has
// no end yet, and not another cluster's.
func TestTheArrangementPageShowsTheLatestCompletedBackupsWAL(t *testing.T) {
	older := withWAL(aBackup("app-db-1", "prod", "app-db", "completed", 48*time.Hour),
		"0/1000028", "0/1000138", "000000010000000000000001", "000000010000000000000001")
	latest := withWAL(aBackup("app-db-2", "prod", "app-db", "completed", 24*time.Hour),
		longestLSN, longerLSN, "0000000100000000FFFFFFFE", "0000000100000000FFFFFFFF")
	latest["status"].(map[string]any)["method"] = "plugin"
	latest["status"].(map[string]any)["stoppedAt"] = rfc3339Ago(23 * time.Hour)
	running := withWAL(aBackup("app-db-3", "prod", "app-db", "running", time.Hour), "0/5000028", "", "000000010000000000000005", "")
	neighbour := withWAL(aBackup("other-9", "prod", "other", "completed", time.Minute), "0/9000028", "0/9000138", "000000010000000000000009", "000000010000000000000009")
	calls := routedKubectl(t, answers{
		clusterCRD: {body: pluginCluster()}, objectStoreCRD: {body: anObjectStore()},
		backupCRD:          {body: backupItems(older, latest, running, neighbour)},
		scheduledBackupCRD: {body: scheduledItems()},
	})
	v, err := showOf(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	section, has := sectionOf(t, v, "latest-backup")
	if !has {
		t.Fatalf("no latest backup section:\n%s", flatten(v))
	}
	got := map[string]string{}
	for _, p := range section.(view.KeyValue).Pairs {
		got[p.Key] = p.Value
	}
	for key, want := range map[string]string{
		"Backup": "app-db-2", "Method": "plugin", "Begin LSN": longestLSN, "End LSN": longerLSN,
		"Begin WAL": "0000000100000000FFFFFFFE", "End WAL": "0000000100000000FFFFFFFF",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	if !strings.Contains(got["Finished"], "23h ago") {
		t.Errorf("Finished = %q, want when it stopped", got["Finished"])
	}
	if page := flatten(v); strings.Contains(page, "0/5000028") || strings.Contains(page, "0/9000028") ||
		strings.Contains(page, "0/1000028") {
		t.Errorf("the page carries the range of a backup that is not the latest completed one of this cluster:\n%s", page)
	}
	noSecretReads(t, calls())
}

// Only what the Backup records is shown, and a backup that records none says
// so rather than leaving the section looking as though it were cut short.
func TestTheLatestBackupShowsOnlyTheEndsItRecords(t *testing.T) {
	partial := withWAL(aBackup("app-db-1", "prod", "app-db", "completed", time.Hour), "", "0/3000138", "", "000000010000000000000003")
	routedKubectl(t, answers{clusterCRD: {body: pluginCluster()}, objectStoreCRD: {body: anObjectStore()},
		backupCRD: {body: backupItems(partial)}, scheduledBackupCRD: {body: scheduledItems()}})
	v, err := showOf(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	section, _ := sectionOf(t, v, "latest-backup")
	keys := map[string]bool{}
	for _, p := range section.(view.KeyValue).Pairs {
		keys[p.Key] = true
	}
	if keys["Begin LSN"] || keys["Begin WAL"] || !keys["End LSN"] || !keys["End WAL"] || keys["WAL range"] {
		t.Errorf("pairs = %v, want the end and not the beginning", keys)
	}

	routedKubectl(t, answers{clusterCRD: {body: pluginCluster()}, objectStoreCRD: {body: anObjectStore()},
		backupCRD:          {body: backupItems(aBackup("app-db-1", "prod", "app-db", "completed", time.Hour))},
		scheduledBackupCRD: {body: scheduledItems()}})
	v, _ = showOf(t, nil)
	if page := flatten(v); !strings.Contains(page, "WAL range: not recorded on this Backup") ||
		strings.Contains(page, "LSN") {
		t.Errorf("a backup that records no range:\n%s", page)
	}
}

// Without a completed backup there is nothing to read a range from, and the
// page says which of the two reasons it is, with the call that lists them.
func TestWithoutACompletedBackupTheLatestSectionSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []map[string]any
		want  string
	}{
		{"no Backup names the cluster", []map[string]any{aBackup("other-1", "prod", "other", "completed", time.Hour)},
			"No Backup object names this cluster"},
		{"none has completed", []map[string]any{aBackup("app-db-1", "prod", "app-db", "failed", time.Hour),
			aBackup("app-db-2", "prod", "app-db", "running", time.Minute)},
			"None of the 2 Backup objects naming this cluster has completed"},
	} {
		routedKubectl(t, answers{clusterCRD: {body: pluginCluster()}, objectStoreCRD: {body: anObjectStore()},
			backupCRD: {body: backupItems(tc.items...)}, scheduledBackupCRD: {body: scheduledItems()}})
		v, err := showOf(t, nil)
		if err != nil {
			t.Fatal(err)
		}
		section, _ := sectionOf(t, v, "latest-backup")
		body := section.(view.Text).Body
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s: %q does not say %q", tc.name, body, tc.want)
		}
		if tc.name == "none has completed" && !strings.Contains(body, "`rta cnpg backup list --cluster app-db --namespace prod`") {
			t.Errorf("%s: %q does not name the listing", tc.name, body)
		}
		if w := v.(view.Sections).Warnings; len(w) != 0 {
			t.Errorf("%s: an answer is not a partial page: %v", tc.name, w)
		}
	}
}

// The pointer names the listing the way the surface that asked spells it.
func TestTheLatestSectionNamesTheListingInTheWordsOfItsSurface(t *testing.T) {
	routedKubectl(t, answers{clusterCRD: {body: pluginCluster()}, objectStoreCRD: {body: anObjectStore()},
		backupCRD:          {body: backupItems(aBackup("app-db-1", "prod", "app-db", "failed", time.Hour))},
		scheduledBackupCRD: {body: scheduledItems()}})
	v, err := runBackupShow(context.Background(),
		req(map[string]any{"cluster": "app-db", "namespace": "prod"}).WithSurface(plugin.SurfaceMCP))
	if err != nil {
		t.Fatal(err)
	}
	section, _ := sectionOf(t, v, "latest-backup")
	if body := section.(view.Text).Body; !strings.Contains(body, `cnpg_backup_list {"cluster":"app-db","namespace":"prod"}`) {
		t.Errorf("body = %q, want the tool call", body)
	}
}
