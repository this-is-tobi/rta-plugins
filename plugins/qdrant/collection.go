package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Everything in this file is the read tier: it describes collections and
// returns not one point. See the package comment for why that line is drawn
// where it is — the short version is that a vector is not a hash.

type collectionsResponse struct {
	Collections []struct {
		Name string `json:"name"`
	} `json:"collections"`
}

type collectionInfo struct {
	Status              string `json:"status"`
	OptimizerStatus     any    `json:"optimizer_status"`
	PointsCount         *int64 `json:"points_count"`
	IndexedVectorsCount *int64 `json:"indexed_vectors_count"`
	SegmentsCount       int64  `json:"segments_count"`
	Config              struct {
		Params struct {
			// Qdrant answers this two ways: a single unnamed vector config, or
			// a map of named ones. Decoded as `any` and sorted out below,
			// because a struct that only fits one shape silently reports
			// nothing for the other.
			Vectors           any   `json:"vectors"`
			ShardNumber       int64 `json:"shard_number"`
			ReplicationFactor int64 `json:"replication_factor"`
			OnDiskPayload     bool  `json:"on_disk_payload"`
		} `json:"params"`
	} `json:"config"`
}

func collectionField(help string) plugin.Field {
	return plugin.Field{Name: "collection", Short: "c", Type: plugin.String, Required: true, Config: "collection",
		Help: help, Live: true, Suggest: suggestCollections}
}

// collectionsField is `collection` for a listing: the collections it is about, a
// list, and every collection the key can see, inside the profile's fence when it
// has one, when none is named.
//
// It is a list so that a profile's `only: collection:` has something to hold the
// listing to, whatever the fence holds. A connection fenced to its application's
// collection answered the overview and the collection list with every collection
// of the instance, names and point counts the fence was written to keep out, and
// declared with the fence's own Config key a one-entry list fills it and a call
// that names another is refused. A fence of several is the connection of a tile
// that names several, which a single name could not be handed: the list read as
// text that is in no entry, and a call naming none was refused as blank. The host
// holds each element to the fence. The same shape as plugins/kube's namespace.
//
// **Completed a name at a time, on a press of the key.** Live, because the Suggest
// asks the instance: a read of it must be something a press asked for and never
// something typing caused, and a plain Suggest would ask on every keystroke. The
// host hands suggestCollections the name being typed, takes out of the answer the
// collections the box already lists, holds the rest to the profile's fence and
// writes an accepted one with the double quotes a comma in it needs, so none of
// that is tracked here. The collections that act on one stay a String and keep
// their own completion.
//
// **The help says how a name with a comma is written, because the flag splits on
// it.** Qdrant allows a comma in a collection's name, and a value of the flag is
// cut at each one: --collection a,b is two collections, the same as the flag
// given twice, and a name that has a comma is written in double quotes,
// --collection '"a,b"' (the shell's quotes are the outer pair; a backslash or
// single quotes do not keep it whole). A list that is not a flag (a tile's with:,
// a profile, an array over MCP) is not cut.
func collectionsField() plugin.Field {
	return plugin.Field{Name: "collection", Short: "c", Type: plugin.StringSlice, Config: "collection",
		Help: "only these; none reads all the key can see, inside the profile's fence when it has one. " +
			"As a flag, repeat it and double-quote a name with a comma",
		Live: true, Suggest: suggestCollections}
}

// maxNamedCollections is the most collections a call that names some reads. Each
// is a request, and a list longer than this is a typo or a connection that should
// be two tiles, not a refresh to start quietly at the instance somebody's search
// depends on. It is what the overview reads of an instance's own, so naming a
// hundred is no more than naming none.
const maxNamedCollections = overviewCollections

// readsAtOnce is how many named collections are in flight at once: enough that a
// hundred are not a hundred round trips in a line, few enough that a tile is not
// a burst against an instance that is somebody's search.
const readsAtOnce = 6

// namedCollections is the collections a call names, once each and in name order:
// an empty name is no name and a name given twice is read once, so the same page
// is the same whichever way the list was written and a list of one is the name it
// holds. A name is used as it was given, with no trimming: it is the name the
// host held to the profile's fence, and one cleaned up afterwards could be a
// collection outside it.
//
// A list longer than maxNamedCollections is refused here, before the instance is
// asked anything, and the refusal never says how many the call reaches nor that
// somebody named them. The list may be the profile's fence filled in by the host,
// and the host withholds a fence's contents from an agent: what the refusal says
// is the cap, and that this call is over it. who is the listing that refuses.
func namedCollections(req plugin.Request, who string) ([]string, *view.Error) {
	var names []string
	for _, name := range req.StringSlice("collection") {
		if name != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) > maxNamedCollections {
		hint := "name fewer collections, or ask the operator to fence fewer"
		if sf := req.Surface(); sf != plugin.SurfaceMCP {
			hint = "name fewer with " + sf.InputName("collection") + ", or fence fewer in the profile or the tile that names them"
		}
		return nil, view.Errorf("qdrant.collection.toomany", "%s reads at most %d collections, and this call reaches more",
			who, maxNamedCollections).WithHint(hint)
	}
	return names, nil
}

