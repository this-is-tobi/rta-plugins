package main

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The compact form is what a dashboard tile shows, and a tile cannot ask for the
// detail. A bucket's tile used to show four rows that never change, and what
// moves (how much it holds, when it was last written, what keeps it) sat behind
// a detail a tile cannot reach. It reads one page of keys, and says it is one.

const bucketRows = "status,objects,newest,versioning,retention,bucket"

func keysOf(kv view.KeyValue) string {
	keys := make([]string, 0, len(kv.Pairs))
	for _, p := range kv.Pairs {
		keys = append(keys, p.Key)
	}
	return strings.Join(keys, ",")
}

// A tile a third of a screen wide still reads: no row is longer than a line of
// an 80-column terminal, and there are no more rows than a tile holds.
func assertFitsATile(t *testing.T, kv view.KeyValue) {
	t.Helper()
	if len(kv.Pairs) > 8 {
		t.Errorf("%d rows, a tile holds eight: %v", len(kv.Pairs), kv.Pairs)
	}
	for _, p := range kv.Pairs {
		if n := utf8.RuneCountInString(p.Key) + 2 + utf8.RuneCountInString(p.Value); n > 80 || strings.Contains(p.Value, "\n") {
			t.Errorf("row %q is %d columns or wraps: %q", p.Key, n, p.Value)
		}
	}
}

func tileOf(t *testing.T, f *bucketFake, values map[string]any) view.KeyValue {
	t.Helper()
	values["bucket"] = f.bucket
	v, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, f.Server), values))
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("the compact overview is a %s, want the rows alone", view.TypeOf(v))
	}
	return kv
}

func rowOf(kv view.KeyValue, key string) string {
	for _, p := range kv.Pairs {
		if p.Key == key {
			return p.Value
		}
	}
	return ""
}

const thirtyDays = `<LifecycleConfiguration><Rule><ID>expire</ID><Status>Enabled</Status>` +
	`<Filter><Prefix></Prefix></Filter><Expiration><Days>30</Days></Expiration></Rule></LifecycleConfiguration>`

func TestABucketTileReadsOnePageAndSaysItIsOne(t *testing.T) {
	f := newBucketFake(t, "backups", numberedKeys(2500))
	f.lifecycle = thirtyDays
	kv := tileOf(t, f, map[string]any{})

	if got := keysOf(kv); got != bucketRows {
		t.Fatalf("rows = %s, want %s", got, bucketRows)
	}
	for key, want := range map[string]string{
		"status":     "ok",
		"objects":    "1000+ (first page)",
		"versioning": "enabled",
		"retention":  "1 rule, objects expire after 30d",
	} {
		if got := rowOf(kv, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// k1000999 is the last of the first page, written 999 hours after the first.
	if got := rowOf(kv, "newest"); !strings.Contains(got, "2026-02-11 15:00") || !strings.HasSuffix(got, "— of the first page only") {
		t.Errorf("newest = %q, want the newest of the first page, said to be only that", got)
	}
	if got := rowOf(kv, "bucket"); !regexp.MustCompile(`^backups, created 2025-04-27 \([^()]+ ago\)$`).MatchString(got) {
		t.Errorf("bucket = %q, want its name and when it was created", got)
	}
	assertFitsATile(t, kv)

	// One listing, of one page: not the two thousand keys the detail reads, and
	// no peek for one key more.
	lists := f.listings()
	if len(lists) != 1 || lists[0].Get("max-keys") != "1000" || lists[0].Get("start-after") != "" {
		t.Errorf("listings = %v, want one request for the first thousand keys", lists)
	}
	// Five requests: the bucket, the account for its creation date, its
	// versioning, its lifecycle and a page of its keys. It used to be three.
	if got := f.requests(); len(got) != 5 {
		t.Errorf("%d requests %v, want 5", len(got), got)
	}
}

// A page that is the whole bucket is counted exactly, and said nothing more of.
func TestABucketThatFitsInThePageIsCountedExactly(t *testing.T) {
	keys := numberedKeys(5)
	keys = append(keys, fakeKey{Key: "dir/", Size: 0, Modified: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)})
	kv := tileOf(t, newBucketFake(t, "backups", keys), map[string]any{})

	if got := rowOf(kv, "objects"); got != "5" {
		t.Errorf("objects = %q, want 5: the directory marker is not an object", got)
	}
	if got := rowOf(kv, "newest"); strings.Contains(got, "first page") || !strings.Contains(got, "2026-01-01 04:00") {
		t.Errorf("newest = %q, want the newest object, written 4 hours into the year, not qualified", got)
	}
	if got := rowOf(kv, "retention"); got != "none" {
		t.Errorf("retention = %q, want none said: a bucket that keeps everything forever is a fact", got)
	}
	assertFitsATile(t, kv)

	kv = tileOf(t, newBucketFake(t, "backups", numberedKeys(999)), map[string]any{})
	if got := rowOf(kv, "objects"); got != "999" {
		t.Errorf("objects = %q, want exactly 999", got)
	}
}

// A key scoped to one bucket may not list the account, and the date it was
// created is the one thing only that listing carries: the bucket's row is its
// name, and the tile is not a warning for a key doing what it was made to do.
func TestABucketScopedKeyShowsTheNameWithoutTheDateAndIsOk(t *testing.T) {
	f := newBucketFake(t, "backups", numberedKeys(3))
	f.accountDenied = true
	kv := tileOf(t, f, map[string]any{})
	if got := rowOf(kv, "bucket"); got != "backups" {
		t.Errorf("bucket = %q, want the name alone", got)
	}
	if got := rowOf(kv, "status"); got != "ok" {
		t.Errorf("status = %q", got)
	}
}

func TestAnEmptyBucketIsOkAndSaysSo(t *testing.T) {
	kv := tileOf(t, newBucketFake(t, "backups", nil), map[string]any{})
	if got := rowOf(kv, "objects"); got != "0" {
		t.Errorf("objects = %q", got)
	}
	if got := rowOf(kv, "newest"); got != "" {
		t.Errorf("newest = %q: a bucket with nothing in it was written to never", got)
	}
	if got := rowOf(kv, "status"); got != "ok" {
		t.Errorf("status = %q", got)
	}
}

func TestTheRetentionRowSaysWhatTheRulesDo(t *testing.T) {
	rule := func(status, expiration string) string {
		return `<Rule><ID>r</ID><Status>` + status + `</Status><Filter><Prefix></Prefix></Filter>` + expiration + `</Rule>`
	}
	for name, tc := range map[string]struct {
		lifecycle string
		want      string
	}{
		"several expiries":  {`<LifecycleConfiguration>` + rule("Enabled", `<Expiration><Days>90</Days></Expiration>`) + rule("Enabled", `<Expiration><Days>7</Days></Expiration>`) + `</LifecycleConfiguration>`, "2 rules, objects expire after 7d to 90d"},
		"no expiry":         {`<LifecycleConfiguration>` + rule("Enabled", `<Transition><Days>30</Days><StorageClass>GLACIER</StorageClass></Transition>`) + `</LifecycleConfiguration>`, "1 rule"},
		"all disabled":      {`<LifecycleConfiguration>` + rule("Disabled", `<Expiration><Days>30</Days></Expiration>`) + `</LifecycleConfiguration>`, "none enabled (1 rule disabled)"},
		"one of two active": {`<LifecycleConfiguration>` + rule("Enabled", `<Expiration><Days>30</Days></Expiration>`) + rule("Disabled", `<Expiration><Days>1</Days></Expiration>`) + `</LifecycleConfiguration>`, "1 rule, objects expire after 30d"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBucketFake(t, "backups", numberedKeys(3))
			f.lifecycle = tc.lifecycle
			if got := rowOf(tileOf(t, f, map[string]any{}), "retention"); got != tc.want {
				t.Errorf("retention = %q, want %q", got, tc.want)
			}
		})
	}
}

