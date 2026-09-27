package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// This file is the same in every plugin module, and nothing in it names the
// plugin it sits in: each copy reads its own module's Plugin() and source,
// and TestEveryCopyOfTheSpellerIsTheSame holds the copies to one another.
// A copy rather than an import, because a plugin module reaches nothing but
// rta's released SDK, and the SDK exports no speller — rta's own lives in
// its internal/app tests and reads the host's registry, which a plugin
// cannot load. What that costs is said below, at the rules a registry would
// have made exact.

// speller finds, in text a surface other than the CLI reads, what only a
// terminal could act on: a flag, or an `rta …` command line. An agent has
// tools and their arguments, and the TUI a capability's ID and a form's
// boxes, and a hint naming `rta demo key list --limit` sends either one
// looking for something that is not there. Text shown on every surface at
// once — what a capability declares — names an input as `limit` and a
// capability by its ID, and text worded at run time asks the request's
// surface (plugin.Surface.CapabilityName, Call, InputName and the rest).
type speller struct {
	namespace string
	caps      map[string]plugin.Capability
}

// hostSwitches are the flags rta adds to every capability, or to every one
// of a kind, whatever the plugin declares.
var hostSwitches = []string{"detail", "dry-run", "yes", "output", "profile", "server"}

func newSpeller(p plugin.Plugin) speller {
	sp := speller{namespace: p.Name, caps: map[string]plugin.Capability{}}
	for _, c := range p.Capabilities {
		sp.caps[c.ID] = c
	}
	return sp
}

// commandLine is "rta" followed by words, wherever it stands — in a code
// span, inside a shell example, or in running prose after "reach it with:".
// A word may carry a digit after its first letter: s3 is a namespace, and a
// pattern of letters alone read `rta s3 bucket list` as `rta s` and let it
// through.
var commandLine = regexp.MustCompile(`(?:^|[^a-z-])rta((?: [a-z][a-z0-9-]*)+)`)

// find returns each place text spells something for a terminal.
//
// The one command line an agent may read is the one it hands on:
// plugin.AskOperator's phrase, a command for the person at the terminal,
// taken out before anything is looked at.
//
// Flags are read as rta's own speller reads them. One in prose is held
// whichever program it belongs to: an agent reading "run with --jobs 4" cannot
// tell pg_dump's option from rta's, and has neither. Another program's is
// named in a code span with the program before it — `pg_restore --jobs`,
// `vault kv list` — which is that program's spelling, even where a name in it
// is one a capability here shares. A flag in a span is held only when the
// span opens on it, or when the words before it name a capability here that
// declares it: `demo key list --limit 5` is rta's command line without its
// first word.
//
// A span opening on "rta " that names nothing of this plugin's — `rta
// explain`, `rta net dns` — is held in what a capability declares, where rta
// would let one naming no capability through in text only a terminal reads:
// it tells `rta doctor` from `rta net dns` by its registry, and a plugin,
// which has none, holds both rather than guess. In source (literal) it is
// let through, since a handler words those for the CLI in the branch that
// asked the surface first, and only this plugin's own command lines have a
// helper to go through.
func (sp speller) find(text string, literal bool) []string {
	ask := strings.TrimSuffix(plugin.AskOperator(""), "`")
	for {
		i := strings.Index(text, ask)
		if i < 0 {
			break
		}
		end := strings.IndexByte(text[i+len(ask):], '`')
		if end < 0 {
			break
		}
		text = text[:i] + text[i+len(ask)+end+1:]
	}
	var found []string
	quote := func(i, j int) string {
		from, to := max(0, i-30), min(len(text), j+30)
		return strings.ReplaceAll(text[from:to], "\n", " ")
	}
	for _, m := range commandLine.FindAllStringSubmatchIndex(text, -1) {
		if sp.names(text[m[2]:m[3]]) {
			found = append(found, quote(m[0], m[1]))
		}
	}
	for i := 0; i < len(text); i++ {
		if text[i] == '`' {
			end := strings.IndexByte(text[i+1:], '`')
			if end < 0 {
				break
			}
			span := text[i+1 : i+1+end]
			if rest, ok := strings.CutPrefix(span, "rta "); ok {
				// One naming this plugin was found above.
				if !sp.names(rest) && !literal {
					found = append(found, quote(i, i+2+end))
				}
			} else {
				c, named := sp.capabilityOf(span)
				if slices.ContainsFunc(flagsIn(span), func(flag string) bool {
					return strings.HasPrefix(span, "--") || named && declares(c, flag)
				}) {
					found = append(found, quote(i, i+2+end))
				}
			}
			i += end + 1
			continue
		}
		if flag := flagAt(text, i); flag != "" {
			found = append(found, quote(i, i+2+len(flag)))
			i += 1 + len(flag)
		}
	}
	for _, m := range sp.bareCommands(maskSpans(text)) {
		found = append(found, quote(m[0], m[1]))
	}
	return found
}

