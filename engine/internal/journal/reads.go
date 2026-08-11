package journal

import "database/sql"

// LossReport is what the journal can work out about its own gaps -- which is
// less than all of them.
//
// An event passes through three states, and only the third leaves a record:
//
//  1. observed by the shim, as a message went past;
//  2. accepted by the daemon, over a one-way socket that carries no answer;
//  3. written to the journal.
//
// Losses between 1 and 2 leave a shape here, because the shim keeps counting
// while it fails: a session that never ends, and a per-session seq that skips.
// Those are what this reports, and the shim also says so on stderr as it
// happens.
//
// Losses between 2 and 3 leave nothing, with one exception. If the daemon
// accepts an event and then cannot write it, the shim is never told and the
// journal has no trace, so this report cannot tell that case apart from one
// where no event was ever sent. The same is true of events lost at the very end
// of a session that still closes normally: with the highest seq gone too,
// nothing is left to be missing from.
//
// The exception is call.request, and only call.request. From M2 the shim waits
// for the daemon's decision before forwarding a call, and the daemon answers
// only after the entry is written -- so a request that was accepted and not
// written is refused rather than silently dropped. Sessions, outcomes and
// anomalies are still one-way and still have the blind spot.
//
// So: losses that leave evidence are reported, plus call.request losses, which
// now cannot happen without the call being denied. The rest are not reported and
// cannot be. docs/journal-format.md sets the same out at more length.
type LossReport struct {
	UnfinishedSessions int // includes any session running right now
	SessionsWithGaps   int
	MissingCallEntries int
}

func (j *Journal) Loss() (LossReport, error) {
	var r LossReport

	err := j.db.QueryRow(`
		select count(*) from nim_journal s
		 where s.kind = 'session.start'
		   and not exists (select 1 from nim_journal e
		                    where e.kind = 'session.end' and e.session_id = s.session_id)
	`).Scan(&r.UnfinishedSessions)
	if err != nil {
		return r, err
	}

	// seq is assigned per session, starting at 1 and increasing by one. Fewer
	// request entries than the highest seq means the difference was dropped.
	var missing sql.NullInt64
	err = j.db.QueryRow(`
		select count(*), coalesce(sum(hi - n), 0) from (
		    select max(seq) as hi, count(*) as n
		      from nim_journal
		     where kind = 'call.request'
		     group by session_id
		    having count(*) < max(seq)
		)
	`).Scan(&r.SessionsWithGaps, &missing)
	if err != nil {
		return r, err
	}
	r.MissingCallEntries = int(missing.Int64)
	return r, nil
}

// Anomalies counts the messages NIM saw but could not account for: batches that
// slip past inspection, JSON it could not parse, more than one value in a
// frame, and reused in-flight ids. None of them are blocked in this milestone.
// Counting them turns a blind spot into a number.
func (j *Journal) Anomalies() (map[string]int, error) {
	rows, err := j.db.Query(
		`select anomaly, count(*) from nim_journal where kind = ? group by anomaly order by anomaly`,
		KindAnomaly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var name sql.NullString
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name.String] = n
	}
	return out, rows.Err()
}

// MaxEntriesPerRead caps one read, so a caller asking for everything gets a
// page instead of the whole journal in memory. Reads stay short on purpose:
// a long-running read transaction keeps SQLite from checkpointing the WAL,
// which would grow the file while the daemon is writing to it.
const MaxEntriesPerRead = 1000