// suggestCollections lists the collections the key can see, by name.
//
// It serves the collection of the capabilities that act on one and the list of the
// two listings alike, and reads the box of neither: on a list the request carries
// the name being typed under `collection`, and the answer is every collection the
// instance has, for the box to match against what is typed. A name with a comma is
// given as it is; the host writes it in double quotes when it is accepted. The
// instance is asked whole and the fence holds the answer, not the call.
//
// The answer is not capped. The text typed is never sent to the instance: the host
// narrows what the box offers by it after this has answered, so a cap here would cut
// the collections by their place in the list, and no typing could bring a cut one back.
func suggestCollections(ctx context.Context, req plugin.Request) []string {
	var out collectionsResponse
	if verr := get(ctx, req, "/collections", &out); verr != nil {
		// No suggestions rather than an error: there is nowhere to show one at
		// a completion prompt, and a half-typed flag is not where somebody
		// should learn the instance is unreachable.
		return nil
	}
	names := make([]string, 0, len(out.Collections))
	for _, c := range out.Collections {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}

// overviewCollections is how many collections the overview reads, one request
// each, and no more: a collection's status is only known by asking for it, so
// the cost of the page is the number of them, and an instance of a thousand
// tenants made a thousand and three requests a run, seven seconds and 195 KB at
// three milliseconds each, on a tile that re-ran it at the host's pace.
// qdrant.collection.list is the report that reads every one.
const overviewCollections = 100

// overviewRefresh is how often a dashboard tile of qdrant.overview is re-run,
// and the pace a configuration cannot shorten: a run is three requests and one
// for each collection it shows, to an instance somebody's search depends on.
const overviewRefresh = time.Minute

func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "qdrant.overview",
		Summary:    "What this instance is and what it holds",
		Keywords:   []string{"summary", "version", "health", "status"},
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Refresh:    overviewRefresh,
		Description: "Whether this instance is healthy, in a few rows, worst first. `status` leads and is " +
			"`ok`, or `warn` or `fail` followed by the worst fact: a collection that is red, a peer that " +
			"does not answer, a part of the instance this key cannot read. Then the collections " +
			"counted by status, worst first, the cluster, and last the version.\n\n" +
			"A collection's status is only known by reading it, a request each, so a call reads at " +
			"most 100 and the row says how many of how many (qdrant.collection.list reads every one). " +
			"`yellow` is a collection serving searches from a partly-built index: its results are " +
			"quietly incomplete, not absent.\n\n" +
			"`collection` is a list: a single name reads that collection alone, none reads every " +
			"collection the key can see, inside the profile's fence when it has one, and several " +
			"read exactly those.\n\n" +
			"`detail` adds the address and the table of the collections read: points, indexed " +
			"vectors, segments, status. Against a distributed instance it also lists the peers and " +
			"adds a replicas column, a request for each collection. This peer's row has its raft term, " +
			"commit index and pending operations and a grade of its consensus thread and its leader. " +
			"Every other peer's row has the version, role, term and commit it reported to this one, " +
			"whichever peer is asked, and is red when it does not answer; a cluster on more than one " +
			"version says so. A server too old for that grades a peer by whether this one can message " +
			"it, which only the leader does for every peer: ask the leader. The replicas column says " +
			"how many of a collection's replicas serve and which shards on which peer do not: Dead is " +
			"red, Recovery, Partial and the other states of one being brought level are amber, and a " +
			"peer known not to answer has its replicas graded amber, or red when it was the shard's " +
			"only copy. The cluster's own state needs global read access, which the read-only API key " +
			"has and a JWT scoped to collections does not.\n\n" +
			"Describes collections and returns no point; reading points is qdrant.points.scroll, a write.",
		Agent: "Whether the instance is healthy, worst first: `status` is `ok`, or `warn` or `fail` and the " +
			"worst fact (a red collection, a peer that does not answer, a part this key cannot read). " +
			"Then the collections by status, the cluster and the version. `collection` is an array: " +
			"none reads every collection the key can see, inside the profile's fence when it has one, " +
			"some read exactly those, at most 100 in all. One not there is a `not found` row, one " +
			"it cannot read is named in the verdict. `yellow` is a partly built index, so results are " +
			"quietly incomplete. `detail` adds the collections' points, indexed vectors and segments, " +
			"and on a distributed instance the peers and each collection's replicas. Ask the leader: a " +
			"follower cannot see every peer. Returns no point.",
		Run: runOverview,
	}, collectionsField())
}

