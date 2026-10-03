package main

import (
	"strings"
	"testing"
)

// synchronous_standby_names asks for a number of synchronous standbys, and a
// primary with fewer connected stalls every commit as surely as one with none.
// Run against a PostgreSQL 17 primary with ANY 2 (s1, s2, s3) and two of the
// three standbys stopped: `create table` hung, and the standbys table showed
// the one left as quorum and ok.
func TestFewerSynchronousStandbysThanAskedForIsAStall(t *testing.T) {
	syncOf := func(kind string) standbyRow {
		s := caughtUpStandby()
		s.syncKind, s.priority = str(kind), i32(1)
		return s
	}
	for _, tc := range []struct {
		name, setting string
		standbys      []standbyRow
		want          string
	}{
		{"any 2 with one connected", "ANY 2 (s1, s2, s3)", []standbyRow{syncOf("quorum")},
			"asks for 2 synchronous standbys, and only 1 connected"},
		{"first 2 with one connected", "FIRST 2 (s1, s2, s3)", []standbyRow{syncOf("sync")},
			"asks for 2 synchronous standbys, and only 1 connected"},
		{"the older form", "2 (s1, s2, s3)", []standbyRow{syncOf("sync")},
			"asks for 2 synchronous standbys, and only 1 connected"},
		{"none connected", "ANY 2 (s1, s2)", nil, "no standby is synchronous"},
		{"any 2 with two connected", "ANY 2 (s1, s2, s3)", []standbyRow{syncOf("quorum"), syncOf("quorum")}, ""},
		{"first 2 with a potential beside two", "FIRST 2 (s1, s2, s3)",
			[]standbyRow{syncOf("sync"), syncOf("sync"), syncOf("potential")}, ""},
		{"a bare list asks for one", "s1, s2", []standbyRow{syncOf("sync")}, ""},
		{"a wildcard asks for one", "*", []standbyRow{syncOf("sync")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := primaryFacts(tc.standbys, nil)
			f.server.syncNames = tc.setting
			g := gradeSynchronous(f)
			if tc.want == "" {
				if g.status != gradeOK {
					t.Errorf("status = %q (%s), want ok", g.status, g.detail())
				}
				return
			}
			if g.status != gradeFail || !strings.Contains(g.detail(), tc.want) {
				t.Errorf("status = %q (%s), want fail naming %q", g.status, g.detail(), tc.want)
			}
		})
	}
}

func TestHowManySynchronousStandbysASettingAsksFor(t *testing.T) {
	for setting, want := range map[string]int{
		"FIRST 3 (a, b, c, d)": 3, "first 2 (a, b)": 2, "ANY 2 (a, b, c)": 2, "any\t2 (a,b)": 2, "2 (a, b)": 2,
		"a, b": 1, "*": 1, "\"first\", b": 1, "FIRST (a)": 1, "ANY 0 (a)": 1, "": 1,
	} {
		if got := synchronousStandbysNeeded(setting); got != want {
			t.Errorf("%q asks for %d, want %d", setting, got, want)
		}
	}
}
