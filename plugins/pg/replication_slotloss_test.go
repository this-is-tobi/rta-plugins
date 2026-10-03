package main

import (
	"strings"
	"testing"
)

// A slot given up on says why in the words of the reason the server gives, and
// only the reasons about WAL talk about WAL. A logical slot on a standby that
// the primary's vacuum conflicted with (PostgreSQL 17: rows_removed, 16:
// conflicting) was "the WAL it needs has been removed (rows_removed)", about a
// slot whose WAL was all there.
func TestASlotGivenUpOnSaysWhyInTheServersReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason *string
		status string
		want   string
		not    string
	}{
		{"wal_removed", str("wal_removed"), "lost", "the WAL it needs has been removed (wal_removed)", ""},
		{"rows_removed", str("rows_removed"), "lost", "the primary removed rows it needs, a conflict with recovery (rows_removed)",
			"WAL it needs"},
		{"16's conflicting", str("conflict with recovery"), "lost", "a conflict with recovery, so it can no longer",
			"(conflict with recovery)"},
		{"wal_level_insufficient", str("wal_level_insufficient"), "lost", "wal_level fell below what logical decoding needs",
			"WAL it needs"},
		{"idle_timeout", str("idle_timeout"), "lost", "it sat idle past idle_replication_slot_timeout", "WAL it needs"},
		{"a reason added later", str("something_new"), "lost", "the server invalidated it (something_new)", "WAL it needs"},
		{"lost with no reason, before 17", nil, "lost", "the WAL it needs has been removed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := lostSlot()
			s.invalid, s.status = tc.reason, str(tc.status)
			g := gradeSlot(s)
			if g.status != gradeFail || len(g.reasons) != 1 || !strings.Contains(g.reasons[0], tc.want) ||
				(tc.not != "" && strings.Contains(g.reasons[0], tc.not)) {
				t.Errorf("%s: %q, want fail with %q and without %q", tc.name, g.reasons, tc.want, tc.not)
			}
		})
	}
}
