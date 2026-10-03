package main

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// readReplication gathers every figure the view is built from. The reads that
// every role may make are errors when they fail; the ones a role without
// pg_monitor may be refused are said less of instead (see optional), so a
// monitoring role that cannot see a column still gets the rest of the answer.
func readReplication(ctx context.Context, conn *pgx.Conn) (replicationFacts, error) {
	var f replicationFacts
	var err error
	if f.server, err = readServerFacts(ctx, conn); err != nil {
		return f, err
	}
	if f.server.versionNum < minReplicationVersion {
		return f, nil
	}
	if f.server.recovering {
		err = readStandby(ctx, conn, &f)
	} else {
		err = readPrimary(ctx, conn, &f)
	}
	if err != nil {
		return f, err
	}
	if f.standbys, err = readStandbys(ctx, conn, f.server.recovering); err != nil {
		return f, err
	}
	f.slots, err = readSlots(ctx, conn, f.server)
	return f, err
}

// distance is how many bytes of WAL lie between two positions, floored at
// zero and NULL when either is unknown. The floor is because a gap is read
// from two moments — a standby's report and the primary's own position, taken
// a few microseconds apart — and the later can be behind the earlier by a
// hair. It is a case rather than greatest(..., 0) because greatest ignores a
// NULL: a standby the role may not see has no replay position, and
// greatest(NULL, 0) turned that into a standby that was exactly caught up.
func distance(ahead, behind string) string {
	d := "pg_wal_lsn_diff(" + ahead + ", " + behind + ")"
	return "(case when " + d + " < 0 then 0 else " + d + " end)::bigint"
}

const primarySQL = `select pg_current_wal_lsn()::text, pg_walfile_name(pg_current_wal_lsn())`

func readPrimary(ctx context.Context, conn *pgx.Conn, f *replicationFacts) error {
	p := &f.primary
	err := conn.QueryRow(ctx, primarySQL).Scan(&p.lsn, &p.walFile)
	if err != nil {
		return err
	}
	if len(p.walFile) >= 8 {
		if tli, err := strconv.ParseUint(p.walFile[:8], 16, 32); err == nil {
			p.timeline = strconv.FormatUint(tli, 10)
		}
	}
	return nil
}

var standbySQL = `
	select pg_last_wal_receive_lsn()::text, pg_last_wal_replay_lsn()::text,
	       ` + distance("pg_last_wal_receive_lsn()", "pg_last_wal_replay_lsn()") + `,
	       pg_last_xact_replay_timestamp(), pg_is_wal_replay_paused()`

const checkpointSQL = `select timeline_id from pg_control_checkpoint()`

func readStandby(ctx context.Context, conn *pgx.Conn, f *replicationFacts) error {
	s := &f.standby
	err := conn.QueryRow(ctx, standbySQL).
		Scan(&s.receiveLSN, &s.replayLSN, &s.gap, &s.replayedAt, &s.paused)
	if err != nil {
		return err
	}
	if err := readReceiver(ctx, conn, f); err != nil {
		return err
	}
	if f.receiver.timeline != nil {
		s.timeline = strconv.Itoa(int(*f.receiver.timeline))
		return nil
	}
	var tli int32
	err = conn.QueryRow(ctx, checkpointSQL).Scan(&tli)
	if err := optional(err); err != nil {
		return err
	}
	if err == nil {
		s.timeline, f.timelineGuess = strconv.Itoa(int(tli)), true
	}
	return nil
}

// readReceiver reads pg_stat_wal_receiver, which a role without
// pg_read_all_stats sees as one row of NULLs rather than as no row — the two
// are told apart on purpose, since "no receiver" is a finding about the
// standby and "cannot see it" is one about the role.
var receiverSQL = `
	select status, received_tli, sender_host, sender_port, slot_name,
	       latest_end_lsn::text, latest_end_time,
	       ` + distance("latest_end_lsn", "pg_last_wal_replay_lsn()") + `
	from pg_stat_wal_receiver`

func readReceiver(ctx context.Context, conn *pgx.Conn, f *replicationFacts) error {
	r := &f.receiver
	var status *string
	err := conn.QueryRow(ctx, receiverSQL).
		Scan(&status, &r.timeline, &r.host, &r.port, &r.slot, &r.endLSN, &r.endTime, &r.behindEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	r.present = true
	if status == nil {
		r.hidden = true
		return nil
	}
	r.status = *status
	return nil
}

// readStandbys lists pg_stat_replication, each standby joined to the slot it
// holds by the backend that is serving it — the one link between the two
// views, since the stat row carries no slot name of its own.
func standbysSQL(recovering bool) string {
	return `
		select s.pid, s.application_name, host(s.client_addr), s.state, s.sync_state, s.sync_priority,
		       s.sent_lsn::text, s.write_lsn::text, s.flush_lsn::text, s.replay_lsn::text,
		       ` + distance("cur.lsn", "s.replay_lsn") + `,
		       extract(epoch from s.write_lag)::float8, extract(epoch from s.flush_lag)::float8,
		       extract(epoch from s.replay_lag)::float8, r.slot_name
		from (select ` + ref(recovering) + ` as lsn) cur, pg_stat_replication s
		left join pg_replication_slots r on r.active_pid = s.pid
		order by s.application_name, s.pid`
}

func readStandbys(ctx context.Context, conn *pgx.Conn, recovering bool) ([]standbyRow, error) {
	rows, err := conn.Query(ctx, standbysSQL(recovering))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []standbyRow
	for rows.Next() {
		var s standbyRow
		if err := rows.Scan(&s.pid, &s.name, &s.client, &s.state, &s.syncKind, &s.priority,
			&s.sent, &s.write, &s.flush, &s.replay, &s.behind,
			&s.writeLag, &s.flushLag, &s.replLag, &s.slot); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// invalidationSQL names why a slot can no longer be used, as far as this
// server says. 17 states a reason; 16 only that the slot conflicts with
// recovery, which is a logical slot on a standby; before that wal_status =
// 'lost' is all there is, and it is read separately.
func invalidationSQL(versionNum int) string {
	switch {
	case versionNum >= 170000:
		return "r.invalidation_reason"
	case versionNum >= 160000:
		return "case when r.conflicting then 'conflict with recovery' end"
	}
	return "null::text"
}

func slotsSQL(server serverFacts) string {
	return `
		select r.slot_name, r.slot_type, coalesce(r.database, ''), r.temporary, r.active, s.application_name,
		       r.restart_lsn::text, ` + distance("cur.lsn", "r.restart_lsn") + `,
		       r.wal_status, r.safe_wal_size, ` + invalidationSQL(server.versionNum) + `
		from (select ` + ref(server.recovering) + ` as lsn) cur, pg_replication_slots r
		left join pg_stat_replication s on s.pid = r.active_pid
		order by r.slot_name`
}

func readSlots(ctx context.Context, conn *pgx.Conn, server serverFacts) ([]slotRow, error) {
	rows, err := conn.Query(ctx, slotsSQL(server))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []slotRow
	for rows.Next() {
		var s slotRow
		if err := rows.Scan(&s.name, &s.kind, &s.database, &s.temporary, &s.active, &s.holder,
			&s.restart, &s.retained, &s.status, &s.safeLeft, &s.invalid); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