// names reports whether the words after "rta" are this plugin's: a
// capability it declares, or its namespace and a word after it — `rta demo
// restore` names no capability and is still the CLI's spelling of one the
// reader would go looking for.
func (sp speller) names(words string) bool {
	if _, ok := sp.capabilityOf(words); ok {
		return true
	}
	fields := strings.Fields(words)
	return len(fields) > 1 && fields[0] == sp.namespace
}

// proseWord is one word a capability's ID could be made of.
var proseWord = regexp.MustCompile(`[a-z][a-z0-9-]*`)

// bareCommands returns where prose spells a capability here in the CLI's
// words without the "rta" before them: "use demo key list to find it", a
// hint an agent reads as a command it has no terminal for, with the
// demo_key_list tool in its list.
//
// Words a capability's ID is made of, one space apart, standing alone:
// demo.key.list and demo_key_list are the ID and the tool, each spelled as
// one word, and a path or a file name that happens to hold the words names
// something else. And not after a determiner: the words an ID is made of are
// English words too, and after "a" or "the" they are the noun they say.
func (sp speller) bareCommands(prose string) [][2]int {
	words := proseWord.FindAllStringIndex(prose, -1)
	var out [][2]int
	for i := 0; i < len(words); i++ {
		start := words[i][0]
		if start > 0 && joined(prose[start-1]) || strings.HasSuffix(prose[:start], "rta ") ||
			afterDeterminer(prose[:start]) {
			continue
		}
		id, last := prose[start:words[i][1]], -1
		for j := i + 1; j < len(words) && j < i+3; j++ {
			if prose[words[j-1][1]:words[j][0]] != " " {
				break
			}
			id += "." + prose[words[j][0]:words[j][1]]
			if _, ok := sp.caps[id]; ok && standsAlone(prose, words[j][1]) {
				last = j
			}
		}
		if last >= 0 {
			out = append(out, [2]int{start, words[last][1]})
			i = last
		}
	}
	return out
}

// determiners are the words that make the ones after them a noun.
var determiners = map[string]bool{
	"a": true, "an": true, "the": true, "this": true, "that": true, "these": true, "those": true,
	"its": true, "your": true, "their": true, "our": true, "my": true,
	"each": true, "every": true, "any": true, "no": true,
}

// afterDeterminer reports whether before ends in a determiner and a space, a
// possessive — the operator's — among them.
func afterDeterminer(before string) bool {
	before, spaced := strings.CutSuffix(before, " ")
	if !spaced {
		return false
	}
	word := strings.ToLower(before[strings.LastIndexAny(before, " \t\n(\"")+1:])
	return determiners[word] || strings.HasSuffix(word, "'s")
}

// joined reports whether b, beside a word, makes it part of a longer one: an
// identifier, a path, a dotted ID or a flag.
func joined(b byte) bool {
	return isFlagByte(b) || b == '_' || b == '.' || b == '/'
}

// standsAlone reports whether the word ending at prose[end] ends there, and
// is not the start of a file name or a path.
func standsAlone(prose string, end int) bool {
	if end == len(prose) {
		return true
	}
	if b := prose[end]; b == '.' {
		return end+1 == len(prose) || !isFlagByte(prose[end+1])
	}
	return !joined(prose[end])
}

