package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// addReplication puts the replication sections on a page: the server's own
// position, what needs attention, the standbys, and the slots. pg.replication
// is that page alone and pg.overview carries the same sections inside its
// own, so a person reading either sees one answer rather than two that could
// disagree.
//
// A section with nothing to say is left out rather than drawn empty: a
// primary nobody replicates from has no standbys table, and the server
// section's `replication` line says so in words, which an empty table with
// headers would not — it reads as a listing that failed.
func addReplication(ctx context.Context, p *plugin.Page, conn *pgx.Conn, req plugin.Request) error {
	f, err := readReplication(ctx, conn)
	if err != nil {
		return classify(err, req)
	}
	if f.server.versionNum < minReplicationVersion {
		return tooOldForReplication(f.server, req)
	}
	p.PutAs("replication", "replication", serverSection(f))
	if findings := attentionTable(f); len(findings.Rows) > 0 {
		p.PutAs("attention", "attention", findings)
	}
	if len(f.standbys) > 0 {
		p.PutAs("standbys", "standbys", standbysTable(f))
	}
	if len(f.slots) > 0 {
		p.PutAs("slots", "replication slots", slotsTable(f))
	}
	p.Warn(hiddenWarning(f, req))
	return nil
}

func replicationView(ctx context.Context, conn *pgx.Conn, req plugin.Request) (view.View, error) {
	p := plugin.NewPage(ctx, req)
	if err := addReplication(ctx, p, conn, req); err != nil {
		return nil, err
	}
	return p.View(), nil
}

func tooOldForReplication(s serverFacts, req plugin.Request) *view.Error {
	number, _, _ := strings.Cut(s.version, " ")
	return view.Errorf("pg.replication.version",
		"PostgreSQL %s is too old: replication is read from PostgreSQL 14 and newer", number).
		WithHint("14 is the oldest release PostgreSQL still maintains and the oldest this is tested against — " +
			"upgrade, or read pg_stat_replication yourself with " + req.Surface().CapabilityName("pg.query"))
}

// hiddenWarning is what a role without pg_monitor is told: which part of the
// answer it is not shown, and what would show it. Returned nil when nothing
// was hidden, which Page.Warn ignores.
func hiddenWarning(f replicationFacts, req plugin.Request) *view.Error {
	hidden := 0
	for _, s := range f.standbys {
		if s.hidden() {
			hidden++
		}
	}
	var what []string
	if hidden > 0 {
		what = append(what, "the state and positions of "+format.Count(hidden, "connected standby", "connected standbys"))
	}
	if f.receiver.hidden {
		what = append(what, "the WAL receiver")
	}
	if len(what) == 0 {
		return nil
	}
	user, sf := req.String("user"), req.Surface()
	return view.Errorf("pg.replication.hidden", "%q may not see %s", user, strings.Join(what, " or ")).
		WithHint("PostgreSQL shows a standby's state and positions only to a role in pg_monitor or " +
			"pg_read_all_stats — `GRANT pg_monitor TO \"" + user + "\"`, or name a role that has it with " +
			sf.SettingName("user") + ". Until then those cells read hidden or -, never zero")
}

func orDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

func bytesOrDash(n *int64) string {
	if n == nil {
		return "-"
	}
	return format.Bytes(*n)
}

func lagOrDash(l *float64) string {
	if l == nil {
		return "-"
	}
	return lagText(*l)
}

// sinceText is a moment and how long ago it was, the pair a reader wants
// from "when did this start" without doing the subtraction.
func sinceText(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04 UTC") + " (" + format.Ago(t) + ")"
}

// serverSection is the first screen: which server this is, where its WAL
// stands, and — on a standby — how far it has got. The same facts the
// SRE's own script prints, in the order it prints them.
func serverSection(f replicationFacts) view.KeyValue {
	kv := view.KeyValue{}
	add := func(key, value string) {
		if value != "" {
			kv.Pairs = append(kv.Pairs, view.Pair{Key: key, Value: value})
		}
	}
	if f.server.recovering {
		add("role", "standby")
	} else {
		add("role", "primary")
	}
	add("up since", sinceText(f.server.started))
	if f.server.recovering {
		standbyPairs(f, add)
	} else {
		add("timeline", f.primary.timeline)
		add("WAL position", f.primary.lsn)
		add("WAL file", f.primary.walFile)
		if f.server.syncNames != "" {
			add("synchronous standbys", f.server.syncNames)
		}
	}
	add("replication", replicationSummary(f))
	return kv
}

func standbyPairs(f replicationFacts, add func(key, value string)) {
	s, r := f.standby, f.receiver
	tl := s.timeline
	if f.timelineGuess && tl != "" {
		tl += " (from its last checkpoint, which can trail a recent switch)"
	}
	add("timeline", tl)
	add("upstream", upstreamText(r))
	add("received", orDash(s.receiveLSN))
	add("replayed", orDash(s.replayLSN))
	if s.gap != nil {
		add("received but not replayed", format.Bytes(*s.gap))
	}
	if r.endLSN != nil {
		line := *r.endLSN
		if r.endTime != nil {
			line += " (as of " + format.Ago(*r.endTime) + ")"
		}
		add("upstream position", line)
		add("behind upstream", bytesOrDash(r.behindEnd))
	}
	if s.replayedAt != nil {
		add("last replayed commit", sinceText(*s.replayedAt))
	}
	if s.paused != nil && *s.paused {
		add("replay", "paused")
	}
}

