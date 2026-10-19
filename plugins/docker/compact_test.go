package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The compact answer is what a dashboard tile and an agent read by default: a
// verdict first, then the containers, what is wrong with them, and where the
// disk went. The order, the count and the status row are the contract these
// tests hold; the words are checked where a reader would notice them missing.

var compactKeys = []string{"status", "containers", "problems", "disk"}

// container is one line of `docker ps --format json`, written out.
func container(name, state, status, health string) string {
	return fmt.Sprintf(`{"ID":"sha256:%064x","Names":%q,"Image":"app:1","State":%q,"Status":%q,"HealthStatus":%q}`,
		len(name), name, state, status, health)
}

// anUnwellDaemon is the daemon the audit of the tiles read: ten containers of
// which four are running, one failing its healthcheck, one restarting, one
// crashed with 137, one paused and three one-shots that finished cleanly.
func anUnwellDaemon() []string {
	return []string{
		container("web", "running", "Up 3 hours (unhealthy)", "unhealthy"),
		container("api", "running", "Up 3 hours (healthy)", "healthy"),
		container("db", "running", "Up 2 days", ""),
		container("cache", "running", "Up 2 days", ""),
		container("worker", "restarting", "Restarting (1) 5 seconds ago", ""),
		container("etl", "exited", "Exited (137) 3 days ago", ""),
		container("migrate-20240101", "exited", "Exited (0) 3 weeks ago", ""),
		container("migrate-20240201", "exited", "Exited (0) 2 weeks ago", ""),
		container("seed", "exited", "Exited (0) 3 weeks ago", ""),
		container("old-sidecar", "paused", "Up 3 hours (Paused)", ""),
	}
}

const dfJSON = `{"Type":"Images","TotalCount":"7","Active":"5","Size":"1.35GB","Reclaimable":"290MB (21%)"}
{"Type":"Containers","TotalCount":"10","Active":"4","Size":"12MB","Reclaimable":"8MB (66%)"}
{"Type":"Local Volumes","TotalCount":"12","Active":"3","Size":"48.2GB","Reclaimable":"31GB (64%)"}
{"Type":"Build Cache","TotalCount":"212","Active":"0","Size":"9.4GB","Reclaimable":"9.4GB"}
`

const imagesJSON = `{"ID":"sha256:aaaaaaaaaaaaaaaaaaaa","Repository":"app","Tag":"1","Size":"1.05GB","CreatedSince":"2 days ago"}
{"ID":"sha256:bbbbbbbbbbbbbbbbbbbb","Repository":"<none>","Tag":"<none>","Size":"300MB","CreatedSince":"3 weeks ago"}
`

// fixture is what a fake docker answers, and the calls it records.
type fixture struct {
	containers []string
	df         string
	// dfFails and imagesFail are the stderr the call fails with; empty answers.
	dfFails, imagesFail string
}

// served puts a docker on the PATH of the plugin that answers `ps`, `system df`
// and `image ls` from the fixture and appends every call it gets to a log, and
// returns the function that reads the log.
func served(t *testing.T, fx fixture) func() []string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ps := write("ps.jsonl", strings.Join(fx.containers, "\n")+"\n")
	df := write("df.jsonl", fx.df)
	images := write("images.jsonl", imagesJSON)
	dfErr := write("df.err", fx.dfFails)
	imagesErr := write("images.err", fx.imagesFail)
	log := filepath.Join(dir, "calls.log")
	scriptedDocker(t, `echo "$*" >> `+log+`
case " $* " in
  *" system df "*)
    if [ -s `+dfErr+` ]; then cat `+dfErr+` >&2; exit 1; fi
    cat `+df+` ;;
  *" image ls "*)
    if [ -s `+imagesErr+` ]; then cat `+imagesErr+` >&2; exit 1; fi
    cat `+images+` ;;
  *" ps "*) cat `+ps+` ;;
esac
`)
	return func() []string {
		raw, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
}

func compactPairs(t *testing.T, values map[string]any) []view.Pair {
	t.Helper()
	v, err := runOverview(t.Context(), overviewReq(t, values))
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("the compact answer is %s, want key/value rows", view.TypeOf(v))
	}
	return kv.Pairs
}

