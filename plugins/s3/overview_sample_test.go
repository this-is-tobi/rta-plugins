package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// contentsOfBucket runs the detailed overview of a bucket and returns its
// "contents" section as rows.
func contentsOfBucket(t *testing.T, f *bucketFake) map[string]string {
	t.Helper()
	v, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, f.Server),
		map[string]any{"bucket": f.bucket, "detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("want Sections, got %s", view.TypeOf(v))
	}
	for _, s := range page.Items {
		if s.Title == "contents" {
			return pairsOf(t, s.View)
		}
	}
	t.Fatal("no contents section")
	return nil
}

// A page of keys is a thousand of them, so the overview's two thousand is two
// requests, and what it has to find out after that is whether there is a
// 2001st. Pulling it through the iterator fetched a whole third page, 1000 keys
// of metadata, to answer a yes or no; asking for one key after the last one
// read answers the same and costs a line.
func TestTheSampleReadsItsBudgetAndPeeksForOneKeyMore(t *testing.T) {
	f := newBucketFake(t, "backups", numberedKeys(3500))
	contentsOfBucket(t, f)

	lists := f.listings()
	if len(lists) != 3 {
		t.Fatalf("%d listing requests, want 3 (two pages and a peek): %v", len(lists), lists)
	}
	peek := lists[2]
	if peek.Get("max-keys") != "1" {
		t.Errorf("the third request wanted max-keys=%q, want a peek of one key", peek.Get("max-keys"))
	}
	if want := "k" + "1001999"; peek.Get("start-after") != want {
		t.Errorf("the peek starts after %q, want the last key read, %q", peek.Get("start-after"), want)
	}
	if peek.Get("continuation-token") != "" {
		t.Errorf("the peek carried a continuation token, %q: it is a fresh question", peek.Get("continuation-token"))
	}
}

// A bucket of exactly the budget has nothing more to say about; one key over
// is a bucket that has more. The line between them is the thing a count of
// "2000 objects" has to get right, since the same words would otherwise be
// the whole of a small bucket and a tenth of a large one.
func TestABucketOfExactlyTheBudgetIsNotMarkedPartial(t *testing.T) {
	exact := contentsOfBucket(t, newBucketFake(t, "backups", numberedKeys(walkKeys)))
	if exact["objects"] != "2000 objects" {
		t.Errorf("objects = %q, want exactly 2000 and no plus", exact["objects"])
	}
	for _, k := range []string{"objects", "size", "newest"} {
		if strings.Contains(exact[k], "+") || strings.Contains(exact[k], "at least") || strings.Contains(exact[k], "read") {
			t.Errorf("%s = %q marks a complete count as partial", k, exact[k])
		}
	}

	over := contentsOfBucket(t, newBucketFake(t, "backups", numberedKeys(walkKeys+1)))
	if !strings.HasPrefix(over["objects"], "2000+ objects") {
		t.Errorf("objects = %q, want 2000+ for a bucket one key over the budget", over["objects"])
	}
}

// Three rows are counted over the same keys, so three rows say so. "size 195
// KiB" beside "objects 2000+" read as the size of the bucket, and "newest" as
// its newest write, though both are only of what was read, and a bucket whose
// keys are dates lists its oldest first.
func TestEveryRowOfAPartialCountSaysItIsPartial(t *testing.T) {
	got := contentsOfBucket(t, newBucketFake(t, "backups", numberedKeys(2500)))

	if !strings.HasPrefix(got["objects"], "2000+ objects") {
		t.Errorf("objects = %q", got["objects"])
	}
	if !strings.Contains(got["objects"], "through k1001999") {
		t.Errorf("objects = %q, want where the read stopped, in key order", got["objects"])
	}
	if !strings.Contains(got["objects"], "object tree") {
		t.Errorf("objects = %q, want the capability that reads further", got["objects"])
	}
	if !strings.HasPrefix(got["size"], "at least ") {
		t.Errorf("size = %q, want a lower bound", got["size"])
	}
	if !strings.Contains(got["newest"], "newest of the keys read") {
		t.Errorf("newest = %q, want it said to be the newest of those read", got["newest"])
	}
}

func overviewCapabilityOf(t *testing.T) plugin.Capability {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == "s3.overview" {
			return c
		}
	}
	t.Fatal("no s3.overview")
	return plugin.Capability{}
}

// A profile that fences `prefix` to where an application writes has said what
// the connection may see of a bucket. The overview declared no prefix, so the
// fence neither bound it nor filled it, and the page counted, summed and dated
// the whole bucket past the fence while s3.object.tree and s3.object.list, which
// do declare it, refused.
func TestAFencedPrefixFillsTheOverviewAndAnotherIsRefused(t *testing.T) {
	c := overviewCapabilityOf(t)
	fence := plugin.Fence{"bucket": {"backups"}, "prefix": {"app/"}}

	if breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: map[string]any{}}); breach != nil {
		t.Fatalf("a call naming nothing was refused under a one-entry fence: %+v", breach)
	}
	filled := plugin.ResolveRequest(c, plugin.Inputs{Fence: fence, Caller: map[string]any{}}, false, false)
	if got := filled.String("prefix"); got != "app/" {
		t.Errorf("the fence filled prefix = %q, want app/", got)
	}
	if plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: map[string]any{"prefix": "other/"}}) == nil {
		t.Error("a prefix outside the fence was let through")
	}
}

// With a prefix the page is about the keys under it: the listing asks for
// them, and so does the question of whether there are more, and the page says
// which prefix it counted so "3 objects" is not read as the bucket's.
func TestTheOverviewOfAPrefixCountsOnlyThatPrefix(t *testing.T) {
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	keys := []fakeKey{
		{Key: "app/a", Size: 100, Modified: at}, {Key: "app/b", Size: 200, Modified: at}, {Key: "app/c", Size: 300, Modified: at},
		{Key: "other/x", Size: 1 << 20, Modified: at}, {Key: "other/y", Size: 1 << 20, Modified: at},
	}
	f := newBucketFake(t, "backups", keys)
	v, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, f.Server),
		map[string]any{"bucket": "backups", "prefix": "app/", "detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	var contents map[string]string
	for _, s := range v.(view.Sections).Items {
		if s.Title == "contents" {
			contents = pairsOf(t, s.View)
		}
	}
	if contents["objects"] != "3 objects" || contents["size"] != "600 B" {
		t.Errorf("contents = %v, want the three objects under app/ and nothing of other/", contents)
	}
	if contents["prefix"] != "app/" {
		t.Errorf("prefix = %q, want the page to say what it counted", contents["prefix"])
	}
	lists := f.listings()
	if len(lists) == 0 {
		t.Fatal("nothing was listed")
	}
	for _, q := range lists {
		if q.Get("prefix") != "app/" {
			t.Errorf("a listing went out without the prefix: %v", q)
		}
	}
}
