package main

import (
	stdnet "net"
	"strconv"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// staleContactSeconds is how long a link may go without a word before it is
// graded: min-replicas-max-lag's default, which is the number at which a
// primary configured with min-replicas-to-write starts refusing writes for
// want of enough fresh replicas. A threshold of our own would be a second
// opinion about when replication is late, beside the one the server acts on.
const staleContactSeconds = 10

// staleUpstreamSeconds is the same judgement for the other direction, and not
// the same number. The acknowledgements a primary reads its replicas by are
// sent every second; what a replica reads its primary by is whatever the
// primary last sent, and an idle primary sends only its ping, every
// repl-ping-replica-period, which is 10 seconds. A healthy replica of a quiet
// primary therefore reads 10 one second in every ten, and graded at 10 it was
// warned for it. The primary is silent at three missed pings, well short of
// repl-timeout (60 seconds) at which the replica drops the link itself.
const staleUpstreamSeconds = 30

// replicationTable is the role and every link it has: on a primary one row
// per replica with how far behind it is and whether a reconnect could still
// resume, on a replica the primary it follows and whether the link is up.
//
// **"Behind" is a primary's figure and is blank on a replica, on purpose.** A
// replica knows how much of the stream it has applied and nothing about how
// much exists: its own master_repl_offset is that same applied position, so
// the subtraction would print 0 on the replica furthest behind of all. The
// stream section says where to read it instead of printing a number that
// cannot be right.
//
// **Resync is judged only for a replica that is online.** The PSYNC a
// reconnecting replica sends asks for offset+1, and the primary honours it
// when that byte is still in the backlog. A replica in the middle of its full
// sync reports offset 0, which would read as resumable against a backlog that
// begins at 1, about a replica whose next move is not a reconnect at all.
//
// A peer is named as the address it is dialled at, an IPv6 literal
// bracketed: joined with a bare colon, as it once was, a replica at ::1 read
// ::1:6380, which no reader could split into an address and a port.
func replicationTable(in info) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Role"},
		{Name: "Peer"},
		{Name: "Link", Kind: view.KindStatus},
		{Name: "Offset", Kind: view.KindNumber},
		{Name: "Behind", Kind: view.KindBytes},
		{Name: "Last contact"},
		{Name: "Resync"},
	}}
	role := "primary"
	if r := in.get("role"); r == "slave" || r == "replica" {
		role = "replica"
		t.Rows = append(t.Rows, upstreamRow(in))
	}
	downstream := downstreamRows(in)
	if role == "primary" && len(downstream) == 0 {
		t.Rows = append(t.Rows, []string{"primary", "no replicas", "-", in.get("master_repl_offset"), "-", "-", "-"})
	}
	for _, row := range downstream {
		if role == "replica" {
			row[0] = "replica (relay)"
		}
		t.Rows = append(t.Rows, row)
	}
	t.Total = len(t.Rows)
	return t
}

func upstreamRow(in info) []string {
	return []string{"replica", stdnet.JoinHostPort(in.get("master_host"), in.get("master_port")),
		upstreamLink(in), in.get("slave_repl_offset"), "-", upstreamContact(in), "-"}
}

// upstreamLink grades the link a replica holds to its primary.
//
// **A link that is down is not always a link that is broken.** A replica in
// its full sync reports master_link_status:down for as long as the transfer
// runs, with master_sync_in_progress:1, and calling that "down" sends
// somebody to a primary that is doing exactly what it should. The percentage
// is printed only when the primary has said how large the transfer is: until
// the RDB is written the total is -1, and a share of -1 would be a number
// that is not a measurement.
func upstreamLink(in info) string {
	switch {
	case in.get("master_sync_in_progress") == "1":
		text := "pending — full sync"
		if total, read := in.int("master_sync_total_bytes"), in.int("master_sync_read_bytes"); total > 0 {
			text += " " + strconv.FormatInt(read*100/total, 10) + "%"
		}
		return text
	case in.get("master_link_status") == "up":
		if last := in.int("master_last_io_seconds_ago"); last >= staleUpstreamSeconds {
			return "warn — silent for " + span(secondsOf(last))
		}
		return "ok"
	default:
		if down, ok := parseInt(in.get("master_link_down_since_seconds")); ok && down >= 0 {
			return "down — for " + span(secondsOf(down))
		}
		return "down"
	}
}

func upstreamContact(in info) string {
	if in.get("master_link_status") != "up" {
		return "-"
	}
	return agoText(in.int("master_last_io_seconds_ago"))
}

// downstreamRows are the replicas this node feeds, from the slaveN lines of
// its own INFO. A replica with replicas of its own — a relay — reports them
// the same way, so the loop does not ask which role it is on.
func downstreamRows(in info) [][]string {
	var rows [][]string
	backlog := backlogOf(in)
	for i, n := 0, int(in.int("connected_slaves")); i < n; i++ {
		f := fieldsOf(in.get("slave" + strconv.Itoa(i)))
		offset, haveOffset := parseInt(f["offset"])
		behind, resync := "-", "-"
		if haveOffset && f["state"] == "online" {
			behind = format.Bytes(max(0, in.int("master_repl_offset")-offset))
			resync = backlog.resync(offset)
		}
		rows = append(rows, []string{"primary", stdnet.JoinHostPort(f["ip"], f["port"]),
			downstreamLink(f, resync, backlog), f["offset"], behind, contactText(f["lag"]), resync})
	}
	return rows
}

