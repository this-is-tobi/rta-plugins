package main

import (
	"context"
	"encoding/json"
	"errors"
	stdnet "net"
	"os"
	"strings"
	"syscall"
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The fixtures are what `etcdctl endpoint status -w json` and `member list -w
// json` printed for etcd 3.6.6 and 3.5.21 containers, each member asked at its
// own endpoint. Where a test names a change to one, it is a number edited on
// a captured response to reach a state that is expensive to produce on
// purpose; nothing else is made up.

// A cluster of three after one follower was cut off from its peers' port and
// 300 writes went to the leader: eh1 leads at index 308, eh2 kept up, eh3
// stopped at 8, still serving clients and still saying "no leader".
const (
	statusLeader    = `{"header":{"cluster_id":16509504660443962926,"member_id":11589452085494132595,"revision":301,"raft_term":2},"version":"3.6.6","dbSize":49152,"leader":11589452085494132595,"raftIndex":308,"raftTerm":2,"raftAppliedIndex":308,"dbSizeInUse":49152,"storageVersion":"3.6.0","dbSizeQuota":2147483648,"downgradeInfo":{}}`
	statusFollower  = `{"header":{"cluster_id":16509504660443962926,"member_id":323676404339268582,"revision":301,"raft_term":2},"version":"3.6.6","dbSize":49152,"leader":11589452085494132595,"raftIndex":308,"raftTerm":2,"raftAppliedIndex":308,"dbSizeInUse":49152,"storageVersion":"3.6.0","dbSizeQuota":2147483648,"downgradeInfo":{}}`
	statusCutOff    = `{"header":{"cluster_id":16509504660443962926,"member_id":330631803197386810,"revision":1,"raft_term":2},"version":"3.6.6","dbSize":20480,"raftIndex":8,"raftTerm":2,"raftAppliedIndex":8,"errors":["etcdserver: no leader"],"dbSizeInUse":16384,"storageVersion":"3.6.0","dbSizeQuota":2147483648,"downgradeInfo":{}}`
	memberListThree = `{"header":{"cluster_id":16509504660443962926,"member_id":11589452085494132595,"raft_term":2},"members":[{"ID":323676404339268582,"name":"dbwp-oth-eh2","peerURLs":["http://dbwp-oth-eh2:2380"],"clientURLs":["http://127.0.0.1:32372"]},{"ID":330631803197386810,"name":"dbwp-oth-eh3","peerURLs":["http://dbwp-oth-eh3:2380"],"clientURLs":["http://127.0.0.1:32373"]},{"ID":11589452085494132595,"name":"dbwp-oth-eh1","peerURLs":["http://dbwp-oth-eh1:2380"],"clientURLs":["http://127.0.0.1:32371"]}]}`

	// The learner of a four-member cluster, caught up, and the list that names it.
	statusLearner     = `{"header":{"cluster_id":16509504660443962926,"member_id":6885762516000392223,"revision":301,"raft_term":5},"version":"3.6.6","dbSize":49152,"leader":323676404339268582,"raftIndex":316,"raftTerm":5,"raftAppliedIndex":316,"dbSizeInUse":40960,"isLearner":true,"storageVersion":"3.6.0","dbSizeQuota":2147483648,"downgradeInfo":{}}`
	memberListLearner = `{"header":{"cluster_id":16509504660443962926,"member_id":11589452085494132595,"raft_term":2},"members":[{"ID":323676404339268582,"name":"dbwp-oth-eh2","peerURLs":["http://dbwp-oth-eh2:2380"],"clientURLs":["http://127.0.0.1:32372"]},{"ID":6885762516000392223,"name":"dbwp-oth-eh4","peerURLs":["http://dbwp-oth-eh4:2380"],"clientURLs":["http://127.0.0.1:32374"],"isLearner":true},{"ID":11589452085494132595,"name":"dbwp-oth-eh1","peerURLs":["http://dbwp-oth-eh1:2380"],"clientURLs":["http://127.0.0.1:32371"]}]}`

	// A 3.5 member answers without the fields 3.6 added: no quota, no storage version.
	statusV35 = `{"header":{"cluster_id":10690329912342562330,"member_id":8574685709276319584,"revision":1,"raft_term":2},"version":"3.5.21","dbSize":20480,"leader":8574685709276319584,"raftIndex":8,"raftTerm":2,"raftAppliedIndex":8,"dbSizeInUse":16384}`
)

const (
	idLeader   = 11589452085494132595
	idFollower = 323676404339268582
	idCutOff   = 330631803197386810
	idLearner  = 6885762516000392223
)

func statusOfJSON(t *testing.T, raw string) *clientv3.StatusResponse {
	t.Helper()
	var st clientv3.StatusResponse
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatal(err)
	}
	return &st
}