// maskSpans is text with each code span blanked out, the same length so an
// offset into it is one into text.
func maskSpans(text string) string {
	b := []byte(text)
	for i := 0; i < len(b); i++ {
		if b[i] != '`' {
			continue
		}
		end := strings.IndexByte(text[i+1:], '`')
		if end < 0 {
			break
		}
		for k := i; k <= i+1+end; k++ {
			b[k] = ' '
		}
		i += end + 1
	}
	return string(b)
}

// capabilityOf returns the capability here whose ID words begins with, as
// the CLI spells one: `demo key list` for demo.key.list.
func (sp speller) capabilityOf(words string) (plugin.Capability, bool) {
	var ids []string
	for _, w := range strings.Fields(words) {
		if strings.Trim(w, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			break
		}
		ids = append(ids, w)
		if c, ok := sp.caps[strings.Join(ids, ".")]; ok {
			return c, true
		}
	}
	return plugin.Capability{}, false
}

// declares reports whether flag is one of c's inputs, or a host switch.
func declares(c plugin.Capability, flag string) bool {
	return slices.Contains(hostSwitches, flag) ||
		slices.ContainsFunc(c.Inputs, func(f plugin.Field) bool { return f.Name == flag })
}

// flagAt returns the name of the flag starting at text[i], or "".
//
// A flag the source splices together is one all the same, and is named by
// what stands in for its name: "--" joined to a name, which a sentence read
// out of the source holds as "--" and operand, and "--%s" handed to Sprintf.
// Either reads `--limit` to whoever gets the message, and read as a letter
// after "--" or nothing, both went through.
func flagAt(text string, i int) string {
	if !strings.HasPrefix(text[i:], "--") || i > 0 && isFlagByte(text[i-1]) || i+2 >= len(text) {
		return ""
	}
	switch rest := text[i+2:]; {
	case strings.HasPrefix(rest, operand):
		return operand
	case rest[0] == '%' && len(rest) > 1:
		return rest[:2]
	case rest[0] < 'a' || rest[0] > 'z':
		return ""
	}
	j := i + 2
	for j < len(text) && isFlagByte(text[j]) {
		j++
	}
	return text[i+2 : j]
}

func flagsIn(span string) []string {
	var out []string
	for i := 0; i < len(span); i++ {
		if flag := flagAt(span, i); flag != "" {
			out = append(out, flag)
			i += 1 + len(flag)
		}
	}
	return out
}

func isFlagByte(b byte) bool {
	return b == '-' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z'
}

// declaredText is everything the plugin says about itself, keyed by where it
// says it: the text shown on every surface at once.
func declaredText(p plugin.Plugin) map[string]string {
	texts := map[string]string{"the plugin's summary": p.Summary}
	for _, c := range p.Capabilities {
		texts[c.ID+" summary"] = c.Summary
		texts[c.ID+" description"] = c.Description
		for _, f := range c.Inputs {
			texts[c.ID+" help of "+f.Name] = f.Help
		}
		for _, a := range c.Actions {
			texts[c.ID+" action "+a.Key] = a.Label
		}
		for _, tg := range c.Toggles {
			texts[c.ID+" toggle "+tg.Key] = tg.Label
		}
	}
	return texts
}

// operand stands in, in a sentence read out of the source, for whatever a
// literal is joined to — a name, a value, what a naming helper returned.
const operand = "…"

// sentences returns the text of every string the source spells out, each
// with where it starts: a literal on its own, and a sum of literals and
// other operands as one sentence, with operand in each other operand's
// place. One sentence rather than its pieces, because a code span opened in
// one literal and closed in the next — "`createdb --host=" + host + "`" — is
// only a span read whole, and read piece by piece the flag inside it looked
// like prose.
//
// What plugin.AskOperator is given is left out: the command it hands the
// operator is the one command line an agent may read.
func sentences(f *ast.File) []struct {
	pos  token.Pos
	text string
} {
	var out []struct {
		pos  token.Pos
		text string
	}
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
				text.WriteString(operand)
				ast.Inspect(p, visit)
			}
			if spelled {
				out = append(out, struct {
					pos  token.Pos
					text string
				}{n.Pos(), text.String()})
			}
			return false
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				if s, err := strconv.Unquote(n.Value); err == nil {
					out = append(out, struct {
						pos  token.Pos
						text string
					}{n.Pos(), s})
				}
			}
		}
		return true
	}
	ast.Inspect(f, visit)
	return out
}

