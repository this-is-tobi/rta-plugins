package main

import (
	"context"
	"strings"
	"testing"
)

// "active queries" is read off pg_stat_activity by the same session that is
// asking, so an idle server answered 1 while the activity table beside it, which
// leaves its own session out, was empty. And a role without pg_read_all_stats
// is shown other sessions' rows with their state blank, so a count of the
// `active` ones is a count of its own and reads as a quiet server.

func activeQueries(t *testing.T, seesAll bool, active int64) (string, asking) {
	t.Helper()
	q := askingScript(answer{match: "pg_stat_activity", rows: [][]any{{active, seesAll}}})
	v, err := compactOverview(context.Background(), q, reqFor(t, "pg.overview", nil))
	return pairsOf(t, v, err)["active queries"], q
}

func TestTheActiveCountLeavesOutTheSessionThatIsCountingIt(t *testing.T) {
	_, q := activeQueries(t, true, 0)
	if !q.askedFor("pid <> pg_backend_pid()") {
		t.Errorf("the count does not leave out the overview's own backend: %v", *q.asked)
	}
}

func TestARoleThatSeesOnlyItsOwnSessionsIsToldSoInsteadOfANumber(t *testing.T) {
	got, _ := activeQueries(t, false, 0)
	if got != sessionsHidden {
		t.Errorf("active queries = %q, want %q", got, sessionsHidden)
	}
	if got, _ := activeQueries(t, true, 3); got != "3" {
		t.Errorf("active queries = %q for a role that sees every session, want the number", got)
	}
}

// The role is asked about the privileges it has, the way pg_stat_activity asks,
// and not whether it is a member of one: a member that does not inherit is
// shown the same blanks.
func TestWhoSeesEverySessionIsAskedAsPostgreSQLAsksIt(t *testing.T) {
	_, q := activeQueries(t, true, 0)
	if !q.askedFor("pg_has_role(current_user, 'pg_read_all_stats', 'USAGE')") {
		t.Errorf("the role's reach is not asked as USAGE of pg_read_all_stats: %v", *q.asked)
	}
	for _, sql := range *q.asked {
		if strings.Contains(sql, "'MEMBER'") {
			t.Errorf("a member that does not inherit would be told it sees every session: %s", sql)
		}
	}
}
