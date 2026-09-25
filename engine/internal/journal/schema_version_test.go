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

// A v3 entry -- one carrying exec_path and exec_id -- verifies under its own
// encoding, the same proof the v1 and v2 entries already have. Seeded by
// hand, the way the v1 entry above is: AddAgent now writes at
// CurrentSchemaVersion (4), so a pure v3 entry -- one schema_version behind
// what this build writes -- is exactly what an install upgraded once more
// would already hold, and nothing here can produce a fresh one any longer.
func TestAV3EntryVerifies(t *testing.T) {
	j, _ := openTemp(t)

	agent, path, id := "claude-code", "/bin/claude", "7:42"
	e := Entry{
		ChainSeq: 1, SchemaVersion: SchemaVersion3,
		Kind: KindAgentAdd, SessionID: "", OccurredAt: nowRFC(),
		Agent: &agent, ExecPath: &path, ExecID: &id,
		PrevHash: j.genesis,
	}
	e.Hash = chainHash(e.PrevHash, canonicalEncodeV3(e))
	if _, err := j.db.Exec(
		`insert into nim_journal
		   (chain_seq, schema_version, kind, session_id, agent, exec_path, exec_id, occurred_at, prev_hash, hash)
		 values (?,?,?,?,?,?,?,?,?,?)`,
		e.ChainSeq, e.SchemaVersion, e.Kind, e.SessionID, e.Agent, e.ExecPath, e.ExecID, e.OccurredAt, e.PrevHash, e.Hash,
	); err != nil {
		t.Fatalf("seeding a v3 entry: %v", err)
	}

	entries, err := j.EntriesSince(0, 10)
	if err != nil {
		t.Fatalf("EntriesSince: %v", err)
	}
	if len(entries) != 1 || entries[0].SchemaVersion != SchemaVersion3 {
		t.Fatalf("did not read back one v3 entry: %+v", entries)
	}
	if entries[0].ExecPath == nil || *entries[0].ExecPath != "/bin/claude" ||
		entries[0].ExecID == nil || *entries[0].ExecID != "7:42" {
		t.Fatalf("the agent.add entry does not carry the enrolment: %+v", entries[0])
	}
	if entries[0].BudgetCalls != nil {
		t.Fatalf("a v3 entry gained a budget_calls it was never written with: %+v", entries[0])
	}

	rep, err := j.Verify("")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("a v3 entry did not verify: %s", rep.Problem)
	}
}

// A v4 entry -- one carrying budget_calls, written by AddBudget -- verifies
// under its own encoding, the same proof the v1, v2 and v3 entries already
// have. Unlike v3, this build writes v4 naturally: CurrentSchemaVersion is
// 4, so AddBudget needs no hand-seeding to produce one.
func TestAV4EntryVerifies(t *testing.T) {
	j, _ := openTemp(t)
	if _, err := j.AddBudget(Budget{Tool: "rm", Calls: 5}); err != nil {
		t.Fatalf("AddBudget: %v", err)
	}

	entries, err := j.EntriesSince(0, 10)
	if err != nil {
		t.Fatalf("EntriesSince: %v", err)
	}
	if len(entries) != 1 || entries[0].SchemaVersion != SchemaVersion4 {
		t.Fatalf("AddBudget did not write one v4 entry: %+v", entries)
	}
	if entries[0].BudgetCalls == nil || *entries[0].BudgetCalls != 5 {
		t.Fatalf("the budget.add entry does not carry the cap: %+v", entries[0])
	}

	rep, err := j.Verify("")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("a v4 entry did not verify: %s", rep.Problem)
	}
}

