package main

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The overview's own statements, answered by the test, and every other statement
// by a scenario of captured answers (testdata/replication): the replication
// reading is the one a server really gave, and what is new around it is what the
// test says the server said. Every statement asked is recorded, since what the
// compact form does not ask is as much of its contract as what it does.

type overviewFake struct {
	scenario string
	// version and maxConns are the answer of the first statement; idErr refuses it.
	version  string
	maxConns int64
	idErr    error
	// counters are the answer of SHOW GLOBAL STATUS, of the names the statement
	// asked for, and cErr refuses it.
	counters map[string]string
	cErr     error
	// fixture is the scenario, which a test may change before it is asked.
	fixture fixtureFile

	mu    sync.Mutex
	asked []string
}

var (
	overviewFakesMu sync.Mutex
	overviewFakes   = map[string]*overviewFake{}
)

func init() { sql.Register("mariadb-overview", overviewDriver{}) }

type overviewDriver struct{}

func (overviewDriver) Open(name string) (driver.Conn, error) {
	overviewFakesMu.Lock()
	defer overviewFakesMu.Unlock()
	f, ok := overviewFakes[name]
	if !ok {
		return nil, fmt.Errorf("no overview fake named %q", name)
	}
	return overviewConn{f}, nil
}

type overviewConn struct{ f *overviewFake }

func (c overviewConn) Prepare(q string) (driver.Stmt, error) { return overviewStmt{c.f, q}, nil }
func (overviewConn) Close() error                            { return nil }
func (overviewConn) Begin() (driver.Tx, error)               { return nil, fmt.Errorf("not used") }

type overviewStmt struct {
	f *overviewFake
	q string
}

func (overviewStmt) Close() error  { return nil }
func (overviewStmt) NumInput() int { return -1 }
func (overviewStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, fmt.Errorf("not used")
}

func (s overviewStmt) Query([]driver.Value) (driver.Rows, error) {
	f := s.f
	f.mu.Lock()
	f.asked = append(f.asked, s.q)
	f.mu.Unlock()
	switch {
	case strings.Contains(s.q, "SELECT VERSION(), @@max_connections"):
		if f.idErr != nil {
			return nil, f.idErr
		}
		return &fixtureRows{a: answerOf([]string{"v", "m"}, []string{f.version, strconv.FormatInt(f.maxConns, 10)})}, nil
	case strings.Contains(s.q, "SHOW GLOBAL STATUS WHERE"):
		if f.cErr != nil {
			return nil, f.cErr
		}
		var rows [][]string
		for _, name := range []string{"Uptime", "Threads_connected", "Threads_running", "Innodb_row_lock_current_waits", "Slow_queries"} {
			if value, ok := f.counters[name]; ok && strings.Contains(s.q, "'"+name+"'") {
				rows = append(rows, []string{name, value})
			}
		}
		return &fixtureRows{a: answerOf([]string{"Variable_name", "Value"}, rows...)}, nil
	case strings.Contains(s.q, "INFORMATION_SCHEMA.SCHEMATA"):
		return &fixtureRows{a: answerOf([]string{"a", "b", "c"}, []string{"shop", "212", "53687091200"})}, nil
	case strings.Contains(s.q, "INFORMATION_SCHEMA.PROCESSLIST"):
		return &fixtureRows{a: answerOf(processlistColumns)}, nil
	}
	a, ok := f.fixture.Statements[s.q]
	if !ok {
		return nil, fmt.Errorf("scenario %s has no answer for %q", f.scenario, s.q)
	}
	if a.Error != nil {
		return nil, &mysql.MySQLError{Number: a.Error.Number, Message: a.Error.Message}
	}
	return &fixtureRows{a: a}, nil
}

func answerOf(columns []string, rows ...[]string) fixtureAnswer {
	a := fixtureAnswer{Columns: columns}
	for _, r := range rows {
		cells := make([]*string, len(r))
		for i := range r {
			cells[i] = &r[i]
		}
		a.Rows = append(a.Rows, cells)
	}
	return a
}

// A server that has been up for 14 days, 2 hours and 5 minutes, with 12 of its
// 151 connections taken and nothing running but the counting session.
func quietCounters() map[string]string {
	return map[string]string{
		"Uptime": "1217100", "Threads_connected": "12", "Threads_running": "1",
		"Innodb_row_lock_current_waits": "0", "Slow_queries": "0",
	}
}

// The moment the views read as now: 14d 2h 5m after the server started.
var overviewNow = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

func fixedClock(t *testing.T) {
	t.Helper()
	was := clock
	clock = func() time.Time { return overviewNow }
	t.Cleanup(func() { clock = was })
}

// The server the captured answers were taken from, which is the one thing the
// two forks' tests say differently: the release, the source its replica followed,
// the statement and the column that tell how far behind it is, and the privilege
// the replica status takes.
const (
	capturedScenario      = "mariadb-11.4-"
	capturedVersion       = "11.4.2-MariaDB"
	capturedServer        = "11.4.2-MariaDB"
	capturedSource        = "dbwp-my-d114-p:3306"
	capturedReplicaStatus = "SHOW ALL REPLICAS STATUS"
	capturedLagColumn     = "Seconds_Behind_Master"
	capturedGrant         = "REPLICA MONITOR"
)

// captured is the scenario of a state this fork was captured in.
func captured(state string) string { return capturedScenario + state }

// newOverviewFake is a quiet healthy server in the named scenario.
func newOverviewFake(t *testing.T, scenario string) *overviewFake {
	t.Helper()
	fixture, err := loadFixture(scenario)
	if err != nil {
		t.Fatalf("scenario %s: %v", scenario, err)
	}
	return &overviewFake{scenario: scenario, version: capturedVersion, maxConns: 151, counters: quietCounters(), fixture: fixture}
}

func (f *overviewFake) open(t *testing.T) *sql.DB {
	t.Helper()
	name := t.Name() + "/" + f.scenario
	overviewFakesMu.Lock()
	overviewFakes[name] = f
	overviewFakesMu.Unlock()
	t.Cleanup(func() {
		overviewFakesMu.Lock()
		delete(overviewFakes, name)
		overviewFakesMu.Unlock()
	})
	db, err := sql.Open("mariadb-overview", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (f *overviewFake) askedFor(fragment string) bool {
	for _, q := range f.asked {
		if strings.Contains(q, fragment) {
			return true
		}
	}
	return false
}

// setCell changes one cell of the first row a scenario answers a statement with.
func (f *overviewFake) setCell(t *testing.T, statement, column, value string) {
	t.Helper()
	a, ok := f.fixture.Statements[statement]
	if !ok || len(a.Rows) == 0 {
		t.Fatalf("scenario %s has no row for %s", f.scenario, statement)
	}
	for i, name := range a.Columns {
		if strings.EqualFold(name, column) {
			v := value
			a.Rows[0][i] = &v
			return
		}
	}
	t.Fatalf("%s has no column %s", statement, column)
}

func (f *overviewFake) compact(t *testing.T) []view.Pair {
	t.Helper()
	fixedClock(t)
	v, err := compactOverview(t.Context(), f.open(t), req(t, "mariadb.overview", nil))
	return pairRows(t, v, err)
}

func pairRows(t *testing.T, v view.View, err error) []view.Pair {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("view is %T, want key/value", v)
	}
	return kv.Pairs
}

func keysOf(rows []view.Pair) string {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.Key
	}
	return strings.Join(keys, ",")
}

func valueOf(rows []view.Pair, key string) string {
	for _, r := range rows {
		if r.Key == key {
			return r.Value
		}
	}
	return "<no such row>"
}
