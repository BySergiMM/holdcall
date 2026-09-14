package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A tool a rule names is refused; anything else is allowed. That is the whole
// of the decision for a session no enrolment matched, and the rules are read
// from where the daemon keeps them, not from a file.
func TestRulesDecide(t *testing.T) {
	cfg, dbPath := start(t)
	deny(t, cfg, "dangerous_tool", "", "")
	deny(t, cfg, "rm", "", "")

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
		// Exact names only. No prefixes, no patterns, no case folding: a rule
		// that guesses is a policy engine with no design behind it.
		{"dangerous_tool_2", journal.DecisionAllow},
		{"Dangerous_Tool", journal.DecisionAllow},
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

	// Every decision is on the record, carried by the call it belongs to --
	// after the two rule.add entries the rules themselves left.
	j := openJournal(t, dbPath)
	if got := waitFor(t, j, int64(3+len(cases))); got != int64(3+len(cases)) {
		t.Fatalf("journal has %d entries, want %d", got, 3+len(cases))
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
	cfg, dbPath := start(t)

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
			Tool: "echo", Digest: "d", OccurredAt: now()}, j, "", "")
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
	cfg, dbPath := start(t)
	deny(t, cfg, "rm", "", "")

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
	if got := waitFor(t, j, 10); got != 10 { // rule + start + 5 requests + 2 outcomes + end
		t.Fatalf("journal has %d entries, want 10", got)
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

// The property M4 exists to establish: two agents against one connector
// receive different verdicts, and the record distinguishes them. Here the
// derivation is stood in for -- answer() is given the agent the daemon would
// have derived -- and cmd/nim's end-to-end test does it with two real client
// programs.
func TestTwoAgentsAgainstOneConnectorReceiveDifferentVerdicts(t *testing.T) {
	cfg, dbPath := start(t)
	enrol(t, dbPath, "cursor")
	enrol(t, dbPath, "claude-code")
	deny(t, cfg, "delete_repository", "cursor", "")

	j := openJournal(t, dbPath)
	ask := func(agent string) string {
		t.Helper()
		mine, theirs := net.Pipe()
		defer mine.Close()
		defer theirs.Close()
		go answer(theirs, Event{Kind: KindCallRequest, SessionID: agent + "-s", Seq: 1,
			Tool: "delete_repository", Digest: "d", OccurredAt: now()}, j, agent, "github")
		mine.SetReadDeadline(time.Now().Add(5 * time.Second))
		raw, err := mcp.NewReader(mine).ReadRaw()
		if err != nil {
			t.Fatalf("no answer for %s: %v", agent, err)
		}
		var d Decision
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		return d.Decision
	}

	if got := ask("cursor"); got != journal.DecisionDeny {
		t.Errorf("cursor was %s; the rule names it", got)
	}
	if got := ask("claude-code"); got != journal.DecisionAllow {
		t.Errorf("claude-code was %s; no rule names it", got)
	}
	// An unknown agent meets only the rules that name no agent. This is
	// D-002, decided in docs/decisions/0002: rules only deny, so an unenrolled
	// program has at most what the global rules allow, never more than it had
	// before agents existed.
	if got := ask(""); got != journal.DecisionAllow {
		t.Errorf("an unknown agent was %s under a rule scoped to cursor", got)
	}
}

// A rule scoped to a connector reaches sessions on that connector and no
// other, and the connector is the one the session started with -- it is not
// on the wire per call, so a call cannot name a friendlier one.
func TestARuleScopedToAConnectorStopsAtIt(t *testing.T) {
	cfg, dbPath := start(t)
	deny(t, cfg, "force_push", "", "github")

	verdicts := map[string]string{}
	for i, connector := range []string{"github", "gitlab"} {
		conn, err := net.Dial("unix", cfg.Daemon.Socket)
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("s%d", i)
		send(t, conn, Event{Kind: KindSessionStart, SessionID: id, MachineID: "m", Connector: connector, OccurredAt: now()})
		d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: id, Seq: 1, Tool: "force_push", Digest: "d", OccurredAt: now()})
		verdicts[connector] = d.Decision
		conn.Close()
	}
	if verdicts["github"] != journal.DecisionDeny || verdicts["gitlab"] != journal.DecisionAllow {
		t.Fatalf("verdicts = %v, want github denied and gitlab allowed", verdicts)
	}
	_ = dbPath
}

// The rules are read from SQLite and changed through the daemon; config.toml
// has no say. A rule added, then removed, is gone for the next call -- and
// both changes are in the chain beside the calls they affected.
func TestAPolicyChangeTakesEffectAndIsRecorded(t *testing.T) {
	cfg, dbPath := start(t)

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})

	if d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "rm", Digest: "d", OccurredAt: now()}); d.Decision != journal.DecisionAllow {
		t.Fatalf("before any rule: %s", d.Decision)
	}
	deny(t, cfg, "rm", "", "")
	if d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 2, Tool: "rm", Digest: "d", OccurredAt: now()}); d.Decision != journal.DecisionDeny {
		t.Fatalf("after the rule: %s", d.Decision)
	}

	rc, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := SendRequest(rc, Request{ID: "r", Kind: KindPolicyRemove, RuleTool: "rm"}); err != nil || resp.Error != "" {
		t.Fatalf("policy.remove: %v %s", err, resp.Error)
	}
	rc.Close()
	if d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 3, Tool: "rm", Digest: "d", OccurredAt: now()}); d.Decision != journal.DecisionAllow {
		t.Fatalf("after the rule was removed: %s", d.Decision)
	}

	j := openJournal(t, dbPath)
	waitFor(t, j, 6)
	entries, err := j.EntriesSince(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range entries {
		kinds = append(kinds, e.Kind)
	}
	want := []string{KindSessionStart, KindCallRequest, journal.KindRuleAdd, KindCallRequest, journal.KindRuleRemove, KindCallRequest}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("the chain reads %v, want %v", kinds, want)
	}
	if rep, _ := j.Verify(""); !rep.OK {
		t.Fatalf("the chain does not verify with policy changes in it: %s", rep.Problem)
	}
}

