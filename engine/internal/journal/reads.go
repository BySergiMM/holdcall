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
// Losses between 2 and 3 leave nothing. If the daemon accepts an event and then
// cannot write it, the shim is never told -- the protocol has no reply -- and
// the journal has no trace, so this report cannot tell that case apart from one
// where no event was ever sent. The same is true of events lost at the very end
// of a session that still closes normally: with the highest seq gone too,
// nothing is left to be missing from.
//
// So: losses that leave evidence are reported. Losses that leave none are not,
// and cannot be, until the reporter can be told whether its event was written.
// That needs a reply, which is the M2 protocol. docs/journal-format.md sets the
// same out at more length.
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

// CallRow is one line of `nim log`.
type CallRow struct {
	ChainSeq   int64
	OccurredAt string
	Connector  string
	Tool       string
	Decision   string
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
		`select chain_seq, occurred_at, connector, tool, decision, ok, duration_ms
		   from nim_calls order by chain_seq desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CallRow
	for rows.Next() {
		var c CallRow
		var ok sql.NullBool
		var duration sql.NullInt64
		if err := rows.Scan(&c.ChainSeq, &c.OccurredAt, &c.Connector, &c.Tool, &c.Decision, &ok, &duration); err != nil {
			return nil, err
		}
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