// rowsOf is the member list with each member's own status beside it, as
// askMembers assembles them, without the dial.
func rowsOf(t *testing.T, list string, statuses map[uint64]string) ([]memberRow, leaderView) {
	t.Helper()
	var resp clientv3.MemberListResponse
	if err := json.Unmarshal([]byte(list), &resp); err != nil {
		t.Fatal(err)
	}
	var rows []memberRow
	var lead leaderView
	for _, m := range resp.Members {
		r := memberRow{id: m.ID, name: m.Name, clientURLs: m.ClientURLs, learner: m.IsLearner}
		if raw, ok := statuses[m.ID]; ok {
			r.st = statusOfJSON(t, raw)
		} else {
			r.why = "no answer within 3s at " + m.ClientURLs[0]
		}
		rows = append(rows, r)
	}
	anyone := statusOfJSON(t, statusLeader)
	for _, r := range rows {
		if r.st != nil && anyone.Leader == r.id {
			lead = leaderView{id: r.id, st: r.st}
		}
	}
	if lead.id == 0 {
		lead.id = anyone.Leader
	}
	return rows, lead
}

func rowByName(t *testing.T, rows [][]string, name string) []string {
	t.Helper()
	for _, r := range rows {
		if r[1] == name {
			return r
		}
	}
	t.Fatalf("no member %q in %v", name, rows)
	return nil
}

// Columns: Member Name Role Version Term Index Applied Behind Revision DB Health.
func TestAFollowerCutOffFromItsPeersShowsHowFarBehindItIs(t *testing.T) {
	rows, lead := rowsOf(t, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusCutOff})
	table := membersTable(rows, lead).Rows

	if r := rowByName(t, table, "dbwp-oth-eh1"); r[2] != "leader" || r[5] != "308" || r[7] != "-" || r[10] != "ok" {
		t.Errorf("leader = %v", r)
	}
	if r := rowByName(t, table, "dbwp-oth-eh2"); r[2] != "follower" || r[7] != "0" || r[10] != "ok" {
		t.Errorf("a follower that kept up = %v", r)
	}
	// 308 - 8: the 300 writes it never saw, and a revision stuck at 1.
	r := rowByName(t, table, "dbwp-oth-eh3")
	if r[7] != "300" || r[8] != "1" || !strings.HasPrefix(r[10], "fail — no leader") {
		t.Errorf("the member that was cut off = %v", r)
	}
}

// etcd keeps 5000 entries of log after a snapshot, so a follower further back
// is sent the whole snapshot. 300 behind is not that; the same follower 5900
// behind is.
func TestAFollowerPastTheCatchUpWindowIsGradedForTheSnapshotItNeeds(t *testing.T) {
	rows, lead := rowsOf(t, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusFollower})
	lead.st.RaftIndex = 6208 // 308 + 5900, edited on the captured leader
	for i := range rows {
		if rows[i].id == idLeader {
			rows[i].st = lead.st
		}
	}
	r := rowByName(t, membersTable(rows, lead).Rows, "dbwp-oth-eh2")
	if r[7] != "5900" || !strings.HasPrefix(r[10], "warn — 5900 entries behind the leader, past the 5000") {
		t.Errorf("a follower 5900 behind = %v", r)
	}
	if !strings.Contains(r[10], "needs a snapshot") {
		t.Errorf("health = %q, want the cost named", r[10])
	}
}

