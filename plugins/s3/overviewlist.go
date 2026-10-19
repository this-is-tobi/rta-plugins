package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The buckets of s3.overview: a list, each one read with requests of its own.
//
// A profile that fences the bucket to the several an application keeps is the
// connection of a tile that names them, and the overview was the capability
// that could not be handed them: its bucket was one name, and a list read as
// text that is in no fence entry. Each bucket is read with its own request (the
// first page of its keys) and not the account with a filter over it, for the
// reason the page for one bucket does not find it by listing the account: a key
// scoped to some buckets may not list the account, and a fence is meant to keep
// the read inside what the connection reaches, which a read of everything with
// the rest thrown away does not. The cost is the number of requests, and these
// constants bound it.

// maxBuckets is the most buckets one overview reads. A tile of thirty buckets
// is thirty-one requests every two minutes to a service that bills and logs
// them, and a list longer than that is a typo or a connection that should be
// several tiles. A cap that is refused is worth more than a tile that quietly
// takes the service's attention. An account of more reads the first thirty by
// name, and the verdict says so.
const maxBuckets = 30

// readsAtOnce is how many buckets one overview has in flight: enough that
// thirty are not thirty round trips in a line, few enough that a tile is not a
// burst against a service that rate-limits by account.
const readsAtOnce = 6

const (
	// rowsInATile is how many rows the compact form has, which is what a
	// dashboard tile holds; what does not fit is one row (foldedRow).
	rowsInATile = 8
	// tileWidth is the longest a row is, key and value, so that a third of a
	// screen shows it on a line. A name is cut to fit it (fitName) before
	// anything else on the row is.
	tileWidth    = 80
	minNameWidth = 12
)

// overviewBuckets is the buckets a call names, or none; a list longer than
// maxBuckets is refused before anything is asked.
//
// The refusal says what the cap is and that this call reaches more, and neither
// how many it reaches nor that the buckets were named: the list is not always
// what the caller wrote, since a call that names none under a profile's fence is
// handed the fence itself, and the host keeps what a fence holds from an agent,
// which a count of it would give back.
func overviewBuckets(req plugin.Request) ([]string, *view.Error) {
	named := bucketNames(req)
	if len(named) <= maxBuckets {
		return named, nil
	}
	hint := "name fewer buckets, or ask the operator to fence fewer"
	if sf := req.Surface(); sf != plugin.SurfaceMCP {
		hint = "name fewer with " + sf.InputName("bucket") + ", or fence fewer in the profile or the tile that names them"
	}
	return nil, view.Errorf("s3.bucket.toomany",
		"an overview reads at most %d buckets, and this call reaches more", maxBuckets).WithHint(hint)
}

// readScope is what a list is read of: the buckets to read, and what the page
// says of the rest.
type readScope struct {
	targets []string
	// named is true when the caller named the buckets, and the account was not
	// listed to find them; total is then 0. Otherwise it is how many buckets the
	// credentials see, of which targets are the first maxBuckets by name.
	named bool
	total int
	// created is when the account says each bucket was made, for the detail. Empty
	// when the key may not list the account.
	created map[string]time.Time
}

func (s readScope) capped() bool { return !s.named && s.total > len(s.targets) }

// scopeOf finds the buckets to read. None named is the account's listing, which
// is the one request the page makes beyond a request a bucket, and a key that
// cannot make it is the error it always was: there is nothing to read the
// buckets of. The detail asks the account for the dates the buckets were created
// in either case, once, and does without them when the key may not.
func scopeOf(ctx context.Context, req plugin.Request, client *minio.Client, named []string, detail bool) (readScope, *view.Error) {
	if len(named) > 0 {
		scope := readScope{targets: named, named: true}
		if detail {
			if account, err := client.ListBuckets(ctx); err == nil {
				scope.created = createdDates(account)
			}
		}
		return scope, nil
	}
	account, err := client.ListBuckets(ctx)
	if err != nil {
		return readScope{}, classify(err, req)
	}
	sort.Slice(account, func(i, j int) bool { return account[i].Name < account[j].Name })
	scope := readScope{total: len(account), created: createdDates(account)}
	for _, b := range account[:min(len(account), maxBuckets)] {
		scope.targets = append(scope.targets, b.Name)
	}
	return scope, nil
}

func createdDates(account []minio.BucketInfo) map[string]time.Time {
	dates := make(map[string]time.Time, len(account))
	for _, b := range account {
		if !b.CreationDate.IsZero() {
			dates[b.Name] = b.CreationDate
		}
	}
	return dates
}

