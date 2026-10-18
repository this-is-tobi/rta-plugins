package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What the overview's compact form is made of beyond the replication facts,
// each as a small function of its own so that a part that cannot be read costs
// its rows and not the page.

// clock is the moment an uptime is measured against, a variable so that a test
// can say what time it is.
var clock = time.Now

// uptime is how long a server has been up, in the two units that matter at its
// size: days and hours, hours and minutes inside a day, minutes inside an
// hour. The same spelling in every plugin that says one, so an on-call person
// reads "14d 2h" the same on a database as on a cache; it is written once here
// until the SDK has a helper for it.
func uptime(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return "<1m"
}

// upSince is when a server started and how long ago that was, the pair a
// reader wants from "what changed": a restart or a failover is the first
// answer, and it is the date that matches it to a log. The server says how long
// it has been up and not when it started, so the start is that far back from
// now, to the minute: to the second it moved on every refresh of a tile.
func upSince(up time.Duration) string {
	return clock().Add(-up).UTC().Truncate(time.Minute).Format("2006-01-02 15:04 UTC") + " (" + uptime(up) + ")"
}

// connectionsWarn and connectionsFail are the shares of max_connections taken
// at which the overview warns and fails. Rules of thumb: past the first a deploy
// or a retry storm exhausts the rest, and past the second what is left is what
// the one connection reserved for an administrator is, so an ordinary account
// is about to be refused.
const (
	connectionsWarn = 85
	connectionsFail = 95
)

// **Threads_running counts the session that is counting**, so an idle server
// said 1. The overview leaves its own thread out, as pg.overview leaves out its
// own session, and says nothing when no other thread is running.
func (i serverInfo) othersRunning() int64 { return max(i.running-1, 0) }

// share is how much of max_connections is taken, floored so that a server at
// 94.9% never reads as the 95% that fails.
func (i serverInfo) share() int64 {
	if i.maxConns <= 0 {
		return 0
	}
	return i.threads * 100 / i.maxConns
}

// connectionsText leads with the grade once the share is one worth a word, so
// that the row is coloured by the same rule as a status cell.
func (i serverInfo) connectionsText() string {
	line := fmt.Sprintf("%d of %d", i.threads, i.maxConns)
	switch share := i.share(); {
	case share >= connectionsFail:
		return fmt.Sprintf("fail — %s (%d%%)", line, share)
	case share >= connectionsWarn:
		return fmt.Sprintf("warn — %s (%d%%)", line, share)
	}
	return line
}

func (i serverInfo) connectionsFinding() finding {
	share := i.share()
	switch {
	case share >= connectionsFail:
		return finding{"fail", fmt.Sprintf("%d%% of max_connections", share)}
	case share >= connectionsWarn:
		return finding{"warn", fmt.Sprintf("%d%% of max_connections", share)}
	}
	return finding{}
}

func (i serverInfo) lockWaitsText() string {
	if i.lockWaits == 0 {
		return "none"
	}
	return format.Count(int(i.lockWaits), "transaction", "transactions") + " waiting on a row lock"
}

// loadText is the row for what the server is doing now: the threads other than
// this one that are running a statement, and the statements that took longer
// than long_query_time. The slow ones are a count since the server started, and
// say so, since a counter that is not dated reads as a rate.
func (i serverInfo) loadText() string {
	var parts []string
	if n := i.othersRunning(); n > 0 {
		parts = append(parts, format.Count(int(n), "thread", "threads")+" running")
	}
	if i.slowQueries > 0 {
		parts = append(parts, format.Count(int(i.slowQueries), "slow query", "slow queries")+" since start")
	}
	return strings.Join(parts, ", ")
}

// serverText is the server as a reader names it: the address it was reached at,
// then what answered. A version string that already carries the flavour's name
// is not given it twice.
func serverText(address string, i serverInfo) string {
	what := i.version
	if !strings.Contains(strings.ToLower(i.version), strings.ToLower(i.flavour)) {
		what = i.flavour + " " + i.version
	}
	return address + " · " + what
}

// finding is a grade and the few words that name the fact; the status row is
// made from the worst of them. A finding with no grade is none.
type finding struct{ grade, text string }

func severity(grade string) int {
	switch grade {
	case "fail":
		return 2
	case "warn":
		return 1
	}
	return 0
}

// valueWidth is how much of a line the value of a row has at 80 columns beside
// the longest key of the compact form (`replication`, `connections`) and the
// two spaces after it.
const valueWidth = 66

