package main

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "etcd.overview",
		Summary:    "Whether this cluster is healthy, and what it is made of",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "The endpoint's own status — version, who it thinks the leader is, its raft " +
			"term, committed and applied index and revision — with its storage, and every member " +
			"beside it: role, term, index, how many entries it is behind the leader, revision, " +
			"database size and health, each read from the member itself.\n\n" +
			"The storage row is the one to watch. etcd raises NOSPACE when the database file " +
			"reaches its quota and then refuses every write while continuing to answer reads, " +
			"which looks like a working cluster from anywhere except here. The use column is " +
			"graded against that quota, and a server older than 3.6 does not report one, so " +
			"the column is blank there rather than guessed.\n\n" +
			"The members table is how a split is visible: members that disagree about who the " +
			"leader is, or about the term, are not a cluster. A member 5000 entries behind the " +
			"leader or more is graded, because etcd keeps no more log than that after a " +
			"snapshot and sends a member further back the whole snapshot instead. Members are " +
			"asked together and not at one instant, so a few entries either way is the cost of " +
			"asking. The quorum line says how many voting members answered and how many a write " +
			"needs.\n\n" +
			"Each member is asked at the client URL it advertises, and a published port or a " +
			"forward rarely reaches those names: through a kube: or ssh: forward only the " +
			"endpoint's own member is asked, and a member this machine cannot reach says why " +
			"instead of reading as down. A username without TLS asks no other member, and with it " +
			"asks only those that advertise an https:// URL, so a credential is never sent in the " +
			"clear to an address the member list supplied.",
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

	rows, err := askMembers(ctx, c, req, st)
	if err != nil {
		return nil, err
	}
	lead := leaderView{id: st.Leader}
	for _, r := range rows {
		if r.id == st.Leader {
			lead.st = r.st
		}
	}

	pairs := []view.Pair{
		{Key: "endpoint", Value: req.Reached(endpoint)},
		{Key: "version", Value: st.Version},
		{Key: "member id", Value: hexID(st.Header.MemberId)},
		{Key: "leader", Value: leaderText(st)},
		{Key: "raft term", Value: strconv.FormatUint(st.RaftTerm, 10)},
		{Key: "raft index", Value: strconv.FormatUint(st.RaftIndex, 10)},
		{Key: "applied index", Value: appliedText(st)},
		{Key: "revision", Value: strconv.FormatInt(st.Header.Revision, 10)},
	}
	if text, ok := quorumText(rows, st.Leader); ok {
		pairs = append(pairs, view.Pair{Key: "quorum", Value: text})
	}
	if len(rows) == 1 && rows[0].learner && rows[0].name == "" {
		pairs = append(pairs, view.Pair{Key: "members", Value: "only this one is shown: a learner is not served the " +
			"member list, so point " + req.Surface().SettingName("endpoint") + " at a voting member to see the rest"})
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
	p.Put("status", view.KeyValue{Pairs: pairs})
	p.Put("storage", storageTable(st))

	p.Put("members", membersTable(rows, lead))
	return p.View(), nil
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
	return view.Table{
		Columns: []view.Column{
			{Name: "Size", Kind: view.KindBytes},
			{Name: "In use", Kind: view.KindBytes},
			{Name: "Quota", Kind: view.KindBytes},
			{Name: "Use %", Kind: view.KindUsage},
		},
		Rows: [][]string{{
			format.Bytes(st.DbSize),
			format.Bytes(st.DbSizeInUse),
			quotaBytes(st.DbSizeQuota),
			quotaCell(st.DbSize, st.DbSizeQuota),
		}},
		Total: 1,
	}
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
	t := view.Table{Columns: []view.Column{
		{Name: "ID"},
		{Name: "Name"},
		{Name: "Client URLs"},
		{Name: "Peer URLs"},
		{Name: "State", Kind: view.KindStatus},
	}}
	for _, m := range resp.Members {
		name, state := m.Name, "started"
		if name == "" {
			// etcd leaves the name empty until a member has joined and caught
			// up. Rendering that as a blank cell would read like missing data.
			name, state = "-", "unstarted"
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
	return t, nil
}

func joinURLs(urls []string) string {
	if len(urls) == 0 {
		return "-"
	}
	out := urls[0]
	for _, u := range urls[1:] {
		out += ", " + u
	}
	return out
}

func leaseListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "etcd.lease.list",
		Summary:    "Outstanding leases and how long each has left",
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
	}, plugin.Field{Name: "limit", Type: plugin.Int, Config: "limit", Default: 200, Min: 1, Max: 10000,
		Help: "how many leases to show"})
}

// leasesLeftOut is the row standing in for the n leases past the limit,
// split from leaseTable so its wording is assertable without a cluster.
func leasesLeftOut(sf plugin.Surface, n int) []string {
	return []string{"…", "-", "-", format.CountOf(n, "more lease") + "; raise " + sf.InputName("limit")}
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
			t.Rows = append(t.Rows, leasesLeftOut(req.Surface(), len(resp.Leases)-i))
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
		remaining := "expired"
		if ttl.TTL > 0 {
			remaining = (time.Duration(ttl.TTL) * time.Second).String()
		}
		t.Rows = append(t.Rows, []string{
			leaseID(int64(l.ID)),
			(time.Duration(ttl.GrantedTTL) * time.Second).String(),
			remaining,
			strconv.Itoa(len(ttl.Keys)),
		})
	}
	t.Total = len(t.Rows)
	return t, nil
}
