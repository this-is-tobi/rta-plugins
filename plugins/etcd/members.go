package main

import (
	"context"
	"errors"
	"fmt"
	stdnet "net"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// catchUpEntries is how far behind its leader a member may fall before it
// can no longer be brought level from the log: etcd keeps this many entries
// after each snapshot (--experimental-snapshot-catchup-entries, 5000 unless
// changed), and a follower further back than that is sent a whole snapshot
// instead. Past it a member is not slow, it is expensive to repair, which is
// the line worth grading — the same cliff redis's replication backlog is.
const catchUpEntries = 5000

// memberTimeout bounds each member's own answer, so one that drops packets
// costs a call this long rather than a call as long as every member's wait
// added up. The members are asked together, so it is also the whole cost.
const memberTimeout = 3 * time.Second

// memberRow is one member as the member list names it and, where it could be
// asked, as it reports itself.
type memberRow struct {
	id         uint64
	name       string
	clientURLs []string
	learner    bool
	// st is the member's own status, and nil for a member that was not or
	// could not be asked, in which case why says which and what to do.
	st  *clientv3.StatusResponse
	why string
	// notAsked marks a member that was never dialled, as opposed to one that
	// was dialled and did not answer.
	notAsked bool
}

// leaderView is the leader as the endpoint named it, and its status when the
// leader could be asked: the figure every other member's distance is read
// against.
type leaderView struct {
	id uint64
	st *clientv3.StatusResponse
}

// memberList is the member list as the endpoint holds it, read without asking
// the cluster to agree on it.
//
// **A linearizable read, which is the default, needs a quorum, and the one
// time this view is opened in earnest is when there is not one.** Against a
// cluster that has lost its majority the call waits for an answer no member
// can give until its context ends: the overview hung for as long as the
// caller let it, on exactly the outage it exists to explain, and the
// endpoint's own status, which does answer, was never shown. The list is
// membership, which changes only by an operator's own command, so a member's
// own copy is as good as the cluster's.
func memberList(ctx context.Context, c *clientv3.Client) (*clientv3.MemberListResponse, error) {
	return c.MemberList(ctx, clientv3.WithSerializable())
}

// askMembers reads every member's own status, the endpoint's own from the
// status already in hand and the others from the client URLs they advertise.
//
// **Two connections are not asked of the others.** Through a kube: or ssh:
// forward the host opened one pipe to one member, and every other member's
// advertised URL is a name from inside the cluster that this machine is not
// on: dialling them would cost a timeout each to learn nothing, and ask a
// stranger's address with this connection's credentials. And a username over
// plaintext is a token that would be handed, with each call, to whatever
// host a member's list names — a member list is data the cluster supplies,
// and a compromised member could point it anywhere. Over TLS the certificate
// has to vouch for the host first, which is what makes the same dial safe.
// Both say so in the member's row rather than leaving it to read as down.
//
// A member this machine cannot reach, for whatever reason — advertised
// names are the cluster's own, and a published port rarely reaches them —
// reads "unreachable" with why, which is a fact about the route from here and
// not necessarily about the member.
func askMembers(ctx context.Context, c *clientv3.Client, req plugin.Request, self *clientv3.StatusResponse) ([]memberRow, error) {
	resp, err := memberList(ctx, c)
	if errors.Is(err, rpctypes.Error(rpctypes.ErrGRPCNotSupportedForLearner)) {
		// A learner serves its own status and serializable reads and refuses
		// the member list, which is the one thing that names the others. The
		// endpoint is a member and this is what it can say; the rest are
		// asked of a voting member, and the view says so.
		return []memberRow{{id: self.Header.MemberId, learner: true, st: self}}, nil
	}
	if err != nil {
		return nil, classify(err, req)
	}
	rows := make([]memberRow, 0, len(resp.Members))
	for _, m := range resp.Members {
		rows = append(rows, memberRow{id: m.ID, name: m.Name, clientURLs: m.ClientURLs, learner: m.IsLearner})
	}

	skip := whyNotAsked(req)
	var wg sync.WaitGroup
	for i := range rows {
		r := &rows[i]
		switch {
		case r.id == self.Header.MemberId:
			r.st = self
		case len(r.clientURLs) == 0:
			r.why = "has not started: no client URL yet"
		case skip != "":
			r.notAsked, r.why = true, skip
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()
				mctx, cancel := context.WithTimeout(ctx, memberTimeout)
				defer cancel()
				if st, err := c.Status(mctx, r.clientURLs[0]); err != nil {
					r.why = unreachableWhy(err) + " at " + r.clientURLs[0]
				} else {
					r.st = st
				}
			}()
		}
	}
	wg.Wait()
	return rows, nil
}

