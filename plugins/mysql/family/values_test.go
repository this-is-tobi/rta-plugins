package family

import (
	"slices"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A replica's lag and its relay backlog carry their values beside their text,
// in seconds and in bytes, which is what a program compares instead of a
// string to take apart; a lag the server did not report, and a backlog that
// spans binary log files, have none rather than reading as zero.
func TestAReplicasLagAndBacklogCarryTheirValues(t *testing.T) {
	forEachEngine(t, func(t *testing.T) {
		row := map[string]string{
			"replica_io_running": "Yes", "replica_sql_running": "Yes",
			"seconds_behind_source": "90", "sql_delay": "0",
			"source_host": "db1", "source_port": "3306",
			"source_log_file": "binlog.000003", "relay_source_log_file": "binlog.000003",
			"read_source_log_pos": "2048", "exec_source_log_pos": "1024",
		}
		silent := map[string]string{}
		for k, v := range row {
			silent[k] = v
		}
		silent["seconds_behind_source"] = ""
		silent["read_source_log_pos"], silent["source_log_file"] = "4", "binlog.000005"

		sources := sourcesTable([]channelRow{assessChannel(row), assessChannel(silent)})
		behind := slices.IndexFunc(sources.Columns, func(c view.Column) bool { return c.Name == "Behind" })
		backlog := slices.IndexFunc(sources.Columns, func(c view.Column) bool { return c.Name == "Backlog" })
		if got := sources.Values[0]; got[behind] != "90" || got[backlog] != "1024" {
			t.Errorf("a replica 90s and 1 KiB behind is valued %q", got)
		}
		if got := sources.Values[1]; got != nil {
			t.Errorf("a replica with no lag figure, across log files, is valued %q, want nothing", got)
		}
		if text := sources.Rows[1][backlog]; text != "across 3 binary log files" {
			t.Errorf("the backlog across files reads %q", text)
		}
	})
}

// A listing cut back to its bound keeps its values with its rows: the value
// of the row past the bound goes with it, or a program would read it as the
// last row's.
func TestACutListingKeepsItsValuesWithItsRows(t *testing.T) {
	tbl := view.Table{Columns: []view.Column{{Name: "Database"}, {Name: "Size", Kind: view.KindBytes}}}
	for _, size := range []int64{3072, 2048, 1024} {
		tbl.Add(view.Plain("db"), bytesCell(size))
	}
	cut := cutAtLimit(tbl, 2, "database", false, plugin.SurfaceCLI)
	if len(cut.Rows) != 2 || len(cut.Values) != 2 || cut.Values[1][1] != "2048" {
		t.Errorf("cut to 2: rows %q, values %q", cut.Rows, cut.Values)
	}
}