func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
	named, verr := namedCollections(req, "an overview")
	if verr != nil {
		return nil, verr
	}
	ctx = boundedTo(ctx, overviewTimeout)
	var root struct {
		Title   string `json:"title"`
		Version string `json:"version"`
	}
	// The root endpoint is unwrapped — it is the one response with no
	// "result" envelope — so it is decoded straight rather than through get.
	if verr := getRaw(ctx, req, "/", &root); verr != nil {
		return nil, verr
	}

	cs, verr := fetchCluster(ctx, req)
	if verr != nil {
		return nil, verr
	}
	detail := req.Bool("detail")
	table, unread, verr := collectionTableIn(ctx, req, cs, named, overviewCollections, detail)
	unlisted := ""
	if verr != nil {
		// The credential is valid and may not list the collections: the version
		// and the cluster were read with it, and a row that says so is the answer,
		// not an error that discards them. A collection named is the finding when
		// it cannot be read, and every other failure would fail the next call too.
		if verr.Code != "qdrant.denied" || len(named) != 0 {
			return nil, verr
		}
		unlisted = "this credential may not list the collections"
	}
	summary := compactOverview(root.Version, cs, table, unread, unlisted, time.Now())
	if !detail {
		return summary, nil
	}

	collections := strconv.Itoa(len(table.Rows))
	switch {
	case unlisted != "":
		collections = unreadText(unlisted)
	case table.Total > len(table.Rows):
		collections = collections + " of " + strconv.Itoa(table.Total) + " — the first " +
			strconv.Itoa(len(table.Rows)) + " by name; " + req.Surface().CapabilityName("qdrant.collection.list") + " reads every one"
	}

	p := plugin.NewPage(ctx, req)
	p.Put("summary", summary)
	p.Put("status", view.KeyValue{Pairs: []view.Pair{
		{Key: "address", Value: req.Reached(req.String("address"))},
		{Key: "title", Value: root.Title},
		{Key: "version", Value: root.Version},
		{Key: "cluster", Value: cs.modeText()},
		{Key: "collections", Value: collections},
	}})
	if cs.mode == modeDistributed {
		p.Put("peers", peersTable(cs, time.Now()))
	}
	if unlisted == "" {
		p.Put("collections", table)
	}
	return p.View(), nil
}

// grade is how bad a fact is, in the words the status row leads with and the
// theme colours: nothing, warn, fail.
type grade int

const (
	_ grade = iota
	gradeWarn
	gradeFail
)

// finding is one thing wrong, in a few words, for the status row.
type finding struct {
	grade grade
	text  string
}

// verdict is the status row: ok, or the worst finding with its grade. Of
// several equally bad, the first stands, and the findings are collected in the
// order that puts the cluster before the collections.
func verdict(found []finding) string {
	var worst *finding
	for i := range found {
		if worst == nil || found[i].grade > worst.grade {
			worst = &found[i]
		}
	}
	switch {
	case worst == nil:
		return "ok"
	case worst.grade == gradeFail:
		return "fail — " + worst.text
	}
	return "warn — " + worst.text
}

func unreadText(why string) string { return "could not be read — " + why }

// compactOverview is the answer a tile and an agent read by default: the verdict,
// the collections counted by status, the cluster, and the version last, which is
// what a healthy instance and a dying one have in common. A tile is cut from the
// bottom, and the page this replaces opened with the address and a title that is
// the same on every Qdrant there is.
//
// **It reads no replica.** The replicas column is a request for every collection
// and is the detail's; the compact form's collection requests are the ones that
// give each its status, which is what the row counts. unread are the collections
// among them that could not be read, which the verdict names, and unlisted is why
// the list of collections could not be read, when it could not.
func compactOverview(version string, cs clusterState, table view.Table, unread []unreadCollection, unlisted string, now time.Time) view.KeyValue {
	cluster, clusterFound := clusterRow(cs, now)
	collections, collectionFound := collectionsRow(table, unread)
	if unlisted != "" {
		collections, collectionFound = unreadText(unlisted), []finding{{gradeWarn, "collections could not be read"}}
	}
	return view.KeyValue{Pairs: []view.Pair{
		{Key: "status", Value: verdict(append(clusterFound, collectionFound...))},
		{Key: "collections", Value: collections},
		{Key: "cluster", Value: cluster},
		{Key: "version", Value: version},
	}}
}

// statusOrder is the order a count of collections by status is spelled in: the
// worst first, so that a row a tile clips at the right still shows what is wrong.
func statusOrder(status string) int {
	switch status {
	case "red":
		return 0
	case "yellow":
		return 1
	case "grey":
		return 2
	case statusUnreadable:
		return 3
	case "green":
		return 5
	}
	return 4
}

