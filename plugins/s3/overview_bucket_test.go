package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A connection is for one application's bucket, and the overview said how many
// buckets the key could see and, with detail, named every one of them — the
// account's whole inventory, on a tile about a single application and beside a
// fence written to keep every other name out. With a bucket, named or fenced,
// the page is about it.

// accountServer is an account of three buckets, one of which is the one the
// connection is for. It answers what minio-go asks to build the page: the
// account listing, a bucket's HEAD, its versioning and its keys.
func accountServer(t *testing.T, versioning string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/":
			_, _ = w.Write([]byte(`<ListAllMyBucketsResult><Buckets>` +
				`<Bucket><Name>mirai-ocr-preprod</Name><CreationDate>2025-04-27T10:00:00.000Z</CreationDate></Bucket>` +
				`<Bucket><Name>mirai-drive-prod</Name><CreationDate>2026-05-22T10:00:00.000Z</CreationDate></Bucket>` +
				`<Bucket><Name>mirai-mcr-prod</Name><CreationDate>2025-06-23T10:00:00.000Z</CreationDate></Bucket>` +
				`</Buckets></ListAllMyBucketsResult>`))
		case r.URL.Path == "/mirai-ocr-preprod/" && r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case q.Has("versioning"):
			_, _ = w.Write([]byte(`<VersioningConfiguration><Status>` + versioning + `</Status></VersioningConfiguration>`))
		case q.Get("list-type") == "2":
			_, _ = w.Write([]byte(`<ListBucketResult><Name>mirai-ocr-preprod</Name>` +
				`<Contents><Key>base/a.tar</Key><Size>1048576</Size><ETag>"e"</ETag><LastModified>2026-10-01T10:00:00.000Z</LastModified></Contents>` +
				`<Contents><Key>base/b.tar</Key><Size>2097152</Size><ETag>"e"</ETag><LastModified>2026-10-15T10:00:00.000Z</LastModified></Contents>` +
				`<Contents><Key>base/</Key><Size>0</Size><ETag>"e"</ETag><LastModified>2026-10-16T10:00:00.000Z</LastModified></Contents>` +
				`</ListBucketResult>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pairsOf(t *testing.T, v view.View) map[string]string {
	t.Helper()
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("want KeyValue, got %s", view.TypeOf(v))
	}
	out := map[string]string{}
	for _, p := range kv.Pairs {
		out[p.Key] = p.Value
	}
	return out
}

func TestTheOverviewOfANamedBucketIsAboutThatBucketAndNamesNoOther(t *testing.T) {
	srv := accountServer(t, "Enabled")
	v, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, srv),
		map[string]any{"bucket": "mirai-ocr-preprod"}))
	if err != nil {
		t.Fatal(err)
	}
	got := pairsOf(t, v)
	if got["bucket"] != "mirai-ocr-preprod" || got["versioning"] != "enabled" {
		t.Errorf("pairs = %v, want the bucket and its versioning", got)
	}
	if !strings.HasPrefix(got["created"], "2025-04-27 (") {
		t.Errorf("created = %q, want the bucket's own date", got["created"])
	}
	if _, count := got["buckets"]; count {
		t.Errorf("pairs = %v: a count of the account's buckets is not about this one", got)
	}
	for k, val := range got {
		if strings.Contains(val, "mirai-drive-prod") || strings.Contains(val, "mirai-mcr-prod") {
			t.Errorf("%s = %q names a bucket the connection is not for", k, val)
		}
	}
}

func TestAFencedBucketFillsTheOverviewAndAnotherIsRefused(t *testing.T) {
	var c plugin.Capability
	for _, each := range Plugin().Capabilities {
		if each.ID == "s3.overview" {
			c = each
		}
	}
	fence := plugin.Fence{"bucket": {"mirai-ocr-preprod"}}
	if breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: map[string]any{}}); breach != nil {
		t.Fatalf("a call naming nothing was refused under a one-entry fence: %+v", breach)
	}
	filled := plugin.ResolveRequest(c, plugin.Inputs{Fence: fence, Caller: map[string]any{}}, false, false)
	if got := filled.String("bucket"); got != "mirai-ocr-preprod" {
		t.Errorf("the fence filled bucket = %q", got)
	}
	if plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: map[string]any{"bucket": "mirai-mcr-prod"}}) == nil {
		t.Error("a bucket outside the fence was let through")
	}
}

func TestTheDetailOfABucketSaysWhatItHoldsAndWhenTheNewestWasWritten(t *testing.T) {
	srv := accountServer(t, "Suspended")
	v, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, srv),
		map[string]any{"bucket": "mirai-ocr-preprod", "detail": true}))
	if err != nil {
		t.Fatal(err)
	}
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("want Sections, got %s", view.TypeOf(v))
	}
	var status, contents map[string]string
	for _, s := range page.Items {
		switch s.Title {
		case "status":
			status = pairsOf(t, s.View)
		case "contents":
			contents = pairsOf(t, s.View)
		}
	}
	if status["versioning"] != "suspended" {
		t.Errorf("status = %v", status)
	}
	if contents["objects"] != "2 objects" || contents["size"] != "3.0 MiB" {
		t.Errorf("contents = %v, want two objects and the directory marker not counted", contents)
	}
	if !strings.Contains(contents["newest"], "2026-10-15 10:00") {
		t.Errorf("newest = %q, want the newest object's write", contents["newest"])
	}
}

func TestABucketThatIsNotThereIsAnErrorWithTheNameItWasLookedUnder(t *testing.T) {
	srv := accountServer(t, "Enabled")
	_, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, srv),
		map[string]any{"bucket": "mirai-missing"}))
	verr, ok := err.(*view.Error)
	if !ok || verr.Code != "s3.bucket.notfound" || !strings.Contains(verr.Message, `"mirai-missing"`) {
		t.Fatalf("err = %v, want s3.bucket.notfound naming the bucket", err)
	}
	if verr.Hint == "" {
		t.Error("the refusal does not say what to check")
	}
}

func TestTheBucketListOfANamedBucketHoldsThatBucketAlone(t *testing.T) {
	srv := accountServer(t, "Enabled")
	v, err := runBucketList(context.Background(), reqFor(t, "s3.bucket.list", endpointOf(t, srv),
		map[string]any{"bucket": "mirai-ocr-preprod"}))
	if err != nil {
		t.Fatal(err)
	}
	tbl, ok := v.(view.Table)
	if !ok || len(tbl.Rows) != 1 || tbl.Rows[0][0] != "mirai-ocr-preprod" {
		t.Fatalf("rows = %+v, want the one bucket", tbl.Rows)
	}

	v, err = runBucketList(context.Background(), reqFor(t, "s3.bucket.list", endpointOf(t, srv), map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if tbl, _ := v.(view.Table); len(tbl.Rows) != 3 {
		t.Errorf("rows = %d, want every bucket when none is named", len(tbl.Rows))
	}
}

func TestTheOverviewTilePacesItself(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.ID == "s3.overview" && c.Refresh == 0 {
			t.Error("s3.overview is re-run at the host's own pace, every few seconds, several requests a time")
		}
	}
}
