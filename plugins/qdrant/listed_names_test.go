package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A name a stranger chose — a collection, a vector, a payload field, a point's
// id — is shown the way a list of them shows it: as it is when it reads as
// itself, and quoted with each character a reader would not see written out
// otherwise. The renderer strips control characters from a cell on the way to
// a terminal, which turned an escape sequence in a name into another, ordinary
// name and let a newline split a row.
const (
	oddName       = "esc\x1b[31mred\nline"
	oddNameListed = `"esc\x1b[31mred\nline"`
	plainName     = "café docs"
)

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func collectionAnswer(t *testing.T, vectors any) string {
	t.Helper()
	return envelope(asJSON(t, map[string]any{
		"status": "green", "points_count": 3, "indexed_vectors_count": 3, "segments_count": 1,
		"config": map[string]any{"params": map[string]any{"vectors": vectors, "shard_number": 1,
			"replication_factor": 1, "on_disk_payload": true}},
	}))
}

func noEscapes(t *testing.T, what, s string) {
	t.Helper()
	if strings.ContainsAny(s, "\x1b\n") {
		t.Errorf("%s holds a raw escape or newline: %q", what, s)
	}
}

func TestTheCollectionListShowsAnOddNameQuotedAndAPlainOneAsItIs(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections": envelope(asJSON(t, map[string]any{"collections": []map[string]string{
			{"name": oddName}, {"name": plainName}}})),
		"/collections/" + oddName:   collectionAnswer(t, map[string]any{"size": 4, "distance": "Cosine"}),
		"/collections/" + plainName: collectionAnswer(t, map[string]any{"size": 4, "distance": "Cosine"}),
	})
	table, verr := collectionTable(context.Background(), reqAt(t, f, "qdrant.collection.list", map[string]any{}))
	if verr != nil {
		t.Fatal(verr)
	}
	var got []string
	for _, row := range table.Rows {
		noEscapes(t, "a row", strings.Join(row, "|"))
		got = append(got, row[0])
	}
	slices.Sort(got)
	if want := []string{oddNameListed, plainName}; !slices.Equal(got, want) {
		t.Errorf("names = %q, want %q", got, want)
	}
}

// A collection the key may not read is still a row, and its name is the part
// of it a stranger chose.
func TestAnUnreadableCollectionsOddNameIsQuotedToo(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections": envelope(asJSON(t, map[string]any{"collections": []map[string]string{{"name": oddName}}})),
	})
	table, verr := collectionTable(context.Background(), reqAt(t, f, "qdrant.collection.list", map[string]any{}))
	if verr != nil {
		t.Fatal(verr)
	}
	if len(table.Rows) != 1 || table.Rows[0][0] != oddNameListed || table.Rows[0][4] != "unreadable" {
		t.Errorf("rows = %q, want the quoted name beside unreadable", table.Rows)
	}
}

func TestTheCollectionPageShowsItsNameAndItsVectorsNamesListed(t *testing.T) {
	for _, tc := range []struct {
		name, vector, wantName, wantKey string
	}{
		{oddName, "vec\x1b[2J\nx", oddNameListed, `vector "vec\x1b[2J\nx"`},
		{plainName, "résumé vec", plainName, "vector résumé vec"},
	} {
		f := newFakeQdrant(t, map[string]string{
			"/collections/" + tc.name: collectionAnswer(t, map[string]any{
				tc.vector: map[string]any{"size": 8, "distance": "Dot"},
				"other":   map[string]any{"size": 2, "distance": "Cosine"}}),
		})
		v, err := runCollectionShow(context.Background(),
			reqAt(t, f, "qdrant.collection.show", map[string]any{"collection": tc.name}))
		if err != nil {
			t.Fatal(err)
		}
		kv := v.(view.KeyValue)
		if got := pair(kv, "collection"); got != tc.wantName {
			t.Errorf("collection = %q, want %q", got, tc.wantName)
		}
		if got := pair(kv, tc.wantKey); !strings.HasPrefix(got, "8 dimensions") {
			t.Errorf("no %q among %q (got %q)", tc.wantKey, kv.Pairs, got)
		}
		for _, p := range kv.Pairs {
			noEscapes(t, "a pair", p.Key+"="+p.Value)
		}
	}
}

func TestThePointCountShowsItsCollectionListed(t *testing.T) {
	for name, want := range map[string]string{oddName: oddNameListed, plainName: plainName} {
		f := newFakeQdrant(t, map[string]string{
			"/collections/" + name + "/points/count": envelope(`{"count":7}`),
		})
		v, err := runPointsCount(context.Background(),
			reqAt(t, f, "qdrant.points.count", map[string]any{"collection": name}))
		if err != nil {
			t.Fatal(err)
		}
		if got := pair(v.(view.KeyValue), "collection"); got != want {
			t.Errorf("collection = %q, want %q", got, want)
		}
	}
}