// collectionsRow counts the collections read by status, worst first, and grades
// them: red is a failure, and anything else that is not green is a warning,
// because a collection serving from a partly built index, or one the key cannot
// read, is not a collection that is known to be well.
//
// **Past the bound the count is of the ones read, and says so.** The status of a
// collection is only known by reading it; the verdict is about those and the row
// beside it says how many they are of how many there are.
//
// **A collection that could not be read is named, and the row says why.** A tile
// shows the compact form alone, and "1 collection could not be read" left its
// reader to run the detail to find out which: the verdict names up to two and
// then how many more, and the count in the row carries the reasons in a few words.
func collectionsRow(table view.Table, unread []unreadCollection) (string, []finding) {
	read := len(table.Rows)
	if read == 0 {
		return "none", nil
	}
	counts, first := map[string]int{}, map[string]string{}
	for _, row := range table.Rows {
		status := row[4]
		if status == "" {
			status = "unreported"
		}
		if counts[status] == 0 {
			first[status] = row[0]
		}
		counts[status]++
	}
	statuses := make([]string, 0, len(counts))
	for status := range counts {
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(a, b int) bool {
		if oa, ob := statusOrder(statuses[a]), statusOrder(statuses[b]); oa != ob {
			return oa < ob
		}
		return statuses[a] < statuses[b]
	})
	label := func(status string) string {
		if status == statusUnreadable {
			return status + " (" + reasonsOf(unread) + ")"
		}
		return status
	}

	text := strconv.Itoa(read)
	if table.Total > read {
		text += " of " + strconv.Itoa(table.Total) + " read"
	}
	var found []finding
	if len(statuses) == 1 {
		text += ", all " + label(statuses[0])
		if read == 1 {
			text = strings.Replace(text, ", all ", ", ", 1)
		}
	}
	for _, status := range statuses {
		if len(statuses) > 1 {
			text += ", " + strconv.Itoa(counts[status]) + " " + label(status)
		}
		switch status {
		case "green":
		case "red":
			found = append(found, finding{gradeFail, collectionFinding(counts[status], status, first[status])})
		case statusUnreadable:
			found = append(found, finding{gradeWarn, unreadFinding(unread)})
		default:
			found = append(found, finding{gradeWarn, collectionFinding(counts[status], status, first[status])})
		}
	}
	return text, found
}

func collectionFinding(n int, status, first string) string {
	if n == 1 {
		return first + " is " + status
	}
	return format.CountOf(n, "collection") + " " + status + ", first " + first
}

// statusUnreadable is the status of a row for a collection that was asked for and
// could not be read: not one Qdrant reports, and the one a tile counts.
const statusUnreadable = "unreadable"

// unreadCollection is a collection that was asked for and could not be read: its
// name as it is shown, and why in a few words, which is what a tile has room for.
type unreadCollection struct{ name, why string }

// whyUnread is a failed read said in a few words. The whole of it, the
// instance's address and what it answered, is the detail's warning.
func whyUnread(verr *view.Error) string {
	switch verr.Code {
	case "qdrant.denied":
		return "denied"
	case "qdrant.auth.failed":
		return "key rejected"
	case "qdrant.notfound":
		return "not found"
	case "qdrant.timeout":
		return "timed out"
	case "qdrant.unavailable":
		return "not serving"
	case "qdrant.ratelimited":
		return "rate limited"
	case "qdrant.peer.unreachable":
		return "peer not answering"
	case "qdrant.request.failed":
		return "request failed"
	}
	if strings.HasPrefix(verr.Code, "qdrant.conn.") {
		return "connection failed"
	}
	return "failed"
}

// unreadFinding is the verdict's fact for collections that could not be read:
// the first two by name and how many more, so that a tile says which.
func unreadFinding(unread []unreadCollection) string {
	shown := min(len(unread), 2)
	names := make([]string, shown)
	for i := range shown {
		names[i] = unread[i].name
	}
	text := strings.Join(names, ", ")
	if more := len(unread) - shown; more > 0 {
		text += " +" + strconv.Itoa(more)
	}
	return text + " could not be read"
}

// reasonsOf is why the collections could not be read, each reason once, in the
// order the collections are in: two at most and how many more, because a row of a
// tile has the room for no more.
func reasonsOf(unread []unreadCollection) string {
	var why []string
	for _, u := range unread {
		if !slices.Contains(why, u.why) {
			why = append(why, u.why)
		}
	}
	if len(why) > 2 {
		return strings.Join(why[:2], ", ") + " +" + strconv.Itoa(len(why)-2)
	}
	return strings.Join(why, ", ")
}

