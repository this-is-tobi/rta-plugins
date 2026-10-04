package main

import (
	"strconv"
	"strings"
	"testing"
)

// unicodeUnescaped reads an SQL identifier or string back the way PostgreSQL
// does (U&"…", escape character a backslash) and returns what it names, so the
// tests prove the spelling is the same name and not only a different one that
// looks safe.
func unicodeUnescaped(t *testing.T, sql string) string {
	t.Helper()
	if !strings.HasPrefix(sql, "U&") || len(sql) < 4 {
		t.Fatalf("%q is no Unicode-escaped literal", sql)
	}
	quote := sql[2]
	if sql[len(sql)-1] != quote {
		t.Fatalf("%q is not closed", sql)
	}
	body, out := sql[3:len(sql)-1], strings.Builder{}
	for i := 0; i < len(body); {
		switch c := body[i]; {
		case c == quote:
			if i+1 >= len(body) || body[i+1] != quote {
				t.Fatalf("%q: a lone quote at %d", sql, i)
			}
			out.WriteByte(quote)
			i += 2
		case c == '\\' && i+1 < len(body) && body[i+1] == '\\':
			out.WriteByte('\\')
			i += 2
		case c == '\\' && i+1 < len(body) && body[i+1] == '+':
			n, err := strconv.ParseUint(body[i+2:i+8], 16, 32)
			if err != nil {
				t.Fatalf("%q: %v", sql, err)
			}
			out.WriteRune(rune(n))
			i += 8
		case c == '\\':
			n, err := strconv.ParseUint(body[i+1:i+5], 16, 32)
			if err != nil {
				t.Fatalf("%q: %v", sql, err)
			}
			out.WriteRune(rune(n))
			i += 5
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// What PostgreSQL reads back from the spelling is the name it was given,
// whatever the name holds.
func TestAnOddNameIsWrittenAsSQLReadsBackToTheSameName(t *testing.T) {
	for _, name := range []string{
		oddName,
		"a\"b\x1b",
		`back\slash` + "\n",
		"zero\u200bwidth",
		"hangul\u3164filler",
		"braille\u2800blank",
		"astral\U000e0041tag\n",
		"tab\there",
	} {
		got := sqlName(name)
		if !strings.HasPrefix(got, `U&"`) {
			t.Errorf("sqlName(%q) = %q, want a Unicode-escaped identifier", name, got)
			continue
		}
		if back := unicodeUnescaped(t, got); back != name {
			t.Errorf("sqlName(%q) = %q reads back as %q", name, got, back)
		}
		bare(t, "the identifier", got)
		for _, r := range got {
			if r < 0x20 || r > 0x7e {
				t.Errorf("sqlName(%q) = %q holds %U, which a reader cannot see", name, got, r)
			}
		}
	}
}

// A name that reads as itself is quoted exactly as it was, spaces, accents,
// quotes and edge spaces included: nothing a reader sees changes.
func TestAPlainNameIsQuotedAsItWas(t *testing.T) {
	for name, want := range map[string]string{
		"orders":     `"orders"`,
		"café table": `"café table"`,
		" edge ":     `" edge "`,
		`a"b`:        `"a""b"`,
		`"leading`:   `"""leading"`,
		`back\slash`: `"back\slash"`,
		"日本語":        `"日本語"`,
	} {
		if got := sqlName(name); got != want {
			t.Errorf("sqlName(%q) = %q, want %q", name, got, want)
		}
	}
	if got := sqlIdentifier("my schema é", "café table"); got != `"my schema é"."café table"` {
		t.Errorf("sqlIdentifier = %q", got)
	}
}

// A name that is not UTF-8 has no SQL escape: it is shown written out, as
// ListedName shows it, and is no longer SQL.
func TestANameThatIsNotUTF8IsShownWrittenOut(t *testing.T) {
	if got, want := sqlName("bad\xffbyte"), `"bad\xffbyte"`; got != want {
		t.Errorf("sqlName = %q, want %q", got, want)
	}
}

// The statements the server writes are rewritten only where a stranger's name
// is in them, and only that name.
func TestAServersStatementIsRewrittenOnlyWhereANameIsOdd(t *testing.T) {
	plain := []string{
		`CREATE INDEX orders_idx ON public.orders USING btree (id)`,
		`CREATE UNIQUE INDEX "my index é" ON public."café table" USING btree ("my col é" DESC) WITH (fillfactor='70')`,
		`FOREIGN KEY (a) REFERENCES public.other(id) ON DELETE CASCADE`,
		`PRIMARY KEY ("a""b", c)`,
	}
	for _, sql := range plain {
		if got := writtenOut(sql); got != sql {
			t.Errorf("writtenOut(%q) = %q, want it untouched", sql, got)
		}
	}
	odd := "CREATE UNIQUE INDEX \"idx\x1b[1m\nx\" ON \"sch\x1bema\".\"café table\" USING btree (\"col\n\" DESC, b) WITH (note='a\x1bb')"
	want := `CREATE UNIQUE INDEX U&"idx\001b[1m\000ax" ON U&"sch\001bema"."café table" USING btree (U&"col\000a" DESC, b) WITH (note=U&'a\001bb')`
	if got := writtenOut(odd); got != want {
		t.Errorf("writtenOut = %q, want %q", got, want)
	}
	bare(t, "the statement", writtenOut(odd))
	// A name holding the quote it is written in is doubled inside the escape.
	if got, want := writtenOut("PRIMARY KEY (\"a\"\"\x1b\")"), `PRIMARY KEY (U&"a""\001b")`; got != want {
		t.Errorf("writtenOut = %q, want %q", got, want)
	}
	// An unterminated quote is left as the server wrote it.
	if got := writtenOut("x \"never closed\x1b"); got != "x \"never closed\x1b" {
		t.Errorf("writtenOut = %q, want an unterminated quote left alone", got)
	}
}

// The schema description is read as SQL, so an odd name in it cannot be
// ListedName's spelling: it is SQL's own escape, in every place the name
// appears — the statements this plugin writes and the ones the server wrote
// for the indexes and keys. No escape sequence reaches the renderer, and no
// name makes a line of its own.
func TestTheSchemaDescriptionWritesAnOddNameAsSQLsOwnEscape(t *testing.T) {
	tables := []schemaTable{{
		name: oddName,
		columns: []schemaColumn{
			{name: "id", typ: "bigint", notNull: true},
			{name: oddName, typ: "text"},
			{name: "my col é", typ: "text"},
		},
		keys:    []string{"PRIMARY KEY (id)"},
		indexes: []string{"CREATE INDEX \"idx\x1b[1m\nx\" ON public.\"esc\x1b[31mred\nline\" USING btree (\"esc\x1b[31mred\nline\")"},
		foreign: []string{"FOREIGN KEY (\"esc\x1b[31mred\nline\") REFERENCES public.\"esc\x1b[31mred\nline\"(id)"},
	}, {name: plainName, columns: []schemaColumn{{name: "id", typ: "bigint"}}}}
	body := renderDDL(req(t, map[string]any{"database": "app", "host": "db.internal", "port": 5432}),
		oddName, tables, dropped{})

	for _, line := range strings.Split(body, "\n") {
		if strings.ContainsAny(line, "\x1b") {
			t.Errorf("an escape sequence reaches the description: %q", line)
		}
	}
	const odd = `U&"esc\001b[31mred\000aline"`
	for _, want := range []string{
		`CREATE TABLE ` + odd + `.` + odd + ` (`,
		"    " + odd + " text",
		`    "my col é"`,
		`CREATE INDEX U&"idx\001b[1m\000ax" ON public.` + odd + ` USING btree (` + odd + `);`,
		`ALTER TABLE ` + odd + `.` + odd + ` ADD FOREIGN KEY (` + odd +
			`) REFERENCES public.` + odd + `(id);`,
		`CREATE TABLE U&"esc\001b[31mred\000aline"."café table" (`,
		`-- schema ` + oddShown + ` of app on db.internal:5432`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("description lacks %q:\n%s", want, body)
		}
	}
	// Every line of the description is one statement's line or one comment's:
	// no name makes a line of its own.
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "--") || strings.HasPrefix(trim, "CREATE ") ||
			strings.HasPrefix(trim, "ALTER ") || strings.HasPrefix(trim, ");") || strings.HasPrefix(trim, "\"") ||
			strings.HasPrefix(trim, "U&\"") || strings.HasPrefix(trim, "PRIMARY KEY") {
			continue
		}
		t.Errorf("a line that is no statement's and no comment's: %q", line)
	}
}

// And a name that reads as itself is written exactly as it always was.
func TestTheSchemaDescriptionKeepsAPlainNameAsItWas(t *testing.T) {
	tables := []schemaTable{{
		name:    plainName,
		columns: []schemaColumn{{name: "my col é", typ: "text"}, {name: `a"b`, typ: "text"}},
		keys:    []string{`PRIMARY KEY ("my col é")`},
	}}
	body := renderDDL(req(t, map[string]any{"database": "app", "host": "db.internal", "port": 5432}),
		"my schema é", tables, dropped{})
	for _, want := range []string{
		`-- schema "my schema é" of app on db.internal:5432`,
		`CREATE TABLE "my schema é"."café table" (`,
		`    PRIMARY KEY ("my col é")`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("description lacks %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, `"a""b"`) || !strings.Contains(body, `"my col é"`) {
		t.Errorf("description lacks the columns as they were:\n%s", body)
	}
}
