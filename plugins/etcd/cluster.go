package main

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// overviewRefresh is how often a dashboard tile of etcd.overview is re-run, and
// the pace a configuration cannot shorten. A run is a Status, a MemberList and a
// connection to every other member it can reach, and a member that does not
// answer holds it for memberTimeout: the host's own pace, which a configuration
// could bring down to the timer's tick, dialled a cluster at the speed of a clock
// for a glance.
const overviewRefresh = time.Minute

func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "etcd.overview",
		Summary:    "Whether this cluster is healthy, and what it is made of",
		Keywords:   []string{"summary", "status", "version", "leader", "quorum"},
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Refresh:    overviewRefresh,
		Description: "Whether writes work, in a few rows, worst first. `status` leads and is `ok`, or " +
			"`warn` or `fail` followed by the worst fact. Then the alarms, how many voting members " +
			"answer with a leader against what a write needs, who the leader is, how much of its " +
			"quota the database takes, the revision, and last the version and the member asked.\n\n" +
			"Storage is the row to watch. etcd raises NOSPACE when the database file reaches its " +
			"quota and then refuses every write while continuing to answer reads, which looks like " +
			"a working cluster from anywhere except here: a NOSPACE alarm is a `fail`. A server " +
			"older than 3.6 does not report a quota, and the row says so rather than guessing one.\n\n" +
			"Each member is asked at the client URL it advertises, and waited for two seconds: a " +
			"member that does not answer is named. Through a kube: or ssh: forward only the " +
			"endpoint's own member is asked, and the quorum is not counted. A username without " +
			"TLS asks no other member, and with it asks only those that advertise an https:// " +
			"URL, so a credential is never sent in the clear to an address the member list supplied.\n\n" +
			"`detail` adds the endpoint's own status (member id, raft term, committed and applied " +
			"index, revision, alarms) and a table of every member read from the member itself: role, " +
			"term, index, entries behind the leader, revision, database size and health. Members " +
			"that disagree about the leader or the term are not a cluster. A member 5000 entries " +
			"behind the leader or more is graded, because etcd keeps no more log than that after a " +
			"snapshot and sends a member further back the whole snapshot instead.",
		Agent: "Whether writes work, worst first: `status` is `ok`, or `warn` or `fail` and the worst " +
			"fact. Then alarms (NOSPACE is a fail: at the quota etcd refuses every write and still answers " +
			"reads), voting members answering against what a write needs, the leader, storage against the " +
			"quota (blank before etcd 3.6), the revision and the version. A member that does not answer " +
			"in two seconds is named. Through a forward only the endpoint's member is asked and the quorum " +
			"is not counted. `detail` adds the endpoint's status and a row per member read from the member " +
			"itself: role, term, index, entries behind the leader, revision, size, health.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *clientv3.Client) (view.View, error) {
				return overviewView(ctx, c, req)
			})
		},
	})
}

func overviewView(ctx context.Context, c *clientv3.Client, req plugin.Request) (view.View, error) {
	endpoint := endpointOf(req)
	st, err := c.Status(ctx, endpoint)
	if err != nil {
		return nil, classify(err, req)
	}

	// The endpoint answered, so what it cannot say about the others is a row
	// that says so and not a page that is an error: a role may read the status
	// and not the member list, and a quorum nobody counted must not read as ok.
	var unread string
	rows, err := askMembers(ctx, c, req, st)
	if err != nil {
		rows, unread = []memberRow{{id: st.Header.MemberId, st: st}}, memberListWhy(classify(err, req))
	}
	lead := leaderOf(st, rows)
	summary := compactOverview(st, rows, lead, unread)
	if !req.Bool("detail") {
		return summary, nil
	}

	pairs := []view.Pair{
		{Key: "address", Value: req.Reached(endpoint)},
		{Key: "version", Value: st.Version},
		{Key: "member id", Value: hexID(st.Header.MemberId)},
		{Key: "leader", Value: leaderText(st)},
		{Key: "raft term", Value: strconv.FormatUint(st.RaftTerm, 10)},
		{Key: "raft index", Value: strconv.FormatUint(st.RaftIndex, 10)},
		{Key: "applied index", Value: appliedText(st)},
		{Key: "revision", Value: strconv.FormatInt(st.Header.Revision, 10)},
	}
	if unread != "" {
		pairs = append(pairs, view.Pair{Key: "quorum", Value: unreadText(unread)})
	} else if text, ok := quorumText(rows, st.Leader); ok {
		pairs = append(pairs, view.Pair{Key: "quorum", Value: text})
	}
	if len(rows) == 1 && rows[0].learner && rows[0].name == "" {
		pairs = append(pairs, view.Pair{Key: "members", Value: "only this one is shown: a learner is not served the " +
			"member list, so point " + req.Surface().SettingName("address") + " at a voting member to see the rest"})
	}
	// Alarms are the reason this capability is worth running. A cluster over
	// its quota answers reads normally and refuses every write, which is
	// invisible from the application side until something tries to write.
	if len(st.Errors) > 0 {
		for _, e := range st.Errors {
			pairs = append(pairs, view.Pair{Key: "ALARM", Value: e})
		}
	} else {
		pairs = append(pairs, view.Pair{Key: "alarms", Value: "none"})
	}

	p := plugin.NewPage(ctx, req)
	p.Put("summary", summary)
	p.Put("status", view.KeyValue{Pairs: pairs})
	p.Put("storage", storageTable(st))

	if unread != "" {
		p.Put("members", view.Text{Body: unreadText(unread)})
	} else {
		p.Put("members", membersTable(rows, lead))
	}
	return p.View(), nil
}

