package main

import (
	"context"
	stdnet "net"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The compact form is what a dashboard tile shows and what an agent reads by
// default: the verdict first, then the alarms, the quorum, the leader and the
// storage that decide whether writes work, and what the server is last. A tile
// is the first eight lines of it, and the page that used to be the default held
// four monotonic counters above the alarms.

func pair(kv view.KeyValue, key string) string {
	for _, p := range kv.Pairs {
		if p.Key == key {
			return p.Value
		}
	}
	return ""
}

func keysOf(kv view.KeyValue) string {
	keys := make([]string, 0, len(kv.Pairs))
	for _, p := range kv.Pairs {
		keys = append(keys, p.Key)
	}
	return strings.Join(keys, ",")
}

// A tile a third of a screen wide still reads: no row is longer than a line of
// an 80-column terminal, and there are no more rows than a tile holds.
func assertFitsATile(t *testing.T, kv view.KeyValue) {
	t.Helper()
	if len(kv.Pairs) > 8 {
		t.Errorf("%d rows, a tile holds eight: %v", len(kv.Pairs), kv.Pairs)
	}
	for _, p := range kv.Pairs {
		if n := utf8.RuneCountInString(p.Key) + 2 + utf8.RuneCountInString(p.Value); n > 80 || strings.Contains(p.Value, "\n") {
			t.Errorf("row %q is %d columns or wraps: %q", p.Key, n, p.Value)
		}
	}
}

const compactRows = "status,alarms,quorum,leader,storage,revision,version"

// compactOf is the rows for an endpoint that answered st, in a cluster of the
// members list names, each asked and answering as statuses has it.
func compactOf(t *testing.T, st *clientv3.StatusResponse, list string, statuses map[uint64]string) view.KeyValue {
	t.Helper()
	rows, _ := rowsOf(t, list, statuses)
	return compactOverview(st, rows, leaderOf(st, rows), "")
}

func TestAHealthyClusterIsOkFirstAndItsIdentityLast(t *testing.T) {
	st := statusOfJSON(t, statusLeader)
	kv := compactOf(t, st, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusFollower})

	if got := keysOf(kv); got != compactRows {
		t.Fatalf("rows = %s, want %s", got, compactRows)
	}
	for key, want := range map[string]string{
		"status":   "ok",
		"alarms":   "none",
		"quorum":   "3 of 3 members healthy, a write needs 2",
		"leader":   "dbwp-oth-eh1",
		"storage":  "0.0% of 2.0 GiB quota",
		"revision": "301",
		"version":  "3.6.6, answered by dbwp-oth-eh1",
	} {
		if got := pair(kv, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	assertFitsATile(t, kv)
}

// The alarm that stops every write is the one thing a healthy-looking cluster
// hides: it answers reads, and the storage row is at the quota.
func TestANospaceAlarmIsAFailureNamedForTheMemberThatRaisedIt(t *testing.T) {
	st := statusOfJSON(t, statusLeader)
	st.DbSize = st.DbSizeQuota
	st.Errors = []string{"memberID:" + strconv.FormatUint(idLeader, 10) + " alarm:NOSPACE"}
	kv := compactOf(t, st, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusFollower})

	if got, want := pair(kv, "status"), "fail — NOSPACE on dbwp-oth-eh1: writes are refused, reads still answer"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := pair(kv, "alarms"); got != "NOSPACE on dbwp-oth-eh1" {
		t.Errorf("alarms = %q", got)
	}
	if got := pair(kv, "storage"); got != "100.0% of 2.0 GiB quota" {
		t.Errorf("storage = %q", got)
	}
	assertFitsATile(t, kv)
}

func TestAnAlarmThatIsNotNospaceIsStillAFailure(t *testing.T) {
	st := statusOfJSON(t, statusLeader)
	st.Errors = []string{"memberID:" + strconv.FormatUint(idFollower, 10) + " alarm:CORRUPT"}
	kv := compactOf(t, st, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusFollower})
	if got := pair(kv, "status"); got != "fail — CORRUPT on dbwp-oth-eh2" {
		t.Errorf("status = %q", got)
	}
}

func TestAMemberWithNoLeaderIsTheClusterWithoutOne(t *testing.T) {
	st := statusOfJSON(t, statusLeader)
	st.Leader = 0
	st.Errors = []string{"etcdserver: no leader"}
	kv := compactOf(t, st, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusFollower})

	if got, want := pair(kv, "status"), "fail — no leader: an election is under way or quorum is lost"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := pair(kv, "leader"); got != "none — an election is under way or quorum is lost" {
		t.Errorf("leader = %q", got)
	}
	if got := pair(kv, "alarms"); got != "none" {
		t.Errorf("alarms = %q, want the no-leader error said once, in the leader row", got)
	}
	assertFitsATile(t, kv)
}

