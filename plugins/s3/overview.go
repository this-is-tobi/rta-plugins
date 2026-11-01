package main

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/lifecycle"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// overviewRefresh is how often a dashboard tile of s3.overview is re-run, and the
// pace a configuration cannot shorten. A bucket's tile is five requests, one of
// them a page of a thousand keys, to a service that bills and logs them: what it
// shows is how much the bucket holds and when it was last written, which change
// when somebody writes and not between two ticks of the host's own pace, and on
// a large bucket a page of keys every thirty seconds was a bill for a glance.
const overviewRefresh = 2 * time.Minute

// firstPageKeys is how many keys the compact form of a bucket reads: one page of
// the listing, which is a thousand on every service that follows S3, and one
// request. The detail reads further (walkKeys) and says where it stopped.
const firstPageKeys = 1000

// overviewTimeout is how long the overview waits for a service in all, for the
// few requests of a page together. minio-go waits a minute for the headers of
// each response, so an endpoint that took the connection and never answered held
// the page for that long, and a tile for as long again at every refresh. A
// variable so a test need not wait ten seconds to see it given up on.
//
// The overview's alone: a download or an upload moves the data and is the
// caller's to wait for. Bounding the page and not each request is deliberate
// here, since a page is one request, or three for a bucket, or the three or four
// listings of a bucket's first two thousand keys, and none of them is long. A
// list of buckets holds to it as a whole, and its detail gets it again for each
// round of buckets read at once (listOverview).
var overviewTimeout = 10 * time.Second

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
// **Or about several, when it is for several.** `bucket` is a list, since a
// profile that fences it to an application's buckets has said which those are
// and the overview was the capability that could not be handed them. With more
// than one, or none, the page is a row for each bucket (overviewlist.go) and the
// page for one is exactly what it was: a list of one is the bucket it names.
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
		Summary:    "Whether an endpoint or a bucket is healthy, at a glance",
		Keywords:   []string{"summary", "status", "minio", "region"},
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Refresh:    overviewRefresh,
		Description: "Whether this endpoint is reachable, and how its buckets are doing. `bucket` is a list of " +
			"the buckets to read; naming none reads every bucket the credentials can see, within the " +
			"profile's `only: bucket:` fence when it has one. A list goes in the call, a tile's `with:` or " +
			"that fence: the config's `bucket` and a profile's `set:` take one name, shared with the " +
			"capabilities that act on one bucket.\n\n" +
			"One bucket, named or fenced: the page is about it, worst first. `status` is `ok`, or `warn` and " +
			"the rows the credentials could not read. Then its objects and when the newest was written, from " +
			"the first page of its keys (`1000+ (first page)` when there are more, the newest then of those " +
			"read), versioning, what its lifecycle rules do to an object's age (`none`: it keeps everything), " +
			"its name and creation date. With a `prefix`, only the keys under it. Five " +
			"requests, and a tile is re-run every two minutes at most.\n\n" +
			"Several buckets, or none named: `status` (`ok`, or `warn` naming the buckets that could not be " +
			"read, or how many of an account's were), `buckets` counted, a row for each, and the address. A " +
			"row is a bucket's objects (`1000+` when the first page of its keys is full) and when the newest " +
			"was written, from one request; or `could not be read — <why>`, `not found` for a name that is " +
			"not there. Worst first (unreadable, empty, then written longest ago); past eight rows the well " +
			"ones are one row. At most " + strconv.Itoa(maxBuckets) + " are read, " + strconv.Itoa(readsAtOnce) +
			" at a time: a longer list is refused, an account of more reads the first " + strconv.Itoa(maxBuckets) +
			" by name and says so. A refresh is a request for each bucket, plus the account's listing when " +
			"none is named.\n\n" +
			"`detail` reads further: for one bucket, its objects, size and newest write over the " +
			"first " + strconv.Itoa(walkKeys) + " keys, saying when it stopped; for several, that for each in a " +
			"section, with versioning and lifecycle. A service silent for ten seconds is a timeout; a bucket " +
			"of several that has not answered by then is a row saying so, and the detail gets ten seconds " +
			"more for each " + strconv.Itoa(readsAtOnce) + " buckets.",
		Agent: "`bucket` is an array; none reads every bucket the credentials see (at most " +
			strconv.Itoa(maxBuckets) + ", a request each). One bucket: `status` is `ok`, or `warn` and the rows " +
			"that could not be read; objects and newest write from the first page of keys (`1000+ (first " +
			"page)` when more), versioning, retention (`none`: no rule), name and creation date. Several or " +
			"none: `status`, `buckets`, a row a bucket worst first (`could not be read — why`, `not found`), " +
			"the endpoint; past eight rows the well ones fold into one. `prefix` holds it to the keys under " +
			"it. `detail` reads up to " + strconv.Itoa(walkKeys) + " keys of each: count, size, newest write.",
		Run: runOverview,
	}, optionalBucketField("only these buckets — every bucket the credentials can see when omitted"),
		plugin.Field{Name: "prefix", Short: "p", Type: plugin.String, Config: "prefix",
			Help: "with buckets, count only the keys starting with this",
			Live: true, Suggest: suggestKeys("prefix")})
}

