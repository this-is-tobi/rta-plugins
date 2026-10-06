package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func pointsCountCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:       "qdrant.points.count",
		Summary:  "How many points a collection holds, exactly",
		Keywords: []string{"size", "documents", "vectors", "total"},
		Examples: []plugin.Example{
			{Title: "how many points a collection holds", Inputs: map[string]any{"collection": "docs"}},
		},
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
		plugin.Field{Name: "exact", Short: "e", Type: plugin.Bool, Config: "count.exact", Default: true,
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
		{Key: "collection", Value: plugin.ListedName(name)},
		{Key: "points", Value: strconv.FormatInt(out.Count, 10)},
		{Key: "count", Value: kind},
	}}, nil
}

func pointsScrollCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:       "qdrant.points.scroll",
		Summary:  "Read points out of a collection",
		Keywords: []string{"payload", "browse", "documents", "rows", "export"},
		Examples: []plugin.Example{
			{Title: "the first points of a collection", Inputs: map[string]any{"collection": "docs", "limit": 10}},
			{Title: "the page after the one that stopped at point 1234", Inputs: map[string]any{"collection": "docs", "limit": 50, "offset": "1234"}},
		},
		// **Write, and it needs a grant naming it.** Nothing here mutates.
		//
		// The classification is about what it discloses: the payloads are
		// whatever was indexed, which for most deployments is chunks of
		// documents — tickets, wikis, contracts, customer records.
		//
		// The vectors are the half people forget. An embedding is not a hash.
		// It is a lossy but reversible-enough encoding, and inversion attacks
		// recover substantial parts of the source text from embeddings alone.
		// So a vector is never handed back whole: the column is its dimension
		// count and first components, which says what the collection is built
		// from and recovers nothing, and `vectors` is off by default anyway.
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
		// The call whose answer is the stored payloads: it says so, and they
		// come back as stored. The grant naming the collection is the control,
		// and a mask on top of it was a grant that bought an agent ids and
		// bullets.
		Reveals: true,
		Description: "Points from one collection, with their payloads.\n\n" +
			"**Payloads come back as stored, on every surface** — none of it masked, and an " +
			"agent holding a grant for the collection has it in its context from then on. " +
			"`offset` continues from the next point's id.\n\n" +
			"**Classified write for what it discloses, not what it changes.** The payloads are " +
			"whatever was indexed — for most deployments, chunks of documents.\n\n" +
			"**Vectors are off by default even here, and never whole.** An embedding is not a " +
			"hash: it is a lossy but reversible-enough encoding, and inversion attacks recover " +
			"substantial parts of the source text from embeddings alone. So `vectors` adds only a " +
			"column naming each point's dimensions and its first components, which recovers " +
			"nothing, and the whole vector is not something this call hands out.\n\n" +
			"The read tier — qdrant.collection.show and qdrant.points.count — describes a " +
			"collection and counts it, which is usually the question and costs none of this.",
		Agent: "Points from one collection, up to `limit` (default 10, at most 1000), each with its payload; " +
			"`offset` continues from the next point's id, which the last page's `page.next` holds. " +
			"`vectors` adds each point's dimensions and first components, never the whole vector. " +
			"Needs a grant naming the collection.",
		Run: runPointsScroll,
	}, collectionField("collection to read from"),
		plugin.Field{Name: "limit", Short: "n", Type: plugin.Int, Config: "limit", Default: 10, Min: 1, Max: 1000,
			Help: "how many points to return"},
		plugin.Field{Name: "offset", Type: plugin.String, Default: "",
			Help: "start at this point id — the next page's first, which the last page's `page.next` holds"},
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

	// A field's name is a column's, in the one listed spelling: the raw name
	// beside it would draw a control character into a header.
	//
	// **A field named like a column of the table's own is quoted too.** A
	// column is told apart by its name, so a field called ID beside the id
	// column was one name for two columns, and a reader of `-o json` or a
	// script could not say which was which. Quoted, it is the one name it was
	// not.
	own := map[string]bool{"ID": true, "Vector": withVectors}
	fields := make([]string, len(names))
	for i, k := range names {
		fields[i] = plugin.ListedName(k)
		if own[fields[i]] {
			fields[i] = strconv.Quote(fields[i])
		}
	}
	cols := []view.Column{{Name: "ID"}}
	if withVectors {
		cols = append(cols, view.Column{Name: "Vector"})
	}
	for _, f := range fields {
		cols = append(cols, view.Column{Name: f})
	}
	t := view.Table{Columns: cols}

	for _, p := range out.Points {
		row := []string{plugin.ListedName(fmt.Sprint(p.ID))}
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
		next := fmt.Sprint(out.NextPageOffset)
		t.Page = &view.Cursor{Next: next}
		// A bare cursor over MCP names no input to pass it to, and an agent
		// holding it could not tell a continuation from an id. The id is the
		// first of the next page, which is why the input is `offset` and not
		// an "after".
		sf := req.Surface()
		t.Warnings = append(t.Warnings, view.Error{
			Code:    "qdrant.points.scroll.partial",
			Message: fmt.Sprintf("stopped after %s; the next page starts at %s", format.CountOf(len(t.Rows), "point"), next),
			Hint:    "pass " + sf.InputTo("offset", next) + " for the next page, or raise " + sf.InputName("limit"),
		})
	}
	return t, nil
}

// vectorSummary renders a vector as its dimensions and first few components
// rather than as several thousand floats. It is the cell on every surface — a
// view carries the summary, never the whole vector, so nothing a grant on
// the collection reveals can be inverted into its text.
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
			parts = append(parts, fmt.Sprintf("%s:%dd", plugin.ListedName(name), len(v)))
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