// One member cut off from its peers is a degraded cluster that still takes
// writes: amber, and the quorum row counts who is left.
func TestOneMemberCutOffIsAWarningAndTheQuorumCountsTheRest(t *testing.T) {
	st := statusOfJSON(t, statusLeader)
	kv := compactOf(t, st, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusCutOff})

	if got, want := pair(kv, "status"), "warn — member dbwp-oth-eh3 has no leader, cut off from its peers"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := pair(kv, "quorum"); got != "2 of 3 members healthy, a write needs 2" {
		t.Errorf("quorum = %q", got)
	}
	assertFitsATile(t, kv)
}

// A member that does not answer is named, with how long it was waited for.
func TestAMemberThatDoesNotAnswerIsNamed(t *testing.T) {
	st := statusOfJSON(t, statusLeader)
	kv := compactOf(t, st, memberListThree, map[uint64]string{idLeader: statusLeader, idFollower: statusFollower})

	if got, want := pair(kv, "status"), "warn — member dbwp-oth-eh3 unreachable, no answer within 2s"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := pair(kv, "quorum"); got != "2 of 3 members healthy, a write needs 2; dbwp-oth-eh3 unreachable" {
		t.Errorf("quorum = %q", got)
	}
	assertFitsATile(t, kv)
}

func TestAFollowerPastTheCatchUpWindowIsNamedWithHowFarBehindItIs(t *testing.T) {
	st := statusOfJSON(t, statusLeader)
	st.RaftIndex = 6208
	leader := statusOfJSON(t, statusLeader)
	leader.RaftIndex = 6208
	rows, lead := rowsOf(t, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusFollower})
	for i := range rows {
		if rows[i].id == idLeader {
			rows[i].st = leader
		}
	}
	lead.st = leader
	kv := compactOverview(st, rows, lead, "")
	if got := pair(kv, "status"); got != "warn — member dbwp-oth-eh2 is 5900 entries behind the leader" {
		t.Errorf("status = %q", got)
	}
}

// The worst of several facts leads; of equals the first, in the order the rows
// are in.
func TestTheWorstFactLeads(t *testing.T) {
	st := statusOfJSON(t, statusLeader)
	st.DbSize = st.DbSizeQuota / 10 * 9
	kv := compactOf(t, st, memberListThree, map[uint64]string{idLeader: statusLeader, idFollower: statusFollower})
	if got := pair(kv, "status"); !strings.HasPrefix(got, "warn — member dbwp-oth-eh3 unreachable") {
		t.Errorf("status = %q, want the first of two warnings", got)
	}

	st.Errors = []string{"memberID:" + strconv.FormatUint(idLeader, 10) + " alarm:NOSPACE"}
	kv = compactOf(t, st, memberListThree, map[uint64]string{idLeader: statusLeader, idFollower: statusFollower})
	if got := pair(kv, "status"); !strings.HasPrefix(got, "fail — NOSPACE") {
		t.Errorf("status = %q, want the failure over the warnings", got)
	}
}

func TestTheStorageRowIsGradedAgainstTheQuotaAndBlankWhereNoneIsReported(t *testing.T) {
	for name, tc := range map[string]struct {
		dbSize, quota int64
		storage       string
		status        string
	}{
		"comfortable":           {1 << 30, 2 << 30, "50.0% of 2.0 GiB quota", "ok"},
		"nearly full":           {17 * (1 << 30) / 10, 2 << 30, "85.0% of 2.0 GiB quota", "warn — storage at 85.0% of its quota"},
		"full":                  {2 << 30, 2 << 30, "100.0% of 2.0 GiB quota", "fail — storage is at its quota: writes are refused"},
		"no quota (before 3.6)": {1 << 30, 0, "1.0 GiB, no quota reported (before etcd 3.6)", "ok"},
	} {
		t.Run(name, func(t *testing.T) {
			st := statusOfJSON(t, statusLeader)
			st.DbSize, st.DbSizeQuota = tc.dbSize, tc.quota
			kv := compactOf(t, st, memberListThree, map[uint64]string{
				idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusFollower})
			if got := pair(kv, "storage"); got != tc.storage {
				t.Errorf("storage = %q, want %q", got, tc.storage)
			}
			if got := pair(kv, "status"); got != tc.status {
				t.Errorf("status = %q, want %q", got, tc.status)
			}
		})
	}
}