// runOverview reads one bucket as it always did, and a list of buckets, or none,
// a bucket at a time (listOverview). The bound of overviewTimeout is each one's
// own: the list sets its own, since what it reads is a number of buckets.
func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
	named, verr := overviewBuckets(req)
	if verr != nil {
		return nil, verr
	}
	return withClient(ctx, req, func(ctx context.Context, client *minio.Client) (view.View, error) {
		if len(named) != 1 {
			return listOverview(ctx, req, client, named)
		}
		ctx, cancel := context.WithTimeout(ctx, overviewTimeout)
		defer cancel()
		// The bucket as a name, which is what everything below it reads: a hint
		// that names the bucket and the check of its spelling are the one
		// bucket's, and a list of one is that bucket.
		return targetOverview(ctx, req.With(map[string]any{"bucket": named[0]}), client, named[0])
	})
}

// grade is how bad a fact is, in the words the status row leads with and the
// theme colours: nothing, warn.
type grade int

const (
	_ grade = iota
	gradeWarn
)

// finding is one row that could not be read, for the status row.
type finding struct {
	grade  grade
	unread string
}

// verdict is the status row: ok, or a warning that names every row the
// credentials could not read — a key that may list a bucket and not read its
// lifecycle is an ordinary least-privilege key, and a verdict that named one of
// three would read as the whole of it.
func verdict(found []finding) string {
	if len(found) == 0 {
		return "ok"
	}
	rows := make([]string, 0, len(found))
	for _, f := range found {
		rows = append(rows, f.unread)
	}
	return "warn — " + strings.Join(rows, ", ") + " could not be read"
}

// unreadWhy is why a read failed, short enough for a row: a refusal by the
// credentials' policy and a service that does not answer say the same everywhere
// they happen, and the rest is the classified message, cut to one line of a tile.
func unreadWhy(verr *view.Error) string {
	switch verr.Code {
	case "s3.denied":
		return "access denied"
	case "s3.timeout":
		return "did not answer in time"
	case "s3.bucket.notfound":
		return "not found"
	case "s3.bucket.invalid":
		return "not a valid bucket name"
	}
	head, rest := format.Head(verr.Message, 48)
	if rest > 0 {
		head += "…"
	}
	return head
}

// bucketHead is what the overview of a bucket reads beside its keys, each fact
// beside the reason it could not be read.
type bucketHead struct {
	created    string
	versioning string
	retention  string

	versioningErr *view.Error
	retentionErr  *view.Error
}

// readHead asks the bucket how it keeps versions and what its lifecycle rules do,
// and the account when it was created.
//
// The existence check before it is the one request that must answer: a bucket
// that is not there is the finding, and it is said as an error rather than as a
// page of blanks. Everything here is best-effort, because a key that may read a
// bucket and not ask about its versioning or its lifecycle is an ordinary
// least-privilege key, and a tile that went red for lacking those would be
// reporting the key's rights and not the bucket's state. A fact the server would
// not give is said to be unread, and never guessed. The region is not one of
// them: minio-go answers it from the connection's own `region` setting whenever
// one is set, which would show the operator's configuration as if the bucket had
// said it. And the creation date, which only the account's listing carries, is
// left out when the key may not list the account: a bucket-scoped key is that,
// and the date is the bucket's name's company and not a row of its own.
func readHead(ctx context.Context, req plugin.Request, client *minio.Client, bucket string) bucketHead {
	created, ok := createdOf(ctx, client, bucket)
	h := readSettings(ctx, req, client, bucket)
	if ok {
		h.created = createdText(created)
	}
	return h
}

func createdText(created time.Time) string {
	return created.Format("2006-01-02") + " (" + format.Ago(created) + ")"
}

// readSettings is readHead without the date, for the page of several buckets,
// which asks the account for all of them at once.
func readSettings(ctx context.Context, req plugin.Request, client *minio.Client, bucket string) bucketHead {
	var h bucketHead
	if v, err := client.GetBucketVersioning(ctx, bucket); err == nil {
		h.versioning = versioningWord(v)
	} else {
		h.versioningErr = classify(err, req)
	}
	cfg, err := client.GetBucketLifecycle(ctx, bucket)
	switch {
	case err == nil:
		h.retention = retentionText(cfg.Rules)
	case minio.ToErrorResponse(err).Code == "NoSuchLifecycleConfiguration":
		h.retention = "none"
	default:
		h.retentionErr = classify(err, req)
	}
	return h
}

