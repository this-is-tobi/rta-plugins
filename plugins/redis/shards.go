package main

import (
	"context"
	"strings"
)

// position is where one cluster node stands in its shard's replication
// stream, as the node answering CLUSTER SHARDS knows it.
type position struct {
	offset int64
	// behind is a replica's distance from its shard's primary, and -1 for
	// every node it is not known for: a primary has no primary, and a shard
	// that reported none has nothing to measure against.
	behind  int64
	health  string
	replica bool
}

// clusterPositions is every node's replication offset and the distance of
// each replica behind its shard's primary, from CLUSTER SHARDS, which has
// carried both since 7.0. A server that cannot answer is not a failed view:
// the nodes table is printed with those two columns blank and the second
// result says why in words that name what would fix it, which is a version
// older than 7.0 or a proxy that does not pass the subcommand in one case,
// and an ACL user without +cluster|shards in the other.
//
// **The numbers are the cluster bus's, not each node's own.** A node learns
// another's offset from the heartbeats it exchanges, so a distance of a few
// bytes — a write that landed between two of them — is noise, and a replica
// can even read ahead of a primary whose last heartbeat is older than its
// own. The distance is clamped at zero for that reason, and it is the figure
// for "which replica is far behind", never for the exact count: redis
// overview on the primary reads the acknowledged offsets themselves.
func clusterPositions(ctx context.Context, c *client) (map[string]position, string) {
	r, err := c.do(ctx, "CLUSTER", "SHARDS")
	if err != nil {
		return nil, unpositioned(err)
	}
	out := map[string]position{}
	for _, shard := range r.items {
		fields := flatFields(shard.items)
		var primary int64 = -1
		type member struct {
			id string
			position
		}
		var members []member
		for _, node := range fields["nodes"].items {
			f := flatFields(node.items)
			off, _ := parseInt(f["replication-offset"].text())
			m := member{id: f["id"].text(), position: position{
				offset: off, health: f["health"].text(), replica: f["role"].text() == "replica"}}
			if !m.replica {
				primary = off
			}
			members = append(members, m)
		}
		for _, m := range members {
			m.behind = -1
			if m.replica && primary >= 0 {
				m.behind = max(0, primary-m.offset)
			}
			out[m.id] = m.position
		}
	}
	return out, ""
}

// unpositioned is why the offsets are missing, or "" when the failure is not
// one this node can be told about: a dropped connection fails the next
// command of the view too, which names it properly.
func unpositioned(err error) string {
	var srv *serverError
	if !asServerError(err, &srv) {
		return ""
	}
	switch code, _, _ := strings.Cut(srv.msg, " "); code {
	case "NOPERM":
		return "offsets not shown: the ACL user may not run CLUSTER SHARDS — grant +cluster|shards to read them"
	default:
		return "offsets not shown: this server does not answer CLUSTER SHARDS, which redis added in 7.0 — " +
			"on an older one, the overview of each node has its own offset"
	}
}

// flatFields reads the key, value, key, value array RESP2 flattens a map
// into — CLUSTER SHARDS answers with them nested, a map of maps.
func flatFields(items []reply) map[string]reply {
	out := make(map[string]reply, len(items)/2)
	for i := 0; i+1 < len(items); i += 2 {
		out[strings.ToLower(items[i].text())] = items[i+1]
	}
	return out
}