// Through a forward only the endpoint's own member is asked, and a count of
// one member is a claim about the cluster it cannot make.
func TestARouteThatAsksOneMemberDoesNotCountTheQuorum(t *testing.T) {
	st := statusOfJSON(t, statusLeader)
	rows, lead := rowsOf(t, memberListThree, map[uint64]string{idLeader: statusLeader})
	for i := range rows {
		if rows[i].st == nil {
			rows[i].notAsked, rows[i].why = true, "the kube: forward reaches one member"
		}
	}
	kv := compactOverview(st, rows, lead, "")
	if got := pair(kv, "quorum"); got != "not counted — not every member is asked from here" {
		t.Errorf("quorum = %q", got)
	}
	if got := pair(kv, "status"); got != "ok" {
		t.Errorf("status = %q, want a route's limit not to be a cluster's fault", got)
	}
	assertFitsATile(t, kv)
}

func TestALearnerEndpointIsNotServedTheMemberList(t *testing.T) {
	st := statusOfJSON(t, statusLearner)
	rows := []memberRow{{id: idLearner, learner: true, st: st}}
	kv := compactOverview(st, rows, leaderView{id: st.Leader}, "")
	if got := pair(kv, "quorum"); got != "not counted — a learner is not served the member list" {
		t.Errorf("quorum = %q", got)
	}
	assertFitsATile(t, kv)
}

// A member name is the member's to choose, and a row shows it as a list does.
func TestAMembersNameIsShownAsAListShowsIt(t *testing.T) {
	st := statusOfJSON(t, statusLeader)
	rows, lead := rowsOf(t, memberListThree, map[uint64]string{
		idLeader: statusLeader, idFollower: statusFollower, idCutOff: statusFollower})
	rows[2].name = "eh1\x1b[31mred"
	kv := compactOverview(st, rows, lead, "")
	if got := pair(kv, "leader"); strings.Contains(got, "\x1b") {
		t.Errorf("leader = %q carries a raw escape", got)
	}
}

func TestTheOverviewDeclaresADetailedPageAndSaysSoToAgents(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.ID != "etcd.overview" {
			continue
		}
		if !c.Detailed {
			t.Error("etcd.overview is not Detailed")
		}
		for name, text := range map[string]string{"description": c.Description, "agent": c.Agent} {
			if !strings.Contains(text, "`detail`") || !strings.Contains(text, "status") {
				t.Errorf("the %s text does not say what the default answer is and what `detail` adds: %q", name, text)
			}
		}
		return
	}
	t.Fatal("no etcd.overview")
}

// Each member is waited for two seconds and no longer, so that one that drops
// packets costs a refresh two seconds and not three, and the answer names it.
func TestAMemberDialIsBoundedAtTwoSeconds(t *testing.T) {
	if memberTimeout != 2*time.Second {
		t.Errorf("memberTimeout = %s, want 2s", memberTimeout)
	}
}

// fakeCluster is a cluster of members each served over gRPC, so that the
// overview is run end to end: the client, the member list, a status per member.
type fakeMember struct {
	id      uint64
	name    string
	status  etcdserverpb.StatusResponse
	port    int
	silent  bool
	address string
}

type fakeMaintenance struct {
	etcdserverpb.UnimplementedMaintenanceServer
	status *etcdserverpb.StatusResponse
}

func (m fakeMaintenance) Status(context.Context, *etcdserverpb.StatusRequest) (*etcdserverpb.StatusResponse, error) {
	return m.status, nil
}

type fakeMembership struct {
	etcdserverpb.UnimplementedClusterServer
	members []*etcdserverpb.Member
	self    uint64
}

func (m fakeMembership) MemberList(context.Context, *etcdserverpb.MemberListRequest) (*etcdserverpb.MemberListResponse, error) {
	return &etcdserverpb.MemberListResponse{Header: &etcdserverpb.ResponseHeader{MemberId: m.self}, Members: m.members}, nil
}