// downstreamLink grades one replica as its primary sees it. Anything but
// online is a full sync in progress — wait_bgsave while the RDB is written,
// send_bulk while it is sent — which is amber rather than red: nothing is
// broken, the replica is being rebuilt, and it will read as a long way
// behind until it finishes.
func downstreamLink(f map[string]string, resync string, b backlog) string {
	lag, _ := parseInt(f["lag"])
	switch {
	case f["state"] != "online":
		return "pending — full sync (" + f["state"] + ")"
	case resync == resyncFull:
		return "warn — behind the backlog, a reconnect now means a full resync"
	case lag >= staleContactSeconds:
		return "warn — silent for " + span(secondsOf(lag))
	}
	return "ok"
}

const (
	resyncPartial = "partial"
	resyncFull    = "full"
)

// backlog is the replication backlog as INFO reports it: the window of the
// stream a primary still holds, which is what lets a replica that dropped
// off and came back resume instead of copying the dataset again.
type backlog struct {
	active                 bool
	size, first, held, end int64
}

func backlogOf(in info) backlog {
	b := backlog{
		active: in.get("repl_backlog_active") == "1",
		size:   in.int("repl_backlog_size"),
		first:  in.int("repl_backlog_first_byte_offset"),
		held:   in.int("repl_backlog_histlen"),
	}
	b.end = b.first + b.held
	return b
}

// resync says whether a replica at this offset could still resume.
//
// The window is [first, first+held]: the primary accepts a PSYNC for offset+1
// when that byte is in the backlog, or is the very next one to arrive. A
// backlog that is not active — no replica has ever connected — holds
// nothing to judge against, and says "-" rather than guess.
func (b backlog) resync(replicaOffset int64) string {
	if !b.active {
		return "-"
	}
	if next := replicaOffset + 1; next >= b.first && next <= b.end {
		return resyncPartial
	}
	return resyncFull
}

func parseInt(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func contactText(lag string) string {
	n, ok := parseInt(lag)
	if !ok {
		return "-"
	}
	return agoText(n)
}

func agoText(seconds int64) string {
	if seconds < 0 {
		return "-"
	}
	return span(secondsOf(seconds)) + " ago"
}

func secondsOf(n int64) time.Duration { return time.Duration(n) * time.Second }

// noHistory is what master_replid2 holds until a promotion gives it
// something to remember.
const noHistory = "0000000000000000000000000000000000000000"

// streamPairs is the stream the links carry, which no row of the table can
// say: the replication id every member of one history shares, where the
// stream stands, what the backlog holds, and — after a failover — the id
// this node followed before it.
//
// **The id is the redis counterpart of a timeline.** A promoted replica
// starts a new history and keeps the old id as replid2 with the offset up to
// which the two agree, so the other replicas can continue from it without a
// full sync. Two members printing different ids that are not a promotion
// apart are not in one replication group.
//
// A server with no replication at all — a primary no replica has ever
// connected to — has no stream to describe, and gets none rather than an id
// and an offset about a log nobody reads.
func streamPairs(in info, sf plugin.Surface) (view.KeyValue, bool) {
	isReplica := in.get("role") == "slave" || in.get("role") == "replica"
	b := backlogOf(in)
	if !isReplica && in.int("connected_slaves") == 0 && !b.active && in.get("master_replid2") == noHistory {
		return view.KeyValue{}, false
	}
	pairs := []view.Pair{
		{Key: "replication id", Value: in.get("master_replid")},
	}
	if prev := in.get("master_replid2"); prev != "" && prev != noHistory {
		pairs = append(pairs, view.Pair{Key: "previous id", Value: prev + ", the same stream up to offset " +
			in.get("second_repl_offset") + " (this node was promoted, or followed a promoted one)"})
	}
	if isReplica {
		pairs = append(pairs,
			view.Pair{Key: "applied offset", Value: in.get("master_repl_offset")},
			view.Pair{Key: "primary's offset", Value: "not visible from a replica — " +
				sf.CapabilityName("redis.overview") + " against the primary shows how far behind this one is"})
	} else {
		pairs = append(pairs, view.Pair{Key: "stream offset", Value: in.get("master_repl_offset")})
	}
	pairs = append(pairs, view.Pair{Key: "backlog", Value: b.text()})
	return view.KeyValue{Pairs: pairs}, true
}

func (b backlog) text() string {
	if !b.active {
		return "not active — it exists while a replica is, or recently was, connected"
	}
	return format.Bytes(b.held) + " held from offset " + strconv.FormatInt(b.first, 10) +
		" (limit " + format.Bytes(b.size) + ")"
}
