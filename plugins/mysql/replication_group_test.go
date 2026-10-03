package main

import (
	"strings"
	"testing"
)

// Group replication is read from a member's own tables, and what a member says
// about the others is what the group last told it. The captures are of a real
// two-member group on 8.4: each member healthy, the secondary after the
// primary stopped answering, a member stopped on purpose, and the primary read
// by an account that may not select from performance_schema.

func groupSummary(t *testing.T, name, user string) []string {
	t.Helper()
	row, _, _ := summaryRow(t, name, user)
	return row
}

func TestAHealthyGroupMemberIsNotStandalone(t *testing.T) {
	for name, role := range map[string]string{
		"mysql-8.4-group-primary":   "group replication member (primary, 2 members)",
		"mysql-8.4-group-secondary": "group replication member (secondary, 2 members)",
	} {
		row, tables, st := summaryRow(t, name, "root")
		if row[0] != role || row[1] != "ok" || !strings.Contains(row[2], "all 2 members are online") {
			t.Errorf("%s: summary = %v, want %q and ok", name, row, role)
		}
		members, ok := tables("group")
		if !ok || len(members) != 2 {
			t.Fatalf("%s: group section = %v", name, members)
		}
		local := 0
		for _, m := range members {
			if strings.HasSuffix(m[0], "(this server)") {
				local++
			}
			if m[1] != "online" || m[3] != "0" {
				t.Errorf("%s: member row = %v, want online with an empty applier queue", name, m)
			}
		}
		if local != 1 {
			t.Errorf("%s: %d members marked as this server, want exactly one", name, local)
		}
		if len(st.unread) != 0 {
			t.Errorf("%s: unread parts: %v", name, st.unread)
		}
	}
}

// **The one that matters.** A member that lost its peers reads ONLINE about
// itself, its threads run and its reads work; only the other members' state
// and the arithmetic of the majority say the group has stopped.
func TestAMemberWithoutAMajorityFails(t *testing.T) {
	row := groupSummary(t, "mysql-8.4-group-minority", "root")
	if row[1] != "fail" || !strings.Contains(row[2], "no majority") || !strings.Contains(row[2], "must not be written to") {
		t.Errorf("summary = %v, want a failure naming the lost majority", row)
	}
	_, tables, _ := summaryRow(t, "mysql-8.4-group-minority", "root")
	members, _ := tables("group")
	if len(members) != 2 || members[0][1] != "unreachable" {
		t.Errorf("members = %v, want the primary listed as unreachable", members)
	}
}

func TestAStoppedMemberIsNamedByItsState(t *testing.T) {
	row := groupSummary(t, "mysql-8.4-group-offline", "root")
	if row[0] != "group replication member (offline)" || row[1] != "fail" || !strings.Contains(row[2], "OFFLINE") {
		t.Errorf("summary = %v, want an offline member that fails", row)
	}
}

// An account that cannot select from performance_schema still learns what it
// can: this server belongs to a group, and which grant would show the rest.
// "Not a replica, binary log on" would describe a group member as the one
// thing it is not.
func TestAnAccountThatCannotReadTheGroupIsToldWhichGrantWould(t *testing.T) {
	name := "mysql-8.4-group-nogrant"
	row, tables, st := summaryRow(t, name, "mon")
	if row[0] != "group replication member" || row[1] != "warn" || !strings.Contains(row[2], "not the whole picture") {
		t.Errorf("summary = %v, want a member of a group with an incomplete answer", row)
	}
	if _, ok := tables("group"); ok {
		t.Error("a group table is shown that could not be read")
	}
	var hint string
	for _, w := range replicationView(st).Warnings {
		if strings.Contains(w.Message, "cluster state") {
			hint = w.Hint
		}
	}
	if !strings.Contains(hint, "GRANT SELECT ON performance_schema.* TO 'mon'") {
		t.Errorf("hint = %q, want the grant on the schema the group's tables are in", hint)
	}
}

func TestAServerOutsideAnyGroupIsNeverAsked(t *testing.T) {
	for _, scenario := range []string{"standalone", "replica", "source", "monitor"} {
		for _, name := range fixtureNames(t, scenario) {
			_, tables, _ := summaryRow(t, name, "root")
			if _, ok := tables("group"); ok {
				t.Errorf("%s: a server with no group shows a group section", name)
			}
		}
	}
}

func members(states ...string) []map[string]string {
	rows := make([]map[string]string, len(states))
	for i, s := range states {
		role := "SECONDARY"
		if i == 0 {
			role = "PRIMARY"
		}
		rows[i] = map[string]string{
			"member_host": "n" + string(rune('1'+i)), "member_port": "3306", "member_state": s,
			"member_role": role, "this_server": map[bool]string{true: "1", false: "0"}[i == 0], "member_id": "id" + string(rune('1'+i)),
		}
	}
	return rows
}

func TestHowTheGroupIsGraded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []map[string]string
		queues  []map[string]string
		status  string
		detail  string
	}{
		{"all online", members("ONLINE", "ONLINE", "ONLINE"), nil, "ok", "all 3 members are online"},
		{"alone", members("ONLINE"), nil, "ok", "the only member"},
		{"one of three unreachable keeps a majority", members("ONLINE", "ONLINE", "UNREACHABLE"), nil, "warn", "1 member unreachable"},
		{"two of three unreachable does not", members("ONLINE", "UNREACHABLE", "UNREACHABLE"), nil, "fail", "no majority"},
		{"half of four is not a majority", members("ONLINE", "ONLINE", "UNREACHABLE", "UNREACHABLE"), nil, "fail", "sees 2 members of 4"},
		{"a peer recovering", members("ONLINE", "RECOVERING", "ONLINE"), nil, "warn", "n2:3306 is RECOVERING"},
		{"a peer in error", members("ONLINE", "ERROR", "ONLINE"), nil, "warn", "n2:3306 is ERROR"},
		{"this member recovering", members("RECOVERING", "ONLINE"), nil, "warn", "this member is RECOVERING"},
		{"this member in error", members("ERROR", "ONLINE"), nil, "fail", "this member is ERROR"},
		{
			"a peer far behind", members("ONLINE", "ONLINE"),
			[]map[string]string{{"member_id": "id2", "count_transactions_remote_in_applier_queue": "1000"}},
			"warn", "n2:3306 has 1000 transactions waiting",
		},
		{
			"a queue that is only busy", members("ONLINE", "ONLINE"),
			[]map[string]string{{"member_id": "id2", "count_transactions_remote_in_applier_queue": "999"}},
			"ok", "online",
		},
	} {
		part := groupFrom(true, tc.members, tc.queues)
		if part.facet.status != tc.status || !strings.Contains(part.facet.detail, tc.detail) {
			t.Errorf("%s: facet = %+v, want %s containing %q", tc.name, *part.facet, tc.status, tc.detail)
		}
	}
}

func TestAMultiPrimaryGroupSaysSo(t *testing.T) {
	part := groupFrom(false, members("ONLINE", "ONLINE", "ONLINE"), nil)
	if part.facet.role != "group replication member (multi-primary, 3 members)" {
		t.Errorf("role = %q", part.facet.role)
	}
}
