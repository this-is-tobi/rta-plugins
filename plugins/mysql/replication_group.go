package main

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/view"
)

// MySQL's own clustering layer, Group Replication — what InnoDB Cluster and
// the operators built on it run. It is part of the replication view for the
// reason Galera is part of mariadb's: a group member is also, by the
// replication channels it keeps, a replica and a source in the ordinary sense,
// and the answer that matters at three in the morning is whether the group
// still has a majority, which no replica thread says. Without this a member
// reads as "standalone" — the wrong answer about exactly the servers that
// matter, and what this file was added to stop.
//
// It is asked of a server only when the server is configured for a group.
// The group's tables are in performance_schema, which takes a SELECT grant a
// monitoring account holding REPLICATION CLIENT often lacks; asking every
// server for them would make every other poll of every ordinary server a
// permanent warning. The variable that says the server belongs to a group is
// readable by anyone, so it decides.

const (
	groupVariablesStatement = `SHOW GLOBAL VARIABLES WHERE Variable_name IN ` +
		`('group_replication_group_name','group_replication_single_primary_mode')`
	groupMembersStatement = `SELECT MEMBER_HOST, MEMBER_PORT, MEMBER_STATE, MEMBER_ROLE, ` +
		`MEMBER_ID = @@server_uuid AS THIS_SERVER, MEMBER_ID ` +
		`FROM performance_schema.replication_group_members ORDER BY MEMBER_HOST, MEMBER_PORT`
	groupQueuesStatement = `SELECT MEMBER_ID, COUNT_TRANSACTIONS_REMOTE_IN_APPLIER_QUEUE ` +
		`FROM performance_schema.replication_group_member_stats`
)

// groupQueueWarn is how many transactions may wait in a member's applier queue
// before it is called behind. The group throttles writers on its own at 25000
// (group_replication_flow_control_applier_threshold), a point nobody wants to
// reach unnoticed; a fortieth of that is a queue that is not a momentary spike
// of one bulk transaction and is still far enough from the throttle to be
// something to act on. A judgement, not a value the server publishes.
const groupQueueWarn = 1000

func clusterOf(ctx context.Context, db *sql.DB) clusterPart {
	rows, err := readRows(ctx, db, groupVariablesStatement)
	if err != nil {
		return clusterPart{err: err}
	}
	var name string
	single := true
	for _, r := range rows {
		switch strings.ToLower(r["variable_name"]) {
		case "group_replication_group_name":
			name = r["value"]
		case "group_replication_single_primary_mode":
			single = isOn(r["value"])
		}
	}
	if name == "" {
		return clusterPart{}
	}
	members, err := readRows(ctx, db, groupMembersStatement)
	if err != nil {
		return unreadGroup(err)
	}
	queues, err := readRows(ctx, db, groupQueuesStatement)
	if err != nil {
		return unreadGroup(err)
	}
	return groupFrom(single, members, queues)
}

// unreadGroup keeps what is known when the group's tables are not: this server
// belongs to one. The summary then says so beside the warning, instead of
// describing a group member as the one thing it is not.
func unreadGroup(err error) clusterPart {
	return clusterPart{
		err:   err,
		facet: &facet{role: "group replication member", status: "ok", detail: "configured for a replication group"},
	}
}

type groupMember struct {
	address, state, role string
	local                bool
	queue                string
	queued               int64
}

func groupFrom(single bool, rows, queues []map[string]string) clusterPart {
	waiting := map[string]string{}
	for _, q := range queues {
		waiting[q["member_id"]] = q["count_transactions_remote_in_applier_queue"]
	}
	members := make([]groupMember, len(rows))
	for i, r := range rows {
		m := groupMember{
			address: hostPort(r["member_host"], r["member_port"]),
			state:   strings.ToUpper(r["member_state"]),
			role:    strings.ToLower(r["member_role"]),
			local:   r["this_server"] == "1",
			queue:   "-",
		}
		if n, ok := atoi(waiting[r["member_id"]]); ok {
			m.queue, m.queued = strconv.FormatInt(n, 10), n
		}
		members[i] = m
	}
	f := groupFacet(single, members)
	return clusterPart{
		facet:   &f,
		section: &view.Section{ID: "group", Title: "Group replication", View: groupTable(members)},
	}
}

func groupTable(members []groupMember) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Member"}, {Name: "State"}, {Name: "Role"}, {Name: "Applier queue", Kind: view.KindNumber},
	}}
	for _, m := range members {
		address := m.address
		if m.local {
			address += " (this server)"
		}
		t.Rows = append(t.Rows, []string{address, strings.ToLower(m.state), dash(m.role), m.queue})
	}
	t.Total = len(t.Rows)
	return t
}

func groupFacet(single bool, members []groupMember) facet {
	var local *groupMember
	reachable := 0
	for i, m := range members {
		if m.local {
			local = &members[i]
		}
		if m.state != "UNREACHABLE" {
			reachable++
		}
	}
	status, detail := groupGrade(members, local, reachable)

	role := "group replication member"
	switch {
	case local == nil:
		role += " (unknown)"
	case local.state != "ONLINE":
		role += " (" + strings.ToLower(local.state) + ")"
	case !single:
		role += " (multi-primary, " + format.CountOf(len(members), "member") + ")"
	default:
		role += " (" + firstSet(local.role, "member") + ", " + format.CountOf(len(members), "member") + ")"
	}
	return facet{role: role, status: status, detail: detail}
}

// groupGrade is the verdict, stated here and not left to be assembled from the
// table beside it.
//
// The one that matters is the majority. A member cut off from the others keeps
// reading ONLINE about itself and UNREACHABLE about everyone else, and a
// group of two that loses one is already without a majority: the survivor
// stops committing writes and every other thing about the server — its
// threads, its connections, its reads — looks the same as a minute before.
// Reachable members are counted against all of them, the way the group counts.
func groupGrade(members []groupMember, local *groupMember, reachable int) (status, detail string) {
	var fails, warns []string
	switch {
	case local == nil:
		fails = append(fails, "this server is not among the members the group lists")
	case local.state == "RECOVERING":
		warns = append(warns, "this member is RECOVERING: still catching up from the group, not serving current data")
	case local.state != "ONLINE":
		fails = append(fails, "this member is "+local.state+", not part of the group")
	}
	if unreachable := len(members) - reachable; unreachable > 0 {
		if reachable*2 <= len(members) {
			fails = append(fails, "sees "+format.CountOf(reachable, "member")+" of "+strconv.Itoa(len(members))+
				" — no majority, so the group has stopped committing writes and this server must not be written to")
		} else {
			warns = append(warns, format.CountOf(unreachable, "member")+" unreachable")
		}
	}
	for _, m := range members {
		if m.local || m.state == "ONLINE" || m.state == "UNREACHABLE" {
			continue
		}
		warns = append(warns, m.address+" is "+m.state)
	}
	for _, m := range members {
		if m.queued >= groupQueueWarn {
			warns = append(warns, m.address+" has "+format.CountOf(int(m.queued), "transaction")+
				" waiting to be applied — it is falling behind the group")
		}
	}
	switch {
	case len(fails) > 0:
		return "fail", strings.Join(append(fails, warns...), "; ")
	case len(warns) > 0:
		return "warn", strings.Join(warns, "; ")
	}
	if len(members) == 1 {
		return "ok", "online, the only member of the group"
	}
	return "ok", "online, and all " + format.CountOf(len(members), "member") + " are online"
}
