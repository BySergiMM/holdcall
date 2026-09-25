package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/BySergiMM/holdcall/engine/internal/console"
	"github.com/BySergiMM/holdcall/engine/internal/journal"
	"github.com/BySergiMM/holdcall/engine/internal/readmodel"
)

// decided builds a journal with everything a reader has to tell apart: an
// allowed call that finished, an allowed one still out, a refused one, and a
// call from before decisions existed.
func decided(t *testing.T) (*journal.Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "holdcall.db")

	w, err := journal.Open(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	add := func(e journal.Entry) {
		t.Helper()
		if err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	call := func(seq int64, tool, decision string) {
		add(journal.Entry{Kind: journal.KindCallRequest, SessionID: "s", Seq: ip(seq),
			Tool: sp(tool), ParamsDigest: sp("d"), Decision: sp(decision), OccurredAt: now()})
	}

	add(journal.Entry{Kind: journal.KindSessionStart, SessionID: "s",
		MachineID: sp("test-machine"), Client: sp("cli"), Connector: sp("github"), OccurredAt: now()})

	call(1, "echo", journal.DecisionAllow)
	add(journal.Entry{Kind: journal.KindCallOutcome, SessionID: "s", Seq: ip(1),
		OK: bp(true), DurationMS: ip(4), OccurredAt: now()})

	call(2, "dangerous_tool", journal.DecisionDeny) // refused: no outcome will follow
	call(3, "slow", journal.DecisionAllow)          // allowed, still out
	call(4, "legacy", journal.DecisionObserved)     // written before anything was decided
	add(journal.Entry{Kind: journal.KindCallOutcome, SessionID: "s", Seq: ip(4),
		OK: bp(false), DurationMS: ip(9), OccurredAt: now()})

	add(journal.Entry{Kind: journal.KindSessionEnd, SessionID: "s", OccurredAt: now()})
	w.Close()

	reader, err := journal.OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	return reader, path
}

// A refusal must not be reported as a gap, and the chain must not care that
// decisions are in it now.
func TestDecisionsAreNotLosses(t *testing.T) {
	j, _ := decided(t)

	snap, err := readmodel.Take(j)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Gaps.MissingCallEntries != 0 || snap.Gaps.SessionsWithGaps != 0 {
		t.Errorf("a refused call was counted as missing: %+v", snap.Gaps)
	}
	if snap.Gaps.UnfinishedSessions != 0 {
		t.Errorf("the session reads as unfinished: %+v", snap.Gaps)
	}
	if snap.Journal.Chain != readmodel.ChainSelfConsistent {
		t.Errorf("chain = %q with decisions recorded, want self-consistent", snap.Journal.Chain)
	}
	// Four calls, one of them refused. Recorded is not executed and never was.
	if snap.CallsRecorded != 4 {
		t.Errorf("calls recorded = %d, want 4", snap.CallsRecorded)
	}
}

// Every call reads as the same thing in `holdcall log` and in the console, because
// both ask the same function. This is the test that keeps them from drifting.
func TestCLIAndConsoleAgreeOnEveryCallState(t *testing.T) {
	j, _ := decided(t)

	// What `holdcall log` derives, from the row the CLI reads.
	calls, err := j.RecentCalls(50)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("%d calls, want 4", len(calls))
	}
	cli := map[int64]readmodel.CallState{}
	for _, c := range calls {
		cli[c.ChainSeq] = readmodel.CallStateOf(c.Decision, c.HasOutcome)
	}

	// What the console serves, from the entries it streams.
	srv := httptest.NewServer(console.New(j, "/nonexistent.sock").Handler())
	defer srv.Close()
	res, err := srv.Client().Get(srv.URL + "/api/sessions/s")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var detail struct {
		Session readmodel.Session `json:"session"`
		Events  []readmodel.Event `json:"events"`
	}
	if err := json.NewDecoder(res.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}

	// Pair each request with its outcome the way a reader of the stream must.
	outcomes := map[int64]bool{}
	for _, e := range detail.Events {
		if e.Kind == journal.KindCallOutcome && e.Seq != nil {
			outcomes[*e.Seq] = true
		}
	}
	web := map[int64]readmodel.CallState{}
	for _, e := range detail.Events {
		if e.Kind != journal.KindCallRequest || e.Seq == nil {
			continue
		}
		decision := ""
		if e.Decision != nil {
			decision = *e.Decision
		}
		web[e.ChainSeq] = readmodel.CallStateOf(decision, outcomes[*e.Seq])
	}

	if len(cli) != len(web) {
		t.Fatalf("the CLI sees %d calls and the console %d", len(cli), len(web))
	}
	for seq, state := range cli {
		if web[seq] != state {
			t.Errorf("entry %d: CLI says %q, console says %q", seq, state, web[seq])
		}
	}

	want := map[readmodel.CallState]int{
		readmodel.CallCompleted: 2, // the allowed one that finished, and the legacy one
		readmodel.CallDenied:    1,
		readmodel.CallPending:   1,
	}
	got := map[readmodel.CallState]int{}
	for _, state := range cli {
		got[state]++
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("states = %v, want %v", got, want)
	}

	// The session summary has to carry the refusal, or calls recorded minus
	// outcomes reads as something gone missing.
	if detail.Session.CallsRecorded != 4 || detail.Session.Denied != 1 || detail.Session.Outcomes != 2 {
		t.Errorf("session summary recorded=%d denied=%d outcomes=%d, want 4/1/2",
			detail.Session.CallsRecorded, detail.Session.Denied, detail.Session.Outcomes)
	}
}

// A journal written before enforcement must keep reading correctly. Those rows
// say "observed", which means nothing was decided -- and the relay forwarded
// them anyway, so they read like allowances.
func TestAJournalFromBeforeEnforcementStillReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holdcall.db")
	w, err := journal.Open(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	w.Append(journal.Entry{Kind: journal.KindSessionStart, SessionID: "old",
		MachineID: sp("test-machine"), Connector: sp("github"), OccurredAt: now()})
	w.Append(journal.Entry{Kind: journal.KindCallRequest, SessionID: "old", Seq: ip(1),
		Tool: sp("echo"), ParamsDigest: sp("d"),
		Decision: sp(journal.DecisionObserved), OccurredAt: now()})
	w.Append(journal.Entry{Kind: journal.KindCallOutcome, SessionID: "old", Seq: ip(1),
		OK: bp(true), DurationMS: ip(2), OccurredAt: now()})
	w.Close()

	j, err := journal.OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	snap, err := readmodel.Take(j)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Journal.Chain != readmodel.ChainSelfConsistent {
		t.Errorf("an older journal no longer verifies: %q %s", snap.Journal.Chain, snap.Journal.Problem)
	}

	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("%d calls, want 1", len(calls))
	}
	if calls[0].Decision != journal.DecisionObserved {
		t.Errorf("decision = %q, want it left as written", calls[0].Decision)
	}
	if state := readmodel.CallStateOf(calls[0].Decision, calls[0].HasOutcome); state != readmodel.CallCompleted {
		t.Errorf("an observed call with an outcome reads as %q, want completed", state)
	}

	sessions, err := j.Sessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Denied != 0 {
		t.Errorf("denied = %d in a journal where nothing was ever decided", sessions[0].Denied)
	}
}