// clusterRow is whether the instance stands alone or how its peers are, and what
// is wrong among them: this peer's consensus thread, and each peer the one asked
// cannot message, which it names. It reads nothing more than the cluster state
// the page already holds.
func clusterRow(cs clusterState, now time.Time) (string, []finding) {
	switch cs.mode {
	case modeStandalone:
		return "standalone", nil
	case modeDistributed:
		return distributedRow(cs, now)
	}
	if cs.note != "" {
		return unreadText("this credential reads collections only"), []finding{{gradeWarn, "cluster state could not be read"}}
	}
	return "not reported by this server", nil
}

func distributedRow(cs clusterState, now time.Time) (string, []finding) {
	role := strings.ToLower(cs.info.RaftInfo.Role)
	if role == "" {
		role = "a member"
	}
	text := format.CountOf(len(cs.info.Peers), "peer") + ", this one is " + role

	var found []finding
	if state, why, _ := strings.Cut(cs.selfHealth(), " — "); state == "fail" || state == "warn" {
		found = append(found, finding{gradeOf(state), "this peer: " + why})
	}
	ids := make([]uint64, 0, len(cs.info.Peers))
	for id := range cs.info.Peers {
		if n, err := strconv.ParseUint(id, 10, 64); err == nil && n != cs.info.PeerID {
			ids = append(ids, n)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return cs.label(ids[i]) < cs.label(ids[j]) })
	var down []string
	for _, id := range ids {
		uri := cs.info.Peers[strconv.FormatUint(id, 10)].URI
		switch state, _, _ := strings.Cut(cs.peerHealth(id, uri, now), " — "); state {
		case "fail":
			down = append(down, cs.label(id))
			found = append(found, finding{gradeFail, "peer " + cs.label(id) + " is not answering"})
		case "warn":
			found = append(found, finding{gradeWarn, "peer " + cs.label(id) + " has failed messages"})
		}
	}
	switch {
	case len(down) == 1:
		text += ", " + down[0] + " not answering"
	case len(down) > 1:
		text += ", " + strconv.Itoa(len(down)) + " peers not answering"
	}
	if v := cs.versions(); len(v) > 1 {
		text += ", on " + strconv.Itoa(len(v)) + " versions"
	}
	return text, found
}

func gradeOf(state string) grade {
	if state == "fail" {
		return gradeFail
	}
	return gradeWarn
}

func collectionListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "qdrant.collection.list",
		Summary:    "Every collection, with its size and index status",
		Keywords:   []string{"indexes", "vectors", "embeddings", "catalog"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Names, point counts, how many of those vectors are actually indexed, and " +
			"each collection's status.\n\n" +
			"Indexed against total is the number worth watching: a collection still building its " +
			"index answers searches from what it has, so the gap between those two columns is " +
			"how incomplete the answers currently are.\n\n" +
			"In a cluster a replicas column says whether every replica of the collection is " +
			"serving, as the overview does.\n\n" +
			"`collection` is a list: a single name reads that collection alone, none reads every " +
			"collection the key can see, inside the profile's fence when it has one, and several read " +
			"exactly those, at most 100, a request each and without listing the instance. One that is " +
			"not there is a `not found` row, and one the key may not read is a row with the reason in " +
			"a warning. A list goes in a tile's `with:`, in the call or in a profile's `only:` fence; " +
			"in the config or a profile's `set:` it stays one name, because the other capabilities " +
			"that share the key take text.\n\n" +
			"Names and counts only, never a point.",
		Agent: "Each collection's points, indexed vectors and status, and in a cluster whether its replicas " +
			"serve. `collection` is an array: none reads every collection the key can see, inside the " +
			"profile's fence when it has one, some read exactly those, at most 100, and one not there is " +
			"a `not found` row. Indexed below points is a collection still building its index, which " +
			"answers from what it has. Returns no point.",
		// Not `return collectionTable(ctx, req)`, which reads the same and
		// is not: the *view.Error comes back inside Run's error interface,
		// where a nil pointer is a non-nil error, and every listing that
		// worked reached the host as a failure with no code and no message.
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			table, verr := collectionTable(ctx, req)
			if verr != nil {
				return nil, verr
			}
			return table, nil
		},
	}, collectionsField())
}

func collectionTable(ctx context.Context, req plugin.Request) (view.Table, *view.Error) {
	named, verr := namedCollections(req, "a collection list given a list")
	if verr != nil {
		return view.Table{}, verr
	}
	cs, verr := fetchCluster(ctx, req)
	if verr != nil {
		return view.Table{}, verr
	}
	table, _, verr := collectionTableIn(ctx, req, cs, named, 0, true)
	return table, verr
}