// A chain spanning all four versions has to check out end to end, because
// that is what an install upgraded across three schema changes becomes.
func TestAChainSpanningV1ToV4Verifies(t *testing.T) {
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
		t.Fatalf("seeding v1: %v", err)
	}

	agent := "claude-code"
	v2 := Entry{
		ChainSeq: 2, SchemaVersion: SchemaVersion2,
		Kind: KindSessionStart, SessionID: "newer", Agent: &agent, OccurredAt: nowRFC(),
		PrevHash: v1.Hash,
	}
	v2.Hash = chainHash(v2.PrevHash, canonicalEncodeV2(v2))
	if _, err := j.db.Exec(
		`insert into nim_journal
		   (chain_seq, schema_version, kind, session_id, agent, occurred_at, prev_hash, hash)
		 values (?,?,?,?,?,?,?,?)`,
		v2.ChainSeq, v2.SchemaVersion, v2.Kind, v2.SessionID, v2.Agent, v2.OccurredAt, v2.PrevHash, v2.Hash,
	); err != nil {
		t.Fatalf("seeding v2: %v", err)
	}

	execPath, execID := "/bin/cursor", "1:2"
	v3 := Entry{
		ChainSeq: 3, SchemaVersion: SchemaVersion3,
		Kind: KindAgentAdd, SessionID: "", Agent: &agent, ExecPath: &execPath, ExecID: &execID,
		OccurredAt: nowRFC(), PrevHash: v2.Hash,
	}
	v3.Hash = chainHash(v3.PrevHash, canonicalEncodeV3(v3))
	if _, err := j.db.Exec(
		`insert into nim_journal
		   (chain_seq, schema_version, kind, session_id, agent, exec_path, exec_id, occurred_at, prev_hash, hash)
		 values (?,?,?,?,?,?,?,?,?,?)`,
		v3.ChainSeq, v3.SchemaVersion, v3.Kind, v3.SessionID, v3.Agent, v3.ExecPath, v3.ExecID, v3.OccurredAt, v3.PrevHash, v3.Hash,
	); err != nil {
		t.Fatalf("seeding v3: %v", err)
	}

	// Everything after it is written by the current build, at v4, linking to
	// the v3 entry's hash.
	if _, err := j.AddBudget(Budget{Tool: "rm", Calls: 3}); err != nil {
		t.Fatalf("AddBudget: %v", err)
	}

	rep, err := j.Verify("")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("a chain spanning v1 through v4 did not verify: %s", rep.Problem)
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

// The same proof again for v2 and v3. If they ever produced the same bytes
// for the same entry, exec_path and exec_id could be dropped from a v3 entry
// and it would still verify as v2.
func TestTheV2AndV3EncodingsAreDistinct(t *testing.T) {
	agent, path, id := "claude-code", "/bin/claude", "7:42"
	e := Entry{
		ChainSeq: 1, Kind: KindAgentAdd, SessionID: "", OccurredAt: "t",
		Agent: &agent, ExecPath: &path, ExecID: &id,
	}
	e.SchemaVersion = SchemaVersion2
	v2 := canonicalEncodeV2(e)
	e.SchemaVersion = SchemaVersion3
	v3 := canonicalEncodeV3(e)

	if string(v2) == string(v3) {
		t.Fatal("v2 and v3 encoded the same entry identically")
	}
	if !strings.HasPrefix(string(v2), "nim.journal.v2\n") {
		t.Error("v2 does not carry the v2 domain")
	}
	if !strings.HasPrefix(string(v3), "nim.journal.v3\n") {
		t.Error("v3 does not carry the v3 domain")
	}
	// v2 cannot express exec_path or exec_id at all, which is why a new
	// version was needed rather than two new fields grafted onto v2.
	if strings.Contains(string(v2), path) || strings.Contains(string(v2), id) {
		t.Error("the v2 encoding contained exec_path or exec_id")
	}
	if !strings.Contains(string(v3), path) || !strings.Contains(string(v3), id) {
		t.Error("the v3 encoding did not contain exec_path and exec_id")
	}
}

// The same proof again for v3 and v4. If they ever produced the same bytes
// for the same entry, budget_calls could be dropped from a v4 entry and it
// would still verify as v3.
func TestTheV3AndV4EncodingsAreDistinct(t *testing.T) {
	calls := int64(5)
	e := Entry{
		ChainSeq: 1, Kind: KindBudgetAdd, SessionID: "", OccurredAt: "t",
		BudgetCalls: &calls,
	}
	e.SchemaVersion = SchemaVersion3
	v3 := canonicalEncodeV3(e)
	e.SchemaVersion = SchemaVersion4
	v4 := canonicalEncodeV4(e)

	if string(v3) == string(v4) {
		t.Fatal("v3 and v4 encoded the same entry identically")
	}
	if !strings.HasPrefix(string(v3), "nim.journal.v3\n") {
		t.Error("v3 does not carry the v3 domain")
	}
	if !strings.HasPrefix(string(v4), "nim.journal.v4\n") {
		t.Error("v4 does not carry the v4 domain")
	}
	// v3 cannot express budget_calls at all, which is why a new version was
	// needed rather than one field grafted onto v3.
	if len(v3) == len(v4) {
		t.Error("v3 and v4 encoded to the same length despite v4 carrying one more field")
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