// bucketRead is what was read of one bucket of a list.
type bucketRead struct {
	name string
	// page is the first page of the bucket's keys, which is the whole of the
	// compact form's reading.
	page firstPage
	// rows and found are the detail's: the rows of the bucket's section, and the
	// ones among them that could not be read.
	rows  []view.Pair
	found []finding
	// unread is why the bucket could not be read at all: not there, refused, or a
	// service that failed it.
	unread *view.Error
}

// eachAtOnce runs read for each of n, readsAtOnce at a time, and returns when
// all have. The gate is taken before the goroutine is started and not inside it,
// so that thirty buckets are never thirty goroutines waiting.
func eachAtOnce(n int, read func(i int)) {
	gate := make(chan struct{}, readsAtOnce)
	var wg sync.WaitGroup
	for i := range n {
		gate <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-gate; wg.Done() }()
			read(i)
		}()
	}
	wg.Wait()
}

// listOverview is the page of several buckets, or of the account's.
//
// **One deadline for the page.** The compact form is a request a bucket, six at
// a time, and holds to the overview's bound (overviewTimeout) as one bucket's
// page does. The detail is several requests a bucket and a page a person asked
// for, so its deadline grows with the number of rounds of six it takes: the
// bound a bucket is given alone, for each round. It is one deadline from the
// start of the call, not one for each bucket, so a service that answers nothing
// is given up on at the end of it and not once for each bucket.
//
// **A bucket that does not answer by then is a row, and the others stand.** The
// deadline fails every read still in flight or not yet begun with s3.timeout,
// and a bucket whose read failed that way is `could not be read — did not answer
// in time` like any other (unreadWhy), named by the verdict. It is the endpoint
// that answers none of them that is the error (serviceFailure), as it is for any
// other failure the service gives every bucket alike. Only the caller's own
// cancel ends the call, since nobody is left to read the page.
func listOverview(caller context.Context, req plugin.Request, client *minio.Client, named []string) (view.View, error) {
	started := time.Now()
	detail := req.Bool("detail")

	listing, cancel := context.WithTimeout(caller, overviewTimeout)
	scope, verr := scopeOf(listing, req, client, named, detail)
	cancel()
	if verr != nil {
		return nil, verr
	}

	budget := overviewTimeout
	if detail {
		budget *= time.Duration(max(1, (len(scope.targets)+readsAtOnce-1)/readsAtOnce))
	}
	ctx, cancel := context.WithDeadline(caller, started.Add(budget))
	defer cancel()

	prefix := req.String("prefix")
	reads := make([]bucketRead, len(scope.targets))
	eachAtOnce(len(reads), func(i int) {
		if detail {
			reads[i] = readBucketDetail(ctx, req, client, scope, scope.targets[i], prefix)
		} else {
			reads[i] = readBucketPage(ctx, req, client, scope.targets[i], prefix)
		}
	})
	if verr := ctxErr(caller, req); verr != nil {
		return nil, verr
	}
	if verr := serviceFailure(reads); verr != nil {
		return nil, verr
	}
	if detail {
		sortByTrouble(reads)
		return detailedBuckets(caller, req, scope, reads), nil
	}
	sortByStaleness(reads)
	return compactBuckets(req, scope, reads, prefix), nil
}

// readBucketPage is the compact form's reading of a bucket: one request, for the
// first page of its keys. Whether the bucket is there is what that request says,
// so a name that is not is a row and not a second request to find out.
func readBucketPage(ctx context.Context, req plugin.Request, client *minio.Client, name, prefix string) bucketRead {
	page := readFirstPage(ctx, req.With(map[string]any{"bucket": name}), client, name, prefix)
	return bucketRead{name: name, page: page, unread: page.err}
}

// readBucketDetail is the detail of one bucket, as the page for that bucket alone
// reads it: whether it is there, how it keeps versions, what its rules do, and
// what it holds over the first walkKeys keys. The date it was created is the
// account's, asked once for all of them.
func readBucketDetail(ctx context.Context, req plugin.Request, client *minio.Client, scope readScope, name, prefix string) bucketRead {
	r := bucketRead{name: name}
	one := req.With(map[string]any{"bucket": name})
	exists, err := client.BucketExists(ctx, name)
	switch {
	case err != nil:
		r.unread = classify(err, one)
		return r
	case !exists:
		r.unread = missingBucket(one, name)
		return r
	}
	head := readSettings(ctx, one, client, name)
	if created, ok := scope.created[name]; ok {
		head.created = createdText(created)
	}
	contents, found, verr := detailContents(ctx, one, client, name, prefix)
	if verr != nil {
		r.unread = verr
		return r
	}
	settings, unread := head.rows()
	r.found = append(found, unread...)
	r.rows = append(append([]view.Pair{{Key: "status", Value: verdict(r.found)}}, settings...), contents.Pairs...)
	return r
}

