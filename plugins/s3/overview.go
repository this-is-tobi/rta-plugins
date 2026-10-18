package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// overviewRefresh is how often a dashboard tile of s3.overview is re-run. The
// answer is a bucket's existence and versioning, which change when
// somebody changes them and not between two ticks of the host's own pace, and
// each run is several requests to a service that bills and logs them. Thirty
// seconds is the pace kv.overview keeps for the same reason.
const overviewRefresh = 30 * time.Second

// walkKeys bounds how much of a bucket the detailed page reads to say what it
// holds: one request per thousand keys, the same bound s3.object.tree reads
// before it says where it stopped.
const walkKeys = 2000

// s3.overview composes a bucket count through the one client its own Run
// already built — the same reason pg.overview and vault.overview call their
// sections directly rather than through plugin.Page.AddAs, which would open
// one connection per section for what is supposed to be a single glance.
//
// **About the bucket the connection is for, when it is for one.** An endpoint's
// bucket count says nothing about the application a profile is the connection
// of, and a profile that fences `bucket` to one name has said which that is.
// With a bucket, named or fenced, the page is about it — whether it is there,
// since when, whether it keeps versions, and with `detail` what it holds
// — and not the list of every bucket the key can see, which is also what the
// fence was written to keep out of a connection's answers.
//
// **And the keys of it, when the connection is for part of one.** `prefix` is
// declared here, with the Config key s3.object.tree and s3.object.list already
// use, because a fence binds only the inputs a capability declares: without it
// `only: prefix:` held the other two and left the overview counting, summing
// and dating the whole bucket past it.
//
// NoPreview like every capability here (cap's job): the automatic dashboard
// must not decide on its own that an S3 endpoint is worth polling every few
// seconds. dashboard.tiles still accepts it explicitly — naming a
// capability in a config file is the asking.
func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "s3.overview",
		Summary:    "Endpoint and bucket count at a glance",
		Keywords:   []string{"summary", "status", "minio", "region"},
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Refresh:    overviewRefresh,
		Description: "Whether this endpoint is reachable at all, and how many buckets the " +
			"configured credentials can see. `detail` adds the bucket list, with age and, where the server reports one, region, " +
			"to the same page.\n\n" +
			"With a `bucket` — named, or the one a profile fences with `only: bucket:` — the page " +
			"is about that bucket: whether it exists, when it was created and whether " +
			"it keeps versions, and with `detail` how many objects it holds, how large they are " +
			"and when the newest was written (counted over the first " + strconv.Itoa(walkKeys) +
			" keys, and it says when it stopped). With a `prefix`, only the keys under it are " +
			"counted — a profile that fences `prefix` holds the page to it.",
		Run: runOverview,
	}, optionalBucketField("only this bucket — every bucket the credentials can see when omitted"),
		plugin.Field{Name: "prefix", Short: "p", Type: plugin.String, Config: "prefix",
			Help: "with a bucket, count only the keys starting with this",
			Live: true, Suggest: suggestKeys("prefix")})
}

func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(ctx, req, func(ctx context.Context, client *minio.Client) (view.View, error) {
		if bucket := strings.TrimSpace(req.String("bucket")); bucket != "" {
			return targetOverview(ctx, req, client, bucket)
		}
		buckets, err := client.ListBuckets(ctx)
		if err != nil {
			return nil, classify(err, req)
		}
		if req.Bool("detail") {
			return detailedOverview(req, buckets), nil
		}
		return compactOverview(req, buckets), nil
	})
}

func compactOverview(req plugin.Request, buckets []minio.BucketInfo) view.View {
	return view.KeyValue{Pairs: []view.Pair{
		{Key: "address", Value: req.Reached(req.String("address"))},
		{Key: "buckets", Value: strconv.Itoa(len(buckets))},
	}}
}

func detailedOverview(req plugin.Request, buckets []minio.BucketInfo) view.View {
	p := plugin.NewPage(context.Background(), req)
	p.Put("status", compactOverview(req, buckets))

	p.Put("buckets", bucketTable(buckets))

	return p.View()
}

// targetOverview is the page about one bucket.
//
// The existence check is the one request that must answer: a bucket that is
// not there is the finding, and it is said as an error rather than as a page
// of blanks. Everything after it is best-effort, because a key that may read a
// bucket and not ask about its location or its versioning is an ordinary
// least-privilege key, and a tile that went red for lacking those would be
// reporting the key's rights and not the bucket's state. A row the server would
// not give is left out and not guessed. The region is not one of them: minio-go
// answers it from the connection's own `region` setting whenever one is set,
// which would show the operator's configuration as if the bucket had said it.
func targetOverview(ctx context.Context, req plugin.Request, client *minio.Client, bucket string) (view.View, error) {
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, classify(err, req)
	}
	if !exists {
		return nil, view.Errorf("s3.bucket.notfound", "%s has no bucket %s",
			req.Reached(req.String("address")), quoted(bucket)).
			WithHint("the name comes from the connection's `bucket` setting or its `only: bucket:` fence — " +
				"check it against the bucket's real name; the key may also not reach it")
	}

	pairs := []view.Pair{
		{Key: "address", Value: req.Reached(req.String("address"))},
		{Key: "bucket", Value: plugin.ListedName(bucket)},
	}
	if created, ok := createdOf(ctx, client, bucket); ok {
		pairs = append(pairs, view.Pair{Key: "created", Value: created.Format("2006-01-02") + " (" + format.Ago(created) + ")"})
	}
	if v, err := client.GetBucketVersioning(ctx, bucket); err == nil {
		pairs = append(pairs, view.Pair{Key: "versioning", Value: versioningWord(v)})
	}
	head := view.KeyValue{Pairs: pairs}
	if !req.Bool("detail") {
		return head, nil
	}

	contents, verr := contentsOf(ctx, req, client, bucket, req.String("prefix"))
	if verr != nil {
		return nil, verr
	}
	p := plugin.NewPage(ctx, req)
	p.Put("status", head)
	p.Put("contents", contents)
	return p.View(), nil
}