// A key that may list a bucket and not read its lifecycle is an ordinary
// least-privilege key: the row says so, and the verdict is a warning that names
// it and not an ok that claims the bucket keeps nothing.
func TestARetentionTheKeyMayNotReadIsARowAndAWarning(t *testing.T) {
	for name, tc := range map[string]struct{ lifecycle, row string }{
		"denied": {"denied", "could not be read — access denied"},
		// A service without lifecycle rules says so in a sentence, cut to one
		// line of a tile.
		"unimplemented": {"unimplemented", "could not be read — NotImplemented: A header you provided implies fu…"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBucketFake(t, "backups", numberedKeys(3))
			f.lifecycle = tc.lifecycle
			kv := tileOf(t, f, map[string]any{})
			if got := rowOf(kv, "retention"); got != tc.row {
				t.Errorf("retention = %q, want %q", got, tc.row)
			}
			if got, want := rowOf(kv, "status"), "warn — retention could not be read"; got != want {
				t.Errorf("status = %q, want %q", got, want)
			}
			if got := rowOf(kv, "objects"); got != "3" {
				t.Errorf("objects = %q, want the rows that could be read to stand", got)
			}
			assertFitsATile(t, kv)
		})
	}
}

func TestAListingTheKeyMayNotReadIsARowAndAWarning(t *testing.T) {
	f := newBucketFake(t, "backups", numberedKeys(3))
	f.listDenied = true
	kv := tileOf(t, f, map[string]any{})
	if got, want := rowOf(kv, "objects"), "could not be read — access denied"; got != want {
		t.Errorf("objects = %q, want %q", got, want)
	}
	if got := rowOf(kv, "newest"); got != "" {
		t.Errorf("newest = %q: nothing was listed to date", got)
	}
	if got, want := rowOf(kv, "status"), "warn — objects could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := rowOf(kv, "versioning"); got != "enabled" {
		t.Errorf("versioning = %q, want the rows that could be read to stand", got)
	}
	assertFitsATile(t, kv)
}

