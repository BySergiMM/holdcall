package journal

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// journalTableM2 is nim_journal exactly as the M2 build created it: five
// kinds, four anomalies, and no agent column. Kept as text rather than derived
// from anything current, because the point is to open a file an older build
// left behind.
const journalTableM2 = `create table nim_journal (
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
)`

// anM2Journal writes a database the way the M2 build would have left it: the
// old table, an old view over it, and two v1 entries chained from the genesis.
// It returns the entries as written, so what comes out of the rebuild can be
// held to them byte for byte.
func anM2Journal(t *testing.T, path string) []Entry {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	for _, stmt := range []string{
		journalTableM2,
		`create unique index nim_journal_entry_idx on nim_journal (session_id, seq, kind)`,
		`create view nim_calls as select chain_seq from nim_journal where kind = 'call.request'`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	entries := []Entry{
		{ChainSeq: 1, SchemaVersion: SchemaVersion1, Kind: KindSessionStart, SessionID: "old",
			Connector: sp("github"), OccurredAt: "2026-08-01T00:00:00Z", PrevHash: genesisHash("test-machine")},
		{ChainSeq: 2, SchemaVersion: SchemaVersion1, Kind: KindCallRequest, SessionID: "old", Seq: ip(1),
			Tool: sp("echo"), ParamsDigest: sp("d"), Decision: sp(DecisionAllow), OccurredAt: "2026-08-01T00:00:01Z"},
	}
	entries[0].Hash = chainHash(entries[0].PrevHash, canonicalEncodeV1(entries[0]))
	entries[1].PrevHash = entries[0].Hash
	entries[1].Hash = chainHash(entries[1].PrevHash, canonicalEncodeV1(entries[1]))
	for _, e := range entries {
		if _, err := db.Exec(
			`insert into nim_journal (chain_seq, schema_version, kind, session_id, seq, connector, tool,
			   params_digest, decision, occurred_at, prev_hash, hash) values (?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.ChainSeq, e.SchemaVersion, e.Kind, e.SessionID, e.Seq, e.Connector, e.Tool,
			e.ParamsDigest, e.Decision, e.OccurredAt, e.PrevHash, e.Hash); err != nil {
			t.Fatalf("seeding entry %d: %v", e.ChainSeq, err)
		}
	}
	return entries
}

// A journal written by an earlier build has a table this build cannot write
// its new kinds into, because a CHECK constraint cannot be widened in place.
// Opening it must rebuild the table, and the chain that comes out must be the
// chain that went in.
func TestAJournalTableFromAnEarlierBuildIsRebuiltWithoutChangingTheChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")
	seeded := anM2Journal(t, path)

	j, err := Open(path, "test-machine")
	if err != nil {
		t.Fatalf("Open on an M2 journal: %v", err)
	}
	defer j.Close()

	// The table is now this build's, byte for byte.
	var stored string
	if err := j.db.QueryRow(
		`select sql from sqlite_master where type = 'table' and name = 'nim_journal'`).Scan(&stored); err != nil {
		t.Fatalf("reading the definition: %v", err)
	}
	if stored != journalTableStored {
		t.Fatalf("the table was not rebuilt to this build's definition:\n%s", stored)
	}
	if _, err := j.db.Exec(`select agent from nim_journal limit 0`); err != nil {
		t.Fatalf("the agent column was not added on the way: %v", err)
	}
	var leftovers int
	if err := j.db.QueryRow(
		`select count(*) from sqlite_master where name like 'nim_journal_rebuild%'`).Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 {
		t.Fatalf("%d rebuild leftovers in the schema", leftovers)
	}

	// Every row is exactly as it was.
	got, err := j.EntriesSince(0, 10)
	if err != nil {
		t.Fatalf("EntriesSince: %v", err)
	}
	if len(got) != len(seeded) {
		t.Fatalf("%d entries after the rebuild, want %d", len(got), len(seeded))
	}
	for i, e := range got {
		want := seeded[i]
		if e.ChainSeq != want.ChainSeq || e.SchemaVersion != want.SchemaVersion ||
			e.PrevHash != want.PrevHash || e.Hash != want.Hash || e.Kind != want.Kind {
			t.Errorf("entry %d changed in the rebuild:\n got %+v\nwant %+v", i+1, e, want)
		}
	}
	rep, err := j.Verify("")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK || rep.Entries != int64(len(seeded)) {
		t.Fatalf("the rebuilt chain does not verify: %+v", rep)
	}

	// The reason the rebuild exists: entries the old constraint refused.
	for _, e := range []Entry{
		{Kind: KindRuleAdd, SessionID: "", Tool: sp("rm"), Decision: sp(DecisionDeny), OccurredAt: nowRFC()},
		{Kind: KindAnomaly, SessionID: "old", Anomaly: sp("duplicate_key"), OccurredAt: nowRFC()},
		{Kind: KindSessionEnd, SessionID: "old", OccurredAt: nowRFC()},
	} {
		if err := j.Append(e); err != nil {
			t.Errorf("appending a %s entry after the rebuild: %v", e.Kind, err)
		}
	}
	if rep, _ := j.Verify(""); !rep.OK {
		t.Fatalf("the chain broke across the old and new rows: %s", rep.Problem)
	}

	// And the old view was replaced by this build's, not left pointing at a
	// table that no longer exists.
	var n int
	if err := j.db.QueryRow(`select count(*) from nim_calls`).Scan(&n); err != nil {
		t.Fatalf("nim_calls after the rebuild: %v", err)
	}
	if n != 1 {
		t.Errorf("nim_calls shows %d calls, want 1", n)
	}

	// Once is enough: a second look finds nothing to do.
	if rebuilt, err := rebuildJournalTable(j.db); err != nil || rebuilt {
		t.Fatalf("a second rebuild ran (rebuilt=%v, err=%v)", rebuilt, err)
	}
}

// Opening a journal this build made is a read. It used to drop and recreate
// both views under a write lock every time, because the stored definition was
// compared against the statement as written and SQLite stores the first two
// keywords upper-cased -- so the "only when drifted" path was the only path.
func TestOpeningACurrentJournalRecreatesNothing(t *testing.T) {
	j, _ := openTemp(t)
	for _, v := range views {
		current, err := viewMatches(j.db, v.name, v.ddl)
		if err != nil {
			t.Fatalf("viewMatches(%s): %v", v.name, err)
		}
		if !current {
			t.Errorf("view %s was reported as drifted straight after being created", v.name)
		}
	}
	if rebuilt, err := rebuildJournalTable(j.db); err != nil || rebuilt {
		t.Fatalf("a fresh journal's table was rebuilt (rebuilt=%v, err=%v)", rebuilt, err)
	}
}
