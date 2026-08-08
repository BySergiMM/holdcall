package readmodel

import (
	"testing"

	"github.com/BySergiMM/nim/engine/internal/journal"
)

// An empty journal has not been checked. Reporting it as sound would put it on
// the same footing as one that passed, and the two are not the same: an empty
// journal is indistinguishable from one whose every entry was lost.
func TestEmptyJournalIsNotReportedAsVerified(t *testing.T) {
	src := &fake{verify: journal.VerifyReport{Empty: true}, seedKnown: true}

	snap, err := Take(src)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Journal.Chain != ChainEmpty {
		t.Fatalf("chain = %q, want %q", snap.Journal.Chain, ChainEmpty)
	}
	if snap.Journal.Chain == ChainSelfConsistent {
		t.Error("an empty journal must never read as self-consistent")
	}
	if snap.Journal.Entries != 0 || snap.Journal.Head != "" {
		t.Errorf("unexpected state for an empty journal: %+v", snap.Journal)
	}
}

// A missing seed is missing verification material. It must not read as a fault
// in the journal, because it is not one.
func TestMissingSeedIsMaterialNotFault(t *testing.T) {
	src := &fake{
		head: 10, headHash: "abc",
		verify:    journal.VerifyReport{OK: true, Partial: true, Entries: 10, Head: "abc"},
		seedKnown: false,
	}

	snap, err := Take(src)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Journal.Chain != ChainPartial {
		t.Fatalf("chain = %q, want %q", snap.Journal.Chain, ChainPartial)
	}
	if snap.Journal.Chain == ChainBroken {
		t.Error("a missing seed must never be reported as a broken chain")
	}
	if snap.Journal.VerificationMaterial {
		t.Error("verification material should be reported as unavailable")
	}
	if snap.Journal.Problem != "" {
		t.Errorf("a partial check is not a problem to report: %q", snap.Journal.Problem)
	}
}

func TestChainStatesMapFromTheVerifyReport(t *testing.T) {
	cases := []struct {
		name   string
		report journal.VerifyReport
		seed   bool
		want   ChainState
	}{
		{"intact", journal.VerifyReport{OK: true, Entries: 3}, true, ChainSelfConsistent},
		{"partial", journal.VerifyReport{OK: true, Partial: true, Entries: 3}, false, ChainPartial},
		{"empty", journal.VerifyReport{Empty: true}, true, ChainEmpty},
		{"broken", journal.VerifyReport{Problem: "entry 2 has been altered"}, true, ChainBroken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap, err := Take(&fake{verify: c.report, seedKnown: c.seed})
			if err != nil {
				t.Fatal(err)
			}
			if snap.Journal.Chain != c.want {
				t.Errorf("chain = %q, want %q", snap.Journal.Chain, c.want)
			}
			if c.want == ChainBroken && snap.Journal.Problem == "" {
				t.Error("a broken chain must say where")
			}
		})
	}
}

func TestSnapshotCountsGapsAndAnomalies(t *testing.T) {
	src := &fake{
		verify:    journal.VerifyReport{OK: true, Entries: 20},
		seedKnown: true,
		callCount: 7,
		loss: journal.LossReport{
			UnfinishedSessions: 2, SessionsWithGaps: 1, MissingCallEntries: 3,
		},
		anomalies: map[string]int{"batch": 2, "malformed_json": 1},
	}

	snap, err := Take(src)
	if err != nil {
		t.Fatal(err)
	}
	if snap.CallsRecorded != 7 {
		t.Errorf("calls recorded = %d, want 7", snap.CallsRecorded)
	}
	if snap.Gaps.UnfinishedSessions != 2 || snap.Gaps.MissingCallEntries != 3 {
		t.Errorf("gaps not carried: %+v", snap.Gaps)
	}
	if snap.Gaps.AnomaliesTotal != 3 {
		t.Errorf("anomalies total = %d, want 3", snap.Gaps.AnomaliesTotal)
	}
}

// A snapshot with no anomalies must still encode as an object rather than null,
// so a reader can index it without checking.
func TestAnomaliesAreNeverNil(t *testing.T) {
	snap, err := Take(&fake{verify: journal.VerifyReport{Empty: true}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Gaps.Anomalies == nil {
		t.Fatal("anomalies must be an empty map, not nil")
	}
}

// The journal cannot tell a session that is running from one whose daemon died.
// Both are unfinished, and neither is "active".
func TestSessionStateIsUnfinishedNotActive(t *testing.T) {
	ended := "2026-08-07T10:05:00Z"

	running := SessionFrom(journal.SessionRow{ID: "a", StartedAt: "2026-08-07T10:00:00Z"})
	if running.State != SessionUnfinished {
		t.Errorf("state = %q, want %q", running.State, SessionUnfinished)
	}
	if string(running.State) == "active" {
		t.Error("a session must never be described as active")
	}
	if running.EndedAt != nil {
		t.Error("an unfinished session has no end")
	}

	done := SessionFrom(journal.SessionRow{ID: "b", StartedAt: "x", EndedAt: &ended})
	if done.State != SessionEnded {
		t.Errorf("state = %q, want %q", done.State, SessionEnded)
	}
	if done.EndedAt == nil || *done.EndedAt != ended {
		t.Error("an ended session must carry its end")
	}
}

func TestSessionProjectionCarriesWhatIsKnown(t *testing.T) {
	connector, client, machine := "github", "claude-code", "m1"
	s := SessionFrom(journal.SessionRow{
		ChainSeq: 4, ID: "sess", StartedAt: "t",
		Connector: &connector, Client: &client, MachineID: &machine,
		CallsRecorded: 5, Outcomes: 4, Anomalies: 1,
	})

	if s.ChainSeq != 4 || s.CallsRecorded != 5 || s.Outcomes != 4 || s.Anomalies != 1 {
		t.Errorf("counts lost: %+v", s)
	}
	if s.Connector == nil || *s.Connector != "github" {
		t.Error("connector lost")
	}
	// Copies, not aliases: a reader must not be able to reach the source.
	*s.Connector = "changed"
	if connector != "github" {
		t.Error("the projection aliased its input")
	}
}

func TestSessionsListsNewestFirstAndDetailFindsOne(t *testing.T) {
	rows := []journal.SessionRow{
		{ChainSeq: 9, ID: "b", StartedAt: "t2"},
		{ChainSeq: 1, ID: "a", StartedAt: "t1"},
	}
	src := &fake{
		sessions: rows,
		entries: []journal.Entry{
			{ChainSeq: 1, Kind: journal.KindSessionStart, SessionID: "a", OccurredAt: "t1"},
			{ChainSeq: 2, Kind: journal.KindCallRequest, SessionID: "a", OccurredAt: "t1"},
			{ChainSeq: 9, Kind: journal.KindSessionStart, SessionID: "b", OccurredAt: "t2"},
		},
	}

	list, err := Sessions(src, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "b" {
		t.Fatalf("sessions not in newest-first order: %+v", list)
	}

	detail, found, err := Detail(src, "a", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("session a was not found")
	}
	if detail.Session.ID != "a" {
		t.Errorf("wrong session summary: %+v", detail.Session)
	}
	if len(detail.Events) != 2 {
		t.Fatalf("got %d events for session a, want 2", len(detail.Events))
	}

	if _, found, err = Detail(src, "nope", 100); err != nil || found {
		t.Error("an unknown session must report not found without an error")
	}
}
