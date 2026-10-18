package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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

// optionalCollectionField is collectionField for a listing that is about one
// collection when it is given one and about every collection the key sees when
// it is not.
//
// It exists so that a profile's `only: collection:` has something to hold the
// listing to: a connection fenced to its application's collection answered the
// overview and the collection list with every collection of the instance, names
// and point counts the fence was written to keep out. Declared with the fence's
// own Config key, a one-entry list fills it, and a call that names another
// collection is refused. The same shape as plugins/s3's optionalBucketField.
func optionalCollectionField(help string) plugin.Field {
	f := collectionField(help)
	f.Required = false
	return f
}

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
		Description: "Version, reachability and every collection with its point count and status.\n\n" +
			"The status column is the one to read. A collection in `yellow` is serving searches " +
			"from a partly-built index, so its results are quietly incomplete rather than absent.\n\n" +
			"Against a distributed instance it also lists the peers and adds a replicas column. " +
			"This peer's row has its raft term, commit index and pending operations and a grade of " +
			"its consensus thread and its leader. Every other peer's row has the version, role, term " +
			"and commit it reported to this one over the cluster's own channel, whichever peer is " +
			"asked, and is red when it does not answer, so a stopped peer is named wherever you ask; " +
			"a cluster on more than one version says so. A server too old for that leaves those " +
			"cells blank and grades a peer by whether this one can message it, which only the leader " +
			"does for every peer: ask the leader, since a follower reports the peers it never talks " +
			"to as not observed rather than as fine. The replicas column says how many of a " +
			"collection's replicas are serving and, for those that are not, which shards on which " +
			"peer: Dead is a replica that missed a write and is red, and Recovery, Partial and " +
			"the other states of one being brought level are amber.\n\n" +
			"A replica on another peer is known by the state consensus recorded for it, so it stays " +
			"active until a write fails on it — except that a peer known not to answer has its " +
			"replicas counted as not serving and graded amber, or red when it was the shard's only " +
			"copy. The cluster's own state needs global read access: the read-only API key has it, " +
			"and a JWT scoped to collections does not, in which case the replicas are still shown " +
			"and the page says so.\n\n" +
			"At most 100 collections are read, worst first, and the page says how many there are " +
			"(qdrant.collection.list reads every one). With a `collection`, or the one a profile fences " +
			"with `only: collection:`, only that one.\n\n" +
			"Describes collections and returns no point; reading points is qdrant.points.scroll, a write.",
		Agent: "Version, reachability and every collection with its point count and status. `yellow` means a " +
			"collection serves searches from a partly built index, so its results are quietly incomplete. " +
			"Against a distributed instance it also lists the peers (this peer's raft term, commit index, " +
			"pending operations and consensus grade; another peer's version, role, term and commit as " +
			"reported to this one, red when it does not answer) and adds a replicas column: how many of a " +
			"collection's replicas serve and which shards on which peer do not (Dead is red, Recovery and " +
			"Partial amber). Ask the leader: a follower reports peers it never talks to as not observed. " +
			"The cluster's own state needs global read access. Reads 100 collections at most, worst " +
			"first. Returns no point.",
		Run: runOverview,
	}, optionalCollectionField("only this collection — every collection the key can see when omitted"))
}

func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
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
	table, verr := collectionTableIn(ctx, req, cs, overviewCollections)
	if verr != nil {
		return nil, verr
	}
	collections := strconv.Itoa(len(table.Rows))
	if table.Total > len(table.Rows) {
		collections = collections + " of " + strconv.Itoa(table.Total) + " — the first " +
			strconv.Itoa(len(table.Rows)) + " by name; " + req.Surface().CapabilityName("qdrant.collection.list") + " reads every one"
	}

	p := plugin.NewPage(ctx, req)
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
	p.Put("collections", table)
	return p.View(), nil
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
			"With a `collection` — named, or the one a profile fences with `only: collection:` — " +
			"only that one.\n\n" +
			"Names and counts only, never a point.",
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
	}, optionalCollectionField("only this collection — every collection the key can see when omitted"))
}

func collectionTable(ctx context.Context, req plugin.Request) (view.Table, *view.Error) {
	cs, verr := fetchCluster(ctx, req)
	if verr != nil {
		return view.Table{}, verr
	}
	return collectionTableIn(ctx, req, cs, 0)
}

// collectionTableIn lists the collections, and in a cluster adds the column
// that says whether every replica of each is serving.
//
// **The column is there when it can mean something.** A standalone instance
// has one copy of everything and nothing to say; a distributed one gets it
// whether or not this credential could read the cluster's own state, because
// the per-collection endpoint answers a token scoped to collections where
// /cluster does not, and a collection that turns out to be unsharded in a
// mode nobody could name reads "-" rather than inventing a replica count.
//
// **With a `collection`, the page is about it and the instance's listing is
// never asked for.** A fence on `collection` is written to keep the other names
// out of a connection's answers, and the listing is where they are; a key
// scoped to one collection may not be allowed it either. The collection that
// is not there is then the finding, as the bucket is in plugins/s3's overview,
// and not a row of "unreadable" that reads as one the key may not see.
//
// **With a limit, only that many are read, the first by name, and the table says
// how many there are.** Each collection is a request, and its status is only
// known by making it, so the ones to read cannot be chosen by their health: the
// worst of those read are put first, which is all the order can promise. The cut
// is Total, which a program reads, and a warning, which a person does.
func collectionTableIn(ctx context.Context, req plugin.Request, cs clusterState, limit int) (view.Table, *view.Error) {
	only := strings.TrimSpace(req.String("collection"))
	var names []string
	if only != "" {
		names = []string{only}
	} else {
		var list collectionsResponse
		if verr := get(ctx, req, "/collections", &list); verr != nil {
			return view.Table{}, verr
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
	read := make([]collectionRow, 0, len(names))
	anyDistributed := false
	for _, name := range names {
		listed := plugin.ListedName(name)
		replica := "-"
		if cs.mode != modeStandalone {
			var cc collectionCluster
			if verr := get(ctx, req, pathFor("/collections/%s/cluster", name), &cc); verr == nil && (cs.mode == modeDistributed || cc.distributed()) {
				replica = replicasHealth(cc, cs.label, cs.peersDown(time.Now()))
				anyDistributed = true
			}
		}

		var info collectionInfo
		if verr := get(ctx, req, pathFor("/collections/%s", name), &info); verr != nil {
			if only != "" {
				return view.Table{}, verr
			}
			// Reported, not fatal. A collection this key may not see is the
			// normal case on a shared instance, and ending the listing at the
			// first one would turn "here is what you can see" into nothing.
			read = append(read, collectionRow{[]string{listed, "-", "-", "-", "unreadable"}, replica})
			continue
		}
		read = append(read, collectionRow{[]string{
			listed,
			countText(info.PointsCount),
			countText(info.IndexedVectorsCount),
			strconv.FormatInt(info.SegmentsCount, 10),
			info.Status,
		}, replica})
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
	if total > len(t.Rows) {
		t.Warnings = append(t.Warnings, view.Error{
			Code:    "qdrant.collections.partial",
			Message: "read " + strconv.Itoa(len(t.Rows)) + " of " + strconv.Itoa(total) + " collections, the first by name",
			Hint:    nextCall(req, "qdrant.collection.list") + " reads every one",
		})
	}
	t.Empty = "no collections"
	return t, nil
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
