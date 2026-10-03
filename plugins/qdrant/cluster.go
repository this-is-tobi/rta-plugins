package main

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// clusterInfo is GET /cluster: this peer's view of the consensus group and of
// the peers it names.
//
// Peer ids are unsigned 64-bit integers, drawn at random, so they are decoded
// as such and keyed as the strings JSON makes of them; a float64 would round
// any above 2^53 into a different peer.
type clusterInfo struct {
	Status string `json:"status"`
	PeerID uint64 `json:"peer_id"`
	Peers  map[string]struct {
		URI string `json:"uri"`
	} `json:"peers"`
	RaftInfo struct {
		Term              uint64  `json:"term"`
		Commit            uint64  `json:"commit"`
		PendingOperations uint64  `json:"pending_operations"`
		Leader            *uint64 `json:"leader"`
		Role              string  `json:"role"`
		IsVoter           *bool   `json:"is_voter"`
	} `json:"raft_info"`
	Consensus struct {
		State      string `json:"consensus_thread_status"`
		LastUpdate string `json:"last_update"`
		Err        string `json:"err"`
	} `json:"consensus_thread_status"`
	SendFailures map[string]struct {
		Count       uint64 `json:"count"`
		LatestError string `json:"latest_error"`
		At          string `json:"latest_error_timestamp"`
	} `json:"message_send_failures"`
}

// clusterMode is what could be learned about whether this instance is one of
// several: and "unknown" is its own answer, because a credential limited to
// collections is refused /cluster and still reads every collection's shards.
type clusterMode int

const (
	modeUnknown clusterMode = iota
	modeStandalone
	modeDistributed
)

// clusterState is the cluster as this peer reports it, or why it could not.
type clusterState struct {
	mode clusterMode
	info clusterInfo
	// note says why the consensus state is not shown, in words that name what
	// would show it. Empty when it is shown, and when the instance has none.
	note string
	// names maps a peer id to the host its URI names, which is how a peer is
	// told from another in a table: sixteen random digits are not.
	names map[uint64]string
}

// fetchCluster reads /cluster and degrades by what refused it.
//
// **A 403 is a finding about the credential, not a failed view.** A JWT scoped
// to collections is refused the cluster's own state ("Global access is
// required") and answers everything about the collections it covers, so the
// view goes on without the consensus section and names the access that would
// show it. A 404 is a server too old to have the endpoint, or a proxy that
// does not pass it, and the mode is simply not known.
//
// **Every other failure is returned, not swallowed.** A refused connection,
// a wrong key and a timeout would fail the next call the same way, and
// swallowed here the view would wait out a second timeout to say it.
func fetchCluster(ctx context.Context, req plugin.Request) (clusterState, *view.Error) {
	var info clusterInfo
	verr := get(ctx, req, "/cluster", &info)
	switch {
	case verr == nil && info.Status == "enabled":
		return clusterState{mode: modeDistributed, info: info, names: peerNames(info)}, nil
	case verr == nil:
		return clusterState{mode: modeStandalone, info: info}, nil
	case verr.Code == "qdrant.denied":
		return clusterState{note: "consensus state not shown: this credential's access is limited to collections, " +
			"and the cluster's own state needs global read access — a JWT with access \"r\", or the read-only API key"}, nil
	case verr.Code == "qdrant.notfound":
		return clusterState{}, nil
	}
	return clusterState{}, verr
}

func peerNames(info clusterInfo) map[uint64]string {
	names := map[uint64]string{}
	for id, p := range info.Peers {
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil {
			continue
		}
		names[n] = id
		if u, err := url.Parse(p.URI); err == nil && u.Hostname() != "" {
			names[n] = u.Hostname()
		}
	}
	return names
}