// memberListWhy is why the member list could not be read, short enough for a
// row: the error a refusal comes as names the endpoint and the server's own
// sentence, which the row beside it does not need.
func memberListWhy(verr *view.Error) string {
	switch verr.Code {
	case "etcd.denied":
		return "the role may not list the members"
	case "etcd.timeout":
		return "no answer in time"
	}
	return "the member list failed (" + verr.Code + ")"
}

func unreadText(why string) string { return "could not be read — " + why }

// leaderOf is the leader as the endpoint named it, with its status when the
// leader was among the members asked: the figure every other member's distance
// is read against.
func leaderOf(st *clientv3.StatusResponse, rows []memberRow) leaderView {
	lead := leaderView{id: st.Leader}
	for _, r := range rows {
		if r.id == st.Leader {
			lead.st = r.st
		}
	}
	return lead
}

// grade is how bad a fact is, in the words the status row leads with and the
// theme colours: nothing, warn, fail.
type grade int

const (
	_ grade = iota
	gradeWarn
	gradeFail
)

// finding is one thing wrong, in a few words, for the status row.
type finding struct {
	grade grade
	text  string
}

// verdict is the status row: ok, or the worst finding with its grade. Of
// several equally bad, the first stands, and the rows are built in the order
// that puts the alarms first, then the leader, then the members.
func verdict(found []finding) string {
	var worst *finding
	for i := range found {
		if worst == nil || found[i].grade > worst.grade {
			worst = &found[i]
		}
	}
	switch {
	case worst == nil:
		return "ok"
	case worst.grade == gradeFail:
		return "fail — " + worst.text
	}
	return "warn — " + worst.text
}

// alarmPattern is how the status of a member spells an alarm in its errors:
// the member that raised it as a decimal id, which no tool of etcd's prints a
// member id as, and the alarm's name.
var alarmPattern = regexp.MustCompile(`^memberID:(\d+) alarm:(\w+)$`)

// noLeaderError is what a member with no leader reports among its errors, which
// the leader row already says.
const noLeaderError = "etcdserver: no leader"

