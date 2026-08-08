package journal

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nim.db")
	j, err := Open(path, "test-machine")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { j.Close() })
	return j, path
}

func call(session string, seq int64, tool string) Entry {
	return Entry{
		Kind:         KindCallRequest,
		SessionID:    session,
		Seq:          ip(seq),
		Tool:         sp(tool),
		ParamsDigest: sp("digest"),
		Decision:     sp(DecisionObserved),
		OccurredAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func session(id, connector string) Entry {
	return Entry{
		Kind:       KindSessionStart,
		SessionID:  id,
		MachineID:  sp("test-machine"),
		Client:     sp("test-client"),
		Connector:  sp(connector),
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// tamper reaches past the journal API, the way anyone editing the file would.
func tamper(t *testing.T, path, statement string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening the journal directly: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

// The whole point of the rewrite: a call is two entries, so nothing has to be
// updated after it is written. An UPSERT would leave one row here.
func TestACallIsTwoImmutableEntries(t *testing.T) {
	j, _ := openTemp(t)

	if err := j.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(call("s1", 1, "create_issue")); err != nil {
		t.Fatal(err)
	}
	outcome := Entry{
		Kind:       KindCallOutcome,
		SessionID:  "s1",
		Seq:        ip(1),
		OK:         bp(true),
		DurationMS: ip(12),
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := j.Append(outcome); err != nil {
		t.Fatal(err)
	}

	length, _, err := j.Head()
	if err != nil {
		t.Fatal(err)
	}
	if length != 3 {
		t.Fatalf("expected 3 entries, got %d: an outcome must not overwrite its request", length)
	}

	// And the view joins them back into one row.
	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("nim_calls returned %d rows, want 1", len(calls))
	}
	c := calls[0]
	if c.Tool != "create_issue" || c.Connector != "github" || c.Decision != DecisionObserved {
		t.Errorf("view lost detail: %+v", c)
	}
	if c.OK == nil || !*c.OK || c.DurationMS == nil || *c.DurationMS != 12 {
		t.Errorf("view did not join the outcome: %+v", c)
	}
}

// Nothing here is ever authorized, so nothing may claim it was.
func TestOnlyObservedIsEverWritten(t *testing.T) {
	if DecisionObserved != "observed" {
		t.Fatalf("decision vocabulary changed: %q", DecisionObserved)
	}
	j, path := openTemp(t)
	if err := j.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(call("s1", 1, "t")); err != nil {
		t.Fatal(err)
	}

	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var n int
	if err := db.QueryRow(
		`select count(*) from nim_journal where decision is not null and decision <> 'observed'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d entries claim a decision that was never made", n)
	}
}

func TestChainStartsAtGenesisAndLinksForward(t *testing.T) {
	j, path := openTemp(t)
	for i := 1; i <= 4; i++ {
		if err := j.Append(call("s1", int64(i), "t")); err != nil {
			t.Fatal(err)
		}
	}

	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	rows, err := db.Query(`select chain_seq, prev_hash, hash from nim_journal order by chain_seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	expectSeq := int64(1)
	expectPrev := genesisHash("test-machine")
	for rows.Next() {
		var seq int64
		var prev, hash string
		if err := rows.Scan(&seq, &prev, &hash); err != nil {
			t.Fatal(err)
		}
		if seq != expectSeq {
			t.Fatalf("chain_seq %d, want %d: the sequence must be contiguous from 1", seq, expectSeq)
		}
		if prev != expectPrev {
			t.Fatalf("entry %d links to %s, want %s", seq, short(prev), short(expectPrev))
		}
		expectSeq++
		expectPrev = hash
	}
}

func TestVerifyAcceptsAnIntactChain(t *testing.T) {
	j, _ := openTemp(t)
	for i := 1; i <= 5; i++ {
		if err := j.Append(call("s1", int64(i), "t")); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := j.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("intact chain rejected: %s", rep.Problem)
	}
	if rep.Entries != 5 {
		t.Errorf("walked %d entries, want 5", rep.Entries)
	}
}

func TestVerifyDetectsAnAlteredEntry(t *testing.T) {
	j, path := openTemp(t)
	for i := 1; i <= 5; i++ {
		if err := j.Append(call("s1", int64(i), "t")); err != nil {
			t.Fatal(err)
		}
	}
	// Exactly what someone hiding a call would do, and nothing else.
	tamper(t, path, `update nim_journal set tool = 'something_else' where chain_seq = 3`)

	rep, err := j.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("an altered entry verified cleanly")
	}
	if !contains(rep.Problem, "3") {
		t.Errorf("the problem must name the entry it was found at, got: %s", rep.Problem)
	}
}

func TestVerifyDetectsACorruptedHash(t *testing.T) {
	j, path := openTemp(t)
	for i := 1; i <= 3; i++ {
		if err := j.Append(call("s1", int64(i), "t")); err != nil {
			t.Fatal(err)
		}
	}
	tamper(t, path, `update nim_journal set hash = 'deadbeef' where chain_seq = 2`)

	rep, _ := j.Verify("")
	if rep.OK {
		t.Fatal("a corrupted hash verified cleanly")
	}
	if !contains(rep.Problem, "2") {
		t.Errorf("expected entry 2 to be named, got: %s", rep.Problem)
	}
}

func TestVerifyDetectsAMissingEntry(t *testing.T) {
	j, path := openTemp(t)
	for i := 1; i <= 5; i++ {
		if err := j.Append(call("s1", int64(i), "t")); err != nil {
			t.Fatal(err)
		}
	}
	tamper(t, path, `delete from nim_journal where chain_seq = 3`)

	rep, _ := j.Verify("")
	if rep.OK {
		t.Fatal("a chain with a hole verified cleanly")
	}
	if !contains(rep.Problem, "3") {
		t.Errorf("expected the gap at 3 to be named, got: %s", rep.Problem)
	}
}

func TestVerifyRejectsAnUnknownSchemaVersion(t *testing.T) {
	j, path := openTemp(t)
	if err := j.Append(call("s1", 1, "t")); err != nil {
		t.Fatal(err)
	}
	tamper(t, path, `update nim_journal set schema_version = 99 where chain_seq = 1`)

	rep, _ := j.Verify("")
	if rep.OK {
		t.Fatal("an entry written under an unknown schema version was verified anyway")
	}
	if !contains(rep.Problem, "99") {
		t.Errorf("the version must be named, got: %s", rep.Problem)
	}
}

// This test exists to document the limitation, not to celebrate a feature.
//
// Truncating the tail leaves a chain that is internally consistent, and nothing
// in the journal can tell. Only a head recorded beforehand catches it. If this
// ever starts failing at the first assertion, the chain has gained a property
// it does not have, and the claims in docs/journal-format.md, `nim status` and
// `nim verify` all need revisiting.
func TestTruncationIsInvisibleWithoutAnExpectedHead(t *testing.T) {
	j, path := openTemp(t)
	for i := 1; i <= 5; i++ {
		if err := j.Append(call("s1", int64(i), "t")); err != nil {
			t.Fatal(err)
		}
	}
	_, headBefore, err := j.Head()
	if err != nil {
		t.Fatal(err)
	}

	tamper(t, path, `delete from nim_journal where chain_seq > 3`)

	rep, err := j.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatal("a truncated chain was detected without an expected head -- " +
			"good news, but the documented limitation is now wrong and must be updated")
	}

	// The head recorded earlier is the only thing that notices.
	rep, err = j.Verify(headBefore)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("--expect-head accepted a chain whose head had changed")
	}
	if !contains(rep.Problem, "removed") {
		t.Errorf("the message should say entries were removed, got: %s", rep.Problem)
	}
}

func TestExpectHeadAcceptsTheRealHead(t *testing.T) {
	j, _ := openTemp(t)
	for i := 1; i <= 3; i++ {
		if err := j.Append(call("s1", int64(i), "t")); err != nil {
			t.Fatal(err)
		}
	}
	_, head, _ := j.Head()

	rep, err := j.Verify(head)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("the current head was rejected: %s", rep.Problem)
	}
}

// A daemon that restarts must continue the chain, not begin a second one.
func TestChainContinuesAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")

	first, err := Open(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := first.Append(call("s1", int64(i), "t")); err != nil {
			t.Fatal(err)
		}
	}
	_, headBefore, _ := first.Head()
	first.Close()

	second, err := Open(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.Append(call("s2", 1, "t")); err != nil {
		t.Fatal(err)
	}

	length, _, _ := second.Head()
	if length != 4 {
		t.Fatalf("chain length %d after reopen, want 4", length)
	}
	rep, _ := second.Verify("")
	if !rep.OK {
		t.Fatalf("the chain broke across a restart: %s", rep.Problem)
	}
	if headBefore == rep.Head {
		t.Error("the head did not move after appending")
	}
}

// The daemon serves each shim on its own goroutine. Two appends racing for the
// same chain_seq would break the chain, so the writer must serialise them.
func TestConcurrentAppendsProduceOneChain(t *testing.T) {
	j, _ := openTemp(t)

	const writers, each = 8, 10
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 1; i <= each; i++ {
				if err := j.Append(call(fmt.Sprintf("s%d", w), int64(i), "t")); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	length, _, _ := j.Head()
	if length != writers*each {
		t.Fatalf("chain length %d, want %d", length, writers*each)
	}
	rep, _ := j.Verify("")
	if !rep.OK {
		t.Fatalf("concurrent writers broke the chain: %s", rep.Problem)
	}
}

// A pre-chain journal cannot be folded into the chain: hashing rows that were
// updated in place would assert an integrity they never had. They are moved
// aside instead of deleted.
func TestLegacyTablesAreRetiredNotDestroyed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		create table nim_sessions (id text primary key, target text);
		create table nim_calls (id text primary key, tool text);
		insert into nim_calls values ('old-1', 'legacy_tool');
	`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	j, err := Open(path, "test-machine")
	if err != nil {
		t.Fatalf("Open over a legacy journal: %v", err)
	}
	defer j.Close()

	db, _ = sql.Open("sqlite", path)
	defer db.Close()
	var tool string
	if err := db.QueryRow(`select tool from nim_calls_m1 where id = 'old-1'`).Scan(&tool); err != nil {
		t.Fatalf("the old rows were not preserved: %v", err)
	}
	if tool != "legacy_tool" {
		t.Errorf("old row changed: %q", tool)
	}

	var kind string
	if err := db.QueryRow(`select type from sqlite_master where name = 'nim_calls'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "view" {
		t.Errorf("nim_calls is a %s, want a view", kind)
	}
}

func TestLossReportsGapsAndUnfinishedSessions(t *testing.T) {
	j, _ := openTemp(t)
	if err := j.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}
	// seq 2 never arrives: the daemon was unreachable when it was reported.
	for _, seq := range []int64{1, 3, 4} {
		if err := j.Append(call("s1", seq, "t")); err != nil {
			t.Fatal(err)
		}
	}

	loss, err := j.Loss()
	if err != nil {
		t.Fatal(err)
	}
	if loss.MissingCallEntries != 1 {
		t.Errorf("missing calls = %d, want 1", loss.MissingCallEntries)
	}
	if loss.SessionsWithGaps != 1 {
		t.Errorf("sessions with gaps = %d, want 1", loss.SessionsWithGaps)
	}
	if loss.UnfinishedSessions != 1 {
		t.Errorf("unfinished sessions = %d, want 1", loss.UnfinishedSessions)
	}

	if err := j.Append(Entry{
		Kind: KindSessionEnd, SessionID: "s1",
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	loss, _ = j.Loss()
	if loss.UnfinishedSessions != 0 {
		t.Errorf("a closed session still counts as unfinished")
	}
}

func TestAnomaliesAreCounted(t *testing.T) {
	j, _ := openTemp(t)
	for _, name := range []string{"batch", "batch", "malformed_json"} {
		if err := j.Append(Entry{
			Kind: KindAnomaly, SessionID: "s1", Anomaly: sp(name),
			OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			t.Fatal(err)
		}
	}
	counts, err := j.Anomalies()
	if err != nil {
		t.Fatal(err)
	}
	if counts["batch"] != 2 || counts["malformed_json"] != 1 {
		t.Fatalf("anomaly counts = %v", counts)
	}
	// Anomalies are not calls: counting them as such would fake a gap.
	loss, _ := j.Loss()
	if loss.MissingCallEntries != 0 {
		t.Errorf("anomalies were mistaken for missing calls: %+v", loss)
	}
}

// Losing the file that seeds the chain is losing verification material. It is
// not evidence that the journal is wrong, and reporting it as tampering would
// accuse someone on the strength of a missing file.
func TestAMissingSeedIsNotTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")

	seeded, err := Open(path, "the-machine")
	if err != nil {
		t.Fatal(err)
	}
	if err := seeded.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := seeded.Append(call("s1", int64(i), "t")); err != nil {
			t.Fatal(err)
		}
	}
	full, err := seeded.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if !full.OK || full.Partial {
		t.Fatalf("with its seed the chain should verify outright: %+v", full)
	}
	seeded.Close()

	// The machine-id has gone missing: open with no seed at all.
	blind, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer blind.Close()

	rep, err := blind.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("a missing seed was reported as a broken journal: %s", rep.Problem)
	}
	if !rep.Partial {
		t.Error("the report must say the first entry went unchecked")
	}
	if rep.Entries != full.Entries || rep.Head != full.Head {
		t.Errorf("the rest of the chain should still be walked: got %d entries head %s",
			rep.Entries, short(rep.Head))
	}
	if blind.SeedKnown() {
		t.Error("SeedKnown must be false without a seed")
	}

	// Everything after entry 1 is still checked, so real tampering is caught.
	tamper(t, path, `update nim_journal set tool = 'other' where chain_seq = 3`)
	if rep, _ := blind.Verify(""); rep.OK {
		t.Error("an altered entry was accepted while the seed was missing")
	}

	// And restoring the file restores full verification.
	tamper(t, path, `update nim_journal set tool = 't' where chain_seq = 3`)
	restored, err := Open(path, "the-machine")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	again, err := restored.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if !again.OK || again.Partial || again.Head != full.Head {
		t.Fatalf("restoring the seed did not restore verification: %+v", again)
	}
}

// A chain cannot be started without a seed: entry 1 would be linked to a
// genesis that was invented on the spot.
func TestAnEmptyJournalRefusesToStartWithoutASeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")
	j, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	if err := j.Append(call("s1", 1, "t")); err == nil {
		t.Fatal("a journal with no seed accepted its first entry")
	}
	if err := j.AdoptSeed("the-machine"); err != nil {
		t.Fatalf("adopting a seed for an empty journal: %v", err)
	}
	if err := j.Append(call("s1", 1, "t")); err != nil {
		t.Fatalf("after adopting a seed: %v", err)
	}
	if err := j.AdoptSeed("someone-else"); err == nil {
		t.Fatal("a seed was adopted over entries chained from another one")
	}
}

