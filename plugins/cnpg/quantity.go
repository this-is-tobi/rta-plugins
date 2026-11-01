package main

import (
	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/view"
)

// sizeBytes is a volume's size as Kubernetes writes it ("10Gi", "500Mi",
// "10G") as a whole count of bytes. A size is compared by these bytes and
// never by its spelling: a provisioner that rounds a 10G claim up to whole GiB
// answers 10Gi, which as text read as a volume short of its claim.
func sizeBytes(q string) (uint64, bool) {
	n, ok := format.ParseQuantity(q)
	return uint64(n), ok
}

// sizeCell is a quantity as a table's cell: written and valued in bytes, the
// quantity as Kubernetes wrote it when it does not parse, and a dash for none.
func sizeCell(q string) view.Cell {
	if b, ok := sizeBytes(q); ok {
		return view.Bytes(b)
	}
	return view.Plain(orDash(q))
}

// sizeText is sizeCell's text, for a sentence.
func sizeText(q string) string { return sizeCell(q).Text }