// compactOverview is the answer a tile and an agent read by default, built from
// what the overview already asked: the endpoint's status and every member's own.
//
// **The order is the order of a glance, and it is the order of what stops a
// write.** The verdict, then the alarms, then whether a quorum answers and who
// leads, then how close the database is to the quota that raises NOSPACE, and
// last the counters and the version, which are what a healthy cluster and a
// dying one have in common. A tile is cut from the bottom, and the four raft
// counters this used to open with were the first lines of every tile.
//
// **A member that is down is a warning, not a failure, unless the endpoint has
// no leader.** The cluster is serving on the others, and what an on-call
// person needs is which member to go and look at: its name is in the status
// row and the quorum row. Whether the cluster can still take a write is the
// leader's absence, which is the failure.
//
// **A member list the role may not read leaves the quorum unsaid and says so**
// (unread is why): the verdict is a warning that names the row, and the rows
// that came from the endpoint's own status stand.
func compactOverview(st *clientv3.StatusResponse, rows []memberRow, lead leaderView, unread string) view.KeyValue {
	name := func(id uint64) string {
		for _, r := range rows {
			if r.id == id && r.name != "" {
				return plugin.ListedName(r.name)
			}
		}
		return hexID(id)
	}
	alarms, alarmFound := alarmsRow(st, name)
	quorum, memberFound := quorumRow(rows, lead, name)
	if unread != "" {
		quorum, memberFound = unreadText(unread), []finding{{gradeWarn, "quorum could not be read"}}
	}
	leader, leaderFound := leaderRow(st, name)
	storage, storageFound := storageRow(st)

	// The order a finding wins in, which is not the order of the rows: what
	// stops a write, then the leader, then the members, then the room left.
	var found []finding
	for _, fs := range [][]finding{alarmFound, leaderFound, memberFound, storageFound} {
		found = append(found, fs...)
	}
	return view.KeyValue{Pairs: []view.Pair{
		{Key: "status", Value: verdict(found)},
		{Key: "alarms", Value: alarms},
		{Key: "quorum", Value: quorum},
		{Key: "leader", Value: leader},
		{Key: "storage", Value: storage},
		{Key: "revision", Value: strconv.FormatInt(st.Header.Revision, 10)},
		{Key: "version", Value: st.Version + ", answered by " + name(st.Header.MemberId)},
	}}
}

// alarmsRow is what the endpoint reports among its errors, which is where etcd
// puts its alarms: each as the member that raised it, in decimal, and its name.
// The absence of a leader is among them too, and the leader row says it.
func alarmsRow(st *clientv3.StatusResponse, name func(uint64) string) (string, []finding) {
	var texts []string
	var found []finding
	for _, e := range st.Errors {
		if e == noLeaderError {
			continue
		}
		m := alarmPattern.FindStringSubmatch(e)
		if m == nil {
			texts = append(texts, plugin.ListedName(e))
			found = append(found, finding{gradeFail, plugin.ListedName(e)})
			continue
		}
		id, _ := strconv.ParseUint(m[1], 10, 64)
		text := m[2] + " on " + name(id)
		texts = append(texts, text)
		if m[2] == "NOSPACE" {
			text += ": writes are refused, reads still answer"
		}
		found = append(found, finding{gradeFail, text})
	}
	if len(texts) == 0 {
		return "none", nil
	}
	return strings.Join(texts, "; "), found
}

// quorumRow says how many of the voting members answer with a leader against
// how many a write needs, and names the ones that do not answer at all.
//
// **It makes no claim where it did not ask.** A count of the members that were
// asked is a statement about the cluster only when all of them were, and
// through a forward, or with a username over plaintext, the others are not:
// the row says it did not count, and that is the route's limit and no fault of
// the cluster's, so it grades nothing.
func quorumRow(rows []memberRow, lead leaderView, name func(uint64) string) (string, []finding) {
	if len(rows) == 1 && rows[0].learner && rows[0].name == "" {
		return "not counted — a learner is not served the member list", nil
	}
	var found []finding
	var unreachable []string
	for _, r := range rows {
		state, short, _ := memberVerdict(r, lead)
		switch state {
		case "ok", "info":
			continue
		case "unreachable":
			unreachable = append(unreachable, name(r.id))
		}
		found = append(found, finding{gradeWarn, "member " + name(r.id) + " " + short})
	}
	voters, answered, ok := quorumCount(rows)
	if !ok {
		return "not counted — not every member is asked from here", found
	}
	text := fmt.Sprintf("%d of %s healthy, a write needs %d", answered, format.CountOf(voters, "member"), voters/2+1)
	if len(unreachable) > 0 {
		text += "; " + strings.Join(unreachable, ", ") + " unreachable"
	}
	return text, found
}

// leaderRow is who leads, by name. A member that reports leader 0 has none: an
// election is under way or the cluster has lost its quorum, and either way no
// write is committed until one ends.
func leaderRow(st *clientv3.StatusResponse, name func(uint64) string) (string, []finding) {
	if st.Leader == 0 {
		const why = "an election is under way or quorum is lost"
		return "none — " + why, []finding{{gradeFail, "no leader: " + why}}
	}
	return name(st.Leader), nil
}

