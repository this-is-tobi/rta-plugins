package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
)

// The grades are KindStatus words: ok, warn and fail are the ones every
// renderer colours, info is muted, and unknown is the one that says "the
// role may not see this" without painting it either way.
const (
	gradeOK      = "ok"
	gradeInfo    = "info"
	gradeWarn    = "warn"
	gradeFail    = "fail"
	gradeUnknown = "unknown"
)

// The bands a standby's distance behind, and the WAL a slot holds, are graded
// in. Rules of thumb, not a verdict on any workload: a streaming standby
// under write load sits some megabytes behind all day, so warn starts at four
// default WAL segments, and fail at a gigabyte, which is where a standby
// stops being late and starts needing somebody. The same two figures grade a
// slot's retained WAL, since a slot is the same distance seen from the
// primary's disk instead of the standby's clock.
const (
	behindWarn int64 = 64 << 20
	behindFail int64 = 1 << 30
	lagWarn          = 30 * time.Second
	lagFail          = 5 * time.Minute
)

// worse keeps the more severe of two grades, so one row can be judged on
// several rules and read as the worst of them.
func worse(a, b string) string {
	rank := map[string]int{gradeOK: 0, gradeInfo: 1, gradeUnknown: 1, gradeWarn: 2, gradeFail: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// verdict collects the reasons a row earned its grade, so the table carries
// the word and the attention list carries the sentence.
type verdict struct {
	status  string
	reasons []string
}

func (g *verdict) note(status, reason string) {
	g.status = worse(g.status, status)
	g.reasons = append(g.reasons, reason)
}

func (g verdict) detail() string { return strings.Join(g.reasons, "; ") }

// gradeBytes grades a distance in WAL bytes against the two bands, saying
// nothing for one that is not measured: an unknown distance is not a small
// one.
func gradeBytes(g *verdict, n *int64, what string) {
	switch {
	case n == nil:
	case *n >= behindFail:
		g.note(gradeFail, fmt.Sprintf("%s %s", what, format.Bytes(*n)))
	case *n >= behindWarn:
		g.note(gradeWarn, fmt.Sprintf("%s %s", what, format.Bytes(*n)))
	}
}

// gradeLag does the same for the time lags, taking the longest of the three
// a standby reports: the replay lag is nearly always it, but a standby whose
// disk is the slow part shows it in flush first.
func gradeLag(g *verdict, lags ...*float64) {
	var longest time.Duration
	for _, l := range lags {
		if l != nil {
			longest = max(longest, time.Duration(*l*float64(time.Second)))
		}
	}
	switch {
	case longest >= lagFail:
		g.note(gradeFail, "lagging by "+lagText(longest.Seconds()))
	case longest >= lagWarn:
		g.note(gradeWarn, "lagging by "+lagText(longest.Seconds()))
	}
}

// lagText is a lag as a person reads one. Milliseconds below a second, where
// pkg/format's windows round to whole seconds and every healthy standby would
// read "0s".
func lagText(seconds float64) string {
	if seconds < 1 {
		if seconds*1000 < 1 {
			return "<1 ms"
		}
		return fmt.Sprintf("%.0f ms", seconds*1000)
	}
	return format.Duration(time.Duration(seconds * float64(time.Second)))
}

// gradeStandby judges one connected standby. A row the role cannot read is
// unknown rather than ok: a hidden state could be a standby that is hours
// behind, and the table must not paint it green.
func gradeStandby(s standbyRow) verdict {
	if s.hidden() {
		return verdict{status: gradeUnknown}
	}
	g := verdict{status: gradeOK}
	switch *s.state {
	case "streaming":
	case "catchup":
		g.note(gradeWarn, "catching up, not yet streaming")
	case "startup":
		g.note(gradeWarn, "starting up, not yet streaming")
	case "backup":
		g.note(gradeInfo, "taking a base backup")
	default:
		g.note(gradeWarn, "state is "+*s.state)
	}
	if *s.state != "backup" {
		gradeBytes(&g, s.behind, "replay is behind by")
		gradeLag(&g, s.writeLag, s.flushLag, s.replLag)
	}
	return g
}

// gradeSlot judges one slot by what it can cost. A slot nobody reads keeps
// every WAL file written since it last moved, so it is the one that fills a
// primary's disk long after the standby it was for has gone; wal_status says
// how close PostgreSQL itself thinks it is to giving up on the slot.
func gradeSlot(s slotRow) verdict {
	g := verdict{status: gradeOK}
	// Said once. An invalidated slot reads lost, and has no restart position
	// left to measure or an active consumer to wait for, so the inactive and
	// retained rules below would only restate what the first sentence already
	// settles.
	if s.invalid != nil || (s.status != nil && *s.status == "lost") {
		why := "the WAL it needs has been removed"
		if s.invalid != nil {
			why += " (" + *s.invalid + ")"
		}
		g.note(gradeFail, why+", so it can no longer be used and whatever read it must be rebuilt")
		return g
	}
	if s.status != nil {
		switch *s.status {
		case "unreserved":
			g.note(gradeFail, "the WAL it needs is no longer guaranteed and may go at the next checkpoint")
		case "extended":
			g.note(gradeWarn, "keeps more WAL than max_wal_size allows")
		}
	}
	if !s.active && !s.temporary {
		g.note(gradeWarn, "inactive, so nothing is reading it and the WAL it holds cannot be recycled")
	}
	gradeBytes(&g, s.retained, "retains")
	return g
}

// gradeReceiver judges the standby this server is, from the link it holds to
// its upstream. Silent on a primary. On a link the role cannot read there is
// no finding about the link, which would be a guess about a column nobody was
// shown, but a paused replay is still read: pg_is_wal_replay_paused is open to
// every role, and a standby that is not applying WAL is the finding that
// matters most to whoever cannot see the rest.
func gradeReceiver(f replicationFacts) verdict {
	g := verdict{status: gradeOK}
	if !f.server.recovering {
		return g
	}
	r := f.receiver
	switch {
	case r.hidden:
		g.status = gradeUnknown
	case !r.present:
		g.note(gradeWarn, "no WAL receiver is running: it is replaying from an archive, or has lost its upstream")
	case r.status != "streaming":
		g.note(gradeWarn, "the WAL receiver is "+r.status+", not streaming")
	}
	if f.standby.paused != nil && *f.standby.paused {
		g.note(gradeWarn, "replay is paused: WAL still arrives and is not applied, so reads go stale")
	}
	if r.hidden {
		return g
	}
	// One figure, not both: the distance to the upstream contains the one
	// between received and replayed, and naming each would put the same
	// megabytes in the sentence twice. The nearer one stands in when the
	// upstream's own position is not readable.
	if r.behindEnd != nil {
		gradeBytes(&g, r.behindEnd, "behind its upstream by")
	} else {
		gradeBytes(&g, f.standby.gap, "received but not yet replayed:")
	}
	return g
}

// gradeSynchronous is the one finding a commit feels: with
// synchronous_standby_names set and no standby that is synchronous, every
// commit waits for one to appear, which from the application looks like a
// database that hangs on write. Not judged while any standby is hidden, since
// the hidden one may be the synchronous one, and not judged where the
// server-wide synchronous_commit is off or local, which is the setting under
// which a commit never waits for a standby however the names read.
func gradeSynchronous(f replicationFacts) verdict {
	g := verdict{status: gradeOK}
	if f.server.recovering || f.server.syncNames == "" {
		return g
	}
	if f.server.syncCommit == "off" || f.server.syncCommit == "local" {
		return g
	}
	for _, s := range f.standbys {
		if s.hidden() {
			return g
		}
		if s.syncKind != nil && (*s.syncKind == "sync" || *s.syncKind == "quorum") {
			return g
		}
	}
	g.note(gradeFail, "synchronous_standby_names is "+f.server.syncNames+
		" and no standby is synchronous, so every commit waits for one to connect")
	return g
}