// EntriesSince returns entries after a chain_seq, oldest first.
//
// This is the whole of the streaming mechanism. chain_seq is monotonic and
// gapless, so it is already a cursor: a reader remembers the last one it saw
// and asks for what came after. Nothing needs to be published, subscribed to or
// kept in step -- the journal is the stream, and a reader that stops and comes
// back later resumes exactly where it was.
//
// A limit of zero or less means MaxEntriesPerRead.
func (j *Journal) EntriesSince(since int64, limit int) ([]Entry, error) {
	if limit <= 0 || limit > MaxEntriesPerRead {
		limit = MaxEntriesPerRead
	}
	rows, err := j.db.Query(
		`select chain_seq, schema_version, kind, session_id, seq, connector, tool,
		        params_digest, decision, ok, duration_ms, anomaly, occurred_at,
		        machine_id, client, protocol_version, prev_hash, hash
		   from nim_journal
		  where chain_seq > ?
		  order by chain_seq
		  limit ?`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SessionRow is one session with what the journal knows about it.
//
// EndedAt is absent for a session with no session.end. That covers a session
// running right now and one whose daemon died, and the journal cannot tell them
// apart -- so neither can anything built on this.
type SessionRow struct {
	ChainSeq      int64
	ID            string
	MachineID     *string
	Client        *string
	Connector     *string
	StartedAt     string
	EndedAt       *string
	CallsRecorded int
	// Denied is how many of those were refused. It is counted separately because
	// a refused call never gets an outcome, so without it CallsRecorded and
	// Outcomes differ for two unrelated reasons and a reader cannot tell which.
	Denied    int
	Outcomes  int
	Anomalies int
}

// Sessions reads the sessions view, newest first by chain_seq.
func (j *Journal) Sessions(limit int) ([]SessionRow, error) {
	if limit <= 0 || limit > MaxEntriesPerRead {
		limit = MaxEntriesPerRead
	}
	rows, err := j.db.Query(`
		select s.chain_seq, s.id, s.machine_id, s.client, s.connector, s.started_at, s.ended_at,
		       (select count(*) from nim_journal r
		         where r.kind = 'call.request' and r.session_id = s.id),
		       (select count(*) from nim_journal d
		         where d.kind = 'call.request' and d.session_id = s.id
		           and d.decision in ('deny','rejected')),
		       (select count(*) from nim_journal o
		         where o.kind = 'call.outcome' and o.session_id = s.id),
		       (select count(*) from nim_journal a
		         where a.kind = 'anomaly'      and a.session_id = s.id)
		  from nim_sessions s
		 order by s.chain_seq desc
		 limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionRow
	for rows.Next() {
		var s SessionRow
		var machineID, client, connector, endedAt sql.NullString
		if err := rows.Scan(&s.ChainSeq, &s.ID, &machineID, &client, &connector,
			&s.StartedAt, &endedAt, &s.CallsRecorded, &s.Denied, &s.Outcomes, &s.Anomalies); err != nil {
			return nil, err
		}
		s.MachineID = nullable(machineID)
		s.Client = nullable(client)
		s.Connector = nullable(connector)
		s.EndedAt = nullable(endedAt)
		out = append(out, s)
	}
	return out, rows.Err()
}

// SessionEntries returns everything recorded for one session, in journal order.
func (j *Journal) SessionEntries(id string, limit int) ([]Entry, error) {
	if limit <= 0 || limit > MaxEntriesPerRead {
		limit = MaxEntriesPerRead
	}
	rows, err := j.db.Query(
		`select chain_seq, schema_version, kind, session_id, seq, connector, tool,
		        params_digest, decision, ok, duration_ms, anomaly, occurred_at,
		        machine_id, client, protocol_version, prev_hash, hash
		   from nim_journal
		  where session_id = ?
		  order by chain_seq
		  limit ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CallRow is one line of `nim log`.
type CallRow struct {
	ChainSeq   int64
	OccurredAt string
	Connector  string
	Tool       string
	Decision   string
	// HasOutcome says whether the call finished. OK says how it finished, and is
	// meaningless without this: a call with no outcome and a call whose outcome
	// carried nothing would otherwise look the same.
	HasOutcome bool
	OK         *bool
	DurationMS *int64
}

// RecentCalls reads the nim_calls view, newest first.
//
// Ordered by chain_seq, which is the order the journal was written in. Ordering
// by occurred_at would let a wrong clock -- or an entry someone wrote -- decide
// what "recent" means, and a timestamp is a value an entry carries rather than
// a fact about the journal.
func (j *Journal) RecentCalls(limit int) ([]CallRow, error) {
	rows, err := j.db.Query(
		`select chain_seq, occurred_at, connector, tool, decision, has_outcome, ok, duration_ms
		   from nim_calls order by chain_seq desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CallRow
	for rows.Next() {
		var c CallRow
		// Nullable, all three of them. A tools/call with no params.name records
		// no tool, a session that reported no connector records none, and
		// scanning either into a string turns `nim log` into an error message
		// about SQL. The decision is null only in journals older than M2.
		var connector, tool, decision sql.NullString
		var ok sql.NullBool
		var duration sql.NullInt64
		if err := rows.Scan(&c.ChainSeq, &c.OccurredAt, &connector, &tool, &decision,
			&c.HasOutcome, &ok, &duration); err != nil {
			return nil, err
		}
		c.Connector, c.Tool, c.Decision = connector.String, tool.String, decision.String
		if ok.Valid {
			c.OK = &ok.Bool
		}
		if duration.Valid {
			c.DurationMS = &duration.Int64
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ToolTotals is the summary line of `nim log`: how often each tool was seen.
func (j *Journal) ToolTotals() (map[string]int, error) {
	rows, err := j.db.Query(
		`select tool, count(*) from nim_journal where kind = ? group by tool order by count(*) desc`,
		KindCallRequest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var tool sql.NullString
		var n int
		if err := rows.Scan(&tool, &n); err != nil {
			return nil, err
		}
		out[tool.String] = n
	}
	return out, rows.Err()
}