// modeText is the one line the status block carries about the cluster.
func (c clusterState) modeText() string {
	switch c.mode {
	case modeStandalone:
		return "standalone — cluster mode is off, so there is one peer and no replica to be behind"
	case modeDistributed:
		peers := format.CountOf(len(c.info.Peers), "peer")
		role := strings.ToLower(c.info.RaftInfo.Role)
		if role == "" {
			role = "a member"
		}
		return "distributed — " + peers + ", this one (" + c.label(c.info.PeerID) + ") is " + role
	}
	if c.note != "" {
		return "not shown — " + strings.TrimPrefix(c.note, "consensus state not shown: ")
	}
	return "not reported by this server"
}

func (c clusterState) label(id uint64) string {
	if n, ok := c.names[id]; ok {
		return n
	}
	return strconv.FormatUint(id, 10)
}

// recentFailure is how fresh a send failure has to be to count as one that is
// happening: raft sends something to every peer many times a second, so a peer
// that is down accumulates failures continuously, and one that stopped
// failing a minute ago is a peer that came back. The counter never resets, so
// "any failure ever" would grade a recovered peer for as long as the process
// lives.
const recentFailure = 30 * time.Second

// peersTable is every peer this one knows, with the consensus state of this
// one and what this one can say of the others.
//
// **The other peers' term and commit are blank, on purpose.** They are each
// peer's own, served at its own REST address, and the peer list names only the
// internal one: the cluster's gRPC port, which this plugin does not speak.
// Guessing the REST port from it and sending the credential there would be a
// request to an address nobody configured, so the other peers are read by
// pointing this plugin at each, where each is the one that is described.
// What this peer does know of them is whether it can still message them, and
// that is graded.
func peersTable(c clusterState, now time.Time) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Peer"},
		{Name: "URI"},
		{Name: "Role"},
		{Name: "Term", Kind: view.KindNumber},
		{Name: "Commit", Kind: view.KindNumber},
		{Name: "Pending", Kind: view.KindNumber},
		{Name: "Health", Kind: view.KindStatus},
	}}
	ids := make([]uint64, 0, len(c.info.Peers))
	for id := range c.info.Peers {
		if n, err := strconv.ParseUint(id, 10, 64); err == nil {
			ids = append(ids, n)
		}
	}
	// This peer first, then the rest by name, so the table does not reshuffle
	// between calls and the row that has the numbers is the one read first.
	sort.Slice(ids, func(i, j int) bool {
		if (ids[i] == c.info.PeerID) != (ids[j] == c.info.PeerID) {
			return ids[i] == c.info.PeerID
		}
		return c.label(ids[i]) < c.label(ids[j])
	})
	for _, id := range ids {
		uri := c.info.Peers[strconv.FormatUint(id, 10)].URI
		role := c.roleOf(id)
		if id == c.info.PeerID {
			t.Rows = append(t.Rows, []string{c.label(id) + " (this peer)", uri, role,
				strconv.FormatUint(c.info.RaftInfo.Term, 10), strconv.FormatUint(c.info.RaftInfo.Commit, 10),
				strconv.FormatUint(c.info.RaftInfo.PendingOperations, 10), c.selfHealth()})
			continue
		}
		t.Rows = append(t.Rows, []string{c.label(id), uri, role, "-", "-", "-", c.peerHealth(id, uri, now)})
	}
	t.Total = len(t.Rows)
	return t
}

func (c clusterState) roleOf(id uint64) string {
	switch {
	case id == c.info.PeerID && c.info.RaftInfo.IsVoter != nil && !*c.info.RaftInfo.IsVoter:
		return "learner"
	case c.info.RaftInfo.Leader == nil:
		return "-"
	case *c.info.RaftInfo.Leader == id:
		return "leader"
	}
	return "follower"
}