// bucketLevel is whether a failure is the bucket's own, as opposed to the
// service's: a name that is not there, a bucket the key may not list.
func bucketLevel(verr *view.Error) bool {
	switch verr.Code {
	case "s3.bucket.notfound", "s3.denied", "s3.bucket.invalid":
		return true
	}
	return false
}

// serviceFailure is the error of a page in which no bucket could be read and
// none for a reason of its own: a service that cannot be reached, or rejects the
// credentials, fails every bucket the same way, and a page of rows that all say
// so is that error told once for each. The first, since they are the same. A
// bucket that is not there, or not the key's to list, is an answer and stays a
// row, however many of them there are.
func serviceFailure(reads []bucketRead) *view.Error {
	for _, r := range reads {
		if r.unread == nil || bucketLevel(r.unread) {
			return nil
		}
	}
	if len(reads) == 0 {
		return nil
	}
	return reads[0].unread
}

// troubles are the facts of a bucket that could not be read, for the verdict: the
// bucket, when all of it could not, and each of its rows that could not, when
// only those could.
func (r bucketRead) troubles() []string {
	name := plugin.ListedName(r.name)
	if r.unread != nil {
		return []string{name}
	}
	out := make([]string, len(r.found))
	for i, f := range r.found {
		out[i] = name + " " + f.unread
	}
	return out
}

// listVerdict is the status row of a list: ok, or a warning that names the
// buckets that could not be read, and when it read some of an account's buckets
// and not all, how many. The ok of an account of forty-one buckets is a claim
// about the thirty that were read, and the verdict is where that is said.
func listVerdict(reads []bucketRead, scope readScope) string {
	var items []string
	troubled := 0
	for _, r := range reads {
		if t := r.troubles(); len(t) > 0 {
			items = append(items, t...)
			troubled++
		}
	}
	text := "ok"
	if len(items) > 0 {
		text = "warn — "
		if joined := strings.Join(items, ", "); len(items) <= 3 && utf8.RuneCountInString(joined) <= 40 {
			text += joined + " could not be read"
		} else {
			text += format.CountOf(troubled, "bucket") + " could not be read, first " + fitName(items[0], 24)
		}
	}
	if scope.capped() {
		sep := " — "
		if len(items) > 0 {
			sep = "; "
		}
		text += sep + strconv.Itoa(len(scope.targets)) + " of " + strconv.Itoa(scope.total) + " buckets read"
	}
	return text
}

// bucketsRow is how many buckets the page is about: the ones named, or the ones
// the credentials see.
func bucketsRow(scope readScope) string {
	if scope.named {
		return strconv.Itoa(len(scope.targets)) + " named"
	}
	return strconv.Itoa(scope.total)
}

// keyset is the keys a page has, so that no two rows or sections share one.
type keyset map[string]bool

// claim is the first of the candidates the page does not have yet, taken. When
// they are all taken it is the last, with `bucket ` in front of it for as many
// times as it takes: a bucket named like a key of the page itself (status,
// buckets, prefix, more and address are all valid bucket names) is `bucket
// status`, and only then. A reader that looks the page up by key, an agent or a
// tile that colours `status`, would otherwise get the bucket's row in place of
// the verdict.
func (k keyset) claim(candidates ...string) string {
	for _, c := range candidates {
		if !k[c] {
			k[c] = true
			return c
		}
	}
	key := candidates[len(candidates)-1]
	for k[key] {
		key = "bucket " + key
	}
	k[key] = true
	return key
}

// compactBuckets is the tile of several buckets: the verdict, what is counted,
// a row for each bucket, and where it is last.
//
// The keys the page uses itself are taken whether or not this page has the row:
// whether `more` or `prefix` is there depends on how many buckets the account
// has and on the filter, and a reader should not have to tell a bucket's row
// from the page's by which of them happened to be there.
func compactBuckets(req plugin.Request, scope readScope, reads []bucketRead, prefix string) view.KeyValue {
	pairs := []view.Pair{
		{Key: "status", Value: listVerdict(reads, scope)},
		{Key: "buckets", Value: bucketsRow(scope)},
	}
	if prefix != "" {
		pairs = append(pairs, view.Pair{Key: "prefix", Value: plugin.ListedName(prefix)})
	}
	room := rowsInATile - len(pairs) - 1
	shown, folded := reads, []bucketRead(nil)
	if len(reads) > room {
		shown, folded = reads[:room-1], reads[room-1:]
	}
	keys := keyset{"status": true, "buckets": true, "prefix": true, "more": true, "address": true}
	for _, r := range shown {
		value, name := bucketValue(r), plugin.ListedName(r.name)
		// The name cut to the room the row has, and in full when another bucket
		// was cut to the same thing: a name too cut to tell from another is no
		// name, and the row is long instead (fitName).
		key := keys.claim(fitName(name, tileWidth-2-utf8.RuneCountInString(value)), name)
		pairs = append(pairs, view.Pair{Key: key, Value: value})
	}
	if len(folded) > 0 {
		pairs = append(pairs, foldedRow(folded))
	}
	return view.KeyValue{Pairs: append(pairs, view.Pair{Key: "address", Value: req.Reached(req.String("address"))})}
}