// retentionText says what the enabled lifecycle rules of a bucket do to an
// object's age: how many there are and, where they expire objects after a number
// of days, after how many. None is a fact worth a row, since it is a bucket that
// keeps everything until somebody deletes it.
func retentionText(rules []lifecycle.Rule) string {
	var enabled, disabled int
	var days []int
	for _, r := range rules {
		if r.Status != "Enabled" {
			disabled++
			continue
		}
		enabled++
		if d := int(r.Expiration.Days); d > 0 {
			days = append(days, d)
		}
	}
	switch {
	case enabled == 0 && disabled == 0:
		return "none"
	case enabled == 0:
		return "none enabled (" + format.CountOf(disabled, "rule") + " disabled)"
	}
	text := format.CountOf(enabled, "rule")
	if len(days) > 0 {
		sort.Ints(days)
		span := strconv.Itoa(days[0]) + "d"
		if last := days[len(days)-1]; last != days[0] {
			span += " to " + strconv.Itoa(last) + "d"
		}
		text += ", objects expire after " + span
	}
	return text
}

// firstPage is what one page of a bucket's keys says: how many objects, whether
// there may be more, and the newest write among them.
type firstPage struct {
	objects int
	partial bool
	newest  time.Time
	err     *view.Error
}

// readFirstPage lists one page of the bucket's keys, a thousand at most, and
// stops there: the iterator pulls a page at a time in this goroutine, so breaking
// at the last key of the first leaves the second unasked.
//
// **A page that is full may be all of the bucket, and is said not to be.** The
// listing's answer to whether there is more is on the page and not in the keys,
// and the iterator keeps it to itself; a bucket of exactly a thousand reads
// "1000+", where the detail's peek for one more key tells them apart. The newest
// write is the newest of what was read: S3 lists in key order, and a bucket whose
// keys are dates lists its oldest first, so the row says it is of the page.
func readFirstPage(ctx context.Context, req plugin.Request, client *minio.Client, bucket, prefix string) firstPage {
	var p firstPage
	read := 0
	for obj := range listObjects(ctx, client, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true, MaxKeys: firstPageKeys}) {
		if obj.Err != nil {
			p.err = classify(obj.Err, req)
			break
		}
		read++
		// A zero-byte directory marker is not an object anybody stored (see
		// runObjectTree).
		if !strings.HasSuffix(obj.Key, "/") {
			p.objects++
			if obj.LastModified.After(p.newest) {
				p.newest = obj.LastModified
			}
		}
		if read == firstPageKeys {
			p.partial = true
			break
		}
	}
	return p
}

// missingBucket is the finding of a named bucket that is not there.
func missingBucket(req plugin.Request, bucket string) *view.Error {
	return view.Errorf("s3.bucket.notfound", "%s has no bucket %s",
		req.Reached(req.String("address")), quoted(bucket)).
		WithHint("the name comes from the connection's `bucket` setting or its `only: bucket:` fence — " +
			"check it against the bucket's real name; the key may also not reach it").WithInput("bucket")
}

// targetOverview is the page about one bucket.
func targetOverview(ctx context.Context, req plugin.Request, client *minio.Client, bucket string) (view.View, error) {
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, classify(err, req)
	}
	if !exists {
		return nil, missingBucket(req, bucket)
	}

	head := readHead(ctx, req, client, bucket)
	prefix := req.String("prefix")
	if req.Bool("detail") {
		return detailedBucket(ctx, req, client, bucket, prefix, head)
	}
	page := readFirstPage(ctx, req, client, bucket, prefix)
	// A deadline that passed while the reads ran leaves every one after it
	// failed, and a page of rows that all say they could not be read is the
	// timeout told badly.
	if verr := ctxErr(ctx, req); verr != nil {
		return nil, verr
	}
	return compactBucket(bucket, prefix, head, page), nil
}

