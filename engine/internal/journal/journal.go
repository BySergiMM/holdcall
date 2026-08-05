// Package journal is the local record of what agents did. SQLite is the source
// of truth; the Supabase mirror is a copy of these rows and never the reverse.
//
// The schema deliberately matches supabase/migrations: same table names, same
// columns, same nim_ prefix. Arguments are stored as a digest and never in
// full, so the record can be synced without anything sensitive leaving the
// machine.
package journal

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
create table if not exists nim_sessions (
    id          text primary key,
    machine_id  text    not null,
    client      text,
    target      text    not null,
    started_at  text    not null,
    ended_at    text
);

create table if not exists nim_calls (
    id            text primary key,
    session_id    text    not null references nim_sessions(id) on delete cascade,
    seq           integer not null,
    tool          text    not null,
    params_digest text    not null,
    decision      text    not null,
    ok            integer,
    duration_ms   integer,
    occurred_at   text    not null,
    unique (session_id, seq)
);

create index if not exists nim_calls_session_seq_idx on nim_calls (session_id, seq);
create index if not exists nim_calls_occurred_at_idx on nim_calls (occurred_at desc);
`

type Journal struct{ db *sql.DB }

// Open prepares the database. WAL lets the dashboard read while the daemon
// writes, which matters as soon as anything else looks at this file.
func Open(path string) (*Journal, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{
		`pragma journal_mode=wal`,
		`pragma busy_timeout=5000`,
		`pragma foreign_keys=on`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Journal{db: db}, nil
}

func (j *Journal) Close() error { return j.db.Close() }

type Session struct {
	ID        string
	MachineID string
	Client    string
	Target    string
	StartedAt time.Time
}

type Call struct {
	ID           string
	SessionID    string
	Seq          int
	Tool         string
	ParamsDigest string
	Decision     string
	OK           *bool
	DurationMS   *int
	OccurredAt   time.Time
}

func (j *Journal) StartSession(s Session) error {
	_, err := j.db.Exec(
		`insert into nim_sessions (id, machine_id, client, target, started_at) values (?, ?, ?, ?, ?)`,
		s.ID, s.MachineID, nullable(s.Client), s.Target, s.StartedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (j *Journal) EndSession(id string, at time.Time) error {
	_, err := j.db.Exec(
		`update nim_sessions set ended_at = ? where id = ? and ended_at is null`,
		at.UTC().Format(time.RFC3339Nano), id,
	)
	return err
}

// RecordCall is idempotent on (session_id, seq): a shim that retries after a
// dropped connection cannot produce duplicates.
func (j *Journal) RecordCall(c Call) error {
	_, err := j.db.Exec(
		`insert into nim_calls
		   (id, session_id, seq, tool, params_digest, decision, ok, duration_ms, occurred_at)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 on conflict (session_id, seq) do update set
		   ok          = excluded.ok,
		   duration_ms = excluded.duration_ms`,
		c.ID, c.SessionID, c.Seq, c.Tool, c.ParamsDigest, c.Decision,
		c.OK, c.DurationMS, c.OccurredAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

// CountCalls is used by tests and by `nim status`.
func (j *Journal) CountCalls() (int, error) {
	var n int
	err := j.db.QueryRow(`select count(*) from nim_calls`).Scan(&n)
	return n, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
