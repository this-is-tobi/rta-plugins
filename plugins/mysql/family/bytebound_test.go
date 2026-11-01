package family

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The bytes a result of the caller's own statement may hold, held under what the
// host will receive. Over MCP the host refuses an answer of more than
// plugin.DefaultResultLimit while receiving it, counting the answer on the wire,
// which is every cell's text and two bytes more for each. A cell of one byte is
// the worst case, three on the wire for the one this bound counts, so the bound
// has to be a third of the limit at most for the refusal that says to select
// fewer columns to be the one that is always read.
func TestTheByteBoundLeavesRoomUnderTheHostsResultLimit(t *testing.T) {
	const wireBytesPerTextByteAtWorst = 3
	if weight := maxBytes * wireBytesPerTextByteAtWorst; weight >= plugin.DefaultResultLimit {
		t.Errorf("a result of %d bytes of one-byte cells weighs %d on the wire, over the %d the host receives: "+
			"a result this plugin lets through would be refused by the host under another code",
			maxBytes, weight, plugin.DefaultResultLimit)
	}
}

// The SQL plugins answer to one number, because the rows a caller's own
// statement returns are one concept: plugins/pg's mebibyte.
func TestTheByteBoundIsTheOneEverySQLPluginAnswersTo(t *testing.T) {
	if maxBytes != 1<<20 {
		t.Errorf("maxBytes = %d, want 1 MiB, the bound plugins/pg answers to as well", maxBytes)
	}
}