// collectionTableIn lists the collections, and in a cluster adds the column
// that says whether every replica of each is serving. The collections among them
// that could not be read come back by name beside the table, for a verdict that
// names them.
//
// **The column is there when it can mean something.** A standalone instance
// has one copy of everything and nothing to say; a distributed one gets it
// whether or not this credential could read the cluster's own state, because
// the per-collection endpoint answers a token scoped to collections where
// /cluster does not, and a collection that turns out to be unsharded in a
// mode nobody could name reads "-" rather than inventing a replica count. It is
// a request for every collection on top of the one that gives its status, so a
// caller that does not show the column (the overview's compact form) does not
// ask for it.
//
// **With collections named, the page is about them and the instance's listing is
// never asked for.** A fence on `collection` is written to keep the other names
// out of a connection's answers, and the listing is where they are; a key
// scoped to some collections may not be allowed it either. Of one name, the
// collection that is not there is the finding, as the bucket is in plugins/s3's
// overview, and not a row of "unreadable" that reads as one the key may not see.
// Of several, a page about the rest is still the answer: a name that is not
// there is a row that says "not found", which the verdict counts as a warning,
// and one that could not be read, for whatever reason, is an "unreadable" row
// with a warning that names it and says why. The warning is the named ones'
// only: the collections of a blank call are the instance's to name, and the
// rows say which they are.
//
// **Several named are read six at a time, each held to the overview's bound and
// all of them to one deadline.** A request each, and a list of a hundred read one
// after another is a hundred round trips in a line. The bound of a request is the
// overview's, whichever listing asks: a collection's description is not a count
// over a large one, which is what the longer bound of the other calls is for. The
// deadline is twice that bound for all of them together, so that a request that
// fails slowly is a row and the page goes on, and an instance that answers none
// is given up on once and not once for each round. What is not read by then is a
// row that says it did not answer in time. The collections of a call that names
// none, or one, are read as they were: in name order, one after another.
//
// **With a limit, only that many are read, the first by name, and the table says
// how many there are.** Each collection is a request, and its status is only
// known by making it, so the ones to read cannot be chosen by their health: the
// worst of those read are put first, which is all the order can promise. The cut
// is Total, which a program reads, and a warning, which a person does.
func collectionTableIn(ctx context.Context, req plugin.Request, cs clusterState, named []string, limit int, replicas bool) (view.Table, []unreadCollection, *view.Error) {
	names := named
	if len(named) == 0 {
		var list collectionsResponse
		if verr := get(ctx, req, "/collections", &list); verr != nil {
			return view.Table{}, nil, verr
		}
		names = make([]string, 0, len(list.Collections))
		for _, c := range list.Collections {
			names = append(names, c.Name)
		}
		// Qdrant does not promise an order, and a listing that reshuffles between
		// calls cannot be diffed.
		sort.Strings(names)
	}
	total := len(names)
	if limit > 0 && total > limit {
		names = names[:limit]
	}

	t := view.Table{Columns: []view.Column{
		{Name: "Collection"},
		{Name: "Points", Kind: view.KindNumber},
		{Name: "Indexed", Kind: view.KindNumber},
		{Name: "Segments", Kind: view.KindNumber},
		{Name: "Status", Kind: view.KindStatus},
	}}
	atOnce := 1
	if len(named) > 1 {
		atOnce = readsAtOnce
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(boundedTo(ctx, overviewTimeout), 2*overviewTimeout)
		defer cancel()
	}
	reads := make([]collectionRead, len(names))
	eachAtOnce(len(names), atOnce, func(i int) { reads[i] = readCollection(ctx, req, cs, names[i], replicas) })

	read := make([]collectionRow, 0, len(names))
	var unread []unreadCollection
	var warnings []view.Error
	anyDistributed := false
	for i, name := range names {
		r := reads[i]
		listed := plugin.ListedName(name)
		anyDistributed = anyDistributed || r.distributed
		if r.err != nil {
			if len(named) == 1 {
				return view.Table{}, nil, r.err
			}
			// Reported, not fatal. A collection this key may not see is the
			// normal case on a shared instance, and ending the listing at the
			// first one would turn "here is what you can see" into nothing.
			status := statusUnreadable
			switch {
			case len(named) > 1 && r.err.Code == "qdrant.notfound":
				status = "not found"
			default:
				unread = append(unread, unreadCollection{listed, whyUnread(r.err)})
				if len(named) > 1 {
					// Without the error's hint: it is written for the call that failed
					// whole, and sends a reader to the cluster's access.
					warnings = append(warnings, view.Error{
						Code:    "qdrant.collection.unreadable",
						Message: listed + " could not be read — " + r.err.Message,
					})
				}
			}
			read = append(read, collectionRow{[]string{listed, "-", "-", "-", status}, r.replica})
			continue
		}
		read = append(read, collectionRow{[]string{
			listed,
			countText(r.info.PointsCount),
			countText(r.info.IndexedVectorsCount),
			strconv.FormatInt(r.info.SegmentsCount, 10),
			r.info.Status,
		}, r.replica})
	}
	if limit > 0 {
		sort.SliceStable(read, func(a, b int) bool { return statusRank(read[a].cells[4]) < statusRank(read[b].cells[4]) })
	}
	if anyDistributed {
		t.Columns = append(t.Columns, view.Column{Name: "Replicas", Kind: view.KindStatus})
	}
	for _, r := range read {
		if anyDistributed {
			r.cells = append(r.cells, r.replica)
		}
		t.Rows = append(t.Rows, r.cells)
	}
	t.Total = total
	t.Warnings = append(t.Warnings, warnings...)
	if total > len(t.Rows) {
		t.Warnings = append(t.Warnings, view.Error{
			Code:    "qdrant.collections.partial",
			Message: "read " + strconv.Itoa(len(t.Rows)) + " of " + strconv.Itoa(total) + " collections, the first by name",
			Hint:    nextCall(req, "qdrant.collection.list") + " reads every one",
		})
	}
	t.Empty = "no collections"
	return t, unread, nil
}

