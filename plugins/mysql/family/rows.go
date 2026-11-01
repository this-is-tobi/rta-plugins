package family

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// ErrTooManyRows and ErrTooLarge are the two ways a result set refuses to
// become a table.
//
// Both are refusals rather than truncations, and that is the decision worth
// stating: a shortened result set is a different answer wearing the right
// shape. Somebody who asked for every row in a table and silently got the
// first fifty has been told something false about their data, and nothing in
// the output says so. Refusing names the bound and names the flag.
var (
	ErrTooManyRows = errors.New("too many rows")
	ErrTooLarge    = errors.New("result too large")
)

// maxBytes bounds a result by size as well as by count, because a row bound
// is not a size bound. One wide TEXT or BLOB column is what actually exhausts
// memory here — fifty rows of it is not fifty rows of anything else.
//
// **One mebibyte, as plugins/pg's, and well under what the host receives.** Over
// MCP the host refuses an answer of more than 8 MiB while it is still being
// received (core.result.toolarge), under a hint that names no column, and what
// it counts is the answer on the wire: the text of every cell and two bytes more
// for each, a tag and a length. This bound counted the text alone and stood at
// that same 8 MiB, so a result of between about 7.8 MiB of text and the limit
// passed here and was refused there, and the sentence below that says to select
// fewer columns, which is the one that helps, was not the one read. A mebibyte of
// text weighs at most about three on the wire, in cells of one byte, so a result
// this lets through is always one the host receives and the refusal here is
// always the one that is read. The bridge cuts what a model is shown to its
// first rows long before either, so the bytes between are cost, not content.
// The SQL plugins answer to one number because the rows a caller's own
// statement returns are one concept.
const maxBytes = 1 << 20

// rowsToTable renders a result set, bounded in both directions.
//
// The scan target is []any of *any, which is how database/sql hands back a
// value whose type is only known at runtime. The driver gives []byte for most
// string-ish columns, so cell below is where every type becomes text exactly
// once rather than at each of the seven call sites.
func rowsToTable(rows *sql.Rows, limit int) (view.Table, error) {
	names, err := rows.Columns()
	if err != nil {
		return view.Table{}, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return view.Table{}, err
	}

	t := view.Table{Columns: make([]view.Column, len(names))}
	for i, n := range names {
		t.Columns[i] = view.Column{Name: listed(n), Kind: kindOf(types[i])}
	}

	size := 0
	for rows.Next() {
		if len(t.Rows) == limit {
			// Drained before returning: leaving a result set open holds the
			// connection, and this one is about to be handed an error rather
			// than closed by a happy path.
			return view.Table{}, ErrTooManyRows
		}
		scan := make([]any, len(names))
		holders := make([]any, len(names))
		for i := range scan {
			holders[i] = &scan[i]
		}
		if err := rows.Scan(holders...); err != nil {
			return view.Table{}, err
		}
		row := make([]string, len(names))
		for i, v := range scan {
			row[i] = cell(v)
			size += len(row[i])
		}
		if size > maxBytes {
			return view.Table{}, ErrTooLarge
		}
		t.Rows = append(t.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return view.Table{}, err
	}
	t.Total = len(t.Rows)
	return t, nil
}

// listed is a name the server handed back, as a row or a label shows it
// (plugin.ListedName): as it is when it reads as itself, and otherwise quoted,
// with each character a reader would not see written out.
//
// **A name here is somebody else's choice, and cleaning it would show another
// name.** A table, a column or an account is named by whoever created it, and
// the renderer strips an escape sequence from a cell on its way to a terminal:
// a table called `esc` ESC `[31mred` came out as `escred`, which is a table
// that may well exist beside it, and a newline in a name split its row in two.
// Quoted, the one place an odd name differs from an ordinary one is the quotes.
//
// Nothing stays nothing. An empty value here is a NULL — no database selected,
// no user, no host — and ListedName draws an empty name as "", which would
// show a session that selected no database as one whose database is called
// nothing.
func listed(name string) string {
	if name == "" {
		return ""
	}
	return plugin.ListedName(name)
}

// cell turns one scanned value into text.
//
// NULL renders as an empty cell rather than the word "NULL", because a table
// somebody reads should not make them wonder whether a column literally holds
// that string — and the two are genuinely indistinguishable once printed.
func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case time.Time:
		return x.Format("2006-01-02 15:04:05")
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// kindOf maps a column's declared database type onto the view kind that
// decides alignment and formatting. Getting this from the driver rather than
// from the value means an all-NULL numeric column still right-aligns.
func kindOf(ct *sql.ColumnType) view.ColumnKind {
	switch name := strings.ToUpper(ct.DatabaseTypeName()); name {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT",
		"UNSIGNED TINYINT", "UNSIGNED SMALLINT", "UNSIGNED INT", "UNSIGNED BIGINT",
		"DECIMAL", "FLOAT", "DOUBLE", "NEWDECIMAL":
		return view.KindNumber
	case "DATE", "DATETIME", "TIMESTAMP":
		return view.KindTimestamp
	default:
		return ""
	}
}

// bytesCell is a byte count that arrived as an untyped scan value, which is
// what INFORMATION_SCHEMA size expressions produce, shown as format.Bytes
// writes one and valued as the count. A size column nobody can read at a
// glance is a column that gets piped into another tool instead of being looked
// at, and the count beside it is what that tool wanted. A NULL or a negative
// count is "-", with no value.
func bytesCell(v any) view.Cell {
	switch x := v.(type) {
	case nil:
		return view.Plain("-")
	case int64:
		if x < 0 {
			return view.Plain("-")
		}
		return view.Bytes(x)
	case uint64:
		// An UNSIGNED BIGINT, such as DATA_LENGTH + INDEX_LENGTH, as the text
		// protocol hands it over. No size query here travels that way today:
		// each takes arguments, so the driver prepares it and the binary
		// protocol answers with an int64. A query written without them would,
		// and its sizes would print as bare digits.
		return view.Bytes(x)
	case []byte:
		n, err := strconv.ParseInt(string(x), 10, 64)
		if err != nil || n < 0 {
			return view.Plain("-")
		}
		return view.Bytes(n)
	case float64:
		if x < 0 {
			return view.Plain("-")
		}
		return view.Bytes(uint64(x))
	default:
		return view.Plain(fmt.Sprint(x))
	}
}