func TestALearnerIsAmberWhileItCatchesUpAndOtherwiseFine(t *testing.T) {
	statuses := map[uint64]string{idLeader: statusLeader, idFollower: statusFollower, idLearner: statusLearner}
	// The leader of this capture is eh2 at term 5; the leader's own status is
	// the follower fixture with the term and leader of the learner's.
	statuses[idFollower] = `{"header":{"cluster_id":16509504660443962926,"member_id":323676404339268582,"revision":301,"raft_term":5},"version":"3.6.6","dbSize":49152,"leader":323676404339268582,"raftIndex":316,"raftTerm":5,"raftAppliedIndex":316,"dbSizeInUse":40960,"storageVersion":"3.6.0","dbSizeQuota":2147483648,"downgradeInfo":{}}`
	statuses[idLeader] = `{"header":{"cluster_id":16509504660443962926,"member_id":11589452085494132595,"revision":301,"raft_term":5},"version":"3.6.6","dbSize":49152,"leader":323676404339268582,"raftIndex":316,"raftTerm":5,"raftAppliedIndex":316,"dbSizeInUse":40960,"storageVersion":"3.6.0","dbSizeQuota":2147483648,"downgradeInfo":{}}`
	rows, _ := rowsOf(t, memberListLearner, statuses)
	lead := leaderView{id: idFollower, st: statusOfJSON(t, statuses[idFollower])}

	r := rowByName(t, membersTable(rows, lead).Rows, "dbwp-oth-eh4")
	if r[2] != "learner" || r[10] != "ok" {
		t.Errorf("a learner that caught up = %v", r)
	}
	for i := range rows {
		if rows[i].id == idLearner {
			rows[i].st.RaftIndex = 100 // 216 behind, edited on the captured learner
			rows[i].st.RaftAppliedIndex = 100
			rows[i].st.RaftTerm = 5
		}
	}
	lead.st.RaftIndex = 5316 // 5000 behind
	if got := rowByName(t, membersTable(rows, lead).Rows, "dbwp-oth-eh4")[10]; got != "pending — learner catching up, 5216 entries behind" {
		t.Errorf("a learner far behind = %q", got)
	}
}

func TestMembersThatDisagreeAboutTheLeaderOrTheTermAreGraded(t *testing.T) {
	rows, lead := rowsOf(t, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusFollower})

	rows[1].st = statusOfJSON(t, statusFollower)
	rows[1].st.RaftTerm = 3 // an election this member has seen and the leader has not
	if got := memberHealth(rows[1], lead); got != "warn — term 3, the leader's is 2" {
		t.Errorf("a different term = %q", got)
	}

	rows[1].st = statusOfJSON(t, statusFollower)
	rows[1].st.Leader = idCutOff // a member that names someone else
	if got := memberHealth(rows[1], lead); !strings.HasPrefix(got, "warn — believes 12648fb7bfb5aba leads") &&
		!strings.HasPrefix(got, "warn — believes ") {
		t.Errorf("a different leader = %q", got)
	}
}

func TestAMemberThatHasNotAppliedWhatItCommittedIsGraded(t *testing.T) {
	rows, lead := rowsOf(t, memberListThree, map[uint64]string{idLeader: statusLeader, idFollower: statusFollower})
	rows[0].st.RaftIndex, rows[0].st.RaftAppliedIndex = 9000, 100
	lead.st = rows[2].st
	lead.st.RaftIndex = 9000
	if got := memberHealth(rows[0], lead); got != "warn — 8900 entries committed and not yet applied" {
		t.Errorf("health = %q", got)
	}
}

// 3.5 reports no quota and 3.4's applied index is the oldest a member can
// lack: the cells are blank, not zero.
func TestAMemberThatReportsLessIsBlankNotZero(t *testing.T) {
	st := statusOfJSON(t, statusV35)
	if got := storageTable(st).Rows[0]; got[2] != "-" || got[3] != "-" {
		t.Errorf("storage = %v", got)
	}
	st.RaftAppliedIndex = 0
	if got := appliedText(st); got != "-" {
		t.Errorf("applied index absent reads %q", got)
	}
}