// Several rows a key cannot read are one verdict that names them all.
func TestSeveralRowsTheKeyCannotReadAreOneVerdict(t *testing.T) {
	f := newBucketFake(t, "backups", numberedKeys(3))
	f.listDenied, f.lifecycle = true, "denied"
	kv := tileOf(t, f, map[string]any{})
	if got, want := rowOf(kv, "status"), "warn — objects, retention could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

func TestAPrefixFenceHoldsThePageAndIsSaid(t *testing.T) {
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	f := newBucketFake(t, "backups", []fakeKey{
		{Key: "app/a", Size: 1, Modified: at}, {Key: "app/b", Size: 1, Modified: at}, {Key: "other/x", Size: 1, Modified: at},
	})
	kv := tileOf(t, f, map[string]any{"prefix": "app/"})
	if got, want := keysOf(kv), "status,prefix,objects,newest,versioning,retention,bucket"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}
	if got := rowOf(kv, "objects"); got != "2" {
		t.Errorf("objects = %q, want the two under app/", got)
	}
	for _, q := range f.listings() {
		if q.Get("prefix") != "app/" {
			t.Errorf("a listing went out without the prefix: %v", q)
		}
	}
	assertFitsATile(t, kv)
}

// The detail is the page the default was and the bounded walk: the status block
// with the address, the bucket, when it was created, its versioning and its
// retention, and what it holds counted over the first two thousand keys.
func TestTheDetailOfABucketKeepsTheWalkAndTheHeadWithTheRetention(t *testing.T) {
	f := newBucketFake(t, "backups", numberedKeys(2500))
	v, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, f.Server),
		map[string]any{"bucket": "backups", "detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("detail is a %s, want a page", view.TypeOf(v))
	}
	var titles []string
	var status, contents map[string]string
	for _, s := range page.Items {
		titles = append(titles, s.Title)
		switch s.Title {
		case "status":
			status = pairsOf(t, s.View)
		case "contents":
			contents = pairsOf(t, s.View)
		}
	}
	if got, want := strings.Join(titles, ","), "status,contents"; got != want {
		t.Errorf("sections = %s, want %s", got, want)
	}
	for _, key := range []string{"status", "address", "bucket", "created", "versioning", "retention"} {
		if status[key] == "" {
			t.Errorf("the detail status lost its %q row: %v", key, status)
		}
	}
	if status["status"] != "ok" {
		t.Errorf("status = %q", status["status"])
	}
	if !strings.HasPrefix(contents["objects"], "2000+ objects") {
		t.Errorf("objects = %q, want the walk of two thousand keys", contents["objects"])
	}
	if len(f.listings()) != 3 {
		t.Errorf("%d listings, want the walk's two pages and its peek, and no page for the tile", len(f.listings()))
	}
}

func TestADetailListingTheKeyMayNotReadIsARowAndNotAnError(t *testing.T) {
	f := newBucketFake(t, "backups", numberedKeys(3))
	f.listDenied = true
	v, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, f.Server),
		map[string]any{"bucket": "backups", "detail": true}))
	if err != nil {
		t.Fatalf("a refused listing made the detail an error: %v", err)
	}
	var status, contents map[string]string
	for _, s := range v.(view.Sections).Items {
		switch s.Title {
		case "status":
			status = pairsOf(t, s.View)
		case "contents":
			contents = pairsOf(t, s.View)
		}
	}
	if got, want := contents["objects"], "could not be read — access denied"; got != want {
		t.Errorf("objects = %q, want %q", got, want)
	}
	if got, want := status["status"], "warn — objects could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// The endpoint's own tile follows the same order: the verdict, what is counted,
// then a row a bucket, and where it is last.
func TestTheEndpointTileLeadsWithAVerdictAndEndsWithTheAddress(t *testing.T) {
	srv := accountServer(t, "Enabled")
	v, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, srv), map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	kv := v.(view.KeyValue)
	if got, want := keysOf(kv), "status,buckets,mirai-drive-prod,mirai-mcr-prod,mirai-ocr-preprod,address"; got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	if got := rowOf(kv, "status"); got != "ok" {
		t.Errorf("status = %q", got)
	}
	if got := rowOf(kv, "buckets"); got != "3" {
		t.Errorf("buckets = %q", got)
	}
	assertFitsATile(t, kv)
}

func TestTheOverviewTilePacesItselfAtTwoMinutes(t *testing.T) {
	c := overviewCapabilityOf(t)
	if got := c.Pace(); got != 2*time.Minute {
		t.Errorf("s3.overview is re-run every %s, want 2m: a listing page a refresh is a bill on a large bucket", got)
	}
	if pace, cut := plugin.ClampPace(c, 5*time.Second); pace != 2*time.Minute || !cut {
		t.Errorf("a configured 5s pace came to %s (cut %v), want the 2m it cannot go below", pace, cut)
	}
}

func TestTheOverviewDeclaresADetailedPage(t *testing.T) {
	c := overviewCapabilityOf(t)
	if !c.Detailed {
		t.Error("s3.overview is not Detailed")
	}
	for name, text := range map[string]string{"description": c.Description, "agent": c.AgentText()} {
		if !strings.Contains(text, "`detail`") || !strings.Contains(text, "status") {
			t.Errorf("the %s text does not say what the default answer is and what `detail` adds: %q", name, text)
		}
	}
}
