package journal

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// v1 was the whole encoding until agent became field 17. The point of carrying
// schema_version from the start was that this day would not invalidate what is
// already on disk, so it is worth proving rather than assuming.
func TestAV1EntryStillVerifiesAfterV2Exists(t *testing.T) {
	j, _ := openTemp(t)

	// Append a v1 entry the way the old build did: encode under v1, hash it,
	// and store it with schema_version 1. Nothing else in the file can write
	// one now, which is exactly why this is done by hand.
	e := Entry{
		ChainSeq: 1, SchemaVersion: SchemaVersion1,
		Kind: KindSessionStart, SessionID: "old", OccurredAt: nowRFC(),
		PrevHash: j.genesis,
	}
	e.Hash = chainHash(e.PrevHash, canonicalEncodeV1(e))
	if _, err := j.db.Exec(
		`insert into nim_journal
		   (chain_seq, schema_version, kind, session_id, occurred_at, prev_hash, hash)
		 values (?,?,?,?,?,?,?)`,
		e.ChainSeq, e.SchemaVersion, e.Kind, e.SessionID, e.OccurredAt, e.PrevHash, e.Hash,
	); err != nil {
		t.Fatalf("seeding a v1 entry: %v", err)
	}

	rep, err := j.Verify("")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("a v1 entry stopped verifying once v2 existed: %s", rep.Problem)
	}
}

// A chain that spans both versions has to check out end to end, because that
// is what every existing install becomes the moment it is upgraded.
func TestAChainSpanningBothVersionsVerifies(t *testing.T) {
	j, _ := openTemp(t)

	v1 := Entry{
		ChainSeq: 1, SchemaVersion: SchemaVersion1,
		Kind: KindSessionStart, SessionID: "old", OccurredAt: nowRFC(),
		PrevHash: j.genesis,
	}
	v1.Hash = chainHash(v1.PrevHash, canonicalEncodeV1(v1))
	if _, err := j.db.Exec(
		`insert into nim_journal
		   (chain_seq, schema_version, kind, session_id, occurred_at, prev_hash, hash)
		 values (?,?,?,?,?,?,?)`,
		v1.ChainSeq, v1.SchemaVersion, v1.Kind, v1.SessionID, v1.OccurredAt, v1.PrevHash, v1.Hash,
	); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// Everything after it is written by the current build, at v2, linking to
	// the v1 entry's hash.
	agent := "claude-code"
	for i := 0; i < 3; i++ {
		if err := j.Append(Entry{
			Kind: KindSessionStart, SessionID: "new", Agent: &agent, OccurredAt: nowRFC(),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	rep, err := j.Verify("")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("a mixed chain did not verify: %s", rep.Problem)
	}
	if rep.Entries != 4 {
		t.Fatalf("walked %d entries, want 4", rep.Entries)
	}
}

// The version an entry was written under decides how it is hashed. If the two
// encoders ever produced the same bytes for the same entry, the version would
// be decorative and a v2 entry could be re-read as v1 without detection.
func TestTheTwoEncodingsAreDistinct(t *testing.T) {
	agent := "claude-code"
	e := Entry{
		ChainSeq: 1, Kind: KindSessionStart, SessionID: "s", OccurredAt: "t", Agent: &agent,
	}
	e.SchemaVersion = SchemaVersion1
	v1 := canonicalEncodeV1(e)
	e.SchemaVersion = SchemaVersion2
	v2 := canonicalEncodeV2(e)

	if string(v1) == string(v2) {
		t.Fatal("v1 and v2 encoded the same entry identically")
	}
	if !strings.HasPrefix(string(v1), "nim.journal.v1\n") {
		t.Error("v1 does not carry the v1 domain")
	}
	if !strings.HasPrefix(string(v2), "nim.journal.v2\n") {
		t.Error("v2 does not carry the v2 domain")
	}
	// v1 cannot express an agent at all, which is why a new version was needed
	// rather than a new field.
	if strings.Contains(string(v1), agent) {
		t.Error("the v1 encoding contained the agent")
	}
	if !strings.Contains(string(v2), agent) {
		t.Error("the v2 encoding did not contain the agent")
	}
}

// An entry claiming a version this build does not know must be refused by
// name, not encoded under a guess. Encoding it under whatever rules happened
// to be nearest is how a future entry would verify against the wrong ones.
func TestAnUnknownSchemaVersionIsRefused(t *testing.T) {
	j, _ := openTemp(t)
	if err := j.Append(Entry{Kind: KindSessionStart, SessionID: "s", OccurredAt: nowRFC()}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := j.db.Exec(`update nim_journal set schema_version = 99 where chain_seq = 1`); err != nil {
		t.Fatalf("forcing a future version: %v", err)
	}

	rep, err := j.Verify("")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK {
		t.Fatal("an entry with an unknown schema_version verified")
	}
	if !strings.Contains(rep.Problem, "99") {
		t.Errorf("the problem should name the version, got %q", rep.Problem)
	}
	if canonicalEncode(Entry{SchemaVersion: 99}) != nil {
		t.Error("an unknown version produced an encoding instead of nothing")
	}
}

// The agent is part of what the chain covers. Changing it after the fact has
// to break the entry's hash, or recording it would be decoration.
func TestAlteringTheAgentBreaksTheChain(t *testing.T) {
	j, _ := openTemp(t)
	agent := "claude-code"
	if err := j.Append(Entry{
		Kind: KindSessionStart, SessionID: "s", Agent: &agent, OccurredAt: nowRFC(),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if rep, _ := j.Verify(""); !rep.OK {
		t.Fatal("the seeded entry did not verify")
	}

	if _, err := j.db.Exec(`update nim_journal set agent = 'someone-else' where chain_seq = 1`); err != nil {
		t.Fatalf("rewriting the agent: %v", err)
	}
	rep, err := j.Verify("")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK {
		t.Fatal("the agent was rewritten and the chain still verified")
	}
	if !strings.Contains(rep.Problem, "altered") {
		t.Errorf("the problem should say the entry was altered, got %q", rep.Problem)
	}
}

// A null agent and an empty one are different values, and the encoding has to
// keep them apart -- otherwise "no agent" and "an agent whose name is nothing"
// would hash alike, which is the collision the tag-and-length format exists to
// prevent.
func TestNoAgentAndAnEmptyAgentEncodeDifferently(t *testing.T) {
	empty := ""
	base := Entry{
		ChainSeq: 1, SchemaVersion: SchemaVersion2,
		Kind: KindSessionStart, SessionID: "s", OccurredAt: "t",
	}
	withNil := canonicalEncodeV2(base)
	base.Agent = &empty
	withEmpty := canonicalEncodeV2(base)

	if string(withNil) == string(withEmpty) {
		t.Fatal("a nil agent and an empty agent encoded identically")
	}
}

// An install that predates the agent column has to keep working: the migration
// adds it, and everything already there stays valid.
func TestAJournalWithoutTheAgentColumnMigratesAndStillVerifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")

	j, err := Open(path, "test-machine")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := j.Append(Entry{Kind: KindSessionStart, SessionID: "s", OccurredAt: nowRFC()}); err != nil {
		t.Fatalf("append: %v", err)
	}
	j.Close()

	// Reopening runs the migrations again, which must be a no-op.
	j2, err := Open(path, "test-machine")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer j2.Close()
	if err := j2.Append(Entry{Kind: KindSessionEnd, SessionID: "s", OccurredAt: nowRFC()}); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	rep, err := j2.Verify("")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("a reopened journal did not verify: %s", rep.Problem)
	}
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339Nano) }