func TestTheQuorumLineSaysWhatTheMembersAnsweredNotWhatTheClusterIs(t *testing.T) {
	healthy, _ := rowsOf(t, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusFollower})
	if got, ok := quorumText(healthy, idLeader); !ok || got != "3 of 3 voting members answering with a leader; a write needs 2" {
		t.Errorf("all three = %q", got)
	}

	// One cut off, which answers clients and has no leader: asked, not counted.
	cut, _ := rowsOf(t, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusCutOff})
	if got, _ := quorumText(cut, idLeader); got != "2 of 3 voting members answering with a leader; a write needs 2 — one more member lost and writes stop" {
		t.Errorf("one cut off = %q", got)
	}

	// A cluster reached through one published port: the others cannot be asked
	// from here, and the endpoint's own leader says it is serving.
	one, _ := rowsOf(t, memberListThree, map[uint64]string{idLeader: statusLeader})
	if got, _ := quorumText(one, idLeader); !strings.Contains(got, "the cluster is serving") {
		t.Errorf("one reachable, endpoint has a leader = %q", got)
	}
	if got, _ := quorumText(one, 0); !strings.Contains(got, "the endpoint has no leader") {
		t.Errorf("one reachable, endpoint has none = %q", got)
	}

	// A learner votes for nothing, so it is not counted among the voters.
	withLearner, _ := rowsOf(t, memberListLearner, map[uint64]string{idLeader: statusLeader, idFollower: statusFollower, idLearner: statusLearner})
	if got, _ := quorumText(withLearner, idLeader); !strings.HasPrefix(got, "2 of 2 voting members") {
		t.Errorf("with a learner = %q", got)
	}

	// Members never asked make no claim about the cluster.
	skipped, _ := rowsOf(t, memberListThree, map[uint64]string{idLeader: statusLeader})
	skipped[0].notAsked = true
	if got, ok := quorumText(skipped, idLeader); ok {
		t.Errorf("a count of a subset was reported: %q", got)
	}
}

func TestASingleMemberClusterSaysItHasNoPeerToFailOverTo(t *testing.T) {
	one := []memberRow{{id: idLeader, name: "es1", clientURLs: []string{"http://127.0.0.1:2379"}, st: statusOfJSON(t, statusLeader)}}
	got, _ := quorumText(one, idLeader)
	if !strings.Contains(got, "1 of 1 voting member answering with a leader; a write needs 1 — a single member has no peer") {
		t.Errorf("quorum = %q", got)
	}
	if r := membersTable(one, leaderView{id: idLeader, st: one[0].st}).Rows[0]; r[2] != "leader" || r[7] != "-" {
		t.Errorf("row = %v", r)
	}
}

// A member that cannot be read is not a member that is down: the cell says
// what was tried and why it failed, and a member nobody dialled says it was
// not asked.
func TestAMemberThatCouldNotBeAskedSaysSoAndWhy(t *testing.T) {
	lead := leaderView{id: idLeader, st: statusOfJSON(t, statusLeader)}
	silent := memberRow{id: idFollower, name: "eh2", clientURLs: []string{"http://eh2:2379"}, why: "no answer within 3s at http://eh2:2379"}
	if got := memberHealth(silent, lead); got != "unreachable — no answer within 3s at http://eh2:2379" {
		t.Errorf("silent = %q", got)
	}
	skipped := memberRow{id: idFollower, name: "eh2", clientURLs: []string{"http://eh2:2379"}, notAsked: true, why: "the kube: forward reaches one member"}
	if got := memberHealth(skipped, lead); got != "info — not asked: the kube: forward reaches one member" {
		t.Errorf("skipped = %q", got)
	}
	unstarted := memberRow{id: idFollower, why: "has not started: no client URL yet"}
	if got := memberHealth(unstarted, lead); got != "pending — has not started: no client URL yet" {
		t.Errorf("unstarted = %q", got)
	}
	if r := membersTable([]memberRow{unstarted}, lead).Rows[0]; r[2] != "unstarted" || r[1] != "-" {
		t.Errorf("unstarted row = %v", r)
	}
}

