package readmodel

import (
	"testing"

	"github.com/BySergiMM/holdcall/engine/internal/journal"
)

// The whole table, written out. Every cell is a claim about what the journal
// means, and the two that matter most are the ones that look alike: a call with
// no outcome is pending if it was allowed and denied if it was not, and reading
// the second as the first would report a refusal as work in progress.
func TestCallStateTable(t *testing.T) {
	cases := []struct {
		decision   string
		hasOutcome bool
		want       CallState
	}{
		{journal.DecisionObserved, true, CallCompleted},
		{journal.DecisionObserved, false, CallPending},

		{journal.DecisionAllow, true, CallCompleted},
		{journal.DecisionAllow, false, CallPending},

		{journal.DecisionDeny, false, CallDenied},
		{journal.DecisionDeny, true, CallInconsistent},

		// Reserved for human approval. M2 writes neither, but a reader that
		// meets one should not have to guess.
		{journal.DecisionApproved, true, CallCompleted},
		{journal.DecisionApproved, false, CallPending},
		{journal.DecisionRejected, false, CallDenied},
		{journal.DecisionRejected, true, CallInconsistent},

		// A decision the journal never held at all, from a row written before
		// the column meant anything.
		{"", true, CallCompleted},
		{"", false, CallPending},
	}

	for _, c := range cases {
		if got := CallStateOf(c.decision, c.hasOutcome); got != c.want {
			t.Errorf("CallStateOf(%q, %v) = %q, want %q", c.decision, c.hasOutcome, got, c.want)
		}
	}
}

// A refusal is never pending and never a gap. Those are the two readings that
// would turn "Holdcall did its job" into "Holdcall lost something".
func TestARefusalIsNeitherPendingNorMissing(t *testing.T) {
	if s := CallStateOf(journal.DecisionDeny, false); s == CallPending {
		t.Error("a refused call reads as still running")
	}
	if s := CallStateOf(journal.DecisionDeny, false); s != CallDenied {
		t.Errorf("a refused call reads as %q", s)
	}
}

// Four states, and no fifth arriving quietly. Each is a claim, and a new one
// should have to be argued for.
func TestCallVocabularyIsClosed(t *testing.T) {
	want := map[CallState]string{
		CallCompleted:    "completed",
		CallPending:      "pending",
		CallDenied:       "denied",
		CallInconsistent: "inconsistent",
	}
	for state, text := range want {
		if string(state) != text {
			t.Errorf("call state changed: %q, want %q", state, text)
		}
	}

	// Nothing here may claim a call ran. "completed" is the strongest thing the
	// journal supports, and it means an outcome was recorded, not that work was
	// done successfully.
	for state := range want {
		for _, forbidden := range []string{"executed", "succeeded", "blocked", "ran"} {
			if string(state) == forbidden {
				t.Errorf("call state %q claims more than the journal holds", state)
			}
		}
	}
}
