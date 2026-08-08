package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/console"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/readmodel"
)

func sp(s string) *string { return &s }
func ip(v int64) *int64   { return &v }
func bp(v bool) *bool     { return &v }
func now() string         { return time.Now().UTC().Format(time.RFC3339Nano) }

// fixture writes a journal with something of everything worth disagreeing
// about: a finished session, an unfinished one, a gap in a sequence, a failed
// outcome and an anomaly.
func fixture(t *testing.T) *journal.Journal {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nim.db")

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

	add(journal.Entry{Kind: journal.KindSessionStart, SessionID: "done",
		MachineID: sp("test-machine"), Client: sp("cli"), Connector: sp("github"), OccurredAt: now()})
	for i := int64(1); i <= 2; i++ {
		add(journal.Entry{Kind: journal.KindCallRequest, SessionID: "done", Seq: ip(i),
			Tool: sp(fmt.Sprintf("tool%d", i)), ParamsDigest: sp("d"),
			Decision: sp(journal.DecisionObserved), OccurredAt: now()})
		add(journal.Entry{Kind: journal.KindCallOutcome, SessionID: "done", Seq: ip(i),
			OK: bp(i == 1), DurationMS: ip(3), OccurredAt: now()})
	}
	add(journal.Entry{Kind: journal.KindSessionEnd, SessionID: "done", OccurredAt: now()})

	// Unfinished, with seq 2 never recorded and one anomaly.
	add(journal.Entry{Kind: journal.KindSessionStart, SessionID: "open",
		MachineID: sp("test-machine"), Connector: sp("slack"), OccurredAt: now()})
	for _, seq := range []int64{1, 3} {
		add(journal.Entry{Kind: journal.KindCallRequest, SessionID: "open", Seq: ip(seq),
			Tool: sp("t"), ParamsDigest: sp("d"),
			Decision: sp(journal.DecisionObserved), OccurredAt: now()})
	}
	add(journal.Entry{Kind: journal.KindAnomaly, SessionID: "open",
		Anomaly: sp("batch"), OccurredAt: now()})
	w.Close()

	reader, err := journal.OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	return reader
}

// number pulls a labelled integer out of the CLI's text.
func number(t *testing.T, text, label string) int {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(label) + `\s*(\d+)`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("could not find %q in:\n%s", label, text)
	}
	var n int
	fmt.Sscanf(m[1], "%d", &n)
	return n
}

