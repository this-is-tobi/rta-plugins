package main

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/this-is-tobi/rta/pkg/view"
)

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// standbysTable is the streaming-replication table: one row per connected
// standby, its four positions side by side so a gap between any two is the
// eye's job rather than arithmetic, and the one number that makes drift
// obvious — how many bytes of the primary's WAL its replay has not reached.
//
// A lag cell that reads - is a standby with nothing recent to measure:
// PostgreSQL clears a lag once the standby has been caught up and idle for a
// while, so absence is the good news there and zero would claim a
// measurement that was never taken. A hidden row (see gradeStandby) reads the
// same in every cell, with State saying why.
func standbysTable(f replicationFacts) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Standby"}, {Name: "Client"}, {Name: "State"}, {Name: "Sync"}, {Name: "Slot"},
		{Name: "Sent"}, {Name: "Write"}, {Name: "Flush"}, {Name: "Replay"},
		{Name: "Behind", Kind: view.KindBytes},
		{Name: "Write lag", Kind: view.KindDuration},
		{Name: "Flush lag", Kind: view.KindDuration},
		{Name: "Replay lag", Kind: view.KindDuration},
		{Name: "Health", Kind: view.KindStatus},
	}}
	for _, s := range f.standbys {
		state := "hidden"
		if !s.hidden() {
			state = *s.state
		}
		t.Add(
			view.Plain(s.name), view.Plain(orDash(s.client)), view.Plain(state), view.Plain(syncText(s)), view.Plain(orDash(s.slot)),
			view.Plain(orDash(s.sent)), view.Plain(orDash(s.write)), view.Plain(orDash(s.flush)), view.Plain(orDash(s.replay)),
			bytesCell(s.behind),
			lagCell(s.writeLag), lagCell(s.flushLag), lagCell(s.replLag),
			view.Plain(gradeStandby(s).status),
		)
	}
	t.Total = len(t.Rows)
	return t
}

// slotsTable lists what the server is holding WAL back for. Until lost is
// how much more WAL can be written before PostgreSQL gives up on the slot;
// unlimited says max_slot_wal_keep_size is -1, which is not safety but the
// opposite — the slot is never invalidated, so it keeps everything until the
// disk is full.
func slotsTable(f replicationFacts) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Slot"}, {Name: "Type"}, {Name: "Database"},
		{Name: "Active"}, {Name: "Used by"},
		{Name: "Restart LSN"}, {Name: "Retained", Kind: view.KindBytes},
		{Name: "Until lost", Kind: view.KindBytes},
		{Name: "WAL status"},
		{Name: "Health", Kind: view.KindStatus},
	}}
	for _, s := range f.slots {
		active := "no"
		if s.active {
			active = "yes"
		}
		t.Add(
			view.Plain(s.name), view.Plain(s.kind), view.Plain(listedOrDash(s.database)), view.Plain(active), view.Plain(orDash(s.holder)),
			view.Plain(orDash(s.restart)), bytesCell(s.retained), untilLost(s),
			view.Plain(orDash(s.status)), view.Plain(gradeSlot(s).status),
		)
	}
	t.Total = len(t.Rows)
	return t
}

// syncText is the sync state with the priority that decides it: a standby's
// place in synchronous_standby_names is what says which of two potential
// ones is the one commits wait for. Async standbys all carry priority 0,
// which says nothing, so it is left off them.
func syncText(s standbyRow) string {
	if s.syncKind == nil {
		return "-"
	}
	if s.priority == nil || *s.priority == 0 {
		return *s.syncKind
	}
	return *s.syncKind + " (priority " + strconv.Itoa(int(*s.priority)) + ")"
}

func untilLost(s slotRow) view.Cell {
	switch {
	case s.safeLeft != nil:
		return view.Bytes(*s.safeLeft)
	case s.status == nil || *s.status == "lost":
		return view.Plain("-")
	}
	return view.Plain("unlimited")
}

// attentionTable is everything worth acting on, across the server, the
// standbys and the slots, in one list so a reader who wants only to know
// whether to worry reads one place. Empty on a replication that is fine, and
// then left off the page entirely.
func attentionTable(f replicationFacts) view.Table {
	type attention struct {
		what string
		g    verdict
	}
	var rows []attention
	add := func(what string, g verdict) {
		if severity(g.status) < 2 || len(g.reasons) == 0 {
			return
		}
		rows = append(rows, attention{what, g})
	}
	for _, s := range f.standbys {
		add(fmt.Sprintf("standby %s", s.name), gradeStandby(s))
	}
	for _, s := range f.slots {
		add("slot "+s.name, gradeSlot(s))
	}
	add("this standby", gradeReceiver(f))
	add("synchronous commit", gradeSynchronous(f))
	// Sorted before the table is filled, not after: a value is kept beside
	// the row it was added with, and sorting the rows alone would leave every
	// value on whichever row moved into its place.
	slices.SortStableFunc(rows, func(a, b attention) int { return severity(b.g.status) - severity(a.g.status) })
	t := view.Table{Columns: []view.Column{
		{Name: "What"}, {Name: "Status", Kind: view.KindStatus}, {Name: "Detail"},
	}}
	for _, r := range rows {
		t.Add(view.Plain(r.what), view.Plain(r.g.status), view.Plain(r.g.detail()))
	}
	t.Total = len(t.Rows)
	return t
}

func severity(status string) int {
	switch status {
	case gradeFail:
		return 3
	case gradeWarn:
		return 2
	}
	return 1
}