func rowsByKey(pairs []view.Pair) map[string]string {
	out := map[string]string{}
	for _, p := range pairs {
		out[p.Key] = p.Value
	}
	return out
}

func keysOf(pairs []view.Pair) []string {
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.Key
	}
	return out
}

// assertOneLinePerRow holds the shape rule every state keeps: no value spans
// lines, and a row is at most 80 columns once a renderer has set it beside the
// widest key. The hint beside a daemon that did not answer is the one row that
// wraps, because advice cut short at a column is not advice.
func assertOneLinePerRow(t *testing.T, pairs []view.Pair) {
	t.Helper()
	key := 0
	for _, p := range pairs {
		key = max(key, utf8.RuneCountInString(p.Key))
	}
	for _, p := range pairs {
		if strings.ContainsAny(p.Value, "\n\r") {
			t.Errorf("%s spans lines: %q", p.Key, p.Value)
		}
		if w := key + 2 + utf8.RuneCountInString(p.Value); w > 80 && p.Key != "what to check" {
			t.Errorf("%s is %d columns wide, want at most 80: %q", p.Key, w, p.Value)
		}
	}
}

func TestAnUnwellDaemonLeadsWithTheVerdictThenWhatIsWrongThenTheDisk(t *testing.T) {
	served(t, fixture{containers: anUnwellDaemon(), df: dfJSON})
	pairs := compactPairs(t, map[string]any{})
	if got := keysOf(pairs); !slices.Equal(got, compactKeys) {
		t.Fatalf("rows = %v, want %v", got, compactKeys)
	}
	assertOneLinePerRow(t, pairs)
	rows := rowsByKey(pairs)
	for key, want := range map[string]string{
		// A failure first, then what is only a warning, as the same words the
		// theme colours.
		"status": "fail — 1 unhealthy, 1 restarting, 1 exited non-zero, 1 not running",
		// The ones that are broken now ahead of the ones that stopped, the exit
		// code read from the status text, and the cap of the line kept.
		"problems": "web (unhealthy), worker (restarting), etl (exit 137) and 1 more",
		// The one-shots that finished are a clause and not four names.
		"containers": "4 running of 10 · 3 exited cleanly",
		// Where the disk went, the largest first, and what could be taken back.
		"disk": "volumes 48.2GB, cache 9.4GB, images 1.35GB · 40.7GB reclaimable",
	} {
		if rows[key] != want {
			t.Errorf("%s = %q, want %q", key, rows[key], want)
		}
	}
}

func TestAHealthyDaemonIsOkAndSaysThereAreNoProblems(t *testing.T) {
	served(t, fixture{containers: []string{
		container("web", "running", "Up 3 hours (healthy)", "healthy"),
		container("db", "running", "Up 2 days", ""),
	}, df: dfJSON})
	pairs := compactPairs(t, map[string]any{})
	if got := keysOf(pairs); !slices.Equal(got, compactKeys) {
		t.Fatalf("rows = %v, want %v", got, compactKeys)
	}
	rows := rowsByKey(pairs)
	if rows["status"] != "ok" || rows["problems"] != "none" || rows["containers"] != "2 running of 2" {
		t.Errorf("rows = %v, want ok, no problems and every container running", rows)
	}
}

// Finished is not failed: the migrations and seeds of a compose project end with
// 0, and kube does not count a Succeeded pod either.
func TestContainersThatExitedCleanlyAreNotProblems(t *testing.T) {
	served(t, fixture{containers: []string{
		container("db", "running", "Up 2 days", ""),
		container("migrate", "exited", "Exited (0) 3 weeks ago", ""),
		container("seed", "exited", "Exited (0) 3 weeks ago", ""),
	}, df: dfJSON})
	rows := rowsByKey(compactPairs(t, map[string]any{}))
	if rows["status"] != "ok" || rows["problems"] != "none" {
		t.Errorf("status = %q, problems = %q, want ok and none", rows["status"], rows["problems"])
	}
	if rows["containers"] != "1 running of 3 · 2 exited cleanly" {
		t.Errorf("containers = %q", rows["containers"])
	}
}