// An empty journal is not a verified one: it is indistinguishable from one
// whose every entry was lost.
func TestAnEmptyJournalIsReportedAsEmpty(t *testing.T) {
	j, _ := openTemp(t)

	rep, err := j.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Empty {
		t.Error("an empty journal must be reported as empty")
	}
	if rep.Entries != 0 || rep.Head != "" {
		t.Errorf("unexpected report for an empty journal: %+v", rep)
	}
	// Empty is not a failure. The CLI keys on Problem, so leaving one here
	// would make `nim verify` announce that something went wrong on a journal
	// where nothing has happened yet.
	if rep.Problem != "" {
		t.Errorf("an empty journal reported a problem: %q", rep.Problem)
	}
	if rep.OK {
		t.Error("an empty journal must not be reported as verified either")
	}

	// And it certainly does not have the head anyone recorded earlier.
	rep, err = j.Verify("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Error("an empty journal satisfied --expect-head")
	}
}

// The chain covers each entry's contents; this constraint covers the shape of
// the set. Gap detection rests on the second, because it compares how many
// requests a session has against the highest seq it reached.
func TestDuplicateEntriesAreRejected(t *testing.T) {
	j, _ := openTemp(t)
	if err := j.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(call("s1", 1, "a")); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(call("s1", 1, "b")); err == nil {
		t.Fatal("a second call.request reused seq 1: a real gap could now be hidden")
	}

	// The outcome shares its request's seq on purpose, and must still be
	// allowed: they are different kinds.
	outcome := Entry{Kind: KindCallOutcome, SessionID: "s1", Seq: ip(1), OK: bp(true),
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := j.Append(outcome); err != nil {
		t.Fatalf("an outcome for seq 1 was rejected: %v", err)
	}

	// Anomalies carry no seq, so a session may have as many as it produces.
	for i := 0; i < 3; i++ {
		anomaly := Entry{Kind: KindAnomaly, SessionID: "s1", Anomaly: sp("batch"),
			OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if err := j.Append(anomaly); err != nil {
			t.Fatalf("anomaly %d was rejected: %v", i, err)
		}
	}
}

func TestGapDetectionCases(t *testing.T) {
	cases := []struct {
		name        string
		seqs        []int64
		wantMissing int
	}{
		{"contiguous", []int64{1, 2, 3}, 0},
		{"one missing in the middle", []int64{1, 3}, 1},
		{"two missing", []int64{1, 4}, 2},
		{"single call", []int64{1}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j, _ := openTemp(t)
			if err := j.Append(session("s1", "github")); err != nil {
				t.Fatal(err)
			}
			for _, seq := range c.seqs {
				if err := j.Append(call("s1", seq, "t")); err != nil {
					t.Fatal(err)
				}
				outcome := Entry{Kind: KindCallOutcome, SessionID: "s1", Seq: ip(seq), OK: bp(true),
					OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
				if err := j.Append(outcome); err != nil {
					t.Fatal(err)
				}
			}
			loss, err := j.Loss()
			if err != nil {
				t.Fatal(err)
			}
			if loss.MissingCallEntries != c.wantMissing {
				t.Errorf("missing = %d, want %d (outcomes must not be counted as calls)",
					loss.MissingCallEntries, c.wantMissing)
			}
		})
	}
}

// chain_seq is the journal's order. A timestamp is a value an entry carries,
// and a wrong clock -- or an entry someone wrote -- must not reorder the log.
func TestLogOrderFollowsTheChainNotTheClock(t *testing.T) {
	j, _ := openTemp(t)
	if err := j.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}

	// Deliberately backwards: the newest call carries the oldest timestamp.
	stamps := []string{
		"2030-01-01T00:00:00Z",
		"1999-01-01T00:00:00Z",
		"2015-06-15T12:00:00Z",
	}
	for i, stamp := range stamps {
		e := call("s1", int64(i+1), fmt.Sprintf("tool%d", i+1))
		e.OccurredAt = stamp
		if err := j.Append(e); err != nil {
			t.Fatal(err)
		}
	}

	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("got %d rows, want 3", len(calls))
	}
	want := []string{"tool3", "tool2", "tool1"} // newest chain_seq first
	for i, w := range want {
		if calls[i].Tool != w {
			t.Errorf("row %d is %q, want %q: the log followed occurred_at, not chain_seq",
				i, calls[i].Tool, w)
		}
	}
	if calls[0].ChainSeq <= calls[1].ChainSeq {
		t.Error("rows are not in descending chain_seq order")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
