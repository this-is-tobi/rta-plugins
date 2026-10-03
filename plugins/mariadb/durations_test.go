package main

import (
	"database/sql/driver"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A duration reads the way a person types one. time.Duration prints an hour as
// "1h0m0s" and ten minutes as "10m0s": accepted by ParseDuration and typed by
// nobody, with a seconds figure that is never the answer to how long a session
// has been running or the server has been up. The redis spans and the etcd
// leases read "1h" and "10m", and an agent comparing the plugins met two
// notations for one thing.
func TestDurationsReadTheWayAPersonTypesThem(t *testing.T) {
	row := processlistRow("SELECT 1")
	row[5] = int64(3600)
	db := fakeDB(t, processlistColumns, [][]driver.Value{row})
	v, err := activityView(t.Context(), db, req(t, "mariadb.activity", map[string]any{}), true, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(view.Table).Rows[0][5]; got != "1h" {
		t.Errorf("a session running for an hour reads %q, want 1h", got)
	}

	fakeRoutes = nil
	db = fakeDB(t, nil, nil)
	fakeRoutes = []struct {
		match  string
		result fakeResult
	}{
		route("VERSION()", []string{"v", "m"}, [][]driver.Value{{[]byte("8.4.1"), int64(151)}}),
		route("SHOW GLOBAL STATUS", []string{"Variable_name", "Value"}, [][]driver.Value{
			{[]byte("Uptime"), []byte("5400")}, {[]byte("Threads_connected"), []byte("3")},
			{[]byte("Threads_running"), []byte("1")},
		}),
	}
	t.Cleanup(func() { fakeRoutes = nil })
	status, err := statusView(t.Context(), db, req(t, "mariadb.status", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range status.(view.KeyValue).Pairs {
		if p.Key == "uptime" && p.Value != "1h30m" {
			t.Errorf("an uptime of an hour and a half reads %q, want 1h30m", p.Value)
		}
	}
}
