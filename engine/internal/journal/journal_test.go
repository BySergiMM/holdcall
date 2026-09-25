package journal

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// A call whose session.start never reached the journal is a real, decided
// call. It used to vanish from nim_calls -- an inner join on a start that was
// not there -- while the totals still counted it. It is shown without a
// connector and counted as orphaned instead.
func TestACallWithoutASessionStartIsShownAndCounted(t *testing.T) {
	j, _ := openTemp(t)
	if err := j.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(call("s1", 1, "t")); err != nil {
		t.Fatal(err)
	}
	// s2's start was accepted by the daemon and never committed; its call was.
	if err := j.Append(call("s2", 1, "t")); err != nil {
		t.Fatal(err)
	}

	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("nim_calls shows %d calls, want 2: the orphaned call is hidden", len(calls))
	}
	n, _ := j.CountCalls()
	if n != len(calls) {
		t.Fatalf("CountCalls says %d and the listing says %d", n, len(calls))
	}
	loss, err := j.Loss()
	if err != nil {
		t.Fatal(err)
	}
	if loss.CallsWithoutSession != 1 {
		t.Errorf("calls without a session = %d, want 1", loss.CallsWithoutSession)
	}
	if loss.MissingCallEntries != 0 || loss.SessionsWithGaps != 0 {
		t.Errorf("an orphaned call was reported as a seq gap: %+v", loss)
	}
	if _, found, _ := j.Session("s2"); found {
		t.Error("a session with no start was summarised as if it had one")
	}
}

// One session by id reads the same as the same session in the list.
func TestASessionCanBeReadByIdBeyondTheListingWindow(t *testing.T) {
	j, _ := openTemp(t)
	for i := 0; i < MaxEntriesPerRead/4; i++ {
		s := session(fmt.Sprintf("s%d", i), "github")
		if i == 0 {
			s.Agent = sp("claude-code")
		}
		if err := j.Append(s); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := j.Sessions(5)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == "s0" {
			t.Fatal("the oldest session is inside the window; the test needs a smaller one")
		}
	}
	row, found, err := j.Session("s0")
	if err != nil || !found {
		t.Fatalf("Session(s0) = found %v, err %v", found, err)
	}
	if row.Agent == nil || *row.Agent != "claude-code" || row.Connector == nil || *row.Connector != "github" {
		t.Fatalf("the single lookup lost fields: %+v", row)
	}
	if _, found, _ := j.Session("never"); found {
		t.Error("a session that was never started was found")
	}
	if ok, _ := j.SessionExists("s0"); !ok {
		t.Error("SessionExists missed a recorded session")
	}
	if ok, _ := j.SessionExists("never"); ok {
		t.Error("SessionExists invented a session")
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

// A reader must not be able to change the record, and must not bring one into
// existence either: Open creates and migrates as a side effect of being called,
// which is the wrong thing for a console to do to a machine it is inspecting.
func TestOpenReadOnlyCannotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")

	writer, err := Open(path, "the-machine")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(call("s1", 1, "create_issue")); err != nil {
		t.Fatal(err)
	}
	if writer.ReadOnly() {
		t.Error("a journal opened with Open must be writable")
	}
	writer.Close()

	reader, err := OpenReadOnly(path, "the-machine")
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer reader.Close()

	if !reader.ReadOnly() {
		t.Error("ReadOnly() must report what the handle is for")
	}
	if err := reader.Append(call("s1", 2, "t")); err == nil {
		t.Fatal("a read-only journal accepted an entry")
	}
	if err := reader.AdoptSeed("someone-else"); err == nil {
		t.Fatal("a read-only journal accepted a seed")
	}

	// Reading still works, and the refusals left nothing behind.
	length, _, err := reader.Head()
	if err != nil {
		t.Fatal(err)
	}
	if length != 2 {
		t.Fatalf("read %d entries, want 2", length)
	}
	rep, err := reader.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("a read-only handle could not verify: %s", rep.Problem)
	}
}

// Opening a journal that does not exist must fail rather than create one.
func TestOpenReadOnlyRefusesAJournalThatIsNotThere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")

	if _, err := OpenReadOnly(path, "m"); err == nil {
		t.Fatal("OpenReadOnly created or accepted a journal that does not exist")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("OpenReadOnly left a database file behind")
	}
}

