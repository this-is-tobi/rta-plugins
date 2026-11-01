package main

import (
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A volume's size as Kubernetes writes it ("10Gi", "500Mi", "10G"), read by
// hand rather than through k8s.io/apimachinery's resource.Quantity, which this
// plugin does not link for the reason kubectl.go gives for client-go. It is
// plugins/kube's parse of the same quantities, and the two should stay one
// answer.
//
// A size is compared by its bytes and never by its spelling: a claim may ask in
// one suffix and its volume report in the other, and a provisioner that rounds
// up to whole GiB answers a 10G request with 10Gi. Compared as text, that read
// as a volume short of what it was asked for.

var binarySuffixes = map[string]float64{
	"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30,
	"Ti": 1 << 40, "Pi": 1 << 50, "Ei": 1 << 60,
}

var decimalSuffixes = map[string]float64{
	"k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15, "E": 1e18,
}

// parseBytes reads a byte-valued quantity ("8Gi", "512Mi", "1000000000",
// "8G") as a plain byte count.
func parseBytes(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// Every binary suffix is tried before any decimal one: "Ki" must not match
	// on its trailing "i" as if it were a one-character decimal suffix.
	for suffix, mult := range binarySuffixes {
		if rest, ok := strings.CutSuffix(s, suffix); ok {
			return scaledBytes(rest, mult)
		}
	}
	for suffix, mult := range decimalSuffixes {
		if rest, ok := strings.CutSuffix(s, suffix); ok {
			return scaledBytes(rest, mult)
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return uint64(n), true
}

func scaledBytes(numeral string, mult float64) (uint64, bool) {
	n, err := strconv.ParseFloat(numeral, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return uint64(n * mult), true
}

// sizeCell is a quantity as a table's cell: written and valued in bytes, the
// quantity as Kubernetes wrote it when it does not parse, and a dash for none.
func sizeCell(q string) view.Cell {
	if b, ok := parseBytes(q); ok {
		return view.Bytes(b)
	}
	return view.Plain(orDash(q))
}

// sizeText is sizeCell's text, for a sentence.
func sizeText(q string) string { return sizeCell(q).Text }
