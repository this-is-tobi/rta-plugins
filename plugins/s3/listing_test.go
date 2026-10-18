package main

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeKey is one object of a fake bucket.
type fakeKey struct {
	Key      string
	Size     int64
	Modified time.Time
}

// bucketFake is an S3 endpoint holding one bucket, listed the way S3 lists:
// lexicographic key order, at most max-keys a page, a continuation token to
// carry on, start-after and prefix honoured. The fakes before it answered
// every listing in one page, so nothing that read a second page, or decided
// not to, could be seen through them. It records the query of every listing
// request, which is what a test about how much of a bucket is read asks.
type bucketFake struct {
	*httptest.Server
	bucket string

	// lifecycle is the bucket's lifecycle configuration as the server answers
	// it: "" for none (S3's NoSuchLifecycleConfiguration), "denied" for a key
	// that may not read it, "unimplemented" for a service without lifecycle
	// rules, and otherwise the XML itself. listDenied refuses every listing.
	lifecycle  string
	listDenied bool
	// accountDenied refuses the account's list of buckets, as a key scoped to
	// one bucket is.
	accountDenied bool

	mu    sync.Mutex
	lists []url.Values
	asked []string
}

func newBucketFake(t *testing.T, bucket string, keys []fakeKey) *bucketFake {
	t.Helper()
	sorted := append([]fakeKey(nil), keys...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })

	type contents struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
	}
	type result struct {
		XMLName               xml.Name   `xml:"ListBucketResult"`
		Name                  string     `xml:"Name"`
		KeyCount              int        `xml:"KeyCount"`
		MaxKeys               int        `xml:"MaxKeys"`
		IsTruncated           bool       `xml:"IsTruncated"`
		NextContinuationToken string     `xml:"NextContinuationToken,omitempty"`
		Contents              []contents `xml:"Contents"`
	}

	f := &bucketFake{bucket: bucket}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		f.mu.Lock()
		f.asked = append(f.asked, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/" && f.accountDenied:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>Access Denied.</Message></Error>`))
		case r.URL.Path == "/":
			_, _ = w.Write([]byte(`<ListAllMyBucketsResult><Buckets><Bucket><Name>` + bucket +
				`</Name><CreationDate>2025-04-27T10:00:00.000Z</CreationDate></Bucket></Buckets></ListAllMyBucketsResult>`))
		case r.Method == http.MethodHead:
			if r.URL.Path == "/"+bucket+"/" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case q.Has("versioning"):
			_, _ = w.Write([]byte(`<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`))
		case q.Has("lifecycle"):
			switch f.lifecycle {
			case "":
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`<Error><Code>NoSuchLifecycleConfiguration</Code>` +
					`<Message>The lifecycle configuration does not exist</Message></Error>`))
			case "denied":
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>Access Denied.</Message></Error>`))
			case "unimplemented":
				w.WriteHeader(http.StatusNotImplemented)
				_, _ = w.Write([]byte(`<Error><Code>NotImplemented</Code><Message>A header you provided implies functionality that is not implemented</Message></Error>`))
			default:
				_, _ = w.Write([]byte(f.lifecycle))
			}
		case q.Get("list-type") == "2":
			f.mu.Lock()
			f.lists = append(f.lists, q)
			f.mu.Unlock()
			if f.listDenied {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>Access Denied.</Message></Error>`))
				return
			}

			max := 1000
			if v, err := strconv.Atoi(q.Get("max-keys")); err == nil && v > 0 && v < max {
				max = v
			}
			after := q.Get("start-after")
			if tok := q.Get("continuation-token"); tok != "" {
				after = strings.TrimPrefix(tok, "after:")
			}
			res := result{Name: bucket, MaxKeys: max}
			prefix := q.Get("prefix")
			for _, k := range sorted {
				if !strings.HasPrefix(k.Key, prefix) || k.Key <= after {
					continue
				}
				if len(res.Contents) == max {
					res.IsTruncated = true
					res.NextContinuationToken = "after:" + res.Contents[len(res.Contents)-1].Key
					break
				}
				res.Contents = append(res.Contents, contents{
					Key: k.Key, Size: k.Size, ETag: `"e"`,
					LastModified: k.Modified.UTC().Format("2006-01-02T15:04:05.000Z"),
				})
			}
			res.KeyCount = len(res.Contents)
			_ = xml.NewEncoder(w).Encode(res)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// requests is every request so far, as the method and the path with its query.
