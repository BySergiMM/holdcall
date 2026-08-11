// Package journal is the local record of what agents did. SQLite is the source
// of truth; the Supabase mirror is a copy of these rows and never the reverse.
//
// The record is append-only and hash-chained. Append-only in fact, not by
// convention: a call produces two entries, one when it is seen and one when it
// finishes, because a row that is updated cannot be part of a chain.
//
// Arguments are stored as a digest and never in full, so the record can be
// synced without anything sensitive leaving the machine.
//
// docs/journal-format.md is the normative definition of the encoding and the
// chain, including -- importantly -- what the chain does not protect against.
package journal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Entry kinds. A call is two entries, not one mutable row.
const (
	KindSessionStart = "session.start"
	KindCallRequest  = "call.request"
	KindCallOutcome  = "call.outcome"
	KindSessionEnd   = "session.end"
	KindAnomaly      = "anomaly"
)

// The decisions an entry can carry.
//
// DecisionObserved is what M1.5 wrote, when the relay recorded calls without
// authorizing them; journals from that milestone are full of it, so everything
// reading the record still has to understand it.
//
// From M2 a call.request carries what the daemon actually decided. Approved and
// rejected belong to human approval and are not written yet -- they are here so
// the vocabulary a reader must handle is stated in one place rather than
// scattered as literals.
const (
	DecisionObserved = "observed"
	DecisionAllow    = "allow"
	DecisionDeny     = "deny"
	DecisionApproved = "approved"
	DecisionRejected = "rejected"
)

const schema = `
create table if not exists nim_journal (
    chain_seq        integer primary key,
    schema_version   integer not null,
    kind             text    not null check (kind in
                       ('session.start','call.request','call.outcome','session.end','anomaly')),
    session_id       text    not null,
    seq              integer,
    connector        text,
    tool             text,
    params_digest    text,
    decision         text    check (decision is null or decision in
                       ('observed','allow','deny','approved','rejected')),
    ok               integer,
    duration_ms      integer,
    anomaly          text    check (anomaly is null or anomaly in
                       ('batch','malformed_json','framing','duplicate_id')),
    occurred_at      text    not null,
    machine_id       text,
    client           text,
    protocol_version text,
    prev_hash        text    not null,
    hash             text    not null
);

create index if not exists nim_journal_occurred_at_idx on nim_journal (occurred_at desc);
create index if not exists nim_journal_kind_idx        on nim_journal (kind);

-- One entry per (session, seq, kind). The chain covers each entry's contents;
-- this covers the shape of the set, which is a different thing and the one gap
-- detection rests on: that check compares how many requests a session has
-- against the highest seq it reached, and a repeated seq makes a real hole in
-- the sequence add up.
--
-- seq is null on session and anomaly entries, and SQLite treats nulls as
-- distinct, so those stay unconstrained -- a session can have as many anomalies
-- as it produces.
create unique index if not exists nim_journal_entry_idx on nim_journal (session_id, seq, kind);

-- Connector metadata. Not part of the chain, and deliberately so: the chain
-- records what happened, and this is configuration that decides what may
-- happen. Mixing the two would put a mutable row inside an append-only record.
--
-- No secret column. The credential value lives in the OS credential store (see
-- internal/credential) and never here. This holds only the env var name it is
-- injected under and the argv of the one server allowed to receive it.
--
-- command is an authorization input, which is why it is in SQLite rather than
-- config.toml: the standing decision is that SQLite is the only thing an
-- authorization decision reads, and the daemon is its only writer.
create table if not exists nim_connectors (
    target      text primary key,
    env_key     text    not null,
    command     text,
    updated_at  text    not null
);

-- Enrolled agents. Configuration, like nim_connectors and for the same reason:
-- the chain records what happened, this decides what may happen, and a mutable
-- row has no business inside an append-only record.
--
-- An agent is identified by the executable its process is running, as the
-- kernel reports it -- exec_dev and exec_ino, never a path. A path is text and
-- the file at one belongs to whoever owns the directory; identifying by path
-- is the mistake internal/peer exists to avoid.
--
-- exec_path is kept for diagnostics only: to show the operator what they
-- enrolled and to let 'nim agent list' say when the file at that path is no
-- longer the enrolled one. It is never consulted to decide whether a running
-- process is this agent.
--
-- No uid column. Nim runs entirely as one OS user today, so pinning an agent
-- to one would add a column nothing reads. It can be added when there is a
-- reason.
create table if not exists nim_agents (
    name        text primary key,
    exec_dev    integer not null,
    exec_ino    integer not null,
    exec_path   text    not null,
    enrolled_at text    not null
);
`