func whyNotAsked(req plugin.Request) string {
	sf := req.Surface()
	switch {
	case req.Tunnel() != plugin.TunnelNone:
		return "the " + string(req.Tunnel()) + ": forward reaches one member; run this from inside the cluster's network to ask the rest"
	case req.String("username") != "" && !tlsRequested(req):
		return "not over TLS, so the credentials are not sent to an address the member list names; " +
			"turn TLS on (" + sf.SettingTo("tls", true) + ") to ask them"
	}
	return ""
}

// unreachableWhy is the shortest true reason a member's own status could not
// be read, for a cell: a refusal, a name that does not resolve, silence, or
// the words the cluster used.
//
// **Mostly silence, in practice.** etcd's client waits for a connection for as
// long as its context lasts, so a refused dial and a name that does not
// resolve are retried until the bound and arrive as a deadline: "no answer"
// is what an unreachable member usually reads, and the URL beside it is the
// part that says which. The typed reasons are for the dials that do fail
// outright; where gRPC wrapped one in a status the operating system's error
// survives only as the text its errno prints, and plugin.DialRefused and
// DialUnroutable read it as that: a dial's words, "connect: connection
// refused", and never when the text holds a handshake's or a certificate's
// failure, which only a server that was reached gives.
//
// **Not by the errno's words alone.** Matched as bare text, "connection
// refused" in a status read as a refused dial whatever the status was about,
// and a member whose certificate was valid for a name spelled that way, or
// whose reply quoted a backend of its own that refused, was a port nothing
// listened on, on a member that had answered. The name that does not resolve
// is read by its words too, since a flattened lookup keeps nothing else, but
// not from a handshake's.
func unreachableWhy(err error) string {
	msg := err.Error()
	var dnsErr *stdnet.DNSError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded:
		return "no answer within " + memberTimeout.String()
	case plugin.DialRefused(err):
		return "connection refused"
	case errors.As(err, &dnsErr), strings.Contains(msg, "no such host") && !strings.Contains(msg, "x509: ") &&
		!strings.Contains(msg, "tls: "):
		return "the name does not resolve from here"
	case plugin.DialUnroutable(err):
		return "no route from here"
	}
	if st, ok := status.FromError(err); ok {
		return strings.ToLower(st.Code().String()) + ": " + st.Message()
	}
	return msg
}

func membersTable(rows []memberRow, lead leaderView) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Member"},
		{Name: "Name"},
		{Name: "Role"},
		{Name: "Version"},
		{Name: "Term", Kind: view.KindNumber},
		{Name: "Index", Kind: view.KindNumber},
		{Name: "Applied", Kind: view.KindNumber},
		{Name: "Behind", Kind: view.KindNumber},
		{Name: "Revision", Kind: view.KindNumber},
		{Name: "DB", Kind: view.KindBytes},
		{Name: "Health", Kind: view.KindStatus},
	}}
	for _, r := range rows {
		name := r.name
		if name == "" {
			name = "-"
		}
		cells := make([]string, 0, len(t.Columns))
		cells = append(cells, hexID(r.id), name, roleOf(r, lead), "-", "-", "-", "-", "-", "-", "-")
		if r.st != nil {
			cells[3] = r.st.Version
			cells[4] = strconv.FormatUint(r.st.RaftTerm, 10)
			cells[5] = strconv.FormatUint(r.st.RaftIndex, 10)
			cells[6] = appliedText(r.st)
			cells[7] = behindText(r, lead)
			cells[8] = strconv.FormatInt(r.st.Header.Revision, 10)
			cells[9] = format.Bytes(r.st.DbSize)
		}
		t.Rows = append(t.Rows, append(cells, memberHealth(r, lead)))
	}
	t.Total = len(t.Rows)
	return t
}

func roleOf(r memberRow, lead leaderView) string {
	switch {
	case r.learner:
		return "learner"
	case len(r.clientURLs) == 0:
		return "unstarted"
	case r.id == lead.id:
		return "leader"
	}
	return "follower"
}

// appliedText is blank for a server that does not report it: raftAppliedIndex
// is 3.4's, and an older member answers with the field absent, which decodes
// as zero and would read as a member that has applied nothing.
func appliedText(st *clientv3.StatusResponse) string {
	if st.RaftAppliedIndex == 0 {
		return "-"
	}
	return strconv.FormatUint(st.RaftAppliedIndex, 10)
}

