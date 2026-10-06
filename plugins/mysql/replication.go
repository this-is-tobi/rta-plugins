package main

import (
	"context"
	"database/sql"
	"strings"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func replicationCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "mysql.replication.status",
		Summary:    capabilitySummary,
		Keywords:   []string{"slave", "master", "gtid", "binlog", "delay"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Answers \"is replication healthy, where is this server in it, and how far behind is " +
			"it\" without a query: what this server is (a replica, a source with replicas, both, or " +
			"neither), each replication thread's state, seconds behind, the position read against the " +
			"one applied, the transactions received and not yet applied, and the replicas connected " +
			"to it.\n\n" +
			"Each replica is graded. A stopped thread is a failure, named with the error number the " +
			"server recorded; a running replica warns at a minute behind and fails at ten, beyond any " +
			"delay it keeps. The lag figure alone is never the answer — it reads 0 both when caught up " +
			"and when receiving nothing — so it is never shown without the thread states.\n\n" +
			"One connection sees one server: the source's own position is what the same call against " +
			"the source says, so run it on each member and compare. The connected replicas are the " +
			"source's own list: a replica that went away stays on it until the source next fails to " +
			"send it something.\n\n" +
			"A server with no replication says so rather than returning an empty table. Every part is " +
			"read on its own: an account that may read the replica threads and not the connected " +
			"replicas gets the first, with the privilege that adds the second named.\n\n" +
			"Positions and states only: the text of a replication error is the failing statement with " +
			"its row values, so only its number is returned. The message is what `mysql.query` returns " +
			"for SHOW REPLICA STATUS, behind its grant." +
			clusterNote,
		Agent: "Replication health of the one server reached: its role (replica, source, both or neither), " +
			"each thread's state, seconds behind, the position read against the one applied, transactions " +
			"received and not yet applied, and the connected replicas. A stopped thread fails, with the " +
			"recorded error number; a running replica warns at a minute behind and fails at ten. Lag is " +
			"never shown without thread states. One connection sees one server: call it on each member " +
			"and compare. Positions and states only: an error's text is withheld because it carries row " +
			"values." + clusterAgentNote,
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withDB(ctx, req, func(ctx context.Context, db *sql.DB) (view.View, error) {
				st, verr := readState(ctx, db, req)
				if verr != nil {
					return nil, verr
				}
				return replicationView(st), nil
			})
		},
	})
}

func replicationView(st state) view.Sections {
	channels := make([]channelRow, len(st.channels))
	for i, row := range st.channels {
		channels[i] = assessChannel(row)
	}
	out := view.Sections{Warnings: st.warnings()}
	put := func(id, title string, v view.View) {
		out.Items = append(out.Items, view.Section{ID: id, Title: title, View: v})
	}
	put("summary", "Summary", summaryTable(summarise(st, channels)))
	if len(channels) > 0 {
		put("sources", "Replicating from", sourcesTable(channels))
		if t, ok := gtidTable(channels); ok {
			put("gtid", "Transactions", t)
		}
	}
	if pairs := serverPairs(st); len(pairs) > 0 {
		put("server", "This server", view.KeyValue{Pairs: pairs})
	}
	if len(st.hosts) > 0 {
		put("replicas", "Connected replicas", hostsTable(st.hosts))
	}
	if st.cluster.section != nil {
		out.Items = append(out.Items, *st.cluster.section)
	}
	return out
}

// replicationSummary is the compact line the overview carries: what this
// server is in a replication topology and whether that is healthy. It never
// fails the overview — a server that cannot be asked says so in the line and
// in the warning beside it, because an overview that vanished over a missing
// grant would be worse than one that said what it could not see.
func replicationSummary(ctx context.Context, db *sql.DB, req plugin.Request) (view.View, []view.Error) {
	st, verr := readState(ctx, db, req)
	if verr != nil {
		return summaryTable(facet{role: "unknown", status: "warn — " + verr.Message}), []view.Error{*verr}
	}
	channels := make([]channelRow, len(st.channels))
	for i, row := range st.channels {
		channels[i] = assessChannel(row)
	}
	return summaryTable(summarise(st, channels)), st.warnings()
}