// collectionRead is what one collection came to: its description, or why there is
// none, and the replicas cell that goes with it.
type collectionRead struct {
	info        collectionInfo
	err         *view.Error
	replica     string
	distributed bool
}

// readCollection asks for a collection's replicas, in a cluster and when they are
// wanted, and then for the collection. Each result is its own: nothing here
// writes anywhere another read could see.
func readCollection(ctx context.Context, req plugin.Request, cs clusterState, name string, replicas bool) collectionRead {
	r := collectionRead{replica: "-"}
	if replicas && cs.mode != modeStandalone {
		var cc collectionCluster
		if verr := get(ctx, req, pathFor("/collections/%s/cluster", name), &cc); verr == nil && (cs.mode == modeDistributed || cc.distributed()) {
			r.replica = replicasHealth(cc, cs.label, cs.peersDown(time.Now()))
			r.distributed = true
		}
	}
	r.err = get(ctx, req, pathFor("/collections/%s", name), &r.info)
	return r
}

// eachAtOnce runs read for each of n, at most atOnce at a time, and returns when
// all have. The gate is taken before the goroutine is started and not inside it,
// so that a hundred collections are never a hundred goroutines waiting, and with
// one at a time they run in order.
func eachAtOnce(n, atOnce int, read func(i int)) {
	gate := make(chan struct{}, atOnce)
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

// collectionRow is a row of the table and the replicas cell that follows it
// when the instance is distributed, kept together so that ordering the rows
// cannot part them.
type collectionRow struct {
	cells   []string
	replica string
}

// statusRank orders a collection's status for a page that can show only some:
// red first, then everything that is neither red nor green (yellow, grey, a
// collection the key cannot read, one that reported nothing), then green.
func statusRank(status string) int {
	switch status {
	case "red":
		return 0
	case "green":
		return 2
	}
	return 1
}

// countText distinguishes "none" from "not reported". Qdrant omits these
// fields entirely on a collection it has not finished loading, and rendering
// that as 0 would say the collection is empty when it is merely not ready.
func countText(n *int64) string {
	if n == nil {
		return "not reported"
	}
	return strconv.FormatInt(*n, 10)
}

func collectionShowCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:       "qdrant.collection.show",
		Summary:  "How one collection is configured, and whether its index is built",
		Keywords: []string{"schema", "config", "hnsw", "dimensions", "distance", "quantization"},
		Examples: []plugin.Example{
			{Title: "how one collection is set up", Inputs: map[string]any{"collection": "docs"}},
		},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Vector dimensions and distance metric, sharding and replication, payload " +
			"storage, and index progress.\n\n" +
			"The dimension and the distance metric are the two that make a collection " +
			"incompatible with a model: embedding with something that produces a different " +
			"dimension fails loudly, and embedding with a model trained for a different metric " +
			"fails silently, returning plausible and wrong neighbours.\n\n" +
			"For a collection spread over several peers it adds where every replica of every " +
			"shard is and what state it is in, and any shard transfer under way. The point " +
			"count is the replica on this peer's alone: another peer's is its own to report, " +
			"and the state alone does not move until a write fails on that copy.\n\n" +
			"Configuration only, never a point — this describes the shape of the data and " +
			"returns none of it.",
		Agent: "Vector dimensions and distance metric, sharding and replication, payload storage and index " +
			"progress. The dimension and metric decide whether a model's embeddings fit: a different " +
			"dimension fails loudly, a model trained for a different metric fails silently with plausible " +
			"and wrong neighbours. For a collection over several peers it adds where every replica of every " +
			"shard is and its state, and any shard transfer under way; the point count is this peer's " +
			"replica alone. Configuration only, never a point.",
		Run: runCollectionShow,
	}, collectionField("collection to describe"))
}