func TestWhyAMemberWasNotReadIsTheShortestTrueReason(t *testing.T) {
	refused := &stdnet.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	noRoute := &stdnet.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"a deadline", context.DeadlineExceeded, "no answer within 3s"},
		// What etcd's client returns for a member nothing listens on: it keeps
		// retrying until the bound, then reports the last dial's error under
		// DeadlineExceeded.
		{"a refused dial retried until the bound", status.Error(codes.DeadlineExceeded,
			`latest balancer error: connection error: desc = "transport: Error while dialing: dial tcp 127.0.0.1:32372: connect: connection refused"`),
			"no answer within 3s"},
		{"a refused dial", refused, "connection refused"},
		{"a refused dial a status carries as text", status.Error(codes.Unavailable,
			`connection error: desc = "transport: Error while dialing: dial tcp 127.0.0.1:32372: connect: connection refused"`),
			"connection refused"},
		{"no route", noRoute, "no route from here"},
		// A member that was reached and refused its certificate, whose names
		// are the server's to choose: a dial's words only when it was a dial.
		{"a certificate valid for a name that spells a refusal", status.Error(codes.Unavailable,
			`connection error: desc = "transport: authentication handshake failed: tls: failed to verify certificate: `+
				`x509: certificate is valid for connection refused, no route to host, not 10.0.0.9"`),
			`unavailable: connection error: desc = "transport: authentication handshake failed: tls: failed to verify certificate: ` +
				`x509: certificate is valid for connection refused, no route to host, not 10.0.0.9"`},
		{"a name nothing resolves", &stdnet.DNSError{Err: "no such host", Name: "eh2", IsNotFound: true}, "the name does not resolve from here"},
		{"another status", status.Error(codes.Unavailable, "etcdserver: no leader"), "unavailable: etcdserver: no leader"},
		{"anything else", errors.New("boom"), "boom"},
	} {
		if got := unreachableWhy(tc.err); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A forward ends at one member and a username over plaintext would hand its
// token to whatever the member list names: neither asks the others, and each
// says how to get them asked in the words of the surface reading.
func TestTheOtherMembersAreNotAskedWhereAskingWouldBeWrong(t *testing.T) {
	forwarded := req(t, "etcd.overview", nil).WithProfile("lab", plugin.TunnelKube)
	if got := whyNotAsked(forwarded); !strings.Contains(got, "kube: forward reaches one member") {
		t.Errorf("forward = %q", got)
	}

	auth := req(t, "etcd.overview", map[string]any{"username": "monitor"})
	cli := whyNotAsked(auth.WithSurface(plugin.SurfaceCLI))
	mcp := whyNotAsked(auth.WithSurface(plugin.SurfaceMCP))
	if !strings.Contains(cli, "an https:// endpoint, or --tls, --ca-file and --tls-server-name, turns TLS on") {
		t.Errorf("cli = %q", cli)
	}
	if strings.Contains(mcp, "--tls") || !strings.Contains(mcp, "the operator's `tls`, `ca-file` and `tls-server-name` settings") {
		t.Errorf("mcp = %q", mcp)
	}

	secured := req(t, "etcd.overview", map[string]any{"username": "monitor", "endpoint": "https://etcd.internal:2379"})
	if got := whyNotAsked(secured); got != "" {
		t.Errorf("a username over an https:// endpoint is not asked: %q", got)
	}

	tls := req(t, "etcd.overview", map[string]any{"username": "monitor", "tls": true})
	if got := whyNotAsked(tls); got != "" {
		t.Errorf("a username over TLS is asked: %q", got)
	}
	if got := whyNotAsked(req(t, "etcd.overview", nil)); got != "" {
		t.Errorf("a plain direct connection is asked: %q", got)
	}
}

// A learner endpoint is served no member list, so the one member it can speak for
// is itself, and a quorum is a claim about voters it has not seen.
func TestALearnerEndpointMakesNoClaimAboutQuorum(t *testing.T) {
	alone := []memberRow{{id: idLearner, learner: true, st: statusOfJSON(t, statusLearner)}}
	if got, ok := quorumText(alone, idFollower); ok {
		t.Errorf("quorum reported from a learner alone: %q", got)
	}
	if r := membersTable(alone, leaderView{id: idFollower}).Rows[0]; r[2] != "learner" || r[7] != "-" || r[10] != "ok" {
		t.Errorf("row = %v", r)
	}
}