// The speller itself, held to the spellings it exists to catch and the ones
// it must let through: a test that passes because its scanner sees nothing
// guards nothing. Against a plugin of its own, so the cases read the same in
// every module this file is copied into.
func TestTheSpellerTellsATerminalsSpellingFromEveryoneElses(t *testing.T) {
	sp := newSpeller(plugin.Plugin{Name: "demo", Capabilities: []plugin.Capability{
		{ID: "demo.key.list", Inputs: []plugin.Field{{Name: "limit"}, {Name: "jobs"}, {Name: "host"}}},
		{ID: "demo.key.get", Inputs: []plugin.Field{{Name: "key", Positional: true}}},
	}})
	for text, want := range map[string]bool{
		"run `rta demo key list --limit 5` to see more":                      true,
		"raise --limit to see more":                                          true,
		"reach it with: rta demo key get user:1":                             true,
		"There is no `rta demo restore`":                                     true,
		"`rta net dns example.org` shows what DNS returns":                   true,
		"`demo key list --limit 5` finds it":                                 true,
		"`--jobs 4` runs four at once":                                       true,
		"With --detail, every key":                                           true,
		"use demo key list to find it":                                       true,
		"ask the operator to run `rta grant allow demo.key.get user:1`":      false,
		"the `demo_key_list` tool with the \"limit\" argument lists more":    false,
		"`demo.key.list` with the limit box filled lists more":               false,
		"raise `limit` to see more":                                          false,
		"the server is running with --read-only":                             true,
		"the server is running with `read_only` on":                          false,
		"restored through `pg_restore --jobs`":                               false,
		"`createdb --host=db app` makes it":                                  false,
		"the structured equivalent of `demo key list`":                       false,
		"-----BEGIN PUBLIC KEY-----":                                         false,
		"rta does not take the backup; the demo key list is what it read":    false,
		"the files under demo/key and reads demo key.go":                     false,
		"a grant (`grant.allow`, for `demo.key.get` and that key) allows it": false,
	} {
		if got := len(sp.find(text, false)) > 0; got != want {
			t.Errorf("find(%q) found a terminal's spelling: %v, want %v", text, got, want)
		}
	}
	if hits := sp.find("run rta demo key list now", false); len(hits) != 1 {
		t.Errorf("a command line was found %d times: %q", len(hits), hits)
	}
	// A namespace with a digit in it is a namespace all the same.
	s3 := newSpeller(plugin.Plugin{Name: "s3", Capabilities: []plugin.Capability{{ID: "s3.bucket.list"}}})
	for _, text := range []string{"`rta s3 bucket list` shows what is there", "see rta s3 bucket list"} {
		if len(s3.find(text, true)) == 0 {
			t.Errorf("find(%q) passed a command line naming a namespace with a digit", text)
		}
	}
	// Source spells the CLI's own commands in the branch that asked the
	// surface, and only this plugin's are held there.
	if hits := sp.find("`rta explain "+operand+"` lists every input", true); len(hits) > 0 {
		t.Errorf("a command with no capability here was held against source: %v", hits)
	}
	if hits := sp.find("`rta demo key get "+operand+"` shows it", true); len(hits) == 0 {
		t.Error("this plugin's own command line passed in source")
	}

	src := "package main\n\nfunc hints(sf plugin.Surface, host, key string) []string {\n" +
		"\treturn []string{\n" +
		"\t\t\"a new database is worse. `createdb --host=\" + host + \" app` makes it\",\n" +
		"\t\t\"raise --limit to see more\",\n" +
		"\t\t\"remove it first: rta demo key rm \" + key + \" --host \" + host,\n" +
		"\t\tplugin.AskOperator(\"grant allow demo.key.list --limit 5\"),\n" +
		"\t\t\"--host=\" + host,\n" +
		"\t\t\"raise --\" + key + \" to see more\",\n" +
		"\t\tfmt.Sprintf(\"raise --%s to see more\", key),\n" +
		"\t}\n}\n"
	f, err := parser.ParseFile(token.NewFileSet(), "hints.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var held []string
	for _, s := range sentences(f) {
		if strings.Contains(s.text, " ") && len(sp.find(s.text, true)) > 0 {
			held = append(held, s.text)
		}
	}
	want := []string{"raise --limit to see more", "remove it first: rta demo key rm " + operand + " --host " + operand,
		"raise --" + operand + " to see more", "raise --%s to see more"}
	if !slices.Equal(held, want) {
		t.Errorf("held in source: %q, want %q", held, want)
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
		"\t}\n}\n"
	fset := token.NewFileSet()
	f, err = parser.ParseFile(fset, "calls.go", calls, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var wrong []int
	for _, c := range namedCalls(f) {
		if len(sp.callProblems(c)) > 0 {
			wrong = append(wrong, fset.Position(c.pos).Line)
		}
	}
	if want := []int{5, 6, 7, 8, 9}; !slices.Equal(wrong, want) {
		t.Errorf("calls held on lines %v, want %v", wrong, want)
	}
}

// What a capability declares about itself is shown on every surface at once
// — `rta explain` and --help, the TUI's form, an agent's tool list — and has
// no surface to ask which one is reading, so it names an input as `limit` and
// a capability by its ID, and never as one surface spells it.
func TestDeclaredTextSpellsNothingForOneSurface(t *testing.T) {
	p := Plugin()
	sp := newSpeller(p)
	for where, text := range declaredText(p) {
		for _, hit := range sp.find(text, false) {
			t.Errorf("%s spells a terminal's: …%s…", where, hit)
		}
	}
}

// What a handler words at run time names a capability and an input through
// the request's surface, so no sentence this plugin's source spells out holds
// a command line of its own or a flag. Source rather than the answers
// themselves, because most of what a handler says is worded about a
// connection that failed or a server's answer, which no test here can
// provoke without the server — and "raise --limit" is wrong whichever
// surface reads it.
//
// A flag counts only in a sentence, text with a space in it, since an
// argument handed to kubectl or pg_dump is a literal too, "--namespace=" and
// "--no-owner", and is that program's spelling. What this cannot see is a
// built-in's command line spelled out — `rta net dns` — since only rta's
// registry says which words name a capability, and a plugin has none to ask.
func TestNoSentenceInTheSourceSpellsForOneSurface(t *testing.T) {
	sp := newSpeller(Plugin())
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range sentences(f) {
			if !strings.Contains(s.text, " ") {
				continue
			}
			for _, hit := range sp.find(s.text, true) {
				t.Errorf("%s spells a terminal's: …%s…", fset.Position(s.pos), hit)
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
// this plugin's namespace: an ID the plugin does not declare, an input the
// capability does not declare, or one given where the CLI does not take it.
// CapabilityWith spells every input it names as a flag, so a Positional one
// there is given where the command refuses it too. A capability of another
// namespace, rta's own net.dns among them, only the host's registry knows.
func (sp speller) callProblems(c namedCall) []string {
	if ns, _, _ := strings.Cut(c.id, "."); ns != sp.namespace {
		return nil
	}
	cp, ok := sp.caps[c.id]
	if !ok {
		return []string{c.helper + " names " + c.id + ", which this plugin does not declare"}
	}
	var out []string
	for _, in := range c.inputs {
		if slices.Contains(hostSwitches, in.name) && !in.positional {
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
	sp := newSpeller(Plugin())
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range namedCalls(f) {
			for _, problem := range sp.callProblems(c) {
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
	copies, err := filepath.Glob(filepath.Join("..", "*", "spelling_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) < 2 {
		t.Skip("no other plugin module beside this one; the gate needs the repository's layout")
	}
	for _, other := range copies {
		theirs, err := os.ReadFile(other)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ours, theirs) {
			t.Errorf("%s differs from this module's copy — the file is one guard, kept in step "+
				"by copying it to every plugin", other)
		}
	}
}