func runCollectionShow(ctx context.Context, req plugin.Request) (view.View, error) {
	name := req.String("collection")
	var info collectionInfo
	if verr := get(ctx, req, pathFor("/collections/%s", name), &info); verr != nil {
		return nil, verr
	}

	pairs := []view.Pair{
		{Key: "collection", Value: plugin.ListedName(name)},
		{Key: "status", Value: info.Status},
		{Key: "points", Value: countText(info.PointsCount)},
		{Key: "indexed vectors", Value: countText(info.IndexedVectorsCount)},
		{Key: "segments", Value: strconv.FormatInt(info.SegmentsCount, 10)},
		{Key: "shards", Value: strconv.FormatInt(info.Config.Params.ShardNumber, 10)},
		{Key: "replication factor", Value: strconv.FormatInt(info.Config.Params.ReplicationFactor, 10)},
		{Key: "payload on disk", Value: strconv.FormatBool(info.Config.Params.OnDiskPayload)},
	}
	pairs = append(pairs, describeVectors(info.Config.Params.Vectors)...)
	if info.Status == "yellow" {
		pairs = append(pairs, view.Pair{
			Key: "note",
			Value: "yellow means the index is still building — searches answer from what is " +
				"indexed so far, so results are incomplete rather than delayed",
		})
	}

	// What the collection's own configuration does not say: where each shard's
	// replicas are and what state each is in. Beyond a collection that lives
	// on this peer alone there is nothing to add, and the page is the
	// configuration it always was; a failure here is not the page's failure.
	var cc collectionCluster
	if verr := get(ctx, req, pathFor("/collections/%s/cluster", name), &cc); verr == nil && cc.distributed() {
		return replicasPage(ctx, req, pairs, cc), nil
	}
	return view.KeyValue{Pairs: pairs}, nil
}

// replicasPage is the collection page with where its replicas are beside it.
func replicasPage(ctx context.Context, req plugin.Request, pairs []view.Pair, cc collectionCluster) view.View {
	cs, _ := fetchCluster(ctx, req)
	pairs = append(pairs, view.Pair{Key: "replicas", Value: replicasHealth(cc, cs.label, cs.peersDown(time.Now()))})
	p := plugin.NewPage(ctx, req)
	p.Put("collection", view.KeyValue{Pairs: pairs})
	p.Put("shards", shardsTable(cc, cs.label, cs.peersDown(time.Now())))
	if len(cc.ShardTransfers) > 0 {
		p.Put("transfers", transfersTable(cc, cs.label))
	}
	return p.View()
}

// describeVectors flattens Qdrant's two shapes into rows.
//
// A collection with one unnamed vector reports {"size": n, "distance": "..."}.
// A collection with named vectors reports {"name": {"size": ...}, ...}. Both
// are ordinary and a struct fitting one reports nothing for the other, which
// is why this walks the decoded map instead.
func describeVectors(v any) []view.Pair {
	switch shape := v.(type) {
	case map[string]any:
		// The single-vector shape has "size" holding a *number*. A named-
		// vector map can also have a "size" key — a vector somebody named
		// "size" — and checking only for the key's presence read that
		// collection as the other shape, describing the map itself as one
		// vector and dropping every named vector it holds.
		if v, single := shape["size"]; single {
			if _, named := v.(map[string]any); !named {
				return []view.Pair{{Key: "vectors", Value: vectorText("", shape)}}
			}
		}
		names := make([]string, 0, len(shape))
		for name := range shape {
			names = append(names, name)
		}
		sort.Strings(names)
		out := make([]view.Pair, 0, len(names))
		for _, name := range names {
			cfg, _ := shape[name].(map[string]any)
			out = append(out, view.Pair{Key: "vector " + plugin.ListedName(name), Value: vectorText(name, cfg)})
		}
		return out
	default:
		return []view.Pair{{Key: "vectors", Value: "not reported"}}
	}
}

func vectorText(_ string, cfg map[string]any) string {
	if cfg == nil {
		return "not reported"
	}
	size, distance := "?", "?"
	if n, ok := cfg["size"].(float64); ok {
		size = strconv.FormatInt(int64(n), 10)
	}
	if d, ok := cfg["distance"].(string); ok {
		distance = d
	}
	text := fmt.Sprintf("%s dimensions, %s distance", size, distance)
	if onDisk, ok := cfg["on_disk"].(bool); ok && onDisk {
		text += ", on disk"
	}
	return text
}
