package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func pointsCountCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "qdrant.points.count",
		Summary:    "How many points a collection holds, exactly",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "An exact count, which is the difference between this and the estimate " +
			"qdrant.collection.list reports.\n\n" +
			"Exact costs a scan on a large collection, and that is the trade being made rather " +
			"than a detail: the listing's number is what the segments last reported and can be " +
			"stale after a bulk load, so this is the one to use when the number has to be right.\n\n" +
			"A number, never a point. This is the read tier — it says how much is there and " +
			"nothing about what it is.",
		Run: runPointsCount,
	}, collectionField("collection to count"),
		plugin.Field{Name: "exact", Type: plugin.Bool, Config: "count.exact", Default: true,
			Help: "scan for an exact count rather than taking the estimate"})
}

func runPointsCount(ctx context.Context, req plugin.Request) (view.View, error) {
	name := req.String("collection")
	var out struct {
		Count int64 `json:"count"`
	}
	body := map[string]any{"exact": req.Bool("exact")}
	if verr := post(ctx, req, pathFor("/collections/%s/points/count", name), body, &out); verr != nil {
		return nil, verr
	}
	kind := "estimated"
	if req.Bool("exact") {
		kind = "exact"
	}
	return view.KeyValue{Pairs: []view.Pair{
		{Key: "collection", Value: name},
		{Key: "points", Value: strconv.FormatInt(out.Count, 10)},
		{Key: "count", Value: kind},
	}}, nil
}

func pointsScrollCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:      "qdrant.points.scroll",
		Summary: "Read points out of a collection",
		// **Write, and it needs a grant naming it.** Nothing here mutates.
		//
		// The classification is about what it discloses, and there are two
		// things being disclosed rather than one.
		//
		// The payloads are the obvious half: whatever was indexed, which for
		// most deployments is chunks of documents — tickets, wikis, contracts,
		// customer records.
		//
		// The vectors are the half people forget. An embedding is not a hash.
		// It is a lossy but reversible-enough encoding, and inversion attacks
		// recover substantial parts of the source text from embeddings alone.
		// Handing back raw vectors is therefore closer to handing back the
		// documents than to handing back a checksum, which is why --vectors is
		// off by default even here, inside the write tier.
		//
		// NeedsGrant on top, because this names one collection — so a grant
		// can name it too, and the narrow consent is actually available.
		Safety:     plugin.Write,
		NeedsGrant: true,
		// Without this, `rta grant allow qdrant.points.scroll
		// support-tickets` — the exact consent the description advertises
		// — parsed and sealed but matched nothing: scopes() derives the
		// record from the field Scope names, and an empty Scope derives
		// "", so the only grant that ever worked was the unscoped one
		// covering every collection.
		Scope:      "collection",
		Idempotent: true,
		Description: "Points from one collection, with their payloads.\n\n" +
			"**Classified write for what it discloses, not what it changes.** The payloads are " +
			"whatever was indexed — for most deployments, chunks of documents.\n\n" +
			"**Vectors are off by default even here.** An embedding is not a hash: it is a " +
			"lossy but reversible-enough encoding, and inversion attacks recover substantial " +
			"parts of the source text from embeddings alone. So --vectors is a second, separate " +
			"decision rather than something that rides along with the payload.\n\n" +
			"It also needs a grant naming it, which is available because this names one " +
			"collection: `rta grant allow qdrant.points.scroll support-tickets` is a consent " +
			"somebody can read.\n\n" +
			"The read tier — qdrant.collection.show and qdrant.points.count — describes a " +
			"collection and counts it, which is usually the question and costs none of this.",
		Run: runPointsScroll,
	}, collectionField("collection to read from"),
		plugin.Field{Name: "limit", Type: plugin.Int, Config: "limit", Default: 10, Min: 1, Max: 1000,
			Help: "how many points to return"},
		plugin.Field{Name: "offset", Type: plugin.String, Default: "",
			Help: "continue from the id the last page ended at"},
		plugin.Field{Name: "vectors", Type: plugin.Bool, Default: false,
			Help: "include the raw vectors — a second decision, see the description"})
}

type scrollPoint struct {
	ID      any             `json:"id"`
	Payload map[string]any  `json:"payload"`
	Vector  json.RawMessage `json:"vector"`
}

