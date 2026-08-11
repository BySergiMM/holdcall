package daemon

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/mcp"
)

// ask sends one call.request and reads the answer.
func ask(t *testing.T, conn net.Conn, ev Event) Decision {
	t.Helper()
	if err := json.NewEncoder(conn).Encode(ev); err != nil {
		t.Fatalf("asking: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := mcp.NewReader(conn).ReadRaw()
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	var d Decision
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("the answer is not a decision: %v (%s)", err, raw)
	}
	return d
}

// A tool on the list is refused; anything else is allowed. That is the whole of
// the decision, and it is meant to stay that way.
func TestTheDenyListDecides(t *testing.T) {
	cfg, dbPath := startWithPolicy(t, config.Policy{Deny: []string{"dangerous_tool", "rm"}})

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m",
		Connector: "github", OccurredAt: now()})

	cases := []struct {
		tool string
		want string
	}{
		{"echo", journal.DecisionAllow},
		{"dangerous_tool", journal.DecisionDeny},
		{"rm", journal.DecisionDeny},
		// Exact names only. No prefixes, no patterns, no case folding: a deny
		// list that guesses is a policy engine with no design behind it.
		{"dangerous_tool_2", journal.DecisionAllow},
		{"Dangerous_Tool", journal.DecisionAllow},
		{"", journal.DecisionAllow},
	}

	for i, c := range cases {
		d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: i + 1,
			Tool: c.tool, Digest: "d", OccurredAt: now()})
		if d.Kind != KindDecision {
			t.Errorf("%q: kind = %q", c.tool, d.Kind)
		}
		if d.SessionID != "s1" || d.Seq != i+1 {
			t.Errorf("%q: the answer names %s/%d, not the call that was asked", c.tool, d.SessionID, d.Seq)
		}
		if d.Decision != c.want {
			t.Errorf("%q: decided %q, want %q", c.tool, d.Decision, c.want)
		}
		if c.want == journal.DecisionDeny && d.Reason == "" {
			t.Errorf("%q: refused with no reason given", c.tool)
		}
	}

	// Every decision is on the record, carried by the call it belongs to.
	j := openJournal(t, dbPath)
	if got := waitFor(t, j, int64(1+len(cases))); got != int64(1+len(cases)) {
		t.Fatalf("journal has %d entries, want %d", got, 1+len(cases))
	}
	calls, err := j.RecentCalls(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != len(cases) {
		t.Fatalf("%d calls recorded, want %d", len(calls), len(cases))
	}
	for _, c := range calls {
		if c.Decision != journal.DecisionAllow && c.Decision != journal.DecisionDeny {
			t.Errorf("recorded decision %q, which M2 does not write", c.Decision)
		}
		if c.HasOutcome {
			t.Errorf("an outcome appeared for a call nobody answered: %+v", c)
		}
	}
}

// The decision is in the journal before the shim hears it. Otherwise a relay
// could forward a call on the strength of an allowance that was never written,
// and the guarantee -- forwarded means recorded -- would not hold.
func TestTheEntryIsWrittenBeforeTheAnswerIsSent(t *testing.T) {
	cfg, dbPath := startWithPolicy(t, config.Policy{})

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m",
		Connector: "github", OccurredAt: now()})

	j := openJournal(t, dbPath)
	if got := waitFor(t, j, 1); got != 1 {
		t.Fatalf("the session was not recorded (%d entries)", got)
	}

	d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1,
		Tool: "echo", Digest: "d", OccurredAt: now()})
	if d.Decision != journal.DecisionAllow {
		t.Fatalf("decided %q", d.Decision)
	}

	// No waiting: by the time the answer arrived the entry was already there.
	length, _, err := j.Head()
	if err != nil {
		t.Fatal(err)
	}
	if length != 2 {
		t.Errorf("journal has %d entries when the answer arrived, want 2: the allowance "+
			"was sent before it was recorded", length)
	}
}

// If the entry cannot be written the answer is deny. An allow that nothing
// recorded is the one answer this milestone must never give.
func TestAnUnwritableJournalRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")
	w, err := journal.Open(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	w.Close()

	// A read-only handle refuses writes exactly as a full disk or a locked file
	// would, without needing either.
	j, err := journal.OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	mine, theirs := net.Pipe()
	defer mine.Close()
	defer theirs.Close()

	go func() {
		answer(theirs, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1,
			Tool: "echo", Digest: "d", OccurredAt: now()}, j, config.Policy{}, "")
	}()

	mine.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := mcp.NewReader(mine).ReadRaw()
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	var d Decision
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	// Not journal.DecisionDeny: that is a decision that was actually reached
	// and recorded. Nothing was recorded here, so the answer must say so
	// distinctly -- a shim that read this as a policy verdict would tell the
	// client not to retry a failure that may already have cleared.
	if d.Decision != DecisionUndecided {
		t.Errorf("decided %q on a journal that cannot be written; want %q", d.Decision, DecisionUndecided)
	}
}

// A refused call is one entry, and the pair it never formed is not a hole.
func TestARefusedCallIsNotAGap(t *testing.T) {
	cfg, dbPath := startWithPolicy(t, config.Policy{Deny: []string{"rm"}})

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m",
		Connector: "github", OccurredAt: now()})

	ok := true
	ms := 3
	for i, tool := range []string{"echo", "rm", "echo", "rm", "rm"} {
		ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: i + 1,
			Tool: tool, Digest: "d", OccurredAt: now()})
		if tool != "rm" {
			send(t, conn, Event{Kind: KindCallOutcome, SessionID: "s1", Seq: i + 1,
				OK: &ok, DurationMS: &ms, OccurredAt: now()})
		}
	}
	send(t, conn, Event{Kind: KindSessionEnd, SessionID: "s1", OccurredAt: now()})

	j := openJournal(t, dbPath)
	if got := waitFor(t, j, 9); got != 9 { // start + 5 requests + 2 outcomes + end
		t.Fatalf("journal has %d entries, want 9", got)
	}
	conn.Close()

	loss, err := j.Loss()
	if err != nil {
		t.Fatal(err)
	}
	if loss.MissingCallEntries != 0 || loss.SessionsWithGaps != 0 {
		t.Errorf("three refused calls were reported as losses: %+v", loss)
	}
	if loss.UnfinishedSessions != 0 {
		t.Errorf("the session was left unfinished: %+v", loss)
	}

	rep, err := j.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Errorf("the chain does not verify with decisions in it: %s", rep.Problem)
	}

	sessions, err := j.Sessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("%d sessions, want 1", len(sessions))
	}
	s := sessions[0]
	if s.CallsRecorded != 5 || s.Denied != 3 || s.Outcomes != 2 {
		t.Errorf("session summary is recorded=%d denied=%d outcomes=%d, want 5/3/2",
			s.CallsRecorded, s.Denied, s.Outcomes)
	}
}
