package main

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/this-is-tobi/rule-them-all/pkg/format"
	"github.com/this-is-tobi/rule-them-all/pkg/plugin"
	"github.com/this-is-tobi/rule-them-all/pkg/view"
)

func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "etcd.overview",
		Summary:    "Whether this cluster is healthy, and what it is made of",
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Description: "The endpoint's own status — version, who it thinks the leader is, and how " +
			"far behind its raft log is — with its storage and the member list beside it.\n\n" +
			"The storage row is the one to watch. etcd raises NOSPACE when the database file " +
			"reaches its quota and then refuses every write while continuing to answer reads, " +
			"which looks like a working cluster from anywhere except here. The use column is " +
			"graded against that quota, and a server older than 3.6 does not report one, so " +
			"the column is blank there rather than guessed.\n\n" +
			"--detail adds every member's own view, which is how a split is visible: members that " +
			"disagree about who the leader is are not a cluster.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *clientv3.Client) (view.View, error) {
				return overviewView(ctx, c, req)
			})
		},
	})
}

func overviewView(ctx context.Context, c *clientv3.Client, req plugin.Request) (view.View, error) {
	endpoint := req.String("endpoint")
	st, err := c.Status(ctx, endpoint)
	if err != nil {
		return nil, classify(err, req)
	}

	pairs := []view.Pair{
		{Key: "endpoint", Value: endpoint},
		{Key: "version", Value: st.Version},
		{Key: "member id", Value: hexID(st.Header.MemberId)},
		{Key: "leader", Value: leaderText(st)},
		{Key: "raft term", Value: strconv.FormatUint(st.RaftTerm, 10)},
		{Key: "raft index", Value: strconv.FormatUint(st.RaftIndex, 10)},
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

	members, err := memberTable(ctx, c, req)
	if err != nil {
		return nil, err
	}
	p.Put("members", members)

	if req.Bool("detail") {
		health, err := memberHealthTable(ctx, c, req)
		if err != nil {
			return nil, err
		}
		p.Put("health", health)
	}
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
			format.Bytes(uint64(st.DbSize)),
			format.Bytes(uint64(st.DbSizeInUse)),
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
	return format.Bytes(uint64(quota))
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
	resp, err := c.MemberList(ctx)
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

// memberHealthTable asks every member for its own view, which is the only way
// a split is visible. Members that disagree about the leader or the raft term
// are not a cluster, and no single endpoint's status can show that.
func memberHealthTable(ctx context.Context, c *clientv3.Client, req plugin.Request) (view.Table, error) {
	resp, err := c.MemberList(ctx)
	if err != nil {
		return view.Table{}, classify(err, req)
	}
	t := view.Table{Columns: []view.Column{
		{Name: "Member"},
		{Name: "Endpoint"},
		{Name: "Leader"},
		{Name: "Term", Kind: view.KindNumber},
		{Name: "Size", Kind: view.KindBytes},
		{Name: "Quota", Kind: view.KindBytes},
		{Name: "Use %", Kind: view.KindUsage},
		{Name: "Health", Kind: view.KindStatus},
	}}
	for _, m := range resp.Members {
		if len(m.ClientURLs) == 0 {
			t.Rows = append(t.Rows, []string{hexID(m.ID), "-", "-", "-", "-", "-", "-", "unstarted"})
			continue
		}
		// Bounded per member: one unreachable member must not make this call
		// hang for as long as every other member's timeout combined.
		mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st, err := c.Status(mctx, m.ClientURLs[0])
		cancel()
		if err != nil {
			// Reported, not fatal. A member that cannot be reached is the
			// answer somebody came here for, and ending the walk at the first
			// one would turn "here is which member is down" into nothing.
			t.Rows = append(t.Rows, []string{
				hexID(m.ID), m.ClientURLs[0], "-", "-", "-", "-", "-", "unreachable",
			})
			continue
		}
		health := "ok"
		if st.Leader == 0 {
			health = "no leader"
		} else if len(st.Errors) > 0 {
			health = "alarm"
		}
		t.Rows = append(t.Rows, []string{
			hexID(m.ID), m.ClientURLs[0], hexID(st.Leader),
			strconv.FormatUint(st.RaftTerm, 10), format.Bytes(uint64(st.DbSize)),
			quotaBytes(st.DbSizeQuota), quotaCell(st.DbSize, st.DbSizeQuota), health,
		})
	}
	t.Total = len(t.Rows)
	return t, nil
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
			t.Rows = append(t.Rows, []string{
				"…", "-", "-",
				fmt.Sprintf("%d more leases; raise --limit", len(resp.Leases)-i),
			})
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
			hexID(uint64(l.ID)),
			(time.Duration(ttl.GrantedTTL) * time.Second).String(),
			remaining,
			strconv.Itoa(len(ttl.Keys)),
		})
	}
	t.Total = len(t.Rows)
	return t, nil
}