// The ranking and the cap: the broken ones first, then the ones that crashed,
// then the rest, six at most.
func TestTheProblemListRanksUnhealthyAndRestartingThenCrashedThenTheRestAndKeepsTheCap(t *testing.T) {
	var all []string
	for i := range 4 {
		all = append(all, container(fmt.Sprintf("a%d", i), "exited", fmt.Sprintf("Exited (%d) 1 day ago", i+1), ""))
	}
	all = append(all,
		container("z", "paused", "Up 1 hour (Paused)", ""),
		container("y", "running", "Up 1 hour (unhealthy)", ""),
		container("x", "restarting", "Restarting (1) 5 seconds ago", ""),
		container("w", "created", "Created", ""),
	)
	served(t, fixture{containers: all, df: dfJSON})
	rows := rowsByKey(compactPairs(t, map[string]any{}))
	if want := "x (restarting), y (unhealthy), a0 (exit 1), a1 (exit 2) and 4 more"; rows["problems"] != want {
		t.Errorf("problems = %q, want %q", rows["problems"], want)
	}
	if rows["status"] != "fail — 1 unhealthy, 1 restarting, 4 exited non-zero, 2 not running" {
		t.Errorf("status = %q", rows["status"])
	}
}

func TestTheExitCodeIsReadFromTheStatusTextAndNothingIsMadeUp(t *testing.T) {
	for status, want := range map[string]struct {
		code int
		ok   bool
	}{
		"Exited (137) 3 days ago":      {137, true},
		"Exited (0) 2 weeks ago":       {0, true},
		"Exited (1) About an hour ago": {1, true},
		"Exited":                       {0, false},
		"Up 3 hours (Paused)":          {0, false},
		"Restarting (1) 5 seconds ago": {0, false},
		"Exited (x) 1 day ago":         {0, false},
		"Exited (137":                  {0, false},
	} {
		code, ok := exitCode(containerRow{State: "exited", Status: status})
		if code != want.code || ok != want.ok {
			t.Errorf("exitCode(%q) = %d, %v, want %d, %v", status, code, ok, want.code, want.ok)
		}
	}
	if _, ok := exitCode(containerRow{State: "running", Status: "Exited (1) 1 day ago"}); ok {
		t.Error("a running container has an exit code")
	}
}

// Rule five: a row the CLI cannot read says so and the page goes on.
func TestADiskTheCLICannotReadSaysSoAndTheRestStillAnswers(t *testing.T) {
	for name, fx := range map[string]fixture{
		"refused": {containers: anUnwellDaemon(),
			dfFails: "permission denied while trying to connect to the Docker daemon socket"},
		"a CLI whose system df has no json": {containers: anUnwellDaemon(), df: "json\njson\njson\njson\n"},
		"nothing at all":                    {containers: anUnwellDaemon(), df: ""},
	} {
		t.Run(name, func(t *testing.T) {
			served(t, fx)
			pairs := compactPairs(t, map[string]any{})
			if got := keysOf(pairs); !slices.Equal(got, compactKeys) {
				t.Fatalf("rows = %v, want all four with the disk unread", got)
			}
			assertOneLinePerRow(t, pairs)
			rows := rowsByKey(pairs)
			if !strings.HasPrefix(rows["disk"], "could not be read — ") {
				t.Errorf("disk = %q, want 'could not be read — <why>'", rows["disk"])
			}
			if !strings.HasPrefix(rows["status"], "fail — ") || !strings.Contains(rows["status"], "disk could not be read") {
				t.Errorf("status = %q, want the failure and the disk said", rows["status"])
			}
			if rows["problems"] == "" || rows["containers"] == "" {
				t.Errorf("the rest of the page was lost: %v", rows)
			}
		})
	}
}

