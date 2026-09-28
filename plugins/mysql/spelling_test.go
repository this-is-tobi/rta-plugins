package main

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/spelling"
)

// This file is the same in every plugin module, and nothing in it names the
// plugin it sits in: each copy reads its own module's Plugin() and source,
// and TestEveryCopyOfTheSpellerIsTheSame holds the copies to one another.
//
// The speller is the SDK's (pkg/sdk/spelling), the one sdktest.Check holds
// what a plugin declares to and rta's own tests hold the host's text to, so
// the rules a hint is read by are the same wherever it is read. What a
// plugin declares is Check's to read (RuleSpelling), and every plugin's
// main_test.go calls it. What is left here is what Check cannot do: read
// the source, for the sentences a handler words at run time and the calls
// it names through a naming helper — Check is handed a plugin.Plugin, and a
// declaration holds no source. A copy rather than an import, because a
// plugin module reaches nothing but rta's released SDK, and the SDK exports
// no source scan.

// sentence is the text of one string the source spells out, and where it
// starts.
type sentence struct {
	pos  token.Pos
	text string
}

// sentences returns every string the source spells out: a literal on its
// own, and a sum of literals and other operands as one sentence, with
// spelling.Operand in each other operand's place. One sentence rather than
// its pieces, because a code span opened in one literal and closed in the
// next — "`createdb --host=" + host + "`" — is only a span read whole, and
// read piece by piece the flag inside it looked like prose.
//
// What plugin.AskOperator is given is left out: the command it hands the
// operator is the one command line an agent may read.
func sentences(f *ast.File) []sentence {
	var out []sentence
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "AskOperator" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "plugin" {
					return false
				}
			}
		case *ast.BinaryExpr:
			if n.Op != token.ADD {
				return true
			}
			var parts []ast.Expr
			var flatten func(e ast.Expr)
			flatten = func(e ast.Expr) {
				if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
					flatten(b.X)
					flatten(b.Y)
					return
				}
				parts = append(parts, e)
			}
			flatten(n)
			var text strings.Builder
			spelled := false
			for _, p := range parts {
				if lit, ok := p.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, err := strconv.Unquote(lit.Value)
					if err == nil {
						text.WriteString(s)
						spelled = true
						continue
					}
				}
				text.WriteString(spelling.Operand)
				ast.Inspect(p, visit)
			}
			if spelled {
				out = append(out, sentence{n.Pos(), text.String()})
			}
			return false
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				if s, err := strconv.Unquote(n.Value); err == nil {
					out = append(out, sentence{n.Pos(), s})
				}
			}
		}
		return true
	}
	ast.Inspect(f, visit)
	return out
}

// spelledForATerminal returns the sentences in f that spell something only
// a terminal could act on, each with what was found in it.
//
// A sentence is text with a space in it: an argument handed to kubectl or
// pg_dump is a literal too, "--namespace=" and "--no-owner", and is that
// program's spelling.
//
// Each is read as text only a terminal reads (Find's terminalOnly), because
// a handler words one of rta's own commands — `rta doctor`, `rta explain` —
// in the branch that asked its surface for the CLI, and no scan of the
// source can see which branch a literal sits in. A command in a namespace,
// this plugin's or a built-in's, is held all the same: the naming helpers
// spell any capability for whichever surface reads it, `rta net dns` among
// them, so a handler has no reason to write one out.
func spelledForATerminal(sp spelling.Speller, f *ast.File) []held {
	var out []held
	for _, s := range sentences(f) {
		if !strings.Contains(s.text, " ") {
			continue
		}
		if hits := sp.Find(s.text, true); len(hits) > 0 {
			out = append(out, held{s, hits})
		}
	}
	return out
}

// held is a sentence, and what in it only a terminal could act on.
type held struct {
	sentence
	hits []string
}

// parseSource parses every file of this module that is not a test.
func parseSource(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return fset, files
}