// createdOf is when the bucket was created, which only the account's bucket
// listing carries. Read to find one entry and shown as that one: the names of
// the others are not this call's business.
func createdOf(ctx context.Context, client *minio.Client, bucket string) (time.Time, bool) {
	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		return time.Time{}, false
	}
	for _, b := range buckets {
		if b.Name == bucket {
			return b.CreationDate, !b.CreationDate.IsZero()
		}
	}
	return time.Time{}, false
}

func versioningWord(v minio.BucketVersioningConfiguration) string {
	switch {
	case v.Enabled():
		return "enabled"
	case v.Suspended():
		return "suspended"
	}
	return "off"
}

// contentsOf is what the bucket holds, counted over at most walkKeys keys: the
// objects, their size, and the newest write — the line a backup bucket is read
// for, since a bucket whose newest object is a week old is a backup that has
// stopped. Said to be partial when it is, on every row it affects, because
// "2000 objects" of a bucket that has two million reads as the whole of it, and
// a size or a newest write beside it reads as the bucket's too.
//
// **Stopped at the budget, not one key past it.** The iterator only knows
// there is a key beyond the last of a page by fetching the next page, so
// reading the 2001st to find out whether the 2000th was the end cost a whole
// third page, a thousand keys of metadata, to answer yes or no. The walk breaks
// as soon as the budget is read and asks the server for the one key that
// follows it, which is the same answer in a request that carries one line. A
// bucket of exactly the budget is the case that distinguishes the two:
// nothing follows, and it is not partial.
func contentsOf(ctx context.Context, req plugin.Request, client *minio.Client, bucket, prefix string) (view.View, *view.Error) {
	var (
		objects, read int
		bytes         int64
		newest        time.Time
		last          string
	)
	for obj := range listObjects(ctx, client, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, classify(obj.Err, req)
		}
		read++
		last = obj.Key
		// A zero-byte directory marker is not an object anybody stored (see
		// runObjectTree).
		if !strings.HasSuffix(obj.Key, "/") {
			objects++
			bytes += obj.Size
			if obj.LastModified.After(newest) {
				newest = obj.LastModified
			}
		}
		if read == walkKeys {
			break
		}
	}
	if verr := ctxErr(ctx, req); verr != nil {
		return nil, verr
	}

	partial := false
	if read == walkKeys {
		more, err := hasKeyAfter(ctx, client, bucket, prefix, last)
		if err != nil {
			return nil, classify(err, req)
		}
		if verr := ctxErr(ctx, req); verr != nil {
			return nil, verr
		}
		partial = more
	}

	count := format.CountOf(objects, "object")
	if partial {
		count = strconv.Itoa(objects) + "+ objects — read through " + plugin.ListedName(last) + " in key order; " +
			req.Surface().CapabilityName("s3.object.tree") + " reads further"
	}
	var pairs []view.Pair
	if prefix != "" {
		pairs = append(pairs, view.Pair{Key: "prefix", Value: plugin.ListedName(prefix)})
	}
	pairs = append(pairs, view.Pair{Key: "objects", Value: count})
	if objects > 0 {
		size := format.Bytes(bytes)
		when := format.Ago(newest) + " (" + newest.Format("2006-01-02 15:04") + ")"
		if partial {
			size = "at least " + size
			when += " — the newest of the keys read, not necessarily of the bucket"
		}
		pairs = append(pairs,
			view.Pair{Key: "size", Value: size},
			view.Pair{Key: "newest", Value: when},
		)
	}
	return view.KeyValue{Pairs: pairs}, nil
}

// hasKeyAfter asks whether the bucket holds anything after key: one listing
// of one key, which is all a yes or a no needs.
func hasKeyAfter(ctx context.Context, client *minio.Client, bucket, prefix, key string) (bool, error) {
	for obj := range listObjects(ctx, client, bucket, minio.ListObjectsOptions{
		Prefix: prefix, Recursive: true, StartAfter: key, MaxKeys: 1,
	}) {
		if obj.Err != nil {
			return false, obj.Err
		}
		return true, nil
	}
	return false, nil
}
