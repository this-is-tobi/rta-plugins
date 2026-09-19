package main

import (
	"regexp"
	"testing"
)

// The mysql and mariadb plugins are one source tree twice over, and a
// declaration copied from the other fork keeps reading naturally right up to
// the moment somebody follows it. One fork's activity capability shipped,
// release after release, telling the caller to run the other fork's overview
// command: a capability in a namespace an operator of this database has no
// reason to have installed, and one an agent reading the MCP tool description
// cannot tell from a real one.
//
// The scope is narrow on purpose. Prose about the other fork is fine and
// sometimes necessary — a dump description has to explain why its flags
// differ from the other client's, and a tool-skew hint has to name the other
// plugin as the way out. A *command* is not prose: an rta invocation of the
// other namespace, a dotted capability ID in it, or a backticked
// "<namespace> <verb>" line is an instruction the reader will act on.
func TestNoDeclaredCommandNamesTheOtherFork(t *testing.T) {
	forks := map[string]string{"mysql": "mariadb", "mariadb": "mysql"}
	other, ok := forks[Plugin().Name]
	if !ok {
		// Without a counterpart the patterns below degrade into matchers of
		// ordinary prose, and a test that fails on every sentence proves
		// nothing about commands.
		t.Fatalf("%q is not a fork this test knows a counterpart for", Plugin().Name)
	}
	bad := []*regexp.Regexp{
		regexp.MustCompile(`\brta ` + other + `\b`),
		regexp.MustCompile(`\b` + other + `\.[a-z]`),
		regexp.MustCompile("`" + other + ` (overview|status|query|activity|schema|table|database|dump|restore)\b`),
	}
	for _, c := range Plugin().Capabilities {
		texts := []string{c.Summary, c.Description}
		for _, f := range c.Inputs {
			texts = append(texts, f.Help)
		}
		for _, s := range texts {
			for _, re := range bad {
				if m := re.FindString(s); m != "" {
					t.Errorf("%s declares %q — a command in the other fork's namespace", c.ID, m)
				}
			}
		}
	}
}