// compactBucket is the tile of a bucket.
//
// **The order is the order of a glance**: the verdict, what the bucket holds and
// when it was last written, how it keeps versions and for how long, and last what
// it is. A tile is cut from the bottom, and the page this replaces was four rows
// that never change: the address, the name, the date it was created and the
// versioning.
func compactBucket(bucket, prefix string, head bucketHead, page firstPage) view.KeyValue {
	var found []finding
	var rows []view.Pair
	add := func(key, value string) { rows = append(rows, view.Pair{Key: key, Value: value}) }
	unread := func(key string, verr *view.Error) {
		add(key, view.CouldNotRead(unreadWhy(verr)))
		found = append(found, finding{gradeWarn, key})
	}

	if prefix != "" {
		add("prefix", plugin.ListedName(prefix))
	}
	if page.err != nil {
		unread("objects", page.err)
	} else {
		objects := strconv.Itoa(page.objects)
		if page.partial {
			objects += "+ (first page)"
		}
		add("objects", objects)
		if page.objects > 0 {
			when := format.Ago(page.newest) + " (" + page.newest.Format("2006-01-02 15:04") + ")"
			if page.partial {
				when += " — of the first page only"
			}
			add("newest", when)
		}
	}
	if head.versioningErr != nil {
		unread("versioning", head.versioningErr)
	} else {
		add("versioning", head.versioning)
	}
	if head.retentionErr != nil {
		unread("retention", head.retentionErr)
	} else {
		add("retention", head.retention)
	}
	name := plugin.ListedName(bucket)
	if head.created != "" {
		name += ", created " + head.created
	}
	add("bucket", name)
	return view.KeyValue{Pairs: append([]view.Pair{{Key: "status", Value: verdict(found)}}, rows...)}
}

// detailedBucket is the page the default used to be, and the bounded walk: the
// status block with the address, the bucket, when it was created, how it keeps
// versions and what its rules do, and what it holds counted over the first
// walkKeys keys. A listing the key may not read is a row that says so and not an
// error that discards the head; a service that does not answer is the error.
func detailedBucket(ctx context.Context, req plugin.Request, client *minio.Client, bucket, prefix string, head bucketHead) (view.View, error) {
	contents, found, verr := detailContents(ctx, req, client, bucket, prefix)
	if verr != nil {
		return nil, verr
	}
	settings, unread := head.rows()
	pairs := append([]view.Pair{
		{Key: "status", Value: verdict(append(found, unread...))},
		{Key: "address", Value: req.Reached(req.String("address"))},
		{Key: "bucket", Value: plugin.ListedName(bucket)},
	}, settings...)
	p := plugin.NewPage(ctx, req)
	p.Put("status", view.KeyValue{Pairs: pairs})
	p.Put("contents", contents)
	return p.View(), nil
}

// detailContents is what the detail says a bucket holds, and what it could not
// read to say it: a listing the key may not read is a row that says so and not
// an error that discards the head, while a service that does not answer, or a
// call given up on, is the error.
func detailContents(ctx context.Context, req plugin.Request, client *minio.Client, bucket, prefix string) (view.KeyValue, []finding, *view.Error) {
	contents, verr := contentsOf(ctx, req, client, bucket, prefix)
	if verr == nil {
		return contents, nil, nil
	}
	if verr.Code == "s3.timeout" || verr.Code == "s3.cancelled" {
		return view.KeyValue{}, nil, verr
	}
	return view.KeyValue{Pairs: []view.Pair{{Key: "objects", Value: view.CouldNotRead(unreadWhy(verr))}}},
		[]finding{{gradeWarn, "objects"}}, nil
}

// rows are the rows the detail says of a bucket's settings, when it was created,
// how it keeps versions and what its rules do, and the ones among them that
// could not be read.
func (h bucketHead) rows() ([]view.Pair, []finding) {
	var pairs []view.Pair
	var found []finding
	if h.created != "" {
		pairs = append(pairs, view.Pair{Key: "created", Value: h.created})
	}
	if h.versioningErr != nil {
		pairs = append(pairs, view.Pair{Key: "versioning", Value: view.CouldNotRead(unreadWhy(h.versioningErr))})
		found = append(found, finding{gradeWarn, "versioning"})
	} else {
		pairs = append(pairs, view.Pair{Key: "versioning", Value: h.versioning})
	}
	if h.retentionErr != nil {
		pairs = append(pairs, view.Pair{Key: "retention", Value: view.CouldNotRead(unreadWhy(h.retentionErr))})
		found = append(found, finding{gradeWarn, "retention"})
	} else {
		pairs = append(pairs, view.Pair{Key: "retention", Value: h.retention})
	}
	return pairs, found
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
func contentsOf(ctx context.Context, req plugin.Request, client *minio.Client, bucket, prefix string) (view.KeyValue, *view.Error) {
	var (
		objects, read int
		bytes         int64
		newest        time.Time
		last          string
	)
	for obj := range listObjects(ctx, client, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return view.KeyValue{}, classify(obj.Err, req)
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
		return view.KeyValue{}, verr
	}

	partial := false
	if read == walkKeys {
		more, err := hasKeyAfter(ctx, client, bucket, prefix, last)
		if err != nil {
			return view.KeyValue{}, classify(err, req)
		}
		if verr := ctxErr(ctx, req); verr != nil {
			return view.KeyValue{}, verr
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