// behind is how many entries a member's log trails the leader's, and whether
// the leader's was read to say. The leader is behind nobody, and a follower
// read a moment before the leader's last write can seem to be ahead of it: the
// members are asked together and not at one instant, so a few entries either
// way are the cost of asking and the distance is floored at zero.
func behind(r memberRow, lead leaderView) (uint64, bool) {
	if r.st == nil || lead.st == nil || r.id == lead.id {
		return 0, false
	}
	if r.st.RaftIndex >= lead.st.RaftIndex {
		return 0, true
	}
	return lead.st.RaftIndex - r.st.RaftIndex, true
}

func behindText(r memberRow, lead leaderView) string {
	n, ok := behind(r, lead)
	if !ok {
		return "-"
	}
	return strconv.FormatUint(n, 10)
}

// entries is a count of raft entries as words. An index is unsigned and
// format.CountOf takes an int, which a log index never reaches but a
// conversion cannot know.
func entries(n uint64) string {
	if n == 1 {
		return "1 entry"
	}
	return strconv.FormatUint(n, 10) + " entries"
}

// unapplied is how far a member's applied index trails what it has committed:
// a member that has the entries and is slow to act on them, which no
// distance from the leader shows.
func unapplied(st *clientv3.StatusResponse) uint64 {
	if st.RaftAppliedIndex == 0 || st.RaftAppliedIndex >= st.RaftIndex {
		return 0
	}
	return st.RaftIndex - st.RaftAppliedIndex
}

// memberHealth grades one member by the rules its row can show, worst first.
//
// A member that cannot be asked grades nothing about the member. A learner is
// expected to trail while it catches up and is amber for it, never red. Terms
// that differ from the leader's are an election or a partition in progress,
// and a member that names a different leader than the endpoint does is one
// that has not heard of the change yet, or is on the far side of a split.
func memberHealth(r memberRow, lead leaderView) string {
	st := r.st
	if st == nil {
		switch {
		case r.notAsked:
			return "info — not asked: " + r.why
		case len(r.clientURLs) == 0:
			return "pending — " + r.why
		}
		return "unreachable — " + r.why
	}
	// Before the errors, which carry "etcdserver: no leader" for the same
	// member, and say less: a member cut off from its peers reports it, and
	// what it means for the reader is what the rest of the cell is for.
	if st.Leader == 0 {
		return "fail — no leader: cut off from its peers, or an election is under way"
	}
	if len(st.Errors) > 0 {
		return "fail — " + strings.Join(st.Errors, "; ")
	}
	if lead.id != 0 && st.Leader != lead.id {
		return "warn — believes " + hexID(st.Leader) + " leads, the endpoint says " + hexID(lead.id)
	}
	if lead.st != nil && r.id != lead.id && st.RaftTerm != lead.st.RaftTerm {
		return fmt.Sprintf("warn — term %d, the leader's is %d", st.RaftTerm, lead.st.RaftTerm)
	}
	if n, ok := behind(r, lead); ok && n >= catchUpEntries {
		if r.learner {
			return "pending — learner catching up, " + entries(n) + " behind"
		}
		return "warn — " + entries(n) + " behind the leader, past the " +
			strconv.Itoa(catchUpEntries) + " it keeps log for: this member needs a snapshot to catch up"
	}
	if n := unapplied(st); n >= catchUpEntries {
		return "warn — " + entries(n) + " committed and not yet applied"
	}
	return "ok"
}

// quorumText says how many of the voting members answered and how many a
// write needs, or reports nothing when some were never asked: a count taken
// of a subset is a claim about the cluster it cannot make.
//
// "Answered" is a fact about this machine's route to them, not about the
// members, and the wording keeps to it: a cluster reached through one
// published port answers from one member, and said "quorum lost" about a
// cluster that was serving every client but this one. The endpoint's own
// leader is the evidence that decides which it is — a member that can name a
// leader has heard from a quorum.
func quorumText(rows []memberRow, endpointLeader uint64) (string, bool) {
	voters, answered := 0, 0
	for _, r := range rows {
		if r.learner {
			continue
		}
		if r.notAsked {
			return "", false
		}
		voters++
		if r.st != nil && r.st.Leader != 0 {
			answered++
		}
	}
	if voters == 0 {
		return "", false
	}
	need := voters/2 + 1
	text := fmt.Sprintf("%d of %s answering with a leader; a write needs %d", answered, format.CountOf(voters, "voting member"), need)
	switch {
	case voters == 1:
		text += " — a single member has no peer to fail over to"
	case answered < need && endpointLeader != 0:
		text += " — the others could not be reached from here, but the endpoint has a leader, so the cluster is serving"
	case answered < need:
		text += " — and the endpoint has no leader: the cluster is not accepting writes"
	case answered == need:
		text += " — one more member lost and writes stop"
	}
	return text, true
}