// The two scans this file adds to the SDK's speller, held to what they exist
// to catch and what they must let through: a test that passes because its
// scanner sees nothing guards nothing. Against a plugin of its own, so the
// cases read the same in every module this file is copied into. The
// speller's own rules are pkg/sdk/spelling's to hold, and are not repeated
// here.
func TestTheSourceScansCatchWhatTheyExistFor(t *testing.T) {
	demo := plugin.Plugin{Name: "demo", Capabilities: []plugin.Capability{
		{ID: "demo.key.list", Inputs: []plugin.Field{{Name: "limit"}, {Name: "jobs"}, {Name: "host"}}},
		{ID: "demo.key.get", Inputs: []plugin.Field{{Name: "key", Positional: true}}},
	}}

	src := "package main\n\nfunc hints(sf plugin.Surface, host, key string) []string {\n" +
		"\treturn []string{\n" +
		"\t\t\"a new database is worse. `createdb --host=\" + host + \" app` makes it\",\n" +
		"\t\t\"raise --limit to see more\",\n" +
		"\t\t\"remove it first: rta demo key rm \" + key + \" --host \" + host,\n" +
		"\t\tplugin.AskOperator(\"grant allow demo.key.list --limit 5\"),\n" +
		"\t\t\"--host=\" + host,\n" +
		"\t\t\"raise --\" + key + \" to see more\",\n" +
		"\t\tfmt.Sprintf(\"raise --%s to see more\", key),\n" +
		"\t\t\"`rta explain \" + key + \"` lists every input\",\n" +
		"\t\t\"`rta net dns \" + host + \"` shows what DNS returns\",\n" +
		"\t}\n}\n"
	f, err := parser.ParseFile(token.NewFileSet(), "hints.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range spelledForATerminal(spelling.ForPlugin(demo), f) {
		got = append(got, h.text)
	}
	// One of rta's own commands is let through, since a handler words it in
	// the CLI's branch; a built-in's command line in a span is not, though
	// the speller was given none of the built-ins.
	op := spelling.Operand
	want := []string{"raise --limit to see more", "remove it first: rta demo key rm " + op + " --host " + op,
		"raise --" + op + " to see more", "raise --%s to see more", "`rta net dns " + op + "` shows what DNS returns"}
	if !slices.Equal(got, want) {
		t.Errorf("held in source: %q, want %q", got, want)
	}

	calls := "package main\n\nfunc calls(sf plugin.Surface, k string) []string {\n" +
		"\treturn []string{\n" +
		"\t\tsf.Call(\"demo.key.get\", plugin.Arg{Name: \"key\", Value: k}),\n" +
		"\t\tsf.Call(\"demo.key.list\", plugin.Arg{Name: \"limit\", Value: 5, Positional: true}),\n" +
		"\t\tsf.CapabilityWith(\"demo.key.list\", \"depth\"),\n" +
		"\t\tsf.CapabilityWith(\"demo.key.get\", \"key\"),\n" +
		"\t\tsf.CapabilityName(\"demo.key.rm\"),\n" +
		"\t\tsf.Call(\"demo.key.get\", plugin.Arg{Name: \"key\", Value: k, Positional: true}),\n" +
		"\t\tsf.CapabilityWith(\"demo.key.list\", \"limit\", \"detail\"),\n" +
		"\t\tsf.Call(\"net.dns\", plugin.Arg{Name: \"name\", Value: k}),\n" +
		"\t\tsf.CapabilityWith(\"demo.key.list\", \"server\"),\n" +
		"\t\tsf.Call(\"demo.key.list\", plugin.Arg{Name: \"output\", Value: \"json\"}),\n" +
		"\t}\n}\n"
	fset := token.NewFileSet()
	f, err = parser.ParseFile(fset, "calls.go", calls, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var wrong []int
	for _, c := range namedCalls(f) {
		if len(callProblems(demo, c)) > 0 {
			wrong = append(wrong, fset.Position(c.pos).Line)
		}
	}
	// --output is the host's, given to every capability's command; --server
	// is a switch of rta's own commands, which no capability's is given, so
	// a call handing one over names an input the command refuses.
	if want := []int{5, 6, 7, 8, 9, 13}; !slices.Equal(wrong, want) {
		t.Errorf("calls held on lines %v, want %v", wrong, want)
	}
}

// What a handler words at run time names a capability and an input through
// the request's surface, so no sentence this plugin's source spells out holds
// a command line or a flag. Source rather than the answers themselves,
// because most of what a handler says is worded about a connection that
// failed or a server's answer, which no test here can provoke without the
// server — and "raise --limit" is wrong whichever surface reads it.
//
// What this cannot see is a built-in's command line in running prose — "run
// rta net dns" — since only rta's registry says which words after "rta" are
// a namespace, and a plugin has none to ask. In a code span it is held
// whoever declared it, a span opening on "rta " being a command line
// whatever follows.
func TestNoSentenceInTheSourceSpellsForOneSurface(t *testing.T) {
	sp := spelling.ForPlugin(Plugin())
	fset, files := parseSource(t)
	for _, f := range files {
		for _, h := range spelledForATerminal(sp, f) {
			for _, hit := range h.hits {
				t.Errorf("%s spells a terminal's: …%s…", fset.Position(h.pos), hit)
			}
		}
	}
}

// namingHelpers are the plugin.Surface methods that name a capability by the
// ID they are given first.
var namingHelpers = map[string]bool{"CapabilityName": true, "CapabilityWith": true, "Call": true}

// namedCall is a capability a naming helper was given as a literal, with the
// inputs named beside it: CapabilityWith's by name, and Call's as the
// plugin.Arg literals written in the call, each by its place or as a flag.
type namedCall struct {
	pos    token.Pos
	helper string
	id     string
	inputs []namedInput
}

type namedInput struct {
	name       string
	positional bool
}

// namedCalls returns every call to a naming helper in f whose capability is a
// literal. What is not one — an ID in a variable, the Args of a slice built
// beforehand — is left out rather than guessed at.
func namedCalls(f *ast.File) []namedCall {
	var out []namedCall
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !namingHelpers[sel.Sel.Name] {
			return true
		}
		id, ok := stringLit(call.Args[0])
		if !ok {
			return true
		}
		c := namedCall{pos: call.Pos(), helper: sel.Sel.Name, id: id}
		for _, a := range call.Args[1:] {
			if name, ok := stringLit(a); ok {
				c.inputs = append(c.inputs, namedInput{name: name})
			} else if in, ok := argLit(a); ok {
				c.inputs = append(c.inputs, in)
			}
		}
		out = append(out, c)
		return true
	})
	return out
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// argLit reads a plugin.Arg literal's Name, and whether it says Positional.
func argLit(e ast.Expr) (namedInput, bool) {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return namedInput{}, false
	}
	var in namedInput
	named := false
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Name":
			in.name, named = stringLit(kv.Value)
		case "Positional":
			v, ok := kv.Value.(*ast.Ident)
			in.positional = ok && v.Name == "true"
		}
	}
	return in, named
}