func TestAnOtherwiseHealthyDaemonWhoseDiskCannotBeReadIsAWarning(t *testing.T) {
	served(t, fixture{containers: []string{container("db", "running", "Up 2 days", "")}, dfFails: "Error response from daemon: boom"})
	rows := rowsByKey(compactPairs(t, map[string]any{}))
	if want := "warn — disk could not be read"; rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}
	if want := "could not be read — boom"; rows["disk"] != want {
		t.Errorf("disk = %q, want %q", rows["disk"], want)
	}
}

// A daemon with nothing on it is a daemon, not an error.
func TestAnEmptyDaemonIsOkWithNothingOnIt(t *testing.T) {
	served(t, fixture{df: `{"Type":"Images","TotalCount":"0","Active":"0","Size":"0B","Reclaimable":"0B"}
{"Type":"Containers","TotalCount":"0","Active":"0","Size":"0B","Reclaimable":"0B"}
{"Type":"Local Volumes","TotalCount":"0","Active":"0","Size":"0B","Reclaimable":"0B"}
{"Type":"Build Cache","TotalCount":"0","Active":"0","Size":"0B","Reclaimable":"0B"}
`})
	pairs := compactPairs(t, map[string]any{})
	assertOneLinePerRow(t, pairs)
	rows := rowsByKey(pairs)
	for key, want := range map[string]string{
		"status": "ok", "containers": "0 running of 0", "problems": "none", "disk": "images 0B, volumes 0B, cache 0B",
	} {
		if rows[key] != want {
			t.Errorf("%s = %q, want %q", key, rows[key], want)
		}
	}
}

// The daemon not answering is the verdict, and the rows that said why before say
// it in the words the grammar sets.
func TestADaemonThatDoesNotAnswerIsTheVerdictAndKeepsItsReasonAndItsHint(t *testing.T) {
	for _, tc := range []struct {
		name, script, status, daemon string
	}{
		{"silent",
			"echo 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?' >&2\nexit 1\n",
			"fail — the daemon did not answer", "did not answer — Cannot connect to the Docker daemon"},
		{"refused",
			"echo 'permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock' >&2\nexit 1\n",
			"fail — the daemon answered, and refused", "answered, and refused — permission denied"},
		{"never contacted",
			"echo 'context \"lab\": context not found: open /nowhere/lab: no such file or directory' >&2\nexit 1\n",
			"fail — the daemon was never contacted", "was never contacted — context \"lab\""},
		{"something nobody classified",
			"echo 'Error response from daemon: boom' >&2\nexit 1\n",
			"fail — the daemon could not be read", "could not be read — boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scriptedDocker(t, tc.script)
			pairs := compactPairs(t, map[string]any{})
			if got := keysOf(pairs); !slices.Equal(got, []string{"status", "daemon", "what to check"}) {
				t.Fatalf("rows = %v", got)
			}
			assertOneLinePerRow(t, pairs)
			rows := rowsByKey(pairs)
			if rows["status"] != tc.status {
				t.Errorf("status = %q, want %q", rows["status"], tc.status)
			}
			if !strings.HasPrefix(rows["daemon"], tc.daemon) {
				t.Errorf("daemon = %q, want it to begin %q", rows["daemon"], tc.daemon)
			}
			if rows["what to check"] == "" {
				t.Error("no hint beside a daemon that could not be read")
			}
		})
	}
}

func TestAMissingDockerIsNeverContactedAndTheVerdictSaysSo(t *testing.T) {
	old := dockerBin
	dockerBin = "rta-test-no-docker-on-this-path"
	t.Cleanup(func() { dockerBin = old })
	rows := rowsByKey(compactPairs(t, map[string]any{}))
	if want := "fail — the daemon was never contacted"; rows["status"] != want {
		t.Errorf("status = %q, want %q", rows["status"], want)
	}
}