// selfHealth grades what this peer can say of itself: the thread that runs
// consensus, and whether there is a leader to follow.
//
// A candidate is amber, not red: an election takes a moment and a cluster
// that has just lost its leader reads exactly so. A peer with no leader at all
// is red, because nothing can be committed through it.
func (c clusterState) selfHealth() string {
	switch c.info.Consensus.State {
	case "", "working":
	case "stopped_with_err":
		return "fail — the consensus thread stopped: " + truncate(c.info.Consensus.Err, 120)
	default:
		return "fail — the consensus thread is " + strings.ReplaceAll(c.info.Consensus.State, "_", " ")
	}
	switch {
	case c.info.RaftInfo.Leader == nil:
		return "fail — no leader: nothing can be committed until an election finishes"
	case strings.Contains(strings.ToLower(c.info.RaftInfo.Role), "candidate"):
		return "warn — an election is under way"
	}
	return "ok"
}

// sendError drops the sentence every one of these opens with, which names the
// transport library's closure and says nothing about the peer: of the ninety
// characters a cell can spare, it took seventy.
//
// What is left is a gRPC status printed as `code: 'X', message: "Y"`, and Y
// names the peer's own address a second time, in the row that is already
// about that peer. The cell cuts at ninety characters, and with both in it the
// cut fell before "transport error", the part that says what failed.
var grpcStatusText = regexp.MustCompile(`^code: '([^']*)', message: "(.*)"$`)

func sendError(msg, uri string) string {
	msg = strings.TrimPrefix(msg, "Error in closure supplied to transport channel pool: ")
	if m := grpcStatusText.FindStringSubmatch(msg); m != nil {
		msg = m[1] + ": " + strings.ReplaceAll(m[2], " to "+uri, "")
	}
	return msg
}

// peersDown says which peers this one cannot message right now, for the
// replicas that live on them. Only the leader sends to every peer, so only a
// leader can say: asked of a follower the answer is none, which is not "all
// reachable" and is why the peers table says "not observed" there.
func (c clusterState) peersDown(now time.Time) func(uint64) bool {
	leader := c.info.RaftInfo.Leader
	if c.mode != modeDistributed || leader == nil || *leader != c.info.PeerID {
		return nil
	}
	return func(id uint64) bool {
		f, ok := c.info.SendFailures[c.info.Peers[strconv.FormatUint(id, 10)].URI]
		if id == c.info.PeerID || !ok || f.Count == 0 {
			return false
		}
		at, err := time.Parse(time.RFC3339Nano, f.At)
		return err == nil && now.Sub(at) < recentFailure
	}
}

func failedMessages(n uint64) string {
	if n == 1 {
		return "1 failed message"
	}
	return strconv.FormatUint(n, 10) + " failed messages"
}

// peerHealth grades a peer by whether this one can message it, which is all a
// single peer can know of another. The failures are keyed by the URI the peer
// list names, with the count since this process started and the last error.
//
// **Only the leader messages every peer.** A follower sends to the leader and
// to nobody else, so its failure list is empty for every peer but the leader
// whether those peers are up or down: asked of qd2 with qd3 stopped, the row
// for qd3 read "ok". What that row can honestly say is that this peer has no
// view of it, and says so, muted, rather than a green the evidence does not
// support.
func (c clusterState) peerHealth(id uint64, uri string, now time.Time) string {
	leader := c.info.RaftInfo.Leader
	if leader == nil || (*leader != c.info.PeerID && *leader != id) {
		return "info — a follower messages only the leader, so this peer has no view of it"
	}
	f, ok := c.info.SendFailures[uri]
	if !ok || f.Count == 0 {
		return "ok"
	}
	at, err := time.Parse(time.RFC3339Nano, f.At)
	text := failedMessages(f.Count) + " to it, the last: " + truncate(sendError(f.LatestError, uri), 90)
	switch {
	case err != nil:
		// A server that does not date its failures leaves no way to tell a
		// peer that is down from one that was: reported as the former, since
		// a peer that cannot be messaged is the finding worth a second look.
		return "warn — " + text
	case now.Sub(at) < recentFailure:
		return "fail — cannot be messaged: " + text
	}
	return "ok — " + failedMessages(f.Count) + " in the past, the last " +
		format.Duration(now.Sub(at)) + " ago"
}
