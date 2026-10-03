package main

import (
	"sort"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/view"
)

// collectionCluster is GET /collections/{name}/cluster: where this peer knows
// each shard's replicas to be and what state each is in.
//
// Only the replicas on this peer carry a point count. A remote replica is
// known by the state consensus recorded for it, and that is the whole of what
// one peer can say about another's data.
type collectionCluster struct {
	PeerID      uint64 `json:"peer_id"`
	ShardCount  int    `json:"shard_count"`
	LocalShards []struct {
		ShardID     int    `json:"shard_id"`
		PointsCount *int64 `json:"points_count"`
		State       string `json:"state"`
	} `json:"local_shards"`
	RemoteShards []struct {
		ShardID int    `json:"shard_id"`
		PeerID  uint64 `json:"peer_id"`
		State   string `json:"state"`
	} `json:"remote_shards"`
	ShardTransfers []struct {
		ShardID int    `json:"shard_id"`
		From    uint64 `json:"from"`
		To      uint64 `json:"to"`
		Sync    bool   `json:"sync"`
		Method  string `json:"method"`
	} `json:"shard_transfers"`
}

// replica is one copy of one shard, wherever it lives.
type replica struct {
	shard  int
	peer   uint64
	state  string
	points *int64
	local  bool
}

// stateUnreachable is what an Active replica reads on a peer the leader cannot
// message. It is not a state Qdrant reports: consensus keeps a replica Active
// until a write fails on it, and a collection nobody has written to since a
// peer went down would read "all replicas active" beside a collection that
// cannot be searched. The peer list already says which peers are failing; this
// carries that into the replicas that live on them.
const stateUnreachable = "Unreachable"

// replicas lists every copy of every shard. down says which peers cannot be
// messaged, and may be nil for none.
func (cc collectionCluster) replicas(down func(uint64) bool) []replica {
	state := func(peer uint64, s string) string {
		if s == "Active" && down != nil && down(peer) {
			return stateUnreachable
		}
		return s
	}
	out := make([]replica, 0, len(cc.LocalShards)+len(cc.RemoteShards))
	for _, s := range cc.LocalShards {
		out = append(out, replica{shard: s.ShardID, peer: cc.PeerID, state: state(cc.PeerID, s.State), points: s.PointsCount, local: true})
	}
	for _, s := range cc.RemoteShards {
		out = append(out, replica{shard: s.ShardID, peer: s.PeerID, state: state(s.PeerID, s.State)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].shard != out[j].shard {
			return out[i].shard < out[j].shard
		}
		if out[i].local != out[j].local {
			return out[i].local
		}
		return out[i].peer < out[j].peer
	})
	return out
}

// distributed is whether the collection has any shard beyond this peer's own.
func (cc collectionCluster) distributed() bool {
	return len(cc.RemoteShards) > 0 || len(cc.ShardTransfers) > 0
}

// replicasHealth is one collection's replicas as a single graded cell: how
// many are serving, and which are not and in what state.
//
// **Dead is red and every other state that is not Active is amber.** A Dead
// replica has been taken out of service because it failed to apply a write
// the others applied — it is missing data, and stays so until it is brought
// back, which is a repair somebody has to start or wait for. Initializing,
// Recovery, Partial, PartialSnapshot, Listener and Resharding are the
// transitional states of a replica being brought level or moved: nothing is
// lost, and the replica is not yet serving, which is what the amber says. A
// state this version does not know is amber too, since a new state is a new
// transition far more often than a new failure.
//
// **An Active replica on a peer the leader cannot message is not serving.**
// Consensus keeps it Active until a write fails on it, so a collection nobody
// has written to since the peer went down read "all replicas active" beside a
// search that failed: down says which peers those are, and the replicas on
// them are counted as unreachable, amber while another copy of the shard
// serves and red when none does.
func replicasHealth(cc collectionCluster, name func(uint64) string, down func(uint64) bool) string {
	all := cc.replicas(down)
	active, dead := 0, false
	var groups []stateGroup
	serving, cutOff := map[int]bool{}, map[int]bool{}
	for _, r := range all {
		if r.state == "Active" {
			active++
			serving[r.shard] = true
			continue
		}
		dead = dead || r.state == "Dead"
		if r.state == stateUnreachable {
			cutOff[r.shard] = true
		}
		groups = addToGroup(groups, r)
	}
	unserved := false
	for shard := range cutOff {
		unserved = unserved || !serving[shard]
	}
	problems := make([]string, 0, len(groups))
	for _, g := range groups {
		problems = append(problems, g.text(name))
	}
	ratio := strconv.Itoa(active) + " of " + strconv.Itoa(len(all)) + " replicas active"
	moving := len(cc.ShardTransfers)
	switch {
	case dead || unserved:
		return "fail — " + ratio + ": " + listed(problems)
	case len(cutOff) > 0:
		return "warn — " + ratio + ": " + listed(problems) + transferNote(moving)
	case len(problems) > 0:
		return "pending — " + ratio + ": " + listed(problems) + transferNote(moving)
	case moving > 0:
		return "pending — " + ratio + transferNote(moving)
	}
	return "ok — " + ratio
}

