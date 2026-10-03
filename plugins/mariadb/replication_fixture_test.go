package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A server's answers as the driver handed them over, captured from a real one
// and replayed here: every fixture under testdata/replication is the columns,
// rows (a NULL kept distinct from an empty string) and error numbers a live
// server returned to the statements this capability sends, so the grading and
// the wording are tested against what a server actually says rather than
// against what its documentation says it says.
//
// The data is the answer of a real, disposable server — container names,
// generated UUIDs and binary log positions — and carries nothing from any
// deployment.

type fixtureAnswer struct {
	Columns []string    `json:"columns"`
	Rows    [][]*string `json:"rows"`
	Error   *struct {
		Number  uint16 `json:"number"`
		Message string `json:"message"`
	} `json:"error"`
}

type fixtureFile struct {
	Note       string                   `json:"note"`
	Statements map[string]fixtureAnswer `json:"statements"`
}

type fixtureDriver struct{}

func init() { sql.Register("mariadb-fixture", fixtureDriver{}) }

func loadFixture(name string) (fixtureFile, error) {
	var f fixtureFile
	raw, err := os.ReadFile(filepath.Join("testdata", "replication", name+".json"))
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(raw, &f)
}

func (fixtureDriver) Open(name string) (driver.Conn, error) {
	f, err := loadFixture(name)
	if err != nil {
		return nil, err
	}
	return fixtureConn{f, name}, nil
}

type fixtureConn struct {
	file fixtureFile
	name string
}

func (c fixtureConn) Prepare(q string) (driver.Stmt, error) { return fixtureStmt{c, q}, nil }
func (fixtureConn) Close() error                            { return nil }
func (fixtureConn) Begin() (driver.Tx, error)               { return nil, fmt.Errorf("not used") }

type fixtureStmt struct {
	conn  fixtureConn
	query string
}

func (fixtureStmt) Close() error  { return nil }
func (fixtureStmt) NumInput() int { return -1 }
func (fixtureStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, fmt.Errorf("not used")
}

func (s fixtureStmt) Query([]driver.Value) (driver.Rows, error) {
	a, ok := s.conn.file.Statements[s.query]
	if !ok {
		return nil, fmt.Errorf("fixture %s has no answer for %q — capture it, or the code asks something it should not", s.conn.name, s.query)
	}
	if a.Error != nil {
		return nil, &mysql.MySQLError{Number: a.Error.Number, Message: a.Error.Message}
	}
	return &fixtureRows{a, 0}, nil
}

type fixtureRows struct {
	a  fixtureAnswer
	at int
}

func (r *fixtureRows) Columns() []string { return r.a.Columns }
func (r *fixtureRows) Close() error      { return nil }
func (r *fixtureRows) Next(dest []driver.Value) error {
	if r.at >= len(r.a.Rows) {
		return io.EOF
	}
	for i, v := range r.a.Rows[r.at] {
		if v == nil {
			dest[i] = nil
			continue
		}
		dest[i] = []byte(*v)
	}
	r.at++
	return nil
}

func fixtureNames(t *testing.T, scenario string) []string {
	t.Helper()
	all, err := filepath.Glob(filepath.Join("testdata", "replication", "*-"+scenario+".json"))
	if err != nil || len(all) == 0 {
		t.Fatalf("no fixture for the %q scenario (%v)", scenario, err)
	}
	for i, p := range all {
		all[i] = strings.TrimSuffix(filepath.Base(p), ".json")
	}
	return all
}

func fixtureOpen(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mariadb-fixture", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// stateFrom reads a fixture through the code under test exactly as a live
// connection would be, as the account the capture was made with.
func stateFrom(t *testing.T, name, user string) state {
	t.Helper()
	db := fixtureOpen(t, name)
	st, verr := readState(context.Background(), db, req(t, "mariadb.replication.status", map[string]any{"user": user}))
	if verr != nil {
		t.Fatalf("%s: %v", name, verr)
	}
	return st
}

// tableOf returns the section with this id, which must be a table; ok is
// false when the answer has no such section, which is itself a fact a test
// asserts.
func tableOf(sections view.Sections, id string) (view.Table, bool) {
	for _, it := range sections.Items {
		if it.ID == id {
			t, ok := it.View.(view.Table)
			return t, ok
		}
	}
	return view.Table{}, false
}

func pairsOf(sections view.Sections, id string) map[string]string {
	out := map[string]string{}
	for _, it := range sections.Items {
		if kv, ok := it.View.(view.KeyValue); ok && it.ID == id {
			for _, p := range kv.Pairs {
				out[p.Key] = p.Value
			}
		}
	}
	return out
}

func codesOf(sections view.Sections) []string {
	var out []string
	for _, w := range sections.Warnings {
		out = append(out, w.Code)
	}
	return out
}