// warnings are the parts that could not be read, in the order the answer is
// built, so the same server reads the same way twice.
func (st state) warnings() []view.Error {
	var out []view.Error
	for _, s := range []section{sectionVars, sectionReplica, sectionBinlog, sectionHosts, sectionCluster} {
		if e := st.unread[s]; e != nil {
			out = append(out, *e)
		}
	}
	return out
}

func rank(status string) int {
	switch {
	case strings.HasPrefix(status, "fail"):
		return 2
	case strings.HasPrefix(status, "warn"):
		return 1
	}
	return 0
}

func reason(status string) string {
	_, after, ok := strings.Cut(status, " — ")
	if !ok {
		return status
	}
	return after
}

// summarise says what this server is and how it is doing in one line.
//
// An unreadable part is never read as an absent one. A replica whose status
// the account may not read has no channels to show, and "standalone" would be
// a confident wrong answer about exactly the server somebody is worried about.
func summarise(st state, channels []channelRow) facet {
	var facets []facet
	if len(channels) > 0 {
		facets = append(facets, replicaFacet(channels))
	}
	if n := len(st.hosts); n > 0 {
		facets = append(facets, facet{
			role:   "source of " + format.CountOf(n, "replica"),
			status: "ok", detail: format.CountOf(n, "replica") + " connected",
		})
	}
	if st.cluster.facet != nil {
		facets = append(facets, *st.cluster.facet)
	}

	var gaps []string
	for _, s := range []section{sectionReplica, sectionBinlog, sectionCluster} {
		if st.unread[s] != nil {
			gaps = append(gaps, string(s))
		}
	}
	incomplete := "could not read the " + strings.Join(gaps, ", the ") + " — this is not the whole picture"

	if len(facets) == 0 {
		if len(gaps) > 0 {
			// What was read still counts: an account that sees the replica
			// threads and not the connected replicas knows this is no replica,
			// and saying only "unknown" would discard that.
			var known []string
			if st.unread[sectionReplica] == nil {
				known = append(known, "not a replica")
			}
			if isOn(st.vars["log_bin"]) {
				known = append(known, "binary log on")
			}
			role := join(", ", known...)
			if role == "" {
				role = "unknown"
			}
			return facet{role: role, status: "warn", detail: incomplete}
		}
		// Asked only of a server whose binary log is on, so an unread list is
		// a server that may have replicas: neither standalone nor a source
		// that can be counted.
		if st.unread[sectionHosts] != nil {
			return facet{role: "not a replica, binary log on", status: "none",
				detail: "no replica threads here; whether replicas are connected is not readable by this account"}
		}
		return facet{role: "standalone", status: "none",
			detail: standaloneDetail}
	}

	// The summary carries the word and the detail carries the reasons, so a
	// failing replica reads once in the row and its table line holds the
	// full status.
	roles := make([]string, len(facets))
	status, worst := "ok", 0
	var bad, all []string
	for i, f := range facets {
		roles[i] = f.role
		all = append(all, f.detail)
		if r := rank(f.status); r > 0 {
			bad = append(bad, f.detail)
			if r > worst {
				status, worst = strings.Fields(f.status)[0], r
			}
		}
	}
	detail := strings.Join(all, "; ")
	if len(bad) > 0 {
		detail = strings.Join(bad, "; ")
	}
	if len(gaps) > 0 {
		detail = join("; ", detail, incomplete)
		if worst == 0 {
			status = "warn"
		}
	}
	return facet{role: strings.Join(roles, " and "), status: status, detail: detail}
}

func replicaFacet(channels []channelRow) facet {
	if len(channels) == 1 {
		c := channels[0]
		detail := reason(c.status)
		if c.status == "ok" {
			detail = "IO and SQL threads running, " + c.behind + " behind"
			if c.delay != "" {
				detail += ", keeping a " + c.delay + " delay"
			}
		}
		return facet{role: "replica of " + c.source, status: c.status, detail: detail}
	}
	f := facet{role: "replica of " + format.CountOf(len(channels), "source"), status: "ok"}
	var bad []string
	for _, c := range channels {
		if rank(c.status) > 0 {
			bad = append(bad, c.name+": "+reason(c.status))
			if rank(c.status) > rank(f.status) {
				f.status = c.status
			}
		}
	}
	f.detail = "all " + format.CountOf(len(channels), "channel") + " running"
	if len(bad) > 0 {
		f.detail = strings.Join(bad, "; ")
	}
	return f
}