// Rule six: the compact page asks the daemon for what it needs and the detailed
// page for the images besides, and neither asks for anything twice.
func TestTheCompactPageNeverListsTheImagesAndTheDetailedPageDoes(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(fmt.Sprintf("detail=%v", detail), func(t *testing.T) {
			calls := served(t, fixture{containers: anUnwellDaemon(), df: dfJSON})
			if _, err := runOverview(t.Context(), overviewReq(t, map[string]any{"detail": detail})); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, c := range calls() {
				got = append(got, strings.Join(strings.Fields(c)[:2], " "))
			}
			slices.Sort(got)
			want := []string{"ps --format", "system df"}
			if detail {
				want = []string{"image ls", "ps --format", "system df"}
			}
			if !slices.Equal(got, want) {
				t.Errorf("calls = %v, want %v", got, want)
			}
		})
	}
}

// The detailed page keeps what the compact one moved, and the table of what the
// old default summed wrongly.
func TestTheDetailedPageKeepsTheOldRowsAndAddsTheDiskAndTheImages(t *testing.T) {
	served(t, fixture{containers: anUnwellDaemon(), df: dfJSON})
	v, err := runOverview(t.Context(), overviewReq(t, map[string]any{"detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("detailed overview is %s, want Sections", view.TypeOf(v))
	}
	var ids []string
	for _, sec := range s.Items {
		ids = append(ids, sec.ID)
	}
	if want := []string{"daemon", "containers", "disk", "images"}; !slices.Equal(ids, want) {
		t.Fatalf("sections = %v, want %v", ids, want)
	}
	head := rowsByKey(s.Items[0].View.(view.KeyValue).Pairs)
	for _, key := range []string{"status", "daemon", "containers", "problems", "disk", "images"} {
		if head[key] == "" {
			t.Errorf("the daemon section lost %q (has %v)", key, head)
		}
	}
	if head["daemon"] != "answering" {
		t.Errorf("daemon = %q", head["daemon"])
	}
	if want := "2, 1 dangling"; head["images"] != want {
		t.Errorf("images = %q, want %q: the count and the leftovers, and not a sum of sizes that counts a shared layer once per image", head["images"], want)
	}
	disk := s.Items[2].View.(view.Table)
	if len(disk.Rows) != 4 || disk.Rows[2][0] != "Local Volumes" {
		t.Errorf("disk usage = %v, want the four kinds docker reports", disk.Rows)
	}
}

func TestTheDetailedPageStillWarnsWhenTheImagesCannotBeListed(t *testing.T) {
	served(t, fixture{containers: anUnwellDaemon(), df: dfJSON, imagesFail: "permission denied on images"})
	v, err := runOverview(t.Context(), overviewReq(t, map[string]any{"detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	s := v.(view.Sections)
	if len(s.Warnings) == 0 {
		t.Fatal("a failed image listing left the page looking complete")
	}
	head := rowsByKey(s.Items[0].View.(view.KeyValue).Pairs)
	if !strings.HasPrefix(head["images"], "could not be read — ") {
		t.Errorf("images = %q, want it said", head["images"])
	}
}

// A volume's size takes the daemon a while to add up, so the tile is re-run
// every two minutes at most, and a configuration may only lengthen that.
func TestTheOverviewIsDetailedAndDeclaresItsPace(t *testing.T) {
	c := capabilityOf(t, "docker.overview")
	if !c.Detailed || c.Refresh != 2*time.Minute {
		t.Errorf("detailed = %v, refresh = %v, want the detailed form and two minutes", c.Detailed, c.Refresh)
	}
	for _, want := range []string{"exited cleanly", "reclaimable", "exit code"} {
		if !strings.Contains(c.Description, want) {
			t.Errorf("the description does not say %q", want)
		}
	}
}

func capabilityOf(t *testing.T, id string) plugin.Capability {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no capability %s", id)
	return plugin.Capability{}
}

func TestADecimalSizeIsWrittenTheWayDockerWritesIt(t *testing.T) {
	for n, want := range map[int64]string{
		0:                 "0B",
		999:               "999B",
		1500:              "1.5kB",
		290_000_000:       "290MB",
		1_350_000_000:     "1.35GB",
		40_690_000_000:    "40.7GB",
		100_400_000_000:   "100GB",
		2_500_000_000_000: "2.5TB",
	} {
		if got := decimalSize(n); got != want {
			t.Errorf("decimalSize(%d) = %q, want %q", n, got, want)
		}
	}
}