// startCluster serves every member that is not silent on a port of its own and
// returns the address of the first one. A silent member has a port reserved and
// closed: its advertised URL is the address of nothing, which the client retries
// until its bound and then reports.
func startCluster(t *testing.T, members []*fakeMember) string {
	t.Helper()
	var listed []*etcdserverpb.Member
	listeners := make([]stdnet.Listener, len(members))
	for i, m := range members {
		ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[i] = ln
		m.port = ln.Addr().(*stdnet.TCPAddr).Port
		m.address = "127.0.0.1:" + strconv.Itoa(m.port)
		listed = append(listed, &etcdserverpb.Member{
			ID: m.id, Name: m.name,
			PeerURLs:   []string{"http://" + m.address + "0"},
			ClientURLs: []string{"http://" + m.address},
		})
	}
	for i, m := range members {
		if m.silent {
			_ = listeners[i].Close()
			continue
		}
		srv := grpc.NewServer()
		m.status.Header = &etcdserverpb.ResponseHeader{MemberId: m.id, Revision: m.status.Header.GetRevision(), RaftTerm: m.status.RaftTerm}
		etcdserverpb.RegisterMaintenanceServer(srv, fakeMaintenance{status: &m.status})
		etcdserverpb.RegisterClusterServer(srv, fakeMembership{members: listed, self: m.id})
		go func() { _ = srv.Serve(listeners[i]) }()
		t.Cleanup(srv.Stop)
	}
	return members[0].address
}

func healthyMember(id uint64, name string, leader uint64) *fakeMember {
	return &fakeMember{id: id, name: name, status: etcdserverpb.StatusResponse{
		Header:  &etcdserverpb.ResponseHeader{Revision: 40119233},
		Version: "3.6.6", DbSize: 1 << 30, DbSizeInUse: 1 << 29, DbSizeQuota: 2 << 30,
		Leader: leader, RaftIndex: 900, RaftTerm: 14, RaftAppliedIndex: 900,
	}}
}

func overviewAt(t *testing.T, address string, values map[string]any) (view.View, time.Duration) {
	t.Helper()
	if values == nil {
		values = map[string]any{}
	}
	values["address"] = address
	for _, c := range Plugin().Capabilities {
		if c.ID != "etcd.overview" {
			continue
		}
		start := time.Now()
		v, err := c.Run(context.Background(), plugin.NewRequest(
			plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false))
		if err != nil {
			t.Fatal(err)
		}
		return v, time.Since(start)
	}
	t.Fatal("no etcd.overview")
	return nil, 0
}

