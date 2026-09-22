package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The read tier of this plugin describes the database and hands back nothing
// stored in it. Everything in this file stays inside that line: table names,
// column names, types, keys and sizes are all things somebody declared rather
// than things somebody entered.
//
// The line matters because it is what makes `--allow-read` worth granting. An
// agent with nothing but the read tier can tell you what this database is,
// what is in it and what shape it has, and cannot return one row of it.

// schemaField names the database to describe. It falls back to the connection's
// own `database` rather than defaulting to a name this plugin invented, so the
// common case — one database in the config — needs no argument at all.
func schemaField() plugin.Field {
	return plugin.Field{Name: "schema", Type: plugin.String, Positional: true, Default: "",
		Help: "database to describe (defaults to the connected one)",
		Live: true, Suggest: suggestDatabases}
}

// schemaOf resolves which database a call is about, refusing rather than
// guessing when neither the flag nor the connection names one. Guessing here
// would mean silently describing `mysql` or `information_schema`, which is a
// confidently wrong answer to a question nobody asked.
func schemaOf(req plugin.Request) (string, *view.Error) {
	if s := req.String("schema"); s != "" {
		return s, nil
	}
	if s := req.String("database"); s != "" {
		return s, nil
	}
	return "", view.Errorf("mysql.schema.unset", "no database named").
		WithHint("pass one as the argument, or set `database` in this plugin's config section")
}

func tableListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "mysql.table.list",
		Summary:    "List tables with their row estimates and sizes",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Names, engines, row estimates and on-disk sizes for one database.\n\n" +
			"The row counts are estimates the storage engine keeps, not COUNT(*). InnoDB's can " +
			"be off by a wide margin on a busy table — they are for finding the big one, and a " +
			"number that has to be right needs mysql.query.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withDB(ctx, req, func(ctx context.Context, db *sql.DB) (view.View, error) {
				return tableTable(ctx, db, req)
			})
		},
	}, schemaField(),
		plugin.Field{Name: "limit", Type: plugin.Int, Config: "limit", Default: 200, Min: 1, Max: 10000,
			Help: "how many tables to show"})
}