// stateGroup is the shards on one peer that are in one state, which is how a
// peer going down reads: every replica it holds at once, and one line saying
// so is the finding, where nine of them is a wall.
type stateGroup struct {
	peer   uint64
	state  string
	shards []int
}

func addToGroup(groups []stateGroup, r replica) []stateGroup {
	for i := range groups {
		if groups[i].peer == r.peer && groups[i].state == r.state {
			groups[i].shards = append(groups[i].shards, r.shard)
			return groups
		}
	}
	return append(groups, stateGroup{peer: r.peer, state: r.state, shards: []int{r.shard}})
}

func (g stateGroup) text(name func(uint64) string) string {
	ids := make([]string, 0, len(g.shards))
	for _, s := range g.shards {
		ids = append(ids, strconv.Itoa(s))
	}
	verb, state := "are", g.state
	if g.state == stateUnreachable {
		state = "active, but the peer cannot be messaged"
	}
	if len(ids) == 1 {
		return "shard " + ids[0] + " on " + name(g.peer) + " is " + state
	}
	return "shards " + strings.Join(ids, ", ") + " on " + name(g.peer) + " " + verb + " " + state
}

func transferNote(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return ", 1 shard transfer under way"
	}
	return ", " + strconv.Itoa(n) + " shard transfers under way"
}

// listed names the first few problems and counts the rest, so a collection
// with fifty shards on a dead peer is one readable cell and not a page.
func listed(problems []string) string {
	const shown = 3
	if len(problems) <= shown {
		return strings.Join(problems, "; ")
	}
	return strings.Join(problems[:shown], "; ") + "; and " + strconv.Itoa(len(problems)-shown) + " more"
}

// shardsTable is every replica of every shard, one row each: where it lives,
// the state it is in and, for the copy on this peer, how many points it holds.
//
// The count is blank for every other peer's copy and the column says why in
// the capability's description, because it is the one number that would show
// a replica that has silently missed writes and this peer cannot read it: the
// state alone does not move until a write fails on that copy.
func shardsTable(cc collectionCluster, name func(uint64) string, down func(uint64) bool) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Shard", Kind: view.KindNumber},
		{Name: "Peer"},
		{Name: "State", Kind: view.KindStatus},
		{Name: "Points", Kind: view.KindNumber},
	}}
	for _, r := range cc.replicas(down) {
		peer, points := name(r.peer), "-"
		if r.local {
			peer += " (this peer)"
			points = countText(r.points)
		}
		t.Rows = append(t.Rows, []string{strconv.Itoa(r.shard), peer, stateCell(r.state), points})
	}
	t.Total = len(t.Rows)
	return t
}

// stateCell spells a shard state the way the status vocabulary grades it: the
// renderers colour a word they know, and "Dead" or "Recovery" alone are
// neither red nor amber to them.
func stateCell(state string) string {
	switch state {
	case "Active":
		return "active"
	case "Dead":
		return "fail — dead"
	case stateUnreachable:
		return "warn — active, but the peer cannot be messaged"
	}
	return "pending — " + strings.ToLower(state)
}

// transfersTable is the shard transfers in flight: a replica being rebuilt
// from another, or a shard being moved.
func transfersTable(cc collectionCluster, name func(uint64) string) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Shard", Kind: view.KindNumber},
		{Name: "From"},
		{Name: "To"},
		{Name: "Method"},
		{Name: "Sync"},
	}}
	for _, tr := range cc.ShardTransfers {
		method := tr.Method
		if method == "" {
			method = "-"
		}
		t.Rows = append(t.Rows, []string{strconv.Itoa(tr.ShardID), name(tr.From), name(tr.To), method, strconv.FormatBool(tr.Sync)})
	}
	t.Total = len(t.Rows)
	return t
}
