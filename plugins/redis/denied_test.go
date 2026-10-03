package main

import (
	"strings"
	"testing"
)

// Every call opens with a PING, so an ACL user with the commands a view reads
// and no +ping is refused before the view runs, and the refusal is about that
// command: sent to look for it in the view, a monitoring user found nothing.
// The text is the one redis 7.2 gave a user granted +info alone.
func TestAnACLUserWithoutPingIsToldWhatTheConnectionNeeds(t *testing.T) {
	r := req(t, "redis.overview", map[string]any{"address": "10.0.0.1:6379"})
	ping := classify(&serverError{msg: "NOPERM User mon has no permissions to run the 'ping' command"}, "10.0.0.1:6379", r)
	if ping.Code != "redis.denied" || !strings.Contains(ping.Hint, "needs +ping beside the commands the view reads") {
		t.Errorf("ping = %s: %q, want the PING named", ping.Code, ping.Hint)
	}
	other := classify(&serverError{msg: "NOPERM User mon has no permissions to run the 'cluster|shards' command"}, "10.0.0.1:6379", r)
	if other.Code != "redis.denied" || other.Hint != "the ACL user is valid but not allowed this command or key" {
		t.Errorf("another command = %s: %q, want the generic hint", other.Code, other.Hint)
	}
}