func tableTable(ctx context.Context, db *sql.DB, req plugin.Request) (view.View, error) {
	schema, verr := schemaOf(req)
	if verr != nil {
		return nil, verr
	}
	rows, err := db.QueryContext(ctx, `
		SELECT TABLE_NAME, TABLE_TYPE, COALESCE(ENGINE,''), COALESCE(TABLE_ROWS,0),
		       COALESCE(DATA_LENGTH,0) + COALESCE(INDEX_LENGTH,0)
		  FROM INFORMATION_SCHEMA.TABLES
		 WHERE TABLE_SCHEMA = ?
		 ORDER BY 5 DESC
		 LIMIT ?`, schema, req.Int("limit"))
	if err != nil {
		return nil, classify(err, req)
	}
	defer func() { _ = rows.Close() }()

	t := view.Table{Columns: []view.Column{
		{Name: "Table"},
		{Name: "Type"},
		{Name: "Engine"},
		{Name: "Rows", Kind: view.KindNumber},
		{Name: "Size", Kind: view.KindBytes},
	}}
	for rows.Next() {
		var name, typ, engine string
		var estRows int64
		var size any
		if err := rows.Scan(&name, &typ, &engine, &estRows, &size); err != nil {
			return nil, classify(err, req)
		}
		// "BASE TABLE" is the standard's word and nobody's. A view is the
		// distinction worth keeping, and it is the only other value here.
		if typ == "BASE TABLE" {
			typ = "table"
		} else {
			typ = strings.ToLower(typ)
		}
		t.Rows = append(t.Rows, []string{name, typ, engine, strconv.FormatInt(estRows, 10), bytesCell(size)})
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, req)
	}
	t.Total = len(t.Rows)
	if t.Total > 0 && !schemaFullyVisible(ctx, db, schema) {
		t.Warnings = append(t.Warnings, partialListing("mysql.table.partial", schema, "this list"))
	}
	if t.Total == 0 {
		// An empty result is ambiguous between "no such database" and "a
		// database with nothing in it", and the two need different next steps.
		var exists int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME = ?`, schema).
			Scan(&exists); err == nil && exists == 0 {
			return nil, view.Errorf("mysql.database.notfound", "no database %q, or none this user may see", schema).
				WithHint("`rta mysql database list` shows what is there")
		}
	}
	return t, nil
}

// maxSchemaTables bounds how much of a schema one call expands. A tree with a
// thousand tables in it is not something anybody reads — past that the way to
// find something is to name the table.
const maxSchemaTables = 500

func schemaCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "mysql.schema",
		Summary:    "Describe a database's tables, columns and keys — no values",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "The shape of a database as a tree: every table, its columns with their " +
			"types and nullability, and which of them are keys.\n\n" +
			"Names and types only, never a value. That is what keeps it in the read tier — an " +
			"agent that can describe a database still cannot read one row of it, and mysql.query " +
			"is where rows live.\n\n" +
			"Name one table to expand only that one, which is also how to see a database too " +
			"large to draw whole.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withDB(ctx, req, func(ctx context.Context, db *sql.DB) (view.View, error) {
				return schemaTree(ctx, db, req)
			})
		},
	}, schemaField(),
		plugin.Field{Name: "table", Type: plugin.String, Default: "",
			Help: "expand only this table", Live: true, Suggest: suggestTables},
		plugin.Field{Name: "limit", Type: plugin.Int, Config: "limit", Default: 100, Min: 1, Max: maxSchemaTables,
			Help: "how many tables to expand"})
}

type column struct {
	name     string
	dataType string
	nullable bool
	key      string
	extra    string
}

// schemaFullyVisible reports whether this account holds a privilege wide
// enough that INFORMATION_SCHEMA cannot be hiding tables from it.
//
// **INFORMATION_SCHEMA lists only the tables the account holds some
// privilege on.** A reporting account with SELECT on six tables of twenty
// sees exactly six, with no error and no marker — and `SHOW TABLES` is
// filtered identically, so the gap cannot be measured from inside MySQL at
// all. The question therefore has to be asked from the other side: does
// this account hold something that covers the whole schema? A global grant
// (`ON *.*`) or a schema-wide one (“ ON `db`.* “) does; a list of
// per-table grants does not.
//
// A privilege reached through a role is not expanded by SHOW GRANTS, so an
// account whose privileges all arrive that way reads as narrow here and
// gets the caveat. That is the right way to be wrong: the alternative is a
// listing that states it is complete when it is not.
//
// A probe that cannot run says nothing. Its failure is evidence about the
// probe, not about whether tables are hidden, and a caveat driven by it
// would fire on servers that are not hiding anything.
func schemaFullyVisible(ctx context.Context, db *sql.DB, schema string) bool {
	rows, err := db.QueryContext(ctx, `SHOW GRANTS FOR CURRENT_USER()`)
	if err != nil {
		return true
	}
	defer func() { _ = rows.Close() }()
	wide := []string{"ON *.*", "ON `" + schema + "`.*", "ON " + schema + ".*"}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return true
		}
		// `GRANT USAGE ON *.*` is the baseline line every account has and it
		// means no privileges at all. Matching it on the scope alone would
		// read every account on every server as wide, and this caveat would
		// never fire once — which is how a guard passes its own tests and
		// protects nothing.
		if strings.HasPrefix(line, "GRANT USAGE ON ") {
			continue
		}
		for _, w := range wide {
			if strings.Contains(line, w) {
				return true
			}
		}
	}
	return rows.Err() != nil
}

// partialListing is the caveat both listings carry when the grants may be
// hiding tables, so the two cannot come to word it differently.
func partialListing(code, schema, what string) view.Error {
	return view.Error{
		Code: code,
		Message: "this account holds per-table privileges, and INFORMATION_SCHEMA lists only the " +
			"tables it holds one on — there may be tables in " + schema + " missing from " + what,
		Hint: "MySQL offers no way to count what it filtered out; a schema-wide grant " +
			"(GRANT SELECT ON `" + schema + "`.*) makes the listing complete",
	}
}

func schemaTree(ctx context.Context, db *sql.DB, req plugin.Request) (view.View, error) {
	schema, verr := schemaOf(req)
	if verr != nil {
		return nil, verr
	}

	// One query for every column of every table, rather than one query per
	// table. A schema with two hundred tables would otherwise cost two hundred
	// round trips to draw, which is the difference between a call somebody
	// makes and one they learn to avoid.
	args := []any{schema}
	where := "c.TABLE_SCHEMA = ?"
	if only := req.String("table"); only != "" {
		where += " AND c.TABLE_NAME = ?"
		args = append(args, only)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.TABLE_NAME, c.COLUMN_NAME, c.COLUMN_TYPE, c.IS_NULLABLE,
		       COALESCE(c.COLUMN_KEY,''), COALESCE(c.EXTRA,'')
		  FROM INFORMATION_SCHEMA.COLUMNS c
		 WHERE `+where+`
		 ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION`, args...)
	if err != nil {
		return nil, classify(err, req)
	}
	defer func() { _ = rows.Close() }()

	// Insertion order is kept alongside the map, because the query already
	// sorted by table name and rebuilding that order from a map would mean
	// sorting the same data twice.
	byTable := map[string][]column{}
	var order []string
	for rows.Next() {
		var table string
		var c column
		var nullable string
		if err := rows.Scan(&table, &c.name, &c.dataType, &nullable, &c.key, &c.extra); err != nil {
			return nil, classify(err, req)
		}
		c.nullable = nullable == "YES"
		if _, seen := byTable[table]; !seen {
			order = append(order, table)
		}
		byTable[table] = append(byTable[table], c)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, req)
	}

	if len(order) == 0 {
		if only := req.String("table"); only != "" {
			return nil, view.Errorf("mysql.table.notfound", "no table %q in %q", only, schema).
				WithHint("`rta mysql table list " + schema + "` shows what is there")
		}
		return nil, view.Errorf("mysql.database.empty", "%q has no tables, or none this user may see", schema).
			WithHint("`rta mysql database list` shows what is there")
	}

	limit := req.Int("limit")
	root := view.Node{Label: schema}
	for i, table := range order {
		if i == limit {
			root.Children = append(root.Children, view.Node{
				Label:  "…",
				Detail: fmt.Sprintf("%d more tables; raise --limit or name one with --table", len(order)-i),
			})
			break
		}
		cols := byTable[table]
		node := view.Node{
			Label:  table,
			Detail: format.CountOf(len(cols), "column"),
		}
		for _, c := range cols {
			node.Children = append(node.Children, view.Node{Label: c.name, Detail: columnDetail(c)})
		}
		root.Children = append(root.Children, node)
	}
	root.Detail = format.CountOf(len(order), "table")
	tree := view.Tree{Roots: []view.Node{root}}
	// A Tree has nowhere to carry a caveat, so one is wrapped the way
	// plugins/kube's quotaView wraps its table: the shape changes only when
	// there is something to say, and what it says is the reason.
	//
	// Only for the whole-schema view. With --table naming one table that was
	// found, nothing about this answer is partial.
	if req.String("table") == "" && !schemaFullyVisible(ctx, db, schema) {
		return view.Sections{
			Items:    []view.Section{{ID: "schema", Title: schema, View: tree}},
			Warnings: []view.Error{partialListing("mysql.schema.partial", schema, "this shape")},
		}, nil
	}
	return tree, nil
}

// columnDetail renders everything about a column except its contents. The key
// marker comes first because it is what somebody scanning a schema is looking
// for, and "not null" is stated rather than its opposite because the default
// in SQL is nullable and the constraint is the news.
func columnDetail(c column) string {
	parts := []string{c.dataType}
	switch c.key {
	case "PRI":
		parts = append(parts, "primary key")
	case "UNI":
		parts = append(parts, "unique")
	case "MUL":
		parts = append(parts, "indexed")
	}
	if !c.nullable {
		parts = append(parts, "not null")
	}
	if c.extra != "" {
		parts = append(parts, c.extra)
	}
	return strings.Join(parts, ", ")
}
