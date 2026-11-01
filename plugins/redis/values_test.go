package main

import (
	"slices"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The sizes, the share of maxmemory, a client's times and a replica's
// distance behind carry their values beside their text, which is what a
// program reading the answer as JSON compares instead of a string to take
// apart; what the server did not report, or a replica in its full sync has no
// distance to, has none, rather than reading as zero.
func TestTheSizesTimesAndDistancesCarryTheirValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		info string
		want []string
	}{
		{"a ceiling", "used_memory:1048576\r\nmaxmemory:4194304\r\nused_memory_rss:2097152\r\nevicted_keys:3\r\n",
			[]string{"1048576", "4194304", "25", "2097152", "", "", ""}},
		{"no ceiling", "used_memory:1048576\r\nmaxmemory:0\r\nused_memory_rss:2097152\r\n",
			[]string{"1048576", "", "", "2097152", "", "", ""}},
	} {
		memory := memoryTable(parseInfo(tc.info))
		if !slices.Equal(memory.Values[0], tc.want) {
			t.Errorf("%s: the memory row is valued %q, want %q", tc.name, memory.Values[0], tc.want)
		}
		checkValues(t, "memory", memory)
	}

	replication := replicationTable(parseInfo(infoPrimaryThreeReplicas))
	behind := slices.IndexFunc(replication.Columns, func(c view.Column) bool { return c.Name == "Behind" })
	for row, want := range []string{"0", "0", ""} {
		var got string
		if replication.Values[row] != nil {
			got = replication.Values[row][behind]
		}
		if got != want {
			t.Errorf("replica %d is valued %q behind, want %q", row, got, want)
		}
	}
	checkValues(t, "replication", replication)

	clients := clientTable("id=3 addr=10.0.0.1:5000 name=web age=5400 idle=x db=0 cmd=get\n")
	if want := []string{"", "", "", "5400", "", "", ""}; !slices.Equal(clients.Values[0], want) {
		t.Errorf("a client is valued %q, want %q", clients.Values[0], want)
	}
	checkValues(t, "clients", clients)
}

func checkValues(t *testing.T, name string, tbl view.Table) {
	t.Helper()
	if len(tbl.Values) != len(tbl.Rows) {
		t.Fatalf("%s: values for %d rows, and %d rows", name, len(tbl.Values), len(tbl.Rows))
	}
	for r, values := range tbl.Values {
		for c, v := range values {
			if v != "" && !view.ValueOK(tbl.Columns[c].Kind, v) {
				t.Errorf("%s: row %d %s is valued %q, which a %q column does not take", name, r, tbl.Columns[c].Name, v, tbl.Columns[c].Kind)
			}
		}
	}
}
