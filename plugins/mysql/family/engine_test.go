package family

import "testing"

// useEngine serves e for the rest of t, as e's binary would. Safe only because
// no test here runs in parallel: the engine is the package's, not the test's.
func useEngine(t *testing.T, e Engine) {
	t.Helper()
	was := engine
	engine = e
	t.Cleanup(func() { engine = was })
}

// forEachEngine runs fn once for each engine the family serves, as a subtest
// named for its namespace: a declaration is each engine's own words, and one
// of them read wrong is a plugin somebody installs.
func forEachEngine(t *testing.T, fn func(t *testing.T)) {
	t.Helper()
	for _, e := range []Engine{MySQL, MariaDB} {
		t.Run(e.NS, func(t *testing.T) {
			useEngine(t, e)
			fn(t)
		})
	}
}