// migrations are additive statements applied after schema, each of which must
// be safe to run against a database that already has it applied.
//
// SQLite has no "add column if not exists", and create-table-if-not-exists does
// nothing to a table that already exists, so a column added to an install that
// predates it needs this. A duplicate-column error is the expected result on
// every run after the first and is not a failure.
var migrations = []string{
	`alter table nim_connectors add column command text`,
}

func migrate(db *sql.DB) error {
	for _, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migration %q: %w", stmt, err)
		}
	}
	return nil
}

// Views keep the shape the mirror already expects, without letting anything
// mutable back into the journal.
//
// Both expose chain_seq, because that is the journal's own order. occurred_at
// is a value an entry carries, and a value can be wrong.
//
// A view left over from an older build would silently keep answering with the
// old columns, so syncViews replaces one whose definition has drifted -- but
// only then. Recreating them unconditionally would make every open take a write
// lock on the schema, which is a poor thing for `nim status` to do to a daemon
// that is busy recording.
var views = []struct{ name, ddl string }{
	{"nim_sessions", `create view nim_sessions as
select
    s.chain_seq       as chain_seq,
    s.session_id      as id,
    s.machine_id      as machine_id,
    s.client          as client,
    s.connector       as connector,
    s.occurred_at     as started_at,
    (select e.occurred_at from nim_journal e
      where e.kind = 'session.end' and e.session_id = s.session_id
      order by e.chain_seq limit 1) as ended_at
from nim_journal s
where s.kind = 'session.start'`},

	// has_outcome is its own column rather than something a reader infers from
	// ok being non-null. Those two answer different questions -- whether the call
	// finished, and whether it succeeded -- and they only happen to agree because
	// every outcome written so far carries ok. A denied call has no outcome and
	// never will, so telling "no outcome" apart from "outcome says nothing"
	// decides whether it reads as refused or as still running.
	{"nim_calls", `create view nim_calls as
select
    r.chain_seq     as chain_seq,
    r.session_id || '-' || r.seq as id,
    r.session_id    as session_id,
    r.seq           as seq,
    s.connector     as connector,
    r.tool          as tool,
    r.params_digest as params_digest,
    r.decision      as decision,
    o.chain_seq is not null as has_outcome,
    o.ok            as ok,
    o.duration_ms   as duration_ms,
    r.occurred_at   as occurred_at
from nim_journal r
join nim_journal s
  on s.kind = 'session.start' and s.session_id = r.session_id
left join nim_journal o
  on o.kind = 'call.outcome' and o.session_id = r.session_id and o.seq = r.seq
where r.kind = 'call.request'`},
}

