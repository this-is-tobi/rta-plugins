package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

func assertEmptyTable(t *testing.T, v view.View, sentence string) {
	t.Helper()
	tbl, ok := v.(view.Table)
	if !ok {
		t.Fatalf("an empty answer is a %T, want the table", v)
	}
	if len(tbl.Rows) != 0 || tbl.Total != 0 || len(tbl.Columns) == 0 {
		t.Errorf("table = %d columns, %d rows, total %d, want its columns and no rows",
			len(tbl.Columns), len(tbl.Rows), tbl.Total)
	}
	if tbl.Empty != sentence {
		t.Errorf("empty sentence = %q, want %q", tbl.Empty, sentence)
	}
	raw, err := view.Marshal(view.Envelope{View: v})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"rows":[]`) || !strings.Contains(string(raw), `"type":"table"`) {
		t.Errorf("an agent reads %s, want a table with rows []", raw)
	}
}

// An empty listing is still its table (docs: writing a plugin): a program reads
// zero rows under the columns it knows, and a person is told what the nothing
// is, where a bare grid of headings reads like a listing that failed.
func TestAnEmptyObjectListIsItsTable(t *testing.T) {
	srv := listingServer(t, 0, 10)
	for name, tc := range map[string]struct {
		values   map[string]any
		sentence string
	}{
		"a bucket":       {map[string]any{"bucket": "test-bucket"}, "no objects in test-bucket"},
		"under a prefix": {map[string]any{"bucket": "test-bucket", "prefix": "logs/"}, `no objects under "logs/"`},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := runObjectList(context.Background(), reqFor(t, "s3.object.list", endpointOf(t, srv), tc.values))
			if err != nil {
				t.Fatal(err)
			}
			assertEmptyTable(t, v, tc.sentence)
		})
	}
}

func TestAnEmptyBucketListIsItsTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<ListAllMyBucketsResult><Owner><ID>o</ID></Owner><Buckets><Bucket><Name>alpha</Name>` +
			`<CreationDate>2025-04-27T10:00:00.000Z</CreationDate></Bucket></Buckets></ListAllMyBucketsResult>`))
	}))
	t.Cleanup(srv.Close)
	nothing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<ListAllMyBucketsResult><Owner><ID>o</ID></Owner><Buckets></Buckets></ListAllMyBucketsResult>`))
	}))
	t.Cleanup(nothing.Close)

	run := func(srv *httptest.Server, values map[string]any) view.View {
		t.Helper()
		v, err := runBucketList(context.Background(), reqFor(t, "s3.bucket.list", endpointOf(t, srv), values))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	assertEmptyTable(t, run(nothing, map[string]any{}), "no buckets")
	// A name that matches nothing is not an account with no bucket.
	assertEmptyTable(t, run(srv, map[string]any{"bucket": "beta"}), `no bucket named "beta"`)
}
