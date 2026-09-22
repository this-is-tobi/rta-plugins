package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two forks are one source tree twice over (fork_test.go), and a fix
// that lands in one and not the other is what that costs: the kube nanocore
// fix landed in one plugin and the other kept the bug until somebody
// remembered. A shared module was weighed against this and this was
// preferred — it buys the same protection without a third module to release.
//
// Every file both forks carry is compared with the vendor's name mapped
// away, and what remains has to be exactly the divergence recorded under
// testdata/drift: the client names each fork tries, the package each brews,
// the Galera and replica views only one of them has. A difference the record
// does not hold is a change one fork got and the other did not. Port it —
// or, when the divergence is meant, refresh the record:
//
//	go test ./plugins/mariadb -run Drift -update
//
// Read from the sibling directory rather than from a module import, because
// each plugin is its own module by design and there is nothing to import;
// the repository's layout is the only thing this depends on, and `go test`
// runs with the package directory as its working directory.

var updateDrift = flag.Bool("update", false, "rewrite testdata/drift to the divergence as it stands")

const otherFork = "../mysql"

// vendorNames maps this fork's spelling onto the other's before comparing,
// longest first so a compound is not half-mapped.
var vendorNames = [][2]string{
	{"mariadb-dump", "mysqldump"},
	{"MariaDB", "MySQL"},
	{"Mariadb", "Mysql"},
	{"mariadb", "mysql"},
}

func TestDriftBetweenTheForksIsOnlyWhatTheRecordSays(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(otherFork); err != nil {
		t.Skipf("the other fork is not beside this one (%v); the gate needs the repository's layout", err)
	}
	if *updateDrift {
		if err := os.RemoveAll(filepath.Join("testdata", "drift")); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name == "drift_test.go" {
			continue
		}
		theirs, err := os.ReadFile(filepath.Join(otherFork, name))
		if err != nil {
			continue // a file only this fork carries — cluster.go — has nothing to drift from
		}
		ours, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		got := lineDiff(strings.Split(normalise(string(ours)), "\n"), strings.Split(string(theirs), "\n"))
		record := filepath.Join("testdata", "drift", name+".diff")
		if *updateDrift {
			if err := os.MkdirAll(filepath.Dir(record), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(record, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(record)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if string(want) != got {
			t.Errorf("%s diverges from %s/%s in a way testdata/drift does not record — a change landed "+
				"in one fork and not the other; port it, or refresh the record with -update if the "+
				"divergence is meant. The unrecorded difference:\n%s", name, otherFork, name,
				unrecorded(string(want), got))
		}
	}
}

func normalise(s string) string {
	for _, pair := range vendorNames {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	return s
}

// lineDiff renders the lines that differ between a and b, each side marked,
// by the ordinary longest-common-subsequence walk: small, and stable enough
// for a record that is regenerated whole rather than patched.
func lineDiff(a, b []string) string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out.WriteString("- " + a[i] + "\n")
			i++
		default:
			out.WriteString("+ " + b[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		out.WriteString("- " + a[i] + "\n")
	}
	for ; j < m; j++ {
		out.WriteString("+ " + b[j] + "\n")
	}
	return out.String()
}

// unrecorded is the part of the divergence the record does not hold, so the
// failure names the lines to look at rather than the whole file's diff.
func unrecorded(want, got string) string {
	known := map[string]bool{}
	for _, l := range strings.Split(want, "\n") {
		known[l] = true
	}
	var out []string
	for _, l := range strings.Split(got, "\n") {
		if l != "" && !known[l] {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return "(lines the record holds and the code no longer does)"
	}
	return strings.Join(out, "\n")
}
