package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// readsAsItself reports whether s shows as itself inside quotes. Asked of the
// name between two letters, so that a space at either end or a leading quote,
// which ListedName quotes a bare name for, is not mistaken for a character
// that cannot be seen: inside the quotes of an SQL identifier both are plain.
func readsAsItself(s string) bool {
	between := "x" + s + "x"
	return plugin.ListedName(between) == between
}

// sqlIdentifier is the parts of a qualified name as a statement writes them.
//
// **A name that reads as itself is quoted as pgx quotes it, and one that does
// not is written out in SQL's own escape for it**, U&"…" with each such
// character as \XXXX or \+XXXXXX. ListedName's spelling cannot stand here:
// `"a\nb"` in an identifier is a name with a backslash and an n in it, a
// different table, and the DDL is read as SQL. A raw newline was valid and
// split the line a reader takes a statement from, and a raw escape sequence
// reached the renderer, which strips it. This is what PostgreSQL parses back
// to the same name, and what a reader can see.
func sqlIdentifier(parts ...string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = sqlName(p)
	}
	return strings.Join(quoted, ".")
}

func sqlName(name string) string {
	if readsAsItself(name) {
		return pgx.Identifier{name}.Sanitize()
	}
	return unicodeEscaped(name, '"')
}

// unicodeEscaped is s as an SQL identifier (quote is a double quote) or string
// (a single one) written with Unicode escapes for every character that does not
// read as itself. Bytes that are not UTF-8, which only a database that is not
// held in UTF-8 can hold, have no escape: those are ListedName's, which writes
// them out as \xNN and is no longer SQL, so that what the name holds is still
// shown.
func unicodeEscaped(s string, quote byte) string {
	if !utf8.ValidString(s) {
		return plugin.ListedName(s)
	}
	var b strings.Builder
	b.WriteString("U&")
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote):
			b.WriteByte(quote)
			b.WriteByte(quote)
		case r == '\\':
			b.WriteString(`\\`)
		case readsAsItself(string(r)):
			b.WriteRune(r)
		case r > 0xffff:
			fmt.Fprintf(&b, `\+%06x`, r)
		default:
			fmt.Fprintf(&b, `\%04x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// writtenOut is a statement the server generated (pg_get_indexdef,
// pg_get_constraintdef) with each identifier or string in it that holds a
// character that does not read as itself rewritten as unicodeEscaped does, and
// everything else as it came.
//
// The server quotes any name that is not plain lower case, so a name holding an
// escape sequence or a newline is always inside a pair of quotes, and a name
// that does not is never touched: a statement with nothing odd in it comes back
// unchanged, which is every statement but the one a stranger wrote a name into.
func writtenOut(sql string) string {
	if readsAsItself(sql) {
		return sql
	}
	var out strings.Builder
	for i := 0; i < len(sql); {
		quote := sql[i]
		if quote != '"' && quote != '\'' {
			out.WriteByte(quote)
			i++
			continue
		}
		var inner strings.Builder
		j := i + 1
		for j < len(sql) {
			if sql[j] == quote {
				if j+1 < len(sql) && sql[j+1] == quote {
					inner.WriteByte(quote)
					j += 2
					continue
				}
				break
			}
			inner.WriteByte(sql[j])
			j++
		}
		if j >= len(sql) || readsAsItself(inner.String()) {
			out.WriteString(sql[i:min(j+1, len(sql))])
		} else {
			out.WriteString(unicodeEscaped(inner.String(), quote))
		}
		i = j + 1
	}
	return out.String()
}
