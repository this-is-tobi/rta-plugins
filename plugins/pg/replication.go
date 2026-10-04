package main

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// pg.replication answers one question — is replication healthy, where is every
// member, and how far behind is each one — from the three places PostgreSQL
// keeps the answer: the server's own position (pg_current_wal_lsn on a
// primary, the receive and replay positions on a standby), pg_stat_replication
// for who is streaming from it, and pg_replication_slots for what it is
// holding WAL back for. Read together because none of them is the answer
// alone: a standby that has gone away is invisible in pg_stat_replication and
// shows only as a slot nobody reads, and a slot is not a position.
//
// Positions and states only. An LSN, a state word, a byte count and a
// timestamp describe the cluster and carry nothing anybody stored, which is
// what keeps this in the read tier beside pg.status.

// minReplicationVersion is the oldest server this reads. wal_status and
// safe_wal_size, which a slot's headroom is read from, arrived in 13, and 14
// is the oldest release still maintained; a server older than that is refused
// by name rather than answered with columns missing.
const minReplicationVersion = 140000

// serverFacts is what is true of the server before any replication view is
// chosen: which role it plays, and the settings that say whether replication
// is even switched on. Every field is readable by any role.
type serverFacts struct {
	versionNum int
	version    string
	recovering bool
	started    time.Time
	walLevel   string
	senders    int
	syncNames  string
	// syncCommit is the server-wide synchronous_commit. A role or database
	// can override it and this cannot see that, which is why it only lifts a
	// finding (see gradeSynchronous) and never raises one.
	syncCommit string
}

// primaryPosition is a primary's own write position and the WAL file it is
// being written to. The timeline is the file name's first eight hex digits:
// the checkpoint's timeline, which is the other place it can be read, still
// names the old one for the moments after a promotion.
type primaryPosition struct {
	lsn      string
	walFile  string
	timeline string
}

// standbyPosition is a standby's own: what it has received, what it has
// replayed, and when the last transaction it replayed committed on the
// primary. Pointers throughout, because NULL is a fact here — a standby that
// has never connected has no receive position, and zero would say it had.
type standbyPosition struct {
	receiveLSN *string
	replayLSN  *string
	gap        *int64
	replayedAt *time.Time
	paused     *bool
	timeline   string
}

// receiverRow is pg_stat_wal_receiver: the link a standby holds to its
// upstream. present is false when no WAL receiver process exists, which is
// different from one the role may not read, and hidden says which.
type receiverRow struct {
	present   bool
	hidden    bool
	status    string
	timeline  *int32
	host      *string
	port      *int32
	slot      *string
	endLSN    *string
	endTime   *time.Time
	behindEnd *int64
}

// standbyRow is one pg_stat_replication row. Every column past the name is
// NULL to a role without pg_monitor, so state is the one that decides whether
// a row is hidden: it is never NULL on a row the role may read.
type standbyRow struct {
	pid      int32
	name     string
	client   *string
	state    *string
	syncKind *string
	priority *int32
	sent     *string
	write    *string
	flush    *string
	replay   *string
	behind   *int64
	writeLag *float64
	flushLag *float64
	replLag  *float64
	slot     *string
}

func (s standbyRow) hidden() bool { return s.state == nil }

// slotRow is one pg_replication_slots row. retained is measured from
// restart_lsn to the server's own position, which is the WAL the slot is
// what keeps from being recycled.
type slotRow struct {
	name      string
	kind      string
	database  string
	temporary bool
	active    bool
	holder    *string
	restart   *string
	retained  *int64
	status    *string
	safeLeft  *int64
	invalid   *string
}

// replicationFacts is everything one call reads. Which half is filled
// depends on the role, and settings and the lists are filled on both, since
// a standby can have standbys of its own.
type replicationFacts struct {
	server   serverFacts
	primary  primaryPosition
	standby  standbyPosition
	receiver receiverRow
	standbys []standbyRow
	slots    []slotRow
	// timelineGuess is set when a standby's timeline came from its last
	// checkpoint rather than the receiver, which names the timeline being
	// streamed and so is the only one that is right straight after a switch.
	timelineGuess bool
}

// ref is the position every gap is measured against: the primary's own write
// position, or on a standby the one it has received, which is what its
// downstream standbys are catching up to. Evaluated once, in a subselect, so
// that every row of one result is measured from the same point rather than
// from the moment each row was visited.
func ref(recovering bool) string {
	if recovering {
		return "pg_last_wal_receive_lsn()"
	}
	return "pg_current_wal_lsn()"
}

// optional runs a read whose failure to a role without the privilege is a
// reason to say less, not to say nothing. Only a refusal by the server is
// forgiven: a connection that dropped is the same failure for every read
// and goes back to the caller.
func optional(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" {
		return nil
	}
	return err
}

const serverSQL = `
	select current_setting('server_version_num'), current_setting('server_version'),
	       pg_is_in_recovery(), pg_postmaster_start_time(),
	       current_setting('wal_level'), current_setting('max_wal_senders'),
	       current_setting('synchronous_standby_names'), current_setting('synchronous_commit')`

func readServerFacts(ctx context.Context, conn querier) (serverFacts, error) {
	var f serverFacts
	var num, senders, syncNames string
	err := conn.QueryRow(ctx, serverSQL).
		Scan(&num, &f.version, &f.recovering, &f.started, &f.walLevel, &senders, &syncNames, &f.syncCommit)
	if err != nil {
		return f, err
	}
	f.versionNum, _ = strconv.Atoi(num)
	f.syncNames = syncNames
	f.senders, _ = strconv.Atoi(senders)
	return f, nil
}
