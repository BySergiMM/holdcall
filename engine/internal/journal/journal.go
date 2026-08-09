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
//
// journal_mode and foreign_keys are set through the DSN, not a one-time Exec
// after Open: foreign_keys and busy_timeout are per-connection pragmas in
// SQLite, and database/sql's pool can open more than one physical connection
// under concurrent load (one handle() goroutine per shim connection here) --
// an Exec right after Open only ever reaches the first. A connection opened
// later without foreign_keys on would silently stop enforcing the reference
// from nim_calls to nim_sessions. journal_mode itself is persisted in the
// database file the first time it is set, so it does not strictly need to be
// per-connection, but there is no reason to special-case it.
func Open(path string) (*Journal, error) {
	dsn := path + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
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
// dropped connection cannot produce duplicates. The update is monotonic --
// coalesce keeps whatever outcome is already on the row when the incoming
// report has none -- so a retried request-time report (no outcome yet)
// arriving after the response-time report already landed cannot erase it.
func (j *Journal) RecordCall(c Call) error {
	_, err := j.db.Exec(
		`insert into nim_calls
		   (id, session_id, seq, tool, params_digest, decision, ok, duration_ms, occurred_at)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 on conflict (session_id, seq) do update set
		   ok          = coalesce(excluded.ok, nim_calls.ok),
		   duration_ms = coalesce(excluded.duration_ms, nim_calls.duration_ms)`,
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
