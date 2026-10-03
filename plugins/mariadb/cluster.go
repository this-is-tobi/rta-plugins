package main

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The part of mariadb.replication.status that only MariaDB has, and the
// reason somebody running Galera wants this artifact rather than plugins/mysql:
// what the cluster itself says about this node.
//
// It answers the question a Galera node cannot answer by looking healthy — is
// this node actually part of a working cluster, or is it quietly serving stale
// data on its own — and it is part of the replication view rather than a view
// of its own because a node's role in a cluster and its role as an ordinary
// replica are one question to the person asking it: a Galera cluster is
// commonly also a replica of something, or a source for something, and
// answering one half of that and not the other is how the other half is
// missed. Every value is a number the server publishes about itself, not a
// value anybody stored in it.

// wsrepVars reads the wsrep_% status variables. Read as a map because the
// interesting ones are scattered across a list of about seventy, and naming
// them here keeps the query cheap on a busy node.
func wsrepVars(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SHOW GLOBAL STATUS LIKE 'wsrep_%'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		out[strings.ToLower(name)] = value
	}
	return out, rows.Err()
}

func clusterOf(ctx context.Context, db *sql.DB) clusterPart {
	vars, err := wsrepVars(ctx, db)
	if err != nil {
		return clusterPart{err: err}
	}
	return clusterFrom(vars)
}

func clusterFrom(vars map[string]string) clusterPart {
	// wsrep_provider_name is absent on a server built without Galera and
	// reads as "none" or empty on one that has it compiled in and not
	// configured. All of those mean the same thing to somebody asking this
	// question, and none is an error: a standalone MariaDB is a normal thing to
	// run, and it has no cluster section rather than an empty one that reads
	// like a cluster that has fallen apart.
	if provider := vars["wsrep_provider_name"]; provider == "" || strings.EqualFold(provider, "none") {
		return clusterPart{}
	}
	f := galeraFacet(vars)
	return clusterPart{
		facet:   &f,
		section: &view.Section{ID: "galera", Title: "Galera", View: galeraPairs(vars)},
	}
}

// flowControl is the share of time, since the server's status counters were
// last reset, that the cluster has told this node to stop sending. A node that spends a tenth of its time stalled is the early warning
// for one about to be evicted, and half is a node the cluster is carrying.
const (
	flowControlWarn = 0.1
	flowControlFail = 0.5
)

// queueWarn is how many writesets may wait to be applied on this node before
// it is called behind. It is the size of the provider's own default limit for
// the point at which flow control starts: a queue past it is a node that is
// applying slower than the cluster writes, which is how a node falls out of a
// cluster while every flag still reads healthy. It is the node's own queue —
// the one thing a connection to one node can say about how far behind that
// node is.
const queueWarn = 100

func galeraFacet(v map[string]string) facet {
	role := "Galera node"
	if n := v["wsrep_cluster_size"]; n != "" {
		role += " (cluster of " + n + ")"
	}
	status, detail := galeraGrade(v)
	return facet{role: role, status: status, detail: detail}
}

// galeraGrade is the verdict, stated rather than left to be assembled from the
// rows beside it. A node can be connected, ready and still outside the primary
// component, and that combination is exactly the one somebody misreads at three
// in the morning.
func galeraGrade(v map[string]string) (status, detail string) {
	primary := strings.EqualFold(v["wsrep_cluster_status"], "Primary")
	ready := strings.EqualFold(v["wsrep_ready"], "ON")
	connected := strings.EqualFold(v["wsrep_connected"], "ON")
	state := v["wsrep_local_state_comment"]
	clusterUUID, nodeUUID := v["wsrep_cluster_state_uuid"], v["wsrep_local_state_uuid"]
	paused, _ := strconv.ParseFloat(v["wsrep_flow_control_paused"], 64)
	queued, _ := atoi(v["wsrep_local_recv_queue"])

	// Every finding is kept, not the first: a node that has lost quorum and
	// also has a queue is one whose queue is worth knowing about once it is
	// back, and a state that is both desynced and behind says both.
	var fails, warns []string
	if !primary {
		fails = append(fails, "not in the primary component (cluster status "+v["wsrep_cluster_status"]+
			") — a split-brain risk: it must not be written to")
	}
	// Outside the primary component a node is never ready, and saying so a
	// second time buries the one sentence that matters.
	if primary && (!connected || !ready) {
		fails = append(fails, "up but refusing cluster traffic (connected "+v["wsrep_connected"]+", ready "+v["wsrep_ready"]+")")
	}
	if clusterUUID != "" && nodeUUID != "" && clusterUUID != nodeUUID {
		fails = append(fails, "out of step: its state uuid "+nodeUUID+" is not the cluster's "+clusterUUID)
	}
	if primary && !strings.EqualFold(state, "Synced") {
		warns = append(warns, "state "+state+" — up, and not serving current data")
	}
	if text := "flow control has held it back " + percent(paused) + " of the time since the status counters were last reset"; paused >= flowControlFail {
		fails = append(fails, text)
	} else if paused >= flowControlWarn {
		warns = append(warns, text)
	}
	if queued >= queueWarn {
		warns = append(warns, format.CountOf(int(queued), "writeset")+" waiting to be applied — it is falling behind the cluster")
	}
	switch {
	case len(fails) > 0:
		return "fail", strings.Join(append(fails, warns...), "; ")
	case len(warns) > 0:
		return "warn", strings.Join(warns, "; ")
	}
	size, _ := atoi(v["wsrep_cluster_size"])
	return "ok", "in the primary component and synced with " + format.CountOf(int(size), "member")
}

func percent(fraction float64) string { return strconv.FormatFloat(fraction*100, 'f', 0, 64) + "%" }

// galeraPairs puts the two uuids and the last committed sequence side by side,
// because those are what show a node out of step: the cluster's state uuid is
// its history, the node's own is the history it has applied, and a node whose
// last committed sequence trails the others' is behind even while every flag
// reads healthy. The members are the addresses the cluster itself reports.
func galeraPairs(v map[string]string) view.KeyValue {
	var pairs []view.Pair
	add := func(key, value string) {
		if value != "" {
			pairs = append(pairs, view.Pair{Key: key, Value: value})
		}
	}
	add("provider", join(" ", v["wsrep_provider_name"], v["wsrep_provider_version"]))
	add("cluster size", v["wsrep_cluster_size"])
	add("cluster status", v["wsrep_cluster_status"])
	add("cluster uuid", v["wsrep_cluster_state_uuid"])
	add("cluster conf id", v["wsrep_cluster_conf_id"])
	add("node state", v["wsrep_local_state_comment"])
	add("node uuid", v["wsrep_local_state_uuid"])
	add("last committed", v["wsrep_last_committed"])
	add("connected", v["wsrep_connected"])
	add("ready", v["wsrep_ready"])
	add("members", v["wsrep_incoming_addresses"])
	if cur, avg := v["wsrep_local_recv_queue"], v["wsrep_local_recv_queue_avg"]; cur != "" || avg != "" {
		add("recv queue", cur+" (avg "+avg+")")
	}
	add("send queue avg", v["wsrep_local_send_queue_avg"])
	add("flow control paused", v["wsrep_flow_control_paused"])
	return view.KeyValue{Pairs: pairs}
}
