package main

import (
	"strings"
	"testing"
	"time"
)

// Fixtures are rows read from real servers, not written from the
// documentation: a postgres:17 primary with a hot standby built by
// pg_basebackup, read as a role in pg_monitor and as one with no privilege at
// all. The numbers are the ones those servers returned.

func str(s string) *string   { return &s }
func i64(n int64) *int64     { return &n }
func i32(n int32) *int32     { return &n }
func f64(n float64) *float64 { return &n }
func yes() *bool             { b := true; return &b }

// What pg_stat_replication gave a role with no privilege: the row is there,
// the backend's pid and application name are there, and every column that
// says what the standby is doing is NULL. The slot is still joined, because
// pg_replication_slots is readable by everyone.
func hiddenStandby() standbyRow {
	return standbyRow{pid: 101, name: "walreceiver", slot: str("slot_replica_17")}
}

// A standby with replay paused while 106077496 bytes of WAL were written on
// the primary: sent, written and flushed to the standby at 0/A56F6A8, replay
// stuck at 0/4045970, the lags as the server reported them.
func driftingStandby() standbyRow {
	return standbyRow{
		pid: 101, name: "walreceiver", client: str("172.21.0.4"), state: str("streaming"),
		syncKind: str("async"), priority: i32(0),
		sent: str("0/A56F6A8"), write: str("0/A56F6A8"), flush: str("0/A56F6A8"), replay: str("0/4045970"),
		behind:   i64(106077496),
		writeLag: f64(0.113), flushLag: f64(0.114), replLag: f64(1.02),
		slot: str("slot_replica_17"),
	}
}

func caughtUpStandby() standbyRow {
	s := driftingStandby()
	s.replay, s.behind, s.replLag = str("0/A56F6A8"), i64(0), nil
	return s
}

// A slot as postgres:17 reports one whose WAL was removed under
// max_slot_wal_keep_size: nothing left to measure from.
func lostSlot() slotRow {
	return slotRow{
		name: "slot_replica_17", kind: "physical", status: str("lost"), invalid: str("wal_removed"),
	}
}

// A slot whose standby was stopped while 83886440 bytes were written, with
// max_wal_size lowered to 32MB so PostgreSQL said extended.
func extendedSlot() slotRow {
	return slotRow{
		name: "slot_replica_17", kind: "physical", restart: str("0/3000060"),
		retained: i64(83886440), status: str("extended"),
	}
}

func TestAStandbyIsGradedByItsState(t *testing.T) {
	for _, tc := range []struct {
		state, status string
	}{
		{"streaming", gradeOK},
		{"catchup", gradeWarn},
		{"startup", gradeWarn},
		{"stopping", gradeWarn},
		// pg_basebackup shows in pg_stat_replication for as long as it runs,
		// and is a client doing what it should, not a standby gone wrong.
		{"backup", gradeInfo},
	} {
		s := caughtUpStandby()
		s.state = str(tc.state)
		if got := gradeStandby(s).status; got != tc.status {
			t.Errorf("state %q graded %q, want %q", tc.state, got, tc.status)
		}
	}
	// A base backup is far behind the primary by nature, and a distance would
	// grade a client doing what it should as a standby gone wrong.
	backup := driftingStandby()
	backup.state, backup.behind = str("backup"), i64(2<<30)
	if got := gradeStandby(backup).status; got != gradeInfo {
		t.Errorf("a base backup 2 GiB behind graded %q, want %q", got, gradeInfo)
	}
}

// The bands are the whole content of the grade — boundary-tested, so a >=
// that should have been a > fails at the number somebody is staring at.
func TestADistanceBehindIsGradedAtItsBands(t *testing.T) {
	for _, tc := range []struct {
		behind int64
		status string
	}{
		{0, gradeOK},
		{behindWarn - 1, gradeOK},
		{behindWarn, gradeWarn},
		{behindFail - 1, gradeWarn},
		{behindFail, gradeFail},
	} {
		s := caughtUpStandby()
		s.behind = i64(tc.behind)
		if got := gradeStandby(s).status; got != tc.status {
			t.Errorf("%d bytes behind graded %q, want %q", tc.behind, got, tc.status)
		}
	}
}

func TestALagIsGradedAtItsBandsOnTheLongestOfTheThree(t *testing.T) {
	for _, tc := range []struct {
		lag    time.Duration
		status string
	}{
		{lagWarn - time.Millisecond, gradeOK},
		{lagWarn, gradeWarn},
		{lagFail - time.Millisecond, gradeWarn},
		{lagFail, gradeFail},
	} {
		// In flush, not replay: a slow disk shows there first.
		s := caughtUpStandby()
		s.flushLag = f64(tc.lag.Seconds())
		if got := gradeStandby(s).status; got != tc.status {
			t.Errorf("flush lag %v graded %q, want %q", tc.lag, got, tc.status)
		}
	}
}

func TestADriftingStandbyNamesHowFarBehindItIs(t *testing.T) {
	g := gradeStandby(driftingStandby())
	if g.status != gradeWarn {
		t.Fatalf("status = %q, want warn", g.status)
	}
	if !strings.Contains(g.detail(), "101.2 MiB") {
		t.Errorf("detail = %q, want the distance in bytes the reader can act on", g.detail())
	}
}

// **A standby this role cannot see is not a standby that is fine.** Every
// column PostgreSQL blanks to such a role is the one that would show it
// failing, so painting it ok is the quiet way for this view to lie.
func TestAHiddenStandbyIsUnknownNotOK(t *testing.T) {
	g := gradeStandby(hiddenStandby())
	if g.status != gradeUnknown {
		t.Errorf("status = %q, want unknown", g.status)
	}
	if len(g.reasons) != 0 {
		t.Errorf("reasons = %v, want none: nothing was read to reason from", g.reasons)
	}
}

func TestASlotIsGradedByWhatItCosts(t *testing.T) {
	active := slotRow{name: "a", kind: "physical", active: true, retained: i64(0), status: str("reserved")}
	inactive := active
	inactive.active = false
	temporary := inactive
	temporary.temporary = true
	heavy := active
	heavy.retained = i64(behindFail)
	unreserved := active
	unreserved.status = str("unreserved")

	for _, tc := range []struct {
		name   string
		slot   slotRow
		status string
	}{
		{"active and caught up", active, gradeOK},
		{"nothing reads it", inactive, gradeWarn},
		// A temporary slot lives as long as the backup that made it.
		{"temporary and between reads", temporary, gradeOK},
		{"holds a gigabyte", heavy, gradeFail},
		{"WAL about to go", unreserved, gradeFail},
		{"extended and inactive", extendedSlot(), gradeWarn},
		{"WAL removed", lostSlot(), gradeFail},
	} {
		if got := gradeSlot(tc.slot).status; got != tc.status {
			t.Errorf("%s: graded %q, want %q", tc.name, got, tc.status)
		}
	}
}

// One sentence for a slot that is gone, not three that say it in turn: an
// invalidated slot is also inactive and also lost, and each of those rules
// has nothing to add.
func TestALostSlotIsSaidOnce(t *testing.T) {
	g := gradeSlot(lostSlot())
	if len(g.reasons) != 1 {
		t.Fatalf("reasons = %q, want one", g.reasons)
	}
	if !strings.Contains(g.reasons[0], "wal_removed") {
		t.Errorf("reason = %q, want the server's own reason", g.reasons[0])
	}
}
