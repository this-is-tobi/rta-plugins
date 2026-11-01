package main

import (
	"strconv"

	"github.com/this-is-tobi/rta/pkg/format"
)

// parseBytes reads a byte-valued quantity ("8Gi", "512Mi", "1000000000",
// "8G") as a whole byte count. Kubernetes' grammar is format.ParseQuantity's,
// which reads a CPU quantity as its number of cores too: "500m" is half of
// one, and "148912532n", metrics-server's nanocores, a little under a sixth.
func parseBytes(s string) (uint64, bool) {
	q, ok := format.ParseQuantity(s)
	return uint64(q), ok
}

// percent renders used/hard as a whole-number percentage, or "" when hard is
// zero or unparseable — a quota with no limit set has no percentage to
// report, and reporting "0%" would read as "plenty of headroom" for the
// opposite reason.
func percentOf(used, hard float64) string {
	if hard <= 0 {
		return ""
	}
	return strconv.Itoa(int(used/hard*100)) + "%"
}