// syncViews recreates only the views whose stored definition no longer matches
// this build's. In the steady state it is a read, so opening the journal to
// look at it does not contend with the daemon writing to it.
//
// The drop and the create are one transaction, and the check is repeated
// inside it. Without that, two handles opening the same database at once both
// see the view as absent or drifted, both drop it, and then the second create
// fails with "view nim_calls already exists" -- which takes down whichever
// process was opening the journal. Found by CI rather than locally: it needs
// two opens of one file to interleave, which a laptop run happened not to do.
//
// The DSN sets _txlock=immediate, so Begin takes the write lock up front
// rather than discovering it is needed halfway through -- the same reason
// Append does, and the same failure (BUSY_SNAPSHOT, which busy_timeout does
// not retry) if it did not.
func syncViews(db *sql.DB) error {
	for _, v := range views {
		if current, err := viewMatches(db, v.name, v.ddl); err != nil {
			return err
		} else if current {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("replacing view %s: %w", v.name, err)
		}
		// Re-check under the write lock: another handle may have done this
		// between the read above and the lock being granted, and recreating a
		// view that is already correct is how the collision happened.
		var stored sql.NullString
		err = tx.QueryRow(
			`select sql from sqlite_master where type = 'view' and name = ?`, v.name).Scan(&stored)
		if err != nil && err != sql.ErrNoRows {
			tx.Rollback()
			return err
		}
		if err == nil && stored.Valid && stored.String == v.ddl {
			tx.Rollback()
			continue
		}
		if _, err := tx.Exec(`drop view if exists ` + v.name); err != nil {
			tx.Rollback()
			return fmt.Errorf("replacing view %s: %w", v.name, err)
		}
		if _, err := tx.Exec(v.ddl); err != nil {
			tx.Rollback()
			return fmt.Errorf("creating view %s: %w", v.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("replacing view %s: %w", v.name, err)
		}
	}
	return nil
}

// viewMatches reports whether the stored definition of name is already ddl.
// Split out so the common case -- every view current -- stays a plain read
// that takes no write lock at all.
func viewMatches(db *sql.DB, name, ddl string) (bool, error) {
	var stored sql.NullString
	err := db.QueryRow(
		`select sql from sqlite_master where type = 'view' and name = ?`, name).Scan(&stored)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return stored.Valid && stored.String == ddl, nil
}

// Entry is one immutable record. Nullable fields are pointers so that an absent
// value and an empty one encode differently, which the chain depends on.
type Entry struct {
	ChainSeq        int64
	SchemaVersion   int64
	Kind            string
	SessionID       string
	Seq             *int64
	Connector       *string
	Tool            *string
	ParamsDigest    *string
	Decision        *string
	OK              *bool
	DurationMS      *int64
	Anomaly         *string
	OccurredAt      string
	MachineID       *string
	Client          *string
	ProtocolVersion *string
	PrevHash        string
	Hash            string
}

type Journal struct {
	db *sql.DB

	// genesis seeds the chain. seedKnown is false when the install's machine-id
	// could not be read: entries can still be appended and checked against each
	// other, but the first one cannot be checked against anything.
	genesis   string
	seedKnown bool

	// readOnly journals refuse to write before SQLite gets the chance to. The
	// database would reject it anyway; refusing here turns an obscure driver
	// error into a statement about what this handle is for.
	readOnly bool

	// One writer, one chain. The daemon serves each shim on its own goroutine,
	// so without this two appends could read the same head and both claim the
	// next chain_seq.
	mu sync.Mutex
}

// Open prepares the database.
//
// machineID seeds the chain; pass "" when the install's identifier could not be
// read. That is a recoverable state, not a broken journal: verification says so
// instead of reporting the missing seed as a mismatch.
func Open(path, machineID string) (*Journal, error) {
	// Pragmas go in the DSN so every pooled connection gets them. Issued as
	// statements they would apply to whichever single connection happened to
	// serve the call, leaving the rest on defaults -- journal_mode survives
	// because it is stored in the file, but busy_timeout and foreign_keys do
	// not.
	//
	// _txlock=immediate takes the write lock when a transaction opens. Append
	// reads the head and then inserts, and a deferred transaction only asks for
	// write access at the insert: if anything else wrote in between, SQLite
	// fails that upgrade at once and busy_timeout does not apply to it, because
	// waiting could not help -- the snapshot the read was based on is already
	// stale. Asking up front turns that into an ordinary wait.
	dsn := "file:" + path +
		"?_txlock=immediate" +
		"&_pragma=journal_mode(wal)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := retireLegacyTables(db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := syncViews(db); err != nil {
		db.Close()
		return nil, err
	}

	j := &Journal{db: db, seedKnown: machineID != ""}
	if j.seedKnown {
		j.genesis = genesisHash(machineID)
	}
	return j, nil
}