// The cursor: chain_seq is monotonic and gapless, so a reader remembers the
// last position it saw and asks for what came after. This is the whole of the
// streaming mechanism, and it has to resume exactly.
func TestEntriesSinceIsACursor(t *testing.T) {
	j, _ := openTemp(t)
	if err := j.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if err := j.Append(call("s1", int64(i), fmt.Sprintf("tool%d", i))); err != nil {
			t.Fatal(err)
		}
	}

	// From the beginning.
	all, err := j.EntriesSince(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Fatalf("got %d entries from the start, want 6", len(all))
	}
	for i, e := range all {
		if e.ChainSeq != int64(i+1) {
			t.Fatalf("entry %d has chain_seq %d: the stream must be in journal order", i, e.ChainSeq)
		}
	}

	// Paging: two reads of three must equal one read of six, with no overlap
	// and nothing skipped.
	first, err := j.EntriesSince(0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatalf("first page has %d entries, want 3", len(first))
	}
	second, err := j.EntriesSince(first[len(first)-1].ChainSeq, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 3 {
		t.Fatalf("second page has %d entries, want 3", len(second))
	}
	joined := append(append([]Entry{}, first...), second...)
	for i := range joined {
		if joined[i].ChainSeq != all[i].ChainSeq {
			t.Fatalf("paging lost or repeated an entry at %d", i)
		}
	}

	// At the head, nothing comes back -- and asking again after new entries
	// arrive picks up exactly those.
	caughtUp, err := j.EntriesSince(6, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(caughtUp) != 0 {
		t.Fatalf("a cursor at the head returned %d entries", len(caughtUp))
	}
	if err := j.Append(call("s1", 6, "later")); err != nil {
		t.Fatal(err)
	}
	fresh, err := j.EntriesSince(6, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].Tool == nil || *fresh[0].Tool != "later" {
		t.Fatalf("resuming from the cursor did not return just the new entry: %+v", fresh)
	}
}

func TestEntriesSinceCapsTheRead(t *testing.T) {
	j, _ := openTemp(t)
	if err := j.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		if err := j.Append(call("s1", int64(i), "t")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := j.EntriesSince(0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("limit ignored: got %d", len(got))
	}
	// Zero and negative mean the cap, not "everything at once".
	for _, limit := range []int{0, -1, MaxEntriesPerRead + 5000} {
		got, err := j.EntriesSince(0, limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 11 {
			t.Errorf("limit %d returned %d entries, want all 11 (under the cap)", limit, len(got))
		}
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

// TestConcurrentOpensDoNotCollideOnViews reproduces a failure CI found and a
// laptop did not: two handles opening the same database at once both saw a
// view as absent, both dropped it, and the second create failed with "view
// nim_calls already exists", taking down whichever process was opening the
// journal.
//
// It is not a contrived case. The daemon opens the journal to write, and a
// second daemon racing to start does the same before one of them loses the
// socket -- so this is exactly the window the startup lock exists around.
func TestConcurrentOpensDoNotCollideOnViews(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")

	// Seed the file so every opener below races on the same existing database
	// rather than on creating it.
	seed, err := Open(path, "machine")
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	seed.Close()

	// Drop a view, so every opener agrees it has to be recreated: that is the
	// state in which the drop-then-create actually runs.
	drop, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open for drop: %v", err)
	}
	if _, err := drop.Exec(`drop view if exists nim_calls`); err != nil {
		t.Fatalf("dropping the view: %v", err)
	}
	drop.Close()

	const openers = 8
	errs := make(chan error, openers)
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < openers; i++ {
		go func() {
			start.Wait()
			j, err := Open(path, "machine")
			if err != nil {
				errs <- err
				return
			}
			j.Close()
			errs <- nil
		}()
	}
	start.Done()

	for i := 0; i < openers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Open failed: %v", err)
		}
	}

	// And the view is usable afterwards, not merely un-erroring.
	j, err := Open(path, "machine")
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer j.Close()
	if _, err := j.db.Query(`select * from nim_calls limit 1`); err != nil {
		t.Fatalf("nim_calls is not usable after the race: %v", err)
	}
}

// seedChain appends n call.request entries and returns the head after each.
func seedChain(t *testing.T, j *Journal, n int) []string {
	t.Helper()
	heads := make([]string, 0, n)
	for i := 0; i < n; i++ {
		seq := int64(i + 1)
		tool, digest, decision := "t", "d", DecisionAllow
		if err := j.Append(Entry{
			Kind: KindCallRequest, SessionID: "s", Seq: &seq,
			Tool: &tool, ParamsDigest: &digest, Decision: &decision,
			OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		_, head, err := j.Head()
		if err != nil {
			t.Fatalf("Head: %v", err)
		}
		heads = append(heads, head)
	}
	return heads
}

// A journal that has grown since a head was recorded still contains it, and
// everything up to it is still covered -- each entry's hash commits to its
// predecessor. Reporting that as "entries have been removed and the chain
// recomputed" is an accusation rather than a finding, and it was what --expect-head
// did for every head recorded before the agent kept working. An operator told
// that every time stops reading it, which costs the one check that detects the
// attack this exists for.
func TestExpectHeadOnAGrownJournalIsNotAnAccusation(t *testing.T) {
	j, _ := openTemp(t)
	heads := seedChain(t, j, 5)
	recorded := heads[2] // three entries in, then the journal kept growing

	rep, err := j.Verify(recorded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("a journal that merely grew was reported as failing: %s", rep.Problem)
	}
	if rep.ExpectedHeadAt != 3 {
		t.Fatalf("expected head found at %d, want 3", rep.ExpectedHeadAt)
	}
	if rep.Head == recorded {
		t.Fatal("test is wrong: the journal did not actually grow past the recorded head")
	}
}

// The attack the check exists for: truncate and recompute. The recorded head
// is then nowhere in the chain, which is exactly what distinguishes it from
// growth.
func TestExpectHeadStillCatchesTruncateAndRecompute(t *testing.T) {
	j, _ := openTemp(t)
	heads := seedChain(t, j, 5)
	recorded := heads[4]

	// Remove the tail and recompute nothing -- the remaining chain is already
	// internally consistent, which is the whole difficulty.
	if _, err := j.db.Exec(`delete from nim_journal where chain_seq > 3`); err != nil {
		t.Fatalf("truncating: %v", err)
	}

	rep, err := j.Verify(recorded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK {
		t.Fatal("a truncated journal passed verification against a head it no longer contains")
	}
	if rep.ExpectedHeadAt != 0 {
		t.Fatalf("the removed head was reported as present at %d", rep.ExpectedHeadAt)
	}
	if !strings.Contains(rep.Problem, "nowhere in it") {
		t.Errorf("the problem should say the head is absent, got %q", rep.Problem)
	}
}

// Ending exactly at the recorded head is the strongest case and must stay
// distinguishable from having grown past it.
func TestExpectHeadMatchingTheTipReportsAtTheTip(t *testing.T) {
	j, _ := openTemp(t)
	heads := seedChain(t, j, 3)

	rep, err := j.Verify(heads[2])
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("unexpected failure: %s", rep.Problem)
	}
	if rep.ExpectedHeadAt != rep.Entries {
		t.Fatalf("head found at %d but chain has %d entries; the tip case must match",
			rep.ExpectedHeadAt, rep.Entries)
	}
}

// The rest of the --expect-head matrix. Growth and truncation are covered
// above; these are the cases where the two must not be confused with a third
// thing.

// An entry altered *before* the recorded head must fail on its own contents,
// not be excused by the head still being present further along. Content
// verification runs first for exactly this reason.
func TestExpectHeadDoesNotExcuseAnAlteredEarlierEntry(t *testing.T) {
	j, _ := openTemp(t)
	heads := seedChain(t, j, 5)
	recorded := heads[4] // the tip: everything is "covered" by it

	if _, err := j.db.Exec(`update nim_journal set tool = 'rewritten' where chain_seq = 2`); err != nil {
		t.Fatalf("altering entry 2: %v", err)
	}

	rep, err := j.Verify(recorded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK {
		t.Fatal("an altered entry passed because the expected head was still present")
	}
	if !strings.Contains(rep.Problem, "entry 2") {
		t.Errorf("the problem should name the altered entry, got %q", rep.Problem)
	}
}

// A head that was never in this chain at all is the plain forgery case: not a
// truncation, not growth, just wrong.
func TestExpectHeadThatWasNeverInTheChainFails(t *testing.T) {
	j, _ := openTemp(t)
	seedChain(t, j, 3)

	rep, err := j.Verify("00000000000000000000000000000000000000000000000000000000deadbeef")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK {
		t.Fatal("a head that is nowhere in the chain was accepted")
	}
	if rep.ExpectedHeadAt != 0 {
		t.Fatalf("a head that was never present was located at %d", rep.ExpectedHeadAt)
	}
}

// An empty journal cannot have the head you recorded, and saying "verified"
// about it would put "nothing happened" and "everything was deleted" on the
// same footing.
func TestExpectHeadAgainstAnEmptyJournalFails(t *testing.T) {
	j, _ := openTemp(t)

	rep, err := j.Verify("00000000000000000000000000000000000000000000000000000000deadbeef")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK {
		t.Fatal("an empty journal was reported as matching a recorded head")
	}
	if !rep.Empty {
		t.Error("an empty journal should report Empty")
	}
	if rep.Problem == "" {
		t.Error("an empty journal with an expected head must report a problem")
	}
}