func TestTheOverviewRunEndToEndAsksEveryMemberOnceAndAnswersRows(t *testing.T) {
	address := startCluster(t, []*fakeMember{
		healthyMember(1, "etcd-0", 2), healthyMember(2, "etcd-1", 2), healthyMember(3, "etcd-2", 2)})
	v, _ := overviewAt(t, address, nil)
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("the overview is a %s, want the rows", view.TypeOf(v))
	}
	if got := keysOf(kv); got != compactRows {
		t.Errorf("rows = %s, want %s", got, compactRows)
	}
	for key, want := range map[string]string{
		"status": "ok", "quorum": "3 of 3 members healthy, a write needs 2", "leader": "etcd-1",
		"storage": "50.0% of 2.0 GiB quota", "revision": "40119233",
	} {
		if got := pair(kv, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	assertFitsATile(t, kv)
}

// A member nothing answers for costs the refresh the bound and not the sum of
// the waits, and is named, with the member it is.
func TestAMemberNothingAnswersForCostsTwoSecondsAndIsNamed(t *testing.T) {
	down := healthyMember(3, "etcd-2", 2)
	down.silent = true
	address := startCluster(t, []*fakeMember{healthyMember(1, "etcd-0", 2), healthyMember(2, "etcd-1", 2), down})
	v, took := overviewAt(t, address, nil)
	if took > 2900*time.Millisecond {
		t.Errorf("the refresh took %s with one member down, want it held to the two seconds the member is waited for", took)
	}
	kv := v.(view.KeyValue)
	if got, want := pair(kv, "status"), "warn — member etcd-2 unreachable, no answer within 2s"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := pair(kv, "quorum"); got != "2 of 3 members healthy, a write needs 2; etcd-2 unreachable" {
		t.Errorf("quorum = %q", got)
	}
	assertFitsATile(t, kv)
}

type deniedMembership struct {
	etcdserverpb.UnimplementedClusterServer
}

func (deniedMembership) MemberList(context.Context, *etcdserverpb.MemberListRequest) (*etcdserverpb.MemberListResponse, error) {
	return nil, status.Error(codes.PermissionDenied, "etcdserver: permission denied")
}

// A role that may read the endpoint's own status and not the member list gets the
// rows it can have and a row that says which it cannot: the verdict names it
// instead of reading ok on a quorum nobody counted, and the page is not an error.
func TestARoleThatMayNotListTheMembersGetsTheRestAndIsToldSo(t *testing.T) {
	addr := serveGRPC(t, func(s *grpc.Server) {
		etcdserverpb.RegisterClusterServer(s, deniedMembership{})
		etcdserverpb.RegisterMaintenanceServer(s, statusServer{})
	})
	c, err := clientv3.New(clientv3.Config{Endpoints: []string{addr}, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	v, err := overviewView(ctx, c, req(t, "etcd.overview", map[string]any{"address": addr}))
	if err != nil {
		t.Fatalf("a refused member list made the overview an error: %v", err)
	}
	kv := v.(view.KeyValue)
	if got := keysOf(kv); got != compactRows {
		t.Errorf("rows = %s, want %s", got, compactRows)
	}
	if got, want := pair(kv, "status"), "warn — quorum could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := pair(kv, "quorum"), "could not be read — the role may not list the members"; got != want {
		t.Errorf("quorum = %q, want %q", got, want)
	}
	if got := pair(kv, "storage"); got == "" || strings.Contains(got, "could not") {
		t.Errorf("storage = %q, want the rows the role can read to stand", got)
	}
	assertFitsATile(t, kv)

	v, err = overviewView(ctx, c, req(t, "etcd.overview", map[string]any{"address": addr, "detail": true}))
	if err != nil {
		t.Fatalf("detail with a refused member list: %v", err)
	}
	page := v.(view.Sections)
	if got := page.Items[len(page.Items)-1]; got.Key() != "members" {
		t.Errorf("last section = %q, want members", got.Key())
	} else if text, ok := got.View.(view.Text); !ok || !strings.HasPrefix(text.Body, "could not be read") {
		t.Errorf("members = %#v, want a text saying it could not be read", got.View)
	}
}

// The refusal of the endpoint's own status is the page's: there is no row to
// give a role that may read none of them.
func TestARoleThatMayNotReadTheStatusIsAnErrorAndNotARow(t *testing.T) {
	addr := serveGRPC(t, func(s *grpc.Server) {
		etcdserverpb.RegisterMaintenanceServer(s, deniedStatus{})
	})
	c, err := clientv3.New(clientv3.Config{Endpoints: []string{addr}, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = overviewView(ctx, c, req(t, "etcd.overview", map[string]any{"address": addr}))
	if ve := view.AsError(err, "x"); ve.Code != "etcd.denied" {
		t.Errorf("a role without the status = %+v, want etcd.denied", ve)
	}
}

type deniedStatus struct {
	etcdserverpb.UnimplementedMaintenanceServer
}

func (deniedStatus) Status(context.Context, *etcdserverpb.StatusRequest) (*etcdserverpb.StatusResponse, error) {
	return nil, status.Error(codes.PermissionDenied, "etcdserver: permission denied")
}

// A member that is not there at all is the error it always was.
func TestAClusterNothingListensForIsTheErrorItAlwaysWas(t *testing.T) {
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	c, err := clientv3.New(clientv3.Config{Endpoints: []string{addr}, DialTimeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = overviewView(ctx, c, req(t, "etcd.overview", map[string]any{"address": addr}))
	if err == nil {
		t.Fatal("an overview of a port nothing listens on answered")
	}
	if ve := view.AsError(err, "x"); !strings.HasPrefix(ve.Code, "etcd.") {
		t.Errorf("error = %+v, want an etcd.* code", ve)
	}
}

func TestTheDetailPageOpensWithTheCompactRowsAndKeepsEverythingElse(t *testing.T) {
	address := startCluster(t, []*fakeMember{
		healthyMember(1, "etcd-0", 2), healthyMember(2, "etcd-1", 2), healthyMember(3, "etcd-2", 2)})
	v, _ := overviewAt(t, address, map[string]any{"detail": true})
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("detail is a %s, want a page", view.TypeOf(v))
	}
	var titles []string
	for _, it := range page.Items {
		titles = append(titles, it.Title)
	}
	if got := strings.Join(titles, ","); got != "summary,status,storage,members" {
		t.Errorf("sections = %s, want summary,status,storage,members", got)
	}
	status := page.Items[1].View.(view.KeyValue)
	for _, key := range []string{"address", "version", "member id", "leader", "raft term", "raft index", "applied index", "revision", "quorum", "alarms"} {
		if pair(status, key) == "" {
			t.Errorf("the detail status lost its %q row: %v", key, status.Pairs)
		}
	}
	if members := page.Items[3].View.(view.Table); len(members.Rows) != 3 {
		t.Errorf("members = %d rows, want 3", len(members.Rows))
	}
}