// A rule can only name an agent that is enrolled, and a policy connection
// cannot pivot to another purpose.
func TestARuleCannotNameAnUnenrolledAgentAndPolicyIsItsOwnPurpose(t *testing.T) {
	cfg, dbPath := start(t)

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	resp, err := SendRequest(conn, Request{ID: "1", Kind: KindPolicyDeny, RuleTool: "rm", RuleAgent: "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == "" || !strings.Contains(resp.Error, "nim agent add") {
		t.Fatalf("a rule for an unenrolled agent was accepted or badly refused: %q", resp.Error)
	}
	for _, bad := range []Request{
		{ID: "2", Kind: KindPolicyDeny, RuleTool: ""},
		{ID: "3", Kind: KindPolicyDeny, RuleTool: "a\x00b"},
		{ID: "4", Kind: KindPolicyDeny, RuleTool: "rm", RuleConnector: "../etc"},
		{ID: "5", Kind: KindPolicyDeny, RuleTool: "rm", RuleAgent: "a/b"},
	} {
		resp, err := SendRequest(conn, bad)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error == "" {
			t.Errorf("%+v was accepted", bad)
		}
	}
	// The connection is a policy connection now; anything else is unauthorized.
	resp, err = SendRequest(conn, Request{ID: "6", Kind: KindCredentialGet, Target: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != "unauthorized" {
		t.Errorf("a policy connection was allowed to ask for a credential: %q", resp.Error)
	}

	// Nothing above changed anything.
	j := openJournal(t, dbPath)
	if rules, _ := j.ListRules(); len(rules) != 0 {
		t.Fatalf("%d rules were stored from refused requests", len(rules))
	}
	if n, _, _ := j.Head(); n != 0 {
		t.Fatalf("%d entries were written for refused requests", n)
	}
}

// A session id names one session. A second connection announcing a start
// under an id that is live -- or that the journal already holds -- is refused
// and cut off, so it can never report into another connection's story.
func TestASessionCannotBeStartedTwice(t *testing.T) {
	cfg, dbPath := start(t)

	victim, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()
	send(t, victim, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})
	j := openJournal(t, dbPath)
	waitFor(t, j, 1)

	intruder, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer intruder.Close()
	send(t, intruder, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "evil", OccurredAt: now()})
	ok := true
	send(t, intruder, Event{Kind: KindCallOutcome, SessionID: "s1", Seq: 1, OK: &ok, OccurredAt: now()})
	// The connection is closed on the second start; a read sees EOF rather
	// than an answer.
	intruder.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := mcp.NewReader(intruder).ReadRaw(); err == nil {
		t.Fatal("the intruding connection was kept open")
	}

	// The victim's session is untouched and still its own.
	if d := ask(t, victim, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "echo", Digest: "d", OccurredAt: now()}); d.Decision != journal.DecisionAllow {
		t.Fatalf("the victim's own call was %s", d.Decision)
	}
	waitFor(t, j, 2)
	entries, _ := j.EntriesSince(0, 10)
	for _, e := range entries {
		if e.Kind == KindSessionStart && e.Connector != nil && *e.Connector == "evil" {
			t.Fatal("the intruder's session.start was recorded")
		}
		if e.Kind == KindCallOutcome {
			t.Fatal("the intruder's outcome was recorded under the victim's session")
		}
	}

	// Ended is not available either: an id is never reused.
	send(t, victim, Event{Kind: KindSessionEnd, SessionID: "s1", OccurredAt: now()})
	waitFor(t, j, 3)
	late, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	send(t, late, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})
	late.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := mcp.NewReader(late).ReadRaw(); err == nil {
		t.Fatal("a start under an ended session's id was accepted")
	}
	if n, _ := j.CountCalls(); n != 1 {
		t.Fatalf("%d calls recorded, want 1", n)
	}
}

// A call is numbered from 1. A report with no usable number used to be
// decided and journaled with a null seq, where it paired with nothing and
// could hide a real gap; it is refused, and the connection with it.
func TestAnUnnumberedCallIsRefused(t *testing.T) {
	cfg, dbPath := start(t)
	for _, seq := range []int{0, -1} {
		conn, err := net.Dial("unix", cfg.Daemon.Socket)
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("s%d", seq+1)
		send(t, conn, Event{Kind: KindSessionStart, SessionID: id, MachineID: "m", Connector: "github", OccurredAt: now()})
		send(t, conn, Event{Kind: KindCallRequest, SessionID: id, Seq: seq, Tool: "echo", Digest: "d", OccurredAt: now()})
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := mcp.NewReader(conn).ReadRaw(); err == nil {
			t.Fatalf("seq %d was answered", seq)
		}
		conn.Close()
	}
	j := openJournal(t, dbPath)
	waitFor(t, j, 4) // two starts, and the two ends the daemon writes on disconnect
	if n, _ := j.CountCalls(); n != 0 {
		t.Fatalf("%d unnumbered calls were journaled", n)
	}
}