func upstreamText(r receiverRow) string {
	switch {
	case r.hidden:
		return "hidden from this role"
	case !r.present:
		return "none — no WAL receiver is running"
	}
	line := r.status
	if r.host != nil {
		line += " from " + *r.host
		if r.port != nil {
			line += ":" + strconv.Itoa(int(*r.port))
		}
	}
	if r.slot != nil && *r.slot != "" {
		line += ", slot " + *r.slot
	}
	return line
}

// replicationSummary is the one line that says whether this is replicating
// and how far behind, which the overview carries on its own. It names the
// state and never grades it: the colour is in the tables, and a sentence
// that said "healthy" over a hidden column would be claiming what it could
// not see. Nouns are counted with Count and both forms spelled out, since
// CountOf derives the plural from the noun and writes standbies for the one
// noun counted most here.
func replicationSummary(f replicationFacts) string {
	if f.server.recovering {
		return standbySummary(f)
	}
	n := len(f.standbys)
	if n == 0 {
		switch {
		case f.server.walLevel == "minimal":
			return "off — wal_level is minimal, so no WAL is written for a standby to read"
		case f.server.senders == 0:
			return "off — max_wal_senders is 0, so nothing can connect to stream from"
		case len(f.slots) == 0:
			return "no standby is connected and there are no replication slots"
		}
		return "no standby is connected (" + format.Count(len(f.slots), "replication slot", "replication slots") + " defined)"
	}
	var streaming, hidden int
	other := map[string]int{}
	var furthest *int64
	for _, s := range f.standbys {
		switch {
		case s.hidden():
			hidden++
		case *s.state == "streaming":
			streaming++
		default:
			other[*s.state]++
		}
		if s.behind != nil && (furthest == nil || *s.behind > *furthest) {
			furthest = s.behind
		}
	}
	var parts []string
	if streaming > 0 {
		parts = append(parts, format.Count(streaming, "standby", "standbys")+" streaming")
	}
	for _, state := range sortedKeys(other) {
		parts = append(parts, fmt.Sprintf("%d %s", other[state], stateWords(state)))
	}
	if hidden > 0 {
		parts = append(parts, fmt.Sprintf("%d hidden from this role", hidden))
	}
	if furthest != nil {
		parts = append(parts, "furthest behind "+format.Bytes(*furthest))
	}
	return strings.Join(parts, ", ")
}

// stateWords says a pg_stat_replication state the way a sentence wants it:
// the raw words are PostgreSQL's, and "2 backup" is not something anybody
// has seen on a screen before.
func stateWords(state string) string {
	switch state {
	case "backup":
		return "taking a base backup"
	case "catchup":
		return "catching up"
	case "startup":
		return "starting up"
	}
	return state
}

// replicationLine is the summary with a pointer to the page it summarises,
// for the overview's glance. Only where there is a page worth opening: a
// server that replicates to and from nothing has nothing for it to show, and
// pointing at an empty page would send somebody to read one sentence twice.
//
// It counts what the page's attention list holds, because the glance is the
// one place somebody looks while nothing is known to be wrong: a standby that
// went away reads "no standby is connected" and is only a slot that nobody
// reads, and a sentence that stayed silent about the warning beside it would
// be the quiet half of the failure. The count is a number, not a grade; the
// words and the colours are on the page.
func replicationLine(f replicationFacts, sf plugin.Surface) string {
	line := replicationSummary(f)
	attention := len(attentionTable(f).Rows)
	switch attention {
	case 0:
	case 1:
		line += ", 1 thing needs attention"
	default:
		line += fmt.Sprintf(", %d things need attention", attention)
	}
	if f.server.recovering || len(f.standbys) > 0 || len(f.slots) > 0 || attention > 0 {
		line += " — " + sf.CapabilityName("pg.replication") + " has the positions"
	}
	return line
}

func standbySummary(f replicationFacts) string {
	r := f.receiver
	var line string
	switch {
	case r.hidden:
		line = "upstream hidden from this role"
	case !r.present:
		line = "not streaming — no WAL receiver is running"
	default:
		line = r.status
		if r.host != nil {
			line += " from " + *r.host
		}
	}
	if f.standby.paused != nil && *f.standby.paused {
		line += ", replay paused"
	}
	switch {
	case r.behindEnd != nil:
		line += ", " + format.Bytes(*r.behindEnd) + " behind its upstream"
	case f.standby.gap != nil:
		line += ", " + format.Bytes(*f.standby.gap) + " received but not replayed"
	}
	return line
}