// One journal, two surfaces. Both must derive the same values, because both now
// read the same projection -- this test is what keeps that true when somebody
// changes one of them.
func TestCLIAndConsoleAgree(t *testing.T) {
	j := fixture(t)

	// CLI: the same call runStatus makes, rendered to a buffer.
	snap, err := readmodel.Take(j)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	renderStatus(&out, snap)
	text := out.String()

	// Console: the same journal over HTTP.
	srv := httptest.NewServer(console.New(j, "/nonexistent.sock").Handler())
	defer srv.Close()

	res, err := srv.Client().Get(srv.URL + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var api struct {
		Journal       readmodel.JournalState `json:"journal"`
		CallsRecorded int                    `json:"calls_recorded"`
		Gaps          readmodel.Gaps         `json:"gaps"`
		Sessions      []readmodel.Session    `json:"sessions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&api); err != nil {
		t.Fatal(err)
	}

	// calls recorded means the same thing on both.
	if got := number(t, text, "calls    recorded"); got != api.CallsRecorded {
		t.Errorf("calls recorded: CLI says %d, console says %d", got, api.CallsRecorded)
	}
	if api.CallsRecorded != 4 {
		t.Errorf("calls recorded = %d, want 4 (requests only, outcomes are not calls)", api.CallsRecorded)
	}

	// entries, and chain_seq as the authoritative order.
	if got := int64(number(t, text, "entries       ")); got != api.Journal.Entries {
		t.Errorf("entries: CLI says %d, console says %d", got, api.Journal.Entries)
	}
	if !strings.Contains(text, api.Journal.Head) {
		t.Error("the CLI and the console disagree about the head")
	}

	// The chain reads the same on both.
	if api.Journal.Chain != readmodel.ChainSelfConsistent {
		t.Fatalf("fixture chain = %q", api.Journal.Chain)
	}
	if !strings.Contains(text, "chain          self-consistent") {
		t.Errorf("CLI did not report a self-consistent chain:\n%s", text)
	}

	// unfinished means the same thing on both.
	if got := number(t, text, "unfinished     "); got != api.Gaps.UnfinishedSessions {
		t.Errorf("unfinished: CLI says %d, console says %d", got, api.Gaps.UnfinishedSessions)
	}
	if api.Gaps.UnfinishedSessions != 1 {
		t.Errorf("unfinished sessions = %d, want 1", api.Gaps.UnfinishedSessions)
	}
	unfinished := 0
	for _, s := range api.Sessions {
		if s.State == readmodel.SessionUnfinished {
			unfinished++
		}
	}
	if unfinished != api.Gaps.UnfinishedSessions {
		t.Errorf("the session list says %d unfinished, the gap count says %d",
			unfinished, api.Gaps.UnfinishedSessions)
	}

	// detectable gaps mean the same thing on both.
	if got := number(t, text, "gaps           "); got != api.Gaps.MissingCallEntries {
		t.Errorf("missing call entries: CLI says %d, console says %d", got, api.Gaps.MissingCallEntries)
	}
	if api.Gaps.MissingCallEntries != 1 {
		t.Errorf("missing call entries = %d, want 1 (seq 2 of the open session)", api.Gaps.MissingCallEntries)
	}

	// anomalies too.
	if got := number(t, text, "anomaly        batch"); got != api.Gaps.Anomalies["batch"] {
		t.Errorf("anomalies: CLI says %d, console says %d", got, api.Gaps.Anomalies["batch"])
	}
}

// chain_seq orders the stream, whatever the timestamps say.
func TestChainSeqIsTheAuthoritativeOrderOnBothSurfaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")
	w, err := journal.Open(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	w.Append(journal.Entry{Kind: journal.KindSessionStart, SessionID: "s",
		MachineID: sp("m"), Connector: sp("c"), OccurredAt: "2030-01-01T00:00:00Z"})
	// Deliberately backwards in time.
	for i, stamp := range []string{"1999-01-01T00:00:00Z", "2010-01-01T00:00:00Z"} {
		w.Append(journal.Entry{Kind: journal.KindCallRequest, SessionID: "s",
			Seq: ip(int64(i + 1)), Tool: sp(fmt.Sprintf("tool%d", i+1)),
			ParamsDigest: sp("d"), Decision: sp(journal.DecisionObserved), OccurredAt: stamp})
	}
	w.Close()

	j, err := journal.OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	// readmodel, which both surfaces stream from.
	page, err := readmodel.Stream(j, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range page.Events {
		if e.ChainSeq != int64(i+1) {
			t.Fatalf("event %d has chain_seq %d: order followed the clock, not the journal", i, e.ChainSeq)
		}
	}

	// And over HTTP.
	srv := httptest.NewServer(console.New(j, "/nonexistent.sock").Handler())
	defer srv.Close()
	res, err := srv.Client().Get(srv.URL + "/api/events?since=0")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var api readmodel.Page
	if err := json.NewDecoder(res.Body).Decode(&api); err != nil {
		t.Fatal(err)
	}
	if len(api.Events) != len(page.Events) {
		t.Fatalf("console returned %d events, readmodel %d", len(api.Events), len(page.Events))
	}
	for i := range api.Events {
		if api.Events[i].ChainSeq != page.Events[i].ChainSeq {
			t.Errorf("event %d differs: console %d, readmodel %d",
				i, api.Events[i].ChainSeq, page.Events[i].ChainSeq)
		}
	}
}

// An empty journal must read the same on both, and as nothing rather than as
// something that passed.
func TestEmptyJournalAgreesOnBothSurfaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")
	w, err := journal.Open(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
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
	if snap.Journal.Chain != readmodel.ChainEmpty {
		t.Fatalf("chain = %q, want empty", snap.Journal.Chain)
	}

	var out bytes.Buffer
	renderStatus(&out, snap)
	if !strings.Contains(out.String(), "nothing recorded yet -- nothing to check") {
		t.Errorf("the CLI dressed up an empty journal:\n%s", out.String())
	}
	for _, forbidden := range []string{"self-consistent", "verified"} {
		if strings.Contains(out.String(), "chain          "+forbidden) {
			t.Errorf("an empty journal was reported as %q", forbidden)
		}
	}
}