// storageRow is how much of its quota the database file takes, which is where
// NOSPACE is raised. At the quota etcd refuses every write; before it the
// cluster is only closer, so the row warns from the theme's own amber and fails
// when the file is the quota.
func storageRow(st *clientv3.StatusResponse) (string, []finding) {
	share, ok := quotaShare(st.DbSize, st.DbSizeQuota)
	if !ok {
		return format.Bytes(st.DbSize) + ", no quota reported (before etcd 3.6)", nil
	}
	text := strconv.FormatFloat(share, 'f', 1, 64) + "% of " + format.Bytes(st.DbSizeQuota) + " quota"
	switch {
	case share >= 100:
		return text, []finding{{gradeFail, "storage is at its quota: writes are refused"}}
	case share >= view.UsageWarn:
		return text, []finding{{gradeWarn, "storage at " + strconv.FormatFloat(share, 'f', 1, 64) + "% of its quota"}}
	}
	return text, nil
}

// storageTable is the endpoint's own backend: how large the database file is,
// how much of it is live data, and how close it is to the quota that stops
// writes.
//
// **A table rather than three more pairs in the status block, because a
// percentage in a KeyValue cannot be graded.** view.Pair carries a key and a
// string and nothing else — there are no column kinds in a KeyValue — and
// grading is the entire point of showing this one. `use %` declares
// view.KindUsage, the kind for a figure where 100 is a wall rather than a
// total, which is exactly what a backend quota is: etcd raises NOSPACE at it,
// then refuses every write while still answering reads, and that is the
// failure that looks like a healthy cluster from everywhere except here.
func storageTable(st *clientv3.StatusResponse) view.Table {
	t := view.Table{
		Columns: []view.Column{
			{Name: "Size", Kind: view.KindBytes},
			{Name: "In use", Kind: view.KindBytes},
			{Name: "Quota", Kind: view.KindBytes},
			{Name: "Use %", Kind: view.KindUsage},
		},
		Total: 1,
	}
	quota := view.Plain(quotaBytes(st.DbSizeQuota))
	if st.DbSizeQuota > 0 {
		quota = view.Bytes(st.DbSizeQuota)
	}
	use := view.Plain(quotaCell(st.DbSize, st.DbSizeQuota))
	if share, ok := quotaShare(st.DbSize, st.DbSizeQuota); ok {
		use = view.Number(use.Text, share)
	}
	t.Add(view.Bytes(st.DbSize), view.Bytes(st.DbSizeInUse), quota, use)
	return t
}

// quotaShare is how much of its backend quota a member's database file takes,
// as the percentage that is printed, and whether the server said enough to
// work one out.
//
// **DbSize, not DbSizeInUse.** The quota is checked against the physical file,
// so a database whose pages are mostly free still alarms; the difference
// between the two is what a defragment would give back, which is why the table
// prints both beside this.
//
// **A quota of zero is a server that did not report one, not a server without
// one.** dbSizeQuota arrived in etcd 3.6 and an older member answers the same
// StatusResponse with the field absent — while still having a quota, 2 GiB by
// default. Filling that blank with the default would grade a real number
// against an invented denominator, on the one screen somebody opens to find
// out whether they are near it. Nothing is reported instead, and the renderer
// paints a cell it cannot read neutral, which is the colour "this was not
// measured" is supposed to have.
//
// Rounded here, once, so that the number printed is the number graded: 89.96
// prints as "90.0%" and has to land in the band the renderer will put "90.0%"
// in, not the one the raw value falls in. `sys disk` carried exactly that bug
// — amber beside green about a single measurement — until it rounded first.
func quotaShare(dbSize, quota int64) (float64, bool) {
	if quota <= 0 || dbSize < 0 {
		return 0, false
	}
	return math.Round(float64(dbSize)/float64(quota)*1000) / 10, true
}

// quotaCell renders the graded cell, or the "-" this file's tables already use
// for a fact a member did not supply.
func quotaCell(dbSize, quota int64) string {
	share, ok := quotaShare(dbSize, quota)
	if !ok {
		return "-"
	}
	return strconv.FormatFloat(share, 'f', 1, 64) + "%"
}

