package main

import (
	"strings"
	"testing"
)

// The sessions row is read off pg_stat_activity by the same session that is
// asking, so an idle server answered 1 while the activity table beside it, which
// leaves its own session out, was empty. And a role without pg_read_all_stats
// is shown other sessions' rows with their state blank, so a count of the
// `active` ones is a count of its own and reads as a quiet server.

func TestTheActiveCountLeavesOutTheSessionThatIsCountingIt(t *testing.T) {
	q := primaryScript()
	glance(t, q)
	if !q.askedFor("pid <> pg_backend_pid()") {
		t.Errorf("the count does not leave out the overview's own backend: %v", *q.asked)
	}
	// And is of the server, as max_connections is: every client of every database,
	// not the sessions of the one the overview connected to.
	if !q.askedFor("backend_type = 'client backend'") {
		t.Error("the count includes sessions that do not take a connection of max_connections")
	}
	for _, sql := range *q.asked {
		if strings.Contains(sql, "pg_stat_activity") && strings.Contains(sql, "datname = current_database()") {
			t.Errorf("the sessions of max_connections are counted for one database only: %s", sql)
		}
	}
}

func TestARoleThatSeesOnlyItsOwnSessionsIsToldSoInsteadOfANumber(t *testing.T) {
	blind := sessionRow{max: 40}
	q := primaryScript()
	q.script = append(script{blind.answer()}, q.script...)
	rows := glance(t, q)
	want := "could not be read — " + noStatsRole
	if got := valueOf(rows, "sessions"); got != want {
		t.Errorf("sessions = %q, want %q", got, want)
	}
	if got := valueOf(rows, "oldest xact"); got != want {
		t.Errorf("oldest xact = %q, want %q", got, want)
	}
	if got, want := valueOf(rows, "status"), "warn — sessions could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := valueOf(glance(t, primaryScript()), "sessions"); !strings.HasPrefix(got, "12 of 40") {
		t.Errorf("sessions = %q for a role that sees every session, want the number", got)
	}
}

// The role is asked about the privileges it has, the way pg_stat_activity asks,
// and not whether it is a member of one: a member that does not inherit is
// shown the same blanks.
func TestWhoSeesEverySessionIsAskedAsPostgreSQLAsksIt(t *testing.T) {
	q := primaryScript()
	glance(t, q)
	if !q.askedFor("pg_has_role(current_user, 'pg_read_all_stats', 'USAGE')") {
		t.Errorf("the role's reach is not asked as USAGE of pg_read_all_stats: %v", *q.asked)
	}
	for _, sql := range *q.asked {
		if strings.Contains(sql, "'MEMBER'") {
			t.Errorf("a member that does not inherit would be told it sees every session: %s", sql)
		}
	}
}