func runPointsScroll(ctx context.Context, req plugin.Request) (view.View, error) {
	name := req.String("collection")
	withVectors := req.Bool("vectors")

	body := map[string]any{
		"limit":        req.Int("limit"),
		"with_payload": true,
		"with_vector":  withVectors,
	}
	if offset := req.String("offset"); offset != "" {
		body["offset"] = pointID(offset)
	}

	// Decoded here with UseNumber rather than through post's own decode.
	// A point id is an unsigned 64-bit integer or a UUID, and a payload
	// integer is whatever was indexed; through a float64, 1234567 printed as
	// 1.234567e+06, an id past 2^53 as a neighbouring id, and the cursor the
	// next page is asked for with came back in that exponent form too.
	var result json.RawMessage
	if verr := post(ctx, req, pathFor("/collections/%s/points/scroll", name), body, &result); verr != nil {
		return nil, verr
	}
	var out struct {
		Points         []scrollPoint `json:"points"`
		NextPageOffset any           `json:"next_page_offset"`
	}
	dec := json.NewDecoder(bytes.NewReader(result))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, view.Errorf("qdrant.response.unexpected", "could not read the answer: %v", err).
			WithHint("this may be a Qdrant version whose response shape has moved")
	}

	// The payload keys vary per point, so the column set is the union of what
	// came back rather than something declared up front. Sorted, because a
	// map's iteration order would reshuffle the columns between calls.
	keys := map[string]bool{}
	for _, p := range out.Points {
		for k := range p.Payload {
			keys[k] = true
		}
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)

	cols := []view.Column{{Name: "ID"}}
	if withVectors {
		cols = append(cols, view.Column{Name: "Vector"})
	}
	for _, k := range names {
		cols = append(cols, view.Column{Name: k})
	}
	t := view.Table{Columns: cols}

	for _, p := range out.Points {
		row := []string{fmt.Sprint(p.ID)}
		if withVectors {
			row = append(row, vectorSummary(p.Vector))
		}
		for _, k := range names {
			row = append(row, payloadCell(p.Payload[k]))
		}
		t.Rows = append(t.Rows, row)
	}
	t.Total = len(t.Rows)
	if out.NextPageOffset != nil {
		t.Page = &view.Cursor{Next: fmt.Sprint(out.NextPageOffset)}
	}
	// Every payload column carries stored data, so all of them are redacted
	// and the id is not. Which points were read is what the record is for;
	// what they contained is not something to leave in a scrollback.
	t.Redacted = append([]string{}, names...)
	if withVectors {
		t.Redacted = append(t.Redacted, "Vector")
	}
	return t, nil
}

// vectorSummary renders a vector as its dimensions and first few components
// rather than as several thousand floats. The whole thing is still available
// in the JSON output for anybody who asked for it; what this avoids is a
// terminal filling with numbers nobody can read.
func vectorSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "-"
	}
	var floats []float64
	if err := json.Unmarshal(raw, &floats); err != nil {
		// A named-vector collection returns a map here rather than an array.
		var named map[string][]float64
		if err := json.Unmarshal(raw, &named); err != nil {
			return "unreadable"
		}
		parts := make([]string, 0, len(named))
		for name, v := range named {
			parts = append(parts, fmt.Sprintf("%s:%dd", name, len(v)))
		}
		sort.Strings(parts)
		return strings.Join(parts, " ")
	}
	head := make([]string, 0, 3)
	for _, f := range floats[:min(3, len(floats))] {
		head = append(head, strconv.FormatFloat(f, 'g', 4, 64))
	}
	return fmt.Sprintf("%dd [%s…]", len(floats), strings.Join(head, ", "))
}

// pointID is an offset as Qdrant's ExtendedPointId takes it: an unsigned
// integer as a JSON number, anything else — a UUID — as a string. Sent as a
// string, a numeric id is a UUID that does not parse, so a cursor copied from
// one page never reached the next.
//
// The parsed value is what goes out, not the input: ParseUint takes leading
// zeros, a JSON number literal does not, so a hand-typed 007 sent as typed
// failed in the encoder before any request was made. uint64 encodes exactly,
// which is why this is not a float64.
func pointID(s string) any {
	if n, err := strconv.ParseUint(s, 10, 64); err == nil {
		return n
	}
	return s
}

// payloadCell renders one payload value. Nested objects and arrays are shown
// as compact JSON rather than Go's %v, which prints map[a:1] — a shape nothing
// can parse and nobody writes.
func payloadCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case json.Number:
		// The literal Qdrant sent, digit for digit.
		return x.String()
	case float64:
		// A value that did not come through the scroll's own decoder, which
		// keeps numbers as written. Printing 42 rather than 4.2e+01 is the
		// difference between a readable column and one nobody can match
		// against anything.
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		// Without encoding/json's HTML escaping, which would show a payload
		// string "a&b" as "a\u0026b".
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(x); err != nil {
			return fmt.Sprint(x)
		}
		return strings.TrimSuffix(b.String(), "\n")
	}
}