// The scroll's columns are the payload's field names, and the mask is matched
// against them by name: each field that is named odd is quoted in the column
// and in the redaction alike, since a mask that names the raw field no longer
// matches the column that was drawn from it, and the values it hides come back.
func TestAPayloadFieldWithAnOddNameIsAColumnListedAndMasked(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections/docs/points/scroll": envelope(asJSON(t, map[string]any{
			"points": []map[string]any{{"id": 1, "payload": map[string]any{
				oddName: "secret one", "prénom client": "secret two"}}},
			"next_page_offset": nil,
		})),
	})
	v, err := runPointsScroll(context.Background(),
		reqAt(t, f, "qdrant.points.scroll", map[string]any{"collection": "docs"}))
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	var cols []string
	for _, c := range tbl.Columns {
		noEscapes(t, "a column", c.Name)
		cols = append(cols, c.Name)
	}
	if want := []string{"ID", "prénom client", oddNameListed}; !slices.Equal(slices.Sorted(slices.Values(cols)),
		slices.Sorted(slices.Values(want))) {
		t.Errorf("columns = %q, want %q", cols, want)
	}
	for _, c := range tbl.Columns[1:] {
		if !tbl.IsRedacted(c.Name) {
			t.Errorf("column %q is shown and not masked: redacted = %q", c.Name, tbl.Redacted)
		}
	}
	if tbl.IsRedacted("ID") {
		t.Error("the id is masked — that hides which points were read")
	}
}

// The mask goes by a column's name, so a payload field called ID was the id
// column's name twice: the id came back masked beside the field's values, and
// which points were read — the one thing this table leaves readable — was
// hidden with them. A field named like a column the table has of its own is
// quoted, and the id and the vector keep the names they have.
func TestAPayloadFieldNamedLikeATablesOwnColumnDoesNotMaskThatColumn(t *testing.T) {
	for _, vectors := range []bool{false, true} {
		f := newFakeQdrant(t, map[string]string{
			"/collections/docs/points/scroll": envelope(asJSON(t, map[string]any{
				"points": []map[string]any{{"id": 7, "vector": []float64{0.5, 0.25}, "payload": map[string]any{
					"ID": "secret one", "Vector": "secret two", "title": "secret three"}}},
				"next_page_offset": nil,
			})),
		})
		v, err := runPointsScroll(context.Background(), reqAt(t, f, "qdrant.points.scroll",
			map[string]any{"collection": "docs", "vectors": vectors}))
		if err != nil {
			t.Fatal(err)
		}
		tbl := v.(view.Table)
		seen := map[string]bool{}
		for _, c := range tbl.Columns {
			if seen[c.Name] {
				t.Errorf("vectors=%v: %q names two columns of %v", vectors, c.Name, tbl.Columns)
			}
			seen[c.Name] = true
		}
		if !seen["ID"] || tbl.IsRedacted("ID") {
			t.Errorf("vectors=%v: the id column is missing or masked: columns = %v, redacted = %q",
				vectors, tbl.Columns, tbl.Redacted)
		}
		if vectors && (!seen["Vector"] || !tbl.IsRedacted("Vector")) {
			t.Errorf("the vector column is missing or shown: columns = %v, redacted = %q", tbl.Columns, tbl.Redacted)
		}
		for _, field := range []string{`"ID"`, "title"} {
			if !seen[field] || !tbl.IsRedacted(field) {
				t.Errorf("vectors=%v: payload field %s is not a masked column of %v (redacted = %q)",
					vectors, field, tbl.Columns, tbl.Redacted)
			}
		}
		wantVector := "Vector"
		if vectors {
			wantVector = `"Vector"`
		}
		if !seen[wantVector] || !tbl.IsRedacted(wantVector) {
			t.Errorf("vectors=%v: payload field Vector is not the masked column %s of %v (redacted = %q)",
				vectors, wantVector, tbl.Columns, tbl.Redacted)
		}
	}
}

func TestAPointIDThatIsNeitherANumberNorAUUIDIsListed(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections/docs/points/scroll": envelope(asJSON(t, map[string]any{
			"points": []map[string]any{
				{"id": oddName, "payload": map[string]any{}},
				{"id": "pièce 1", "payload": map[string]any{}},
				{"id": 42, "payload": map[string]any{}},
			},
			"next_page_offset": nil,
		})),
	})
	v, err := runPointsScroll(context.Background(),
		reqAt(t, f, "qdrant.points.scroll", map[string]any{"collection": "docs"}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range v.(view.Table).Rows {
		got = append(got, row[0])
	}
	if want := []string{oddNameListed, "pièce 1", "42"}; !slices.Equal(got, want) {
		t.Errorf("ids = %q, want %q", got, want)
	}
}

func TestANamedVectorsNameIsListedInItsSummary(t *testing.T) {
	got := vectorSummary(json.RawMessage(asJSON(t, map[string][]float64{
		"vec\x1b[2J\nx": {0.1, 0.2}, "résumé vec": {0.3}})))
	if want := `"vec\x1b[2J\nx":2d résumé vec:1d`; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	noEscapes(t, "the summary", got)
}