// bucketValue is the row of one bucket: what could not be read of it, or how
// many objects it holds and when the newest was written. A full first page is
// "1000+", and its newest is the newest of those read, which a bucket whose
// keys are dates, listed oldest first, makes a very different thing from the
// bucket's own: the row says it is of the page.
func bucketValue(r bucketRead) string {
	if r.unread != nil {
		return unreadText(unreadWhy(r.unread))
	}
	p := r.page
	count := format.CountOf(p.objects, "object")
	if p.partial {
		count = strconv.Itoa(p.objects) + "+ objects"
	}
	if p.objects == 0 {
		if p.partial {
			count += " (first page)"
		}
		return count
	}
	when := " · newest " + format.Ago(p.newest)
	if p.partial {
		when += " (first page)"
	}
	return count + when
}

// foldedRow is what does not fit a tile, as one row: how many buckets, and that
// they are all well, or how many are not. What is wrong sorts first, so a row
// that says some could not be read is only for a tile with more of them than it
// has rows.
func foldedRow(rest []bucketRead) view.Pair {
	unread := 0
	for _, r := range rest {
		if r.unread != nil {
			unread++
		}
	}
	state := "all ok"
	if unread > 0 {
		state = strconv.Itoa(unread) + " could not be read"
	}
	return view.Pair{Key: "more", Value: format.CountOf(len(rest), "bucket") + ", " + state}
}

// fitName cuts a name to the room it has, with an ellipsis, so that the row it is
// the key of stays on a line. Never to less than minNameWidth: a name too cut to
// tell from another is no name, and the row is long instead.
func fitName(name string, room int) string {
	room = max(room, minNameWidth)
	if utf8.RuneCountInString(name) <= room {
		return name
	}
	return string([]rune(name)[:room-1]) + "…"
}

// sortByStaleness puts the buckets worst first, which for a tile is the order in
// which it is cut from the bottom: what could not be read, what holds nothing,
// then the one written longest ago. A backup bucket whose newest object is a
// week old is a backup that has stopped, and it is the row a glance should find.
// By name within them, so that the same account is the same page.
func sortByStaleness(reads []bucketRead) {
	rank := func(r bucketRead) int {
		switch {
		case r.unread != nil:
			return 0
		case r.page.objects == 0 && !r.page.partial:
			return 1
		}
		return 2
	}
	sort.SliceStable(reads, func(i, j int) bool {
		a, b := reads[i], reads[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		if !a.page.newest.Equal(b.page.newest) {
			return a.page.newest.Before(b.page.newest)
		}
		return a.name < b.name
	})
}

// sortByTrouble is the detail's order: what could not be read, then what had a
// row that could not be, then by name. The detail does not read the first page
// the tile does, and what is written when is not what it sorts by.
func sortByTrouble(reads []bucketRead) {
	rank := func(r bucketRead) int {
		switch {
		case r.unread != nil:
			return 0
		case len(r.found) > 0:
			return 1
		}
		return 2
	}
	sort.SliceStable(reads, func(i, j int) bool {
		a, b := reads[i], reads[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		return a.name < b.name
	})
}

// detailedBuckets is the page of several buckets with the detail of each: the
// status block, then a section for each bucket with what the page for that
// bucket alone would say of it. A bucket that could not be read is a section
// that says so, and the page is said to be partial when it read part of an
// account.
func detailedBuckets(ctx context.Context, req plugin.Request, scope readScope, reads []bucketRead) view.View {
	p := plugin.NewPage(ctx, req)
	p.Put("status", view.KeyValue{Pairs: []view.Pair{
		{Key: "status", Value: listVerdict(reads, scope)},
		{Key: "buckets", Value: bucketsRow(scope)},
		{Key: "address", Value: req.Reached(req.String("address"))},
	}})
	titles := keyset{"status": true}
	for _, r := range reads {
		section := view.KeyValue{Pairs: r.rows}
		if r.unread != nil {
			section.Pairs = []view.Pair{{Key: "status", Value: unreadText(unreadWhy(r.unread))}}
		}
		p.Put(titles.claim(plugin.ListedName(r.name)), section)
	}
	if scope.capped() {
		p.Warn(&view.Error{
			Code:    "s3.buckets.partial",
			Message: fmt.Sprintf("read %d of %d buckets, the first by name", len(scope.targets), scope.total),
			Hint:    nextCall(req, "s3.bucket.list") + " lists every one",
		})
	}
	return p.View()
}