// OpenReadOnly opens an existing journal for reading and nothing else.
//
// SQLite itself refuses writes on this handle, so a reader cannot alter the
// record even by mistake -- which matters because the things that will read a
// journal (a console, a report, someone looking at a machine after the fact)
// have no business changing it, and because Open creates and migrates as a side
// effect of being called.
//
// It does not create, migrate or repair anything. A journal that has never been
// written is an error here rather than an empty one brought into existence: the
// daemon owns that.
//
// machineID seeds the chain and may be "" when it could not be read, exactly as
// in Open.
func OpenReadOnly(path, machineID string) (*Journal, error) {
	// No _txlock and no journal_mode: neither has meaning without writes, and
	// setting journal_mode on a read-only handle would itself be one.
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	var name string
	err = db.QueryRow(
		`select name from sqlite_master where type = 'table' and name = 'nim_journal'`).Scan(&name)
	if err == sql.ErrNoRows {
		db.Close()
		return nil, fmt.Errorf(
			"%s has no journal in it yet: the daemon creates one the first time it records something", path)
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	j := &Journal{db: db, readOnly: true, seedKnown: machineID != ""}
	if j.seedKnown {
		j.genesis = genesisHash(machineID)
	}
	return j, nil
}

// ReadOnly reports whether this handle can write.
func (j *Journal) ReadOnly() bool { return j.readOnly }

var errReadOnly = errors.New("this journal was opened for reading only")

// AdoptSeed records the seed for a journal that has none yet.
//
// It refuses once there are entries: those were chained from a seed this one is
// not, and quietly adopting a different one would turn every existing entry
// into an apparent forgery.
func (j *Journal) AdoptSeed(machineID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.readOnly {
		return errReadOnly
	}
	var n int64
	if err := j.db.QueryRow(`select count(*) from nim_journal`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("journal already has %d entries chained from another seed", n)
	}
	j.genesis = genesisHash(machineID)
	j.seedKnown = true
	return nil
}

// SeedKnown reports whether the chain can be checked back to its start.
func (j *Journal) SeedKnown() bool { return j.seedKnown }

// retireLegacyTables moves a pre-chain journal aside rather than deleting it.
//
// The old nim_calls was a table whose rows were updated in place, so its
// contents cannot be folded into the chain: hashing them now would assert an
// integrity they never had. They are kept under a suffixed name, outside the
// chain, and the names are freed for the views.
func retireLegacyTables(db *sql.DB) error {
	for _, name := range []string{"nim_calls", "nim_sessions"} {
		var kind string
		err := db.QueryRow(
			`select type from sqlite_master where name = ? limit 1`, name).Scan(&kind)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		if kind != "table" {
			continue // already a view from an earlier run
		}
		if _, err := db.Exec(fmt.Sprintf(`alter table %s rename to %s_m1`, name, name)); err != nil {
			return fmt.Errorf("retiring legacy %s: %w", name, err)
		}
	}
	return nil
}

func (j *Journal) Close() error { return j.db.Close() }

// Append writes one entry and links it to the chain. It is the only way rows
// enter the journal, and nothing ever updates them afterwards.
func (j *Journal) Append(e Entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.readOnly {
		return errReadOnly
	}
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var headSeq sql.NullInt64
	var headHash sql.NullString
	err = tx.QueryRow(
		`select chain_seq, hash from nim_journal order by chain_seq desc limit 1`,
	).Scan(&headSeq, &headHash)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	e.ChainSeq, e.PrevHash = 1, j.genesis
	if headSeq.Valid {
		e.ChainSeq, e.PrevHash = headSeq.Int64+1, headHash.String
	} else if !j.seedKnown {
		// Starting a chain needs a seed. Later entries do not: they link to the
		// entry before them, so a journal already under way keeps recording
		// correctly even when the identifier has gone missing.
		return fmt.Errorf("cannot start a journal without an install identifier")
	}
	e.SchemaVersion = SchemaVersion1
	e.Hash = chainHash(e.PrevHash, canonicalEncodeV1(e))

	_, err = tx.Exec(
		`insert into nim_journal
		   (chain_seq, schema_version, kind, session_id, seq, connector, tool,
		    params_digest, decision, ok, duration_ms, anomaly, occurred_at,
		    machine_id, client, protocol_version, prev_hash, hash)
		 values (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ChainSeq, e.SchemaVersion, e.Kind, e.SessionID, e.Seq, e.Connector, e.Tool,
		e.ParamsDigest, e.Decision, e.OK, e.DurationMS, e.Anomaly, e.OccurredAt,
		e.MachineID, e.Client, e.ProtocolVersion, e.PrevHash, e.Hash,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Head reports the length of the chain and its last hash. Both are printed by
// `nim status`: recording them somewhere else is the only way to notice a chain
// that has been rewritten from the genesis.
func (j *Journal) Head() (length int64, hash string, err error) {
	var seq sql.NullInt64
	var h sql.NullString
	err = j.db.QueryRow(
		`select chain_seq, hash from nim_journal order by chain_seq desc limit 1`,
	).Scan(&seq, &h)
	if err == sql.ErrNoRows {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	return seq.Int64, h.String, nil
}

// CountCalls is the number of tool calls seen. Used by `nim status`.
func (j *Journal) CountCalls() (int, error) {
	var n int
	err := j.db.QueryRow(
		`select count(*) from nim_journal where kind = ?`, KindCallRequest).Scan(&n)
	return n, err
}

// Connector is non-secret connector metadata: which env var a target's
// credential is injected under, and the one command that credential may be
// injected into. The credential value itself is never here -- see
// internal/credential.
//
// Command is nil for a connector registered before commands existed. That is
// not a connector with "no restriction": it is one whose authorized command is
// unknown, and the daemon refuses to release its secret at all until it is
// registered again. See daemon.handleCredentialGet.
type Connector struct {
	Target    string
	EnvKey    string
	Command   []string
	UpdatedAt time.Time
}

// SetConnector is an upsert: setting a connector that already exists replaces
// its env key and command, exactly as the credential store's own Set replaces
// the secret.
func (j *Journal) SetConnector(target, envKey string, command []string, at time.Time) error {
	if j.readOnly {
		return errReadOnly
	}
	encoded, err := encodeCommand(command)
	if err != nil {
		return err
	}
	_, err = j.db.Exec(
		`insert into nim_connectors (target, env_key, command, updated_at) values (?, ?, ?, ?)
		 on conflict (target) do update set
		   env_key    = excluded.env_key,
		   command    = excluded.command,
		   updated_at = excluded.updated_at`,
		target, envKey, encoded, at.UTC().Format(time.RFC3339Nano),
	)
	return err
}

// encodeCommand stores argv as JSON rather than a joined string: a command
// argument can contain a space, and re-splitting on one would silently change
// what gets spawned. An empty command is stored as NULL, distinctly from an
// empty argv.
func encodeCommand(command []string) (any, error) {
	if len(command) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("encoding connector command: %w", err)
	}
	return string(b), nil
}

func decodeCommand(s sql.NullString) ([]string, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s.String), &out); err != nil {
		return nil, fmt.Errorf("decoding connector command: %w", err)
	}
	return out, nil
}

// ConnectorInfo looks up one target. found is false, with no error, if no
// connector is configured for it -- the ordinary case for most targets, not a
// failure. See docs/decisions/0001-failure-behaviour.md.
func (j *Journal) ConnectorInfo(target string) (c Connector, found bool, err error) {
	var updatedAt string
	var command sql.NullString
	err = j.db.QueryRow(
		`select target, env_key, command, updated_at from nim_connectors where target = ?`, target).
		Scan(&c.Target, &c.EnvKey, &command, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Connector{}, false, nil
	}
	if err != nil {
		return Connector{}, false, err
	}
	if c.Command, err = decodeCommand(command); err != nil {
		return Connector{}, false, err
	}
	c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return c, true, nil
}

// ListConnectors returns every configured connector, ordered by target.
func (j *Journal) ListConnectors() ([]Connector, error) {
	rows, err := j.db.Query(
		`select target, env_key, command, updated_at from nim_connectors order by target`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Connector
	for rows.Next() {
		var c Connector
		var updatedAt string
		var command sql.NullString
		if err := rows.Scan(&c.Target, &c.EnvKey, &command, &updatedAt); err != nil {
			return nil, err
		}
		if c.Command, err = decodeCommand(command); err != nil {
			return nil, err
		}
		c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteConnector removes a target's metadata. It is not an error to remove one
// that was never configured -- callers that only want removal to be idempotent
// do not need to check first.
func (j *Journal) DeleteConnector(target string) error {
	if j.readOnly {
		return errReadOnly
	}
	_, err := j.db.Exec(`delete from nim_connectors where target = ?`, target)
	return err
}

// Agent is an enrolled client program: the thing that spawns a relay.
//
// ExecDev and ExecIno are the identity, taken from the kernel at enrolment.
// ExecPath is what the operator typed, kept so the enrolment can be explained
// and repeated -- it is never what an identity is matched on.
type Agent struct {
	Name       string
	ExecDev    uint64
	ExecIno    uint64
	ExecPath   string
	EnrolledAt time.Time
}

// SetAgent enrols an agent, replacing any enrolment under the same name.
//
// Replacing is deliberate and is how re-enrolment works. It is needed more
// often than it looks: an inode survives, but a device number can change when
// filesystems are mounted differently across a reboot, and an application that
// updates itself becomes a different file. Both leave an enrolment that no
// longer matches anything, which must be repairable without deleting and
// re-adding.
func (j *Journal) SetAgent(a Agent) error {
	if j.readOnly {
		return errReadOnly
	}
	_, err := j.db.Exec(
		`insert into nim_agents (name, exec_dev, exec_ino, exec_path, enrolled_at)
		 values (?, ?, ?, ?, ?)
		 on conflict (name) do update set
		   exec_dev    = excluded.exec_dev,
		   exec_ino    = excluded.exec_ino,
		   exec_path   = excluded.exec_path,
		   enrolled_at = excluded.enrolled_at`,
		a.Name, a.ExecDev, a.ExecIno, a.ExecPath,
		a.EnrolledAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

// ListAgents returns every enrolment, ordered by name.
func (j *Journal) ListAgents() ([]Agent, error) {
	rows, err := j.db.Query(
		`select name, exec_dev, exec_ino, exec_path, enrolled_at from nim_agents order by name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		var a Agent
		var enrolledAt string
		if err := rows.Scan(&a.Name, &a.ExecDev, &a.ExecIno, &a.ExecPath, &enrolledAt); err != nil {
			return nil, err
		}
		a.EnrolledAt, _ = time.Parse(time.RFC3339Nano, enrolledAt)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAgent removes an enrolment. Removing one that was never enrolled is
// not an error, so a caller that only wants it gone need not check first.
func (j *Journal) DeleteAgent(name string) error {
	if j.readOnly {
		return errReadOnly
	}
	_, err := j.db.Exec(`delete from nim_agents where name = ?`, name)
	return err
}