// quotaBytes is the denominator itself, blanked the same way for the same
// reason — a "0 B" quota would read like a cluster that can hold nothing.
func quotaBytes(quota int64) string {
	if quota <= 0 {
		return "-"
	}
	return format.Bytes(quota)
}

// leaderText spells out the case a raw ID hides. A member reporting leader 0
// has no leader — it is mid-election or has lost quorum — and printing "0"
// looks like an ID rather than like the outage it is.
func leaderText(st *clientv3.StatusResponse) string {
	if st.Leader == 0 {
		return "NONE — this member has no leader, so the cluster is mid-election or has lost quorum"
	}
	if st.Leader == st.Header.MemberId {
		return hexID(st.Leader) + " (this member)"
	}
	return hexID(st.Leader)
}

// hexID renders a member ID the way etcdctl does, so an ID copied from here
// matches one copied from there.
func hexID(id uint64) string { return fmt.Sprintf("%x", id) }

// leaseID renders a lease ID the way etcdctl does, which is not the way it
// renders a member ID: %016x, zero-padded to sixteen digits. Put through
// hexID, a lease below 1<<60 lost its leading zero, and etcd issues those as
// a matter of course: the top two bytes of every ID it generates are the low
// two bytes of the granting member's ID, which begin with zeros for one
// member in eight once the sign bit is cleared. The same lease then read as
// two different strings in the two tools.
//
// It formats the signed ID the client hands over, as etcdctl formats it,
// rather than widening it first. Every lease etcd issues is positive, so the
// two forms agree on each of them; a negative one could only be an ID a
// client chose itself, and it now reads with its sign in both tools instead
// of as a two's complement only this one printed.
func leaseID(id int64) string { return fmt.Sprintf("%016x", id) }

func memberListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "etcd.member.list",
		Summary:    "Who is in this cluster, and how each one is reachable",
		Keywords:   []string{"peers", "nodes", "raft", "leader", "learner"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Member IDs, names and their client and peer URLs.\n\n" +
			"A member still learning the cluster's state has no name yet and is shown as " +
			"unstarted, which is the difference between a cluster mid-join and one with a " +
			"member that never came back.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *clientv3.Client) (view.View, error) {
				return memberTable(ctx, c, req)
			})
		},
	})
}

func memberTable(ctx context.Context, c *clientv3.Client, req plugin.Request) (view.Table, error) {
	resp, err := memberList(ctx, c)
	if err != nil {
		return view.Table{}, classify(err, req)
	}
	return memberListTable(resp.Members), nil
}

// memberListTable is split from the fetch so what a member's name and URLs
// are drawn as is assertable without a cluster.
//
// **A member's name and its URLs are whatever the member says they are.** The
// name is its own --name, never checked beyond being given, and a member
// publishes the URLs it advertises to the cluster itself, so a stranger who
// runs one — or a member that is compromised — chooses both, and what the
// table prints is shown through plugin.ListedName so that an escape sequence
// or a newline in either is written out rather than drawn.
func memberListTable(members []*etcdserverpb.Member) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "ID"},
		{Name: "Name"},
		{Name: "Client URLs"},
		{Name: "Peer URLs"},
		{Name: "State", Kind: view.KindStatus},
	}}
	for _, m := range members {
		name, state := memberName(m.Name), "started"
		if m.Name == "" {
			state = "unstarted"
		}
		if m.IsLearner {
			state = "learner"
		}
		t.Rows = append(t.Rows, []string{
			hexID(m.ID), name,
			joinURLs(m.ClientURLs), joinURLs(m.PeerURLs), state,
		})
	}
	t.Total = len(t.Rows)
	return t
}

// memberName is a member's name as a table shows it. etcd leaves the name
// empty until a member has joined and caught up, and rendering that as a blank
// cell would read like missing data.
func memberName(name string) string {
	if name == "" {
		return "-"
	}
	return plugin.ListedName(name)
}

func joinURLs(urls []string) string {
	if len(urls) == 0 {
		return "-"
	}
	listed := make([]string, len(urls))
	for i, u := range urls {
		listed[i] = plugin.ListedName(u)
	}
	return strings.Join(listed, ", ")
}

func leaseListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:       "etcd.lease.list",
		Summary:  "Outstanding leases and how long each has left",
		Keywords: []string{"ttl", "expiry", "expire", "keepalive"},
		Examples: []plugin.Example{
			{Title: "the first fifty leases", Inputs: map[string]any{"limit": 50}},
		},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Every lease the cluster is holding, with its granted TTL and what remains.\n\n" +
			"Leases are how ephemeral keys die: a service that stops renewing loses its " +
			"registration when the lease expires. A lease with a long TTL and no renewals is " +
			"why a dead service is still in service discovery.\n\n" +
			"IDs and timings only, never the keys attached to them — the same read/write split " +
			"etcd.kv.list and etcd.kv.get draw.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *clientv3.Client) (view.View, error) {
				return leaseTable(ctx, c, req)
			})
		},
	}, plugin.Field{Name: "limit", Short: "n", Type: plugin.Int, Config: "limit", Default: 200, Min: 1, Max: 10000,
		Help: "how many leases to show"})
}

// leasesLeftOut is the row standing in for the n leases past the limit,
// split from leaseTable so its wording is assertable without a cluster.
func leasesLeftOut(sf plugin.Surface, n int) []string {
	return []string{"…", "-", "-", format.CountOf(n, "more lease") + "; raise " + sf.InputName("limit")}
}

// leaseTimes is a lease's granted ttl and what remains of it, in the form every
// other duration here is written: format.Duration, "10m" and "1h30m", not
// time.Duration's "10m0s" and "1h30m0s", which nobody types and which carry
// a seconds figure that is never the answer. A lease the server reports no
// time left on is expired.
func leaseTimes(grantedSeconds, remainingSeconds int64) (granted, remaining string) {
	granted = format.Duration(time.Duration(grantedSeconds) * time.Second)
	if remainingSeconds <= 0 {
		return granted, "expired"
	}
	return granted, format.Duration(time.Duration(remainingSeconds) * time.Second)
}

func leaseTable(ctx context.Context, c *clientv3.Client, req plugin.Request) (view.View, error) {
	resp, err := c.Leases(ctx)
	if err != nil {
		return nil, classify(err, req)
	}
	limit := req.Int("limit")
	t := view.Table{Columns: []view.Column{
		{Name: "Lease"},
		{Name: "Granted TTL", Kind: view.KindDuration},
		{Name: "Remaining", Kind: view.KindDuration},
		{Name: "Keys", Kind: view.KindNumber},
	}}
	for i, l := range resp.Leases {
		if i == limit {
			// Named in the table rather than dropped. There is no cursor to
			// hand back — Leases returns every ID in one response, and the
			// cost being bounded here is the one TimeToLive round trip each
			// row needs — so the honest thing is to say how many were not
			// asked about.
			t.Add(plain(leasesLeftOut(req.Surface(), len(resp.Leases)-i)...)...)
			break
		}
		// TimeToLive with WithAttachedKeys returns the count without the key
		// names, which is exactly the line this capability sits on: how many
		// things depend on this lease is a fact about the lease, and which
		// things they are is a fact about the keyspace.
		ttl, err := c.TimeToLive(ctx, l.ID, clientv3.WithAttachedKeys())
		if err != nil {
			return nil, classify(err, req)
		}
		t.Add(leaseRow(int64(l.ID), ttl.GrantedTTL, ttl.TTL, len(ttl.Keys))...)
	}
	// The leases there are, not the rows: past the limit, the row standing in
	// for the rest is no lease, and counting it said there was one more.
	t.Total = len(resp.Leases)
	t.Empty = "no leases"
	return t, nil
}

// leaseRow is one lease's row, its two times valued in seconds, and an
// expired lease's time left as zero: the server reports -1 for it, which is
// no length of time, and a program asking which leases end within a minute
// should find the ones that already have.
func leaseRow(id, grantedSeconds, remainingSeconds int64, keys int) []view.Cell {
	granted, remaining := leaseTimes(grantedSeconds, remainingSeconds)
	return []view.Cell{
		view.Plain(leaseID(id)),
		view.Duration(granted, time.Duration(grantedSeconds)*time.Second),
		view.Duration(remaining, time.Duration(max(remainingSeconds, 0))*time.Second),
		view.Plain(strconv.Itoa(keys)),
	}
}

// plain is a row of cells with no value of their own.
func plain(row ...string) []view.Cell {
	cells := make([]view.Cell, len(row))
	for i, text := range row {
		cells[i] = view.Plain(text)
	}
	return cells
}