// statusText is the first row of the compact form: ok, or the grade of the worst
// fact and the words for it. A fail outranks a warn whichever was noted first,
// and among equals the first noted stands, so the same server reads the same
// way twice. Rows that could not be read are a warning of their own, last, since
// a service failing in a way that was read says more than what could not be.
func statusText(findings []finding, unreadable []string) string {
	if len(unreadable) > 0 {
		findings = append(findings, finding{"warn", strings.Join(unreadable, ", ") + " could not be read"})
	}
	var worst finding
	for _, f := range findings {
		if worst.grade == "" || severity(f.grade) > severity(worst.grade) {
			worst = f
		}
	}
	if worst.grade == "" {
		return "ok"
	}
	return worst.grade + " — " + fit(worst.text, valueWidth-len(worst.grade+" — "))
}

var parenthetical = regexp.MustCompile(` \([^)]*\)`)

// fit makes words one line: whole when they fit, then without the parentheses
// that explain a number, then cut with an ellipsis. A row that wraps is a row
// the tile does not have.
func fit(text string, width int) string {
	if len([]rune(text)) <= width {
		return text
	}
	text = parenthetical.ReplaceAllString(text, "")
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	n := max(width-1, 1)
	cut := string(runes[:n])
	if runes[n] != ' ' {
		if i := strings.LastIndex(cut, " "); i > width/2 {
			cut = cut[:i]
		}
	}
	return strings.TrimRight(cut, " ,;:—") + "…"
}

// row is one row of the compact form with what it adds to the verdict: a
// finding when it is worth a word in the status, and the name the status gives
// it when it could not be read.
type row struct {
	pair       view.Pair
	finding    finding
	unreadable string
}

// graded is a body that leads with its grade, so that the row is coloured by
// the same rule as a status cell, and the finding the status is made from.
func graded(key, word, body string) row {
	r := row{pair: view.Pair{Key: key, Value: word + " — " + fit(body, valueWidth-len(word+" — "))}}
	if severity(word) > 0 {
		// The verdict is a few words: the first reason, without the explanation
		// after its dash, which the row below it has.
		first, _, _ := strings.Cut(body, "; ")
		first, _, _ = strings.Cut(first, " — ")
		r.finding = finding{word, key + ": " + first}
	}
	return r
}

// gradedFacet is a facet as one row. A healthy one says okBody; one that is not
// says its reasons and leaves the role out, since a reader whose replica has
// stopped wants the thread and not the name of its source.
func gradedFacet(key string, f facet, okBody string) row {
	switch f.status {
	case "ok", "none":
		return graded(key, f.status, okBody)
	case "warn", "fail":
		return graded(key, f.status, firstSet(f.short, f.detail))
	}
	return graded(key, "warn", join(": ", f.role, f.detail))
}

// unreadRow is a row whose part the account cannot read, with the reason.
func unreadRow(key, why string) row {
	return row{pair: view.Pair{Key: key, Value: "could not be read — " + why}, unreadable: key}
}

// withoutCluster is the state as the replication row reads it: the clustering
// layer has a row of its own.
func (st state) withoutCluster() state {
	st.clusterElsewhere = st.cluster.facet != nil || st.unread[sectionCluster] != nil
	st.cluster = clusterPart{}
	unread := make(map[section]*view.Error, len(st.unread))
	for s, e := range st.unread {
		if s != sectionCluster {
			unread[s] = e
		}
	}
	st.unread = unread
	return st
}

// whyUnread is the reason a part could not be read, in a few words: the
// privilege that would give it when the server refused for want of one, and the
// refusal's own message when it was anything else.
func whyUnread(st state, s section) string {
	e := st.unread[s]
	if strings.HasSuffix(e.Code, ".replication.denied") {
		return "needs " + privilegeFor(s, st.version)
	}
	return e.Message
}

func replicationRowOf(st state) row {
	if st.unread[sectionReplica] != nil {
		return unreadRow("replication", whyUnread(st, sectionReplica))
	}
	st = st.withoutCluster()
	channels := make([]channelRow, len(st.channels))
	for i, c := range st.channels {
		channels[i] = assessChannel(c)
	}
	f := summarise(st, channels)
	return gradedFacet("replication", f, join(", ", f.role, f.brief))
}

// clusterRowOf is the row for a clustering layer, and nil where none is
// configured: only a Galera node or a Group Replication member has one.
func clusterRowOf(st state) *row {
	switch {
	case st.unread[sectionCluster] != nil:
		r := unreadRow("cluster", whyUnread(st, sectionCluster))
		return &r
	case st.cluster.facet != nil:
		r := gradedFacet("cluster", *st.cluster.facet, st.cluster.facet.detail)
		return &r
	}
	return nil
}