func (f *bucketFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// listings is the query of every listing request so far, in order.
func (f *bucketFake) listings() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.lists...)
}

// numberedKeys is n keys named in an order that lists as they are numbered,
// one byte a day apart so the newest is the last.
func numberedKeys(n int) []fakeKey {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	keys := make([]fakeKey, n)
	for i := range keys {
		keys[i] = fakeKey{
			Key:      "k" + strconv.Itoa(1000000+i),
			Size:     1024,
			Modified: base.Add(time.Duration(i) * time.Hour),
		}
	}
	return keys
}

// A listing page carries each object's owner when it is asked to, which is a
// third more bytes a page on a real server and a field nothing here reads.
// minio-go asks for it unless told not to, so every place that lists has to
// tell it, and "every place" is the thing a single call site cannot be trusted
// to remember: six did.
func TestNoListingAsksForTheOwner(t *testing.T) {
	f := newBucketFake(t, "backups", numberedKeys(3))
	ep := endpointOf(t, f.Server)
	ctx := context.Background()

	client, verr := connect(reqFor(t, "s3.object.list", ep, map[string]any{"bucket": "backups"}))
	if verr != nil {
		t.Fatal(verr)
	}
	sites := map[string]func(t *testing.T){
		"overview": func(t *testing.T) {
			if _, err := runOverview(ctx, reqFor(t, "s3.overview", ep, map[string]any{"bucket": "backups", "detail": true})); err != nil {
				t.Fatal(err)
			}
		},
		"tree": func(t *testing.T) {
			if _, err := runObjectTree(ctx, reqFor(t, "s3.object.tree", ep, map[string]any{"bucket": "backups"})); err != nil {
				t.Fatal(err)
			}
		},
		"list": func(t *testing.T) {
			if _, err := runObjectList(ctx, reqFor(t, "s3.object.list", ep, map[string]any{"bucket": "backups"})); err != nil {
				t.Fatal(err)
			}
		},
		"completion": func(t *testing.T) {
			suggestKeys("prefix")(ctx, reqFor(t, "s3.object.list", ep, map[string]any{"bucket": "backups"}))
		},
		"download": func(t *testing.T) {
			if _, verr := listForDownload(ctx, client, reqFor(t, "s3.bucket.download", ep, map[string]any{"bucket": "backups"})); verr != nil {
				t.Fatal(verr)
			}
		},
		"upload": func(t *testing.T) {
			if verr := checkUploadTarget(ctx, client, reqFor(t, "s3.bucket.upload", ep, map[string]any{"bucket": "backups"}), ""); verr == nil {
				t.Fatal("an upload into a bucket holding objects was not refused")
			}
		},
	}
	for name, run := range sites {
		t.Run(name, func(t *testing.T) {
			before := len(f.listings())
			run(t)
			after := f.listings()
			if len(after) == before {
				t.Fatal("the call never listed, so it asked for nothing")
			}
			for _, q := range after[before:] {
				if q.Get("fetch-owner") == "true" {
					t.Errorf("a listing asked for the owner: %v", q)
				}
			}
		})
	}
}

// The helper is the only way in. A seventh site calling the client's own
// listing would ask for owners again and nothing above would notice, because
// the test above names the sites it knows.
func TestEveryListingGoesThroughTheHelper(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "client.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if strings.Contains(code, ".ListObjects(") || strings.Contains(code, ".ListObjectsIter(") {
				t.Errorf("%s:%d lists through the client directly; use listObjects", name, i+1)
			}
		}
	}
}