// callProblems returns what is wrong with c, when it names a capability in
// p's namespace: an ID p does not declare, an input the capability does not
// declare, or one given where the CLI does not take it. CapabilityWith
// spells every input it names as a flag, so a Positional one there is given
// where the command refuses it too. A capability of another namespace,
// rta's own net.dns among them, only the host's registry knows.
//
// An input the capability does not declare is still one its command takes
// when it is one of the host's switches (spelling.HostSwitches), given as a
// flag: `--output json` is every capability's.
func callProblems(p plugin.Plugin, c namedCall) []string {
	if ns, _, _ := strings.Cut(c.id, "."); ns != p.Name {
		return nil
	}
	at := slices.IndexFunc(p.Capabilities, func(cp plugin.Capability) bool { return cp.ID == c.id })
	if at < 0 {
		return []string{c.helper + " names " + c.id + ", which this plugin does not declare"}
	}
	cp := p.Capabilities[at]
	var out []string
	for _, in := range c.inputs {
		if slices.Contains(spelling.HostSwitches(), in.name) && !in.positional {
			continue
		}
		i := slices.IndexFunc(cp.Inputs, func(f plugin.Field) bool { return f.Name == in.name })
		switch {
		case i < 0:
			out = append(out, c.helper+" gives "+c.id+" "+strconv.Quote(in.name)+", an input it does not declare")
		case cp.Inputs[i].Positional && !in.positional:
			out = append(out, c.helper+" gives "+c.id+"'s "+strconv.Quote(in.name)+
				" as a flag, where the CLI takes it by its place")
		case !cp.Inputs[i].Positional && in.positional:
			out = append(out, c.helper+" gives "+c.id+"'s "+strconv.Quote(in.name)+
				" by its place, where the CLI takes it as a flag")
		}
	}
	return out
}

// A call the source names through a naming helper is one its reader can
// make: a capability this plugin declares, given inputs it declares, each
// where the CLI takes it. Call spells what it is told, so an input given in
// the wrong place reads as a command line the CLI refuses with core.usage,
// and an ID nothing declares as a command it does not have. Receipts and
// hints once named `rta cnpg backup list shop`, `rta qdrant restore docs
// <file>` and a Galera node's `cluster` command, each refused by the CLI it
// was written for, and the helpers would have spelled the same mistakes
// just as faithfully.
func TestEveryCallTheSourceNamesIsOneItsReaderCanMake(t *testing.T) {
	p := Plugin()
	fset, files := parseSource(t)
	for _, f := range files {
		for _, c := range namedCalls(f) {
			for _, problem := range callProblems(p, c) {
				t.Errorf("%s: %s", fset.Position(c.pos), problem)
			}
		}
	}
}

// Every plugin module carries this file, and a copy that drifted would guard
// its plugin by rules the others no longer share. Read from the sibling
// directories, the way the two SQL forks' drift gate reads the other fork:
// each plugin is its own module, and the repository's layout is the only
// thing this needs.
func TestEveryCopyOfTheSpellerIsTheSame(t *testing.T) {
	ours, err := os.ReadFile("spelling_test.go")
	if err != nil {
		t.Fatal(err)
	}
	// Found by module rather than by copy: a plugin that never received the
	// file is the drift that matters most, and a glob over the copies alone
	// passes it by without a word.
	modules, err := filepath.Glob(filepath.Join("..", "*", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) < 2 {
		t.Skip("no other plugin module beside this one; the gate needs the repository's layout")
	}
	for _, mod := range modules {
		other := filepath.Join(filepath.Dir(mod), "spelling_test.go")
		theirs, err := os.ReadFile(other)
		if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s carries no copy of this file — every plugin module gets one", filepath.Dir(mod))
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ours, theirs) {
			t.Errorf("%s differs from this module's copy — the file is one guard, kept in step "+
				"by copying it to every plugin", other)
		}
	}
}
