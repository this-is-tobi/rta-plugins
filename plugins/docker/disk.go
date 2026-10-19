package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/this-is-tobi/rta/pkg/view"
)

// diskRow is one line of `docker system df --format json`: one kind of thing the
// daemon keeps, how many there are, how many are in use, how much room they take
// and how much of it nothing refers to.
//
// The sizes arrive as the CLI writes them, in decimal units ("1.35GB"), and
// Reclaimable with the share it is of the size ("290MB (21%)"). They are shown
// as written and read back only to order and to add (parseSize).
type diskRow struct {
	Type        string `json:"Type"`
	TotalCount  string `json:"TotalCount"`
	Active      string `json:"Active"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

// The kinds `docker system df` names that the overview reports, with the word it
// uses for each. The containers' own layers are the fourth it prints and the
// least: a tile that listed them would spend its line on the megabytes of a
// stopped container, and the detailed page has them.
var diskKinds = []struct{ docker, label string }{
	{"Images", "images"},
	{"Local Volumes", "volumes"},
	{"Build Cache", "cache"},
}

func fetchDiskUsage(ctx context.Context, c connection) ([]diskRow, *view.Error) {
	raw, verr := run(ctx, c, "system", "df", "--format", "json")
	if verr != nil {
		return nil, verr
	}
	rows, verr := jsonLines[diskRow](raw)
	if verr != nil {
		// A CLI from before `--format json` takes "json" for a template and
		// prints the word once for each kind, which is not a document, and the
		// decoder's complaint about it names a character and no cause.
		return nil, view.Errorf("docker.unreadable",
			"this CLI's `system df` has no JSON output")
	}
	for _, r := range rows {
		if diskKind(r) != "" {
			return rows, nil
		}
	}
	return nil, view.Errorf("docker.unreadable", "`docker system df` listed no images, volumes or build cache")
}

// diskKind is the word the overview uses for a row of the usage, or "" for one
// it does not report.
func diskKind(r diskRow) string {
	for _, k := range diskKinds {
		if r.Type == k.docker {
			return k.label
		}
	}
	return ""
}

// diskValue is the disk row of the compact page: what takes the room, the
// largest first because that is the answer to where it went, and what the daemon
// says could be taken back, added up from its own figures for the same three
// kinds. The sizes are the CLI's own text and not formatted again: the sum is the
// one number written here, and it is written the way the CLI writes the others.
func diskValue(rows []diskRow) string {
	var kinds []diskRow
	for _, r := range rows {
		if diskKind(r) != "" {
			kinds = append(kinds, r)
		}
	}
	sort.SliceStable(kinds, func(i, j int) bool { return parseSize(kinds[i].Size) > parseSize(kinds[j].Size) })
	parts := make([]string, len(kinds))
	var reclaimable int64
	for i, r := range kinds {
		parts[i] = diskKind(r) + " " + r.Size
		reclaimable += parseSize(sizeOf(r.Reclaimable))
	}
	value := strings.Join(parts, ", ")
	if reclaimable > 0 {
		value += " · " + decimalSize(reclaimable) + " reclaimable"
	}
	return value
}

// sizeOf is the size in a "290MB (21%)", which is the figure without the share
// of the whole it is.
func sizeOf(reclaimable string) string {
	size, _, _ := strings.Cut(reclaimable, " ")
	return size
}

// decimalSize is parseSize's inverse, the way the CLI writes a size: decimal
// units, three significant digits.
func decimalSize(n int64) string {
	value, unit := float64(n), "B"
	for _, u := range []struct {
		suffix string
		size   float64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}} {
		if value >= u.size {
			value, unit = value/u.size, u.suffix
			break
		}
	}
	if unit == "B" {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.3g%s", value, unit)
}

func diskTable(rows []diskRow) view.Table {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{r.Type, r.TotalCount, r.Active, r.Size, r.Reclaimable})
	}
	return view.Table{
		Columns: []view.Column{
			{Name: "kind"}, {Name: "total", Kind: view.KindNumber}, {Name: "active", Kind: view.KindNumber},
			{Name: "size", Kind: view.KindBytes}, {Name: "reclaimable"},
		},
		Rows: out, Total: len(out),
	}
}
