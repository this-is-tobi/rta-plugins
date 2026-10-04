package main

import (
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Names a stranger can choose, and how this plugin shows them.
//
// **Whoever may create a role, a database, a schema, a table or a column
// chooses its name, and PostgreSQL takes whatever a quoted identifier holds**:
// an escape sequence, a newline, a zero-width space. A renderer strips control
// characters on the way to a terminal, which is right of a body and wrong of a
// name — a table called `orders<ESC>[2K` came out as `orders`, an ordinary
// name another table could also have, and a newline in one split a row in two.
// So a name read off the server goes through plugin.ListedName before it is a
// cell, a pair or a part of a sentence: as it is when it reads as itself,
// spaces and accents included, and otherwise quoted with each character a
// reader would not see written out.
//
// What is deliberately not passed through it, so the next reader does not
// wonder whether it was forgotten:
//
//   - A value stored in a row. pg.query and pg.table.dump return data, a body
//     the renderer already cleans, and a multi-line value is ordinary there;
//     quoting each one would rewrite every cell that holds a newline.
//   - A session's application name and a slot's name. The server writes the
//     first as printable ASCII, hex-escaping the rest, before it keeps it (an
//     ESC arrives as the four characters \x1b), and only lower-case letters,
//     digits and the underscore are accepted in the second. Neither can hold
//     what ListedName exists for, and an empty application name is a session
//     that gave none, which a quoted pair of quotes would read as a name.
//   - What the person at the keyboard or in the config typed and the call
//     echoes back (the database, the role, the table or schema asked for), and
//     the standby's own settings (its upstream host, synchronous_standby_names)
//     which only an administrator of this server writes. They are `%q`-quoted
//     where a sentence names them.
//   - A line of pg_dump's, pg_restore's or psql's own stderr, which is a
//     sentence the tool wrote, not a name to pick out of it.
//   - A command a hint hands to the reader (the SQL beside a policy lookup, a
//     grant to run): it is pasted, so it is quoted the way a shell or SQL
//     reads it.

// listNames shows the cells of columns, which hold names the server returned,
// as ListedName shows them. An empty cell is NULL here, absent and not a name
// written as nothing, which ListedName would draw as a pair of quotes.
func listNames(t *view.Table, columns ...int) {
	for _, row := range t.Rows {
		for _, c := range columns {
			if c < len(row) && row[c] != "" {
				row[c] = plugin.ListedName(row[c])
			}
		}
	}
}

// listedNames is names as a sentence lists them, each shown as ListedName shows
// it.
func listedNames(names []string) string {
	shown := make([]string, len(names))
	for i, n := range names {
		shown[i] = plugin.ListedName(n)
	}
	return strings.Join(shown, ", ")
}

// quotedName is a name the server returned inside a sentence that quotes it:
// `%q`'s spelling for one that reads as itself, which is how every message
// here wrote it, and ListedName's for one that does not, which also writes out
// what `%q` leaves raw because strconv counts it printable — a Hangul filler,
// a Braille blank, a variation selector draw as nothing.
func quotedName(s string) string {
	if listed := plugin.ListedName(s); listed != s {
		return listed
	}
	return strconv.Quote(s)
}
