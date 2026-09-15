package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/mcp"
)

// M6, human approval: a rule whose effect is ask holds a call in memory
// rather than deciding it, and a human -- through nim approve/nim reject --
// or the approval timer settles it. docs/decisions/0005-human-approval.md is
// the design; these are its regression tests.

// A call an ask rule matches is answered pending, with a hold id, and
// nothing is journaled until the human decides -- the daemon's half of the
// design, checked against the actual database.
func TestTheDaemonAnswersPendingAndJournalsNothingUntilAHumanDecides(t *testing.T) {
	cfg, dbPath := start(t)
	askRule(t, cfg, "send_email", "", "")

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})

	d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "send_email", Digest: "d", OccurredAt: now()})
	if d.Decision != DecisionPending {
		t.Fatalf("decided %q, want %q", d.Decision, DecisionPending)
	}
	if d.Hold == "" {
		t.Error("a pending decision named no hold id for nim approve to list it under")
	}

	send(t, conn, Event{Kind: KindCallArguments, SessionID: "s1", Seq: 1, Arguments: json.RawMessage(`{"to":"ceo@example.com"}`)})

	j := openJournal(t, dbPath)
	// The rule.add entry is the only thing that should be here: a call held
	// for a human has nothing of its own in the journal yet.
	waitFor(t, j, 2) // session.start, rule.add
	time.Sleep(100 * time.Millisecond)
	if n, err := j.CountCalls(); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Errorf("%d call.request entries journaled before a human decided", n)
	}

	// A human approves it, as nim approve <id> would, over its own connection.
	admin, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	resp, err := SendRequest(admin, Request{
		ID: "a1", Kind: KindApprovalDecide, ApprovalID: d.Hold, ApprovalDecision: journal.DecisionApproved,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("approval.decide: %s", resp.Error)
	}

	// Only now does the relay's connection get its answer.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := mcp.NewReader(conn).ReadRaw()
	if err != nil {
		t.Fatalf("the relay's connection never got the final decision: %v", err)
	}
	var final Decision
	if err := json.Unmarshal(raw, &final); err != nil {
		t.Fatal(err)
	}
	if final.Decision != journal.DecisionApproved {
		t.Errorf("final decision = %q, want approved", final.Decision)
	}

	waitFor(t, j, 3) // + the call.request the approval wrote
	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Decision != journal.DecisionApproved || calls[0].Tool != "send_email" {
		t.Errorf("journaled calls = %+v, want one send_email approved", calls)
	}
}

// The order is the point, exactly as it is for allow (TestTheEntryIsWrittenBeforeTheAnswerIsSent):
// a relay told approved has to be able to trust that the approval is already
// on the record, because it is about to forward a call on the strength of it.
func TestApprovingACallWritesApprovedBeforeTellingTheRelay(t *testing.T) {
	cfg, dbPath := start(t)
	askRule(t, cfg, "send_email", "", "")

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})
	d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "send_email", Digest: "d", OccurredAt: now()})
	send(t, conn, Event{Kind: KindCallArguments, SessionID: "s1", Seq: 1, Arguments: json.RawMessage(`{}`)})

	j := openJournal(t, dbPath)
	waitFor(t, j, 2)

	admin, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if resp, err := SendRequest(admin, Request{
		ID: "a1", Kind: KindApprovalDecide, ApprovalID: d.Hold, ApprovalDecision: journal.DecisionApproved,
	}); err != nil || resp.Error != "" {
		t.Fatalf("approval.decide: %v %s", err, resp.Error)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := mcp.NewReader(conn).ReadRaw(); err != nil {
		t.Fatalf("no final decision: %v", err)
	}
	// No waiting: by the time the answer arrived at the relay, the entry was
	// already there.
	if length, _, err := j.Head(); err != nil {
		t.Fatal(err)
	} else if length != 3 { // session.start, rule.add, call.request
		t.Errorf("journal has %d entries when the answer arrived, want 3: the approval "+
			"was sent before it was recorded", length)
	}
}

// A human's rejection writes rejected before the relay hears about it, the
// same ordering approval and every other decision this milestone makes
// keeps -- see TestApprovingACallWritesApprovedBeforeTellingTheRelay.
func TestRejectingACallWritesRejectedBeforeTellingTheRelay(t *testing.T) {
	cfg, dbPath := start(t)
	askRule(t, cfg, "send_email", "", "")

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})
	d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "send_email", Digest: "d", OccurredAt: now()})
	send(t, conn, Event{Kind: KindCallArguments, SessionID: "s1", Seq: 1, Arguments: json.RawMessage(`{}`)})

	j := openJournal(t, dbPath)
	waitFor(t, j, 2)

	admin, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if resp, err := SendRequest(admin, Request{
		ID: "a1", Kind: KindApprovalDecide, ApprovalID: d.Hold,
		ApprovalDecision: journal.DecisionRejected, ApprovalReason: "not today",
	}); err != nil || resp.Error != "" {
		t.Fatalf("approval.decide: %v %s", err, resp.Error)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := mcp.NewReader(conn).ReadRaw()
	if err != nil {
		t.Fatalf("no final decision: %v", err)
	}
	var final Decision
	if err := json.Unmarshal(raw, &final); err != nil {
		t.Fatal(err)
	}
	if final.Decision != journal.DecisionRejected {
		t.Errorf("final decision = %q, want rejected", final.Decision)
	}
	if length, _, err := j.Head(); err != nil {
		t.Fatal(err)
	} else if length != 3 {
		t.Errorf("journal has %d entries when the answer arrived, want 3: the rejection "+
			"was sent before it was recorded", length)
	}
	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Decision != journal.DecisionRejected {
		t.Errorf("journaled calls = %+v, want one rejected", calls)
	}
}

// The journal never holds a call that was neither approved nor rejected:
// nobody deciding within approval_timeout rejects it just as an explicit
// nim reject would.
func TestAnApprovalTimeoutRejectsAndJournalsTheCall(t *testing.T) {
	cfg, dbPath := startWithApprovalTimeout(t, 150*time.Millisecond)
	askRule(t, cfg, "send_email", "", "")

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})
	d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "send_email", Digest: "d", OccurredAt: now()})
	if d.Decision != DecisionPending {
		t.Fatalf("decided %q, want pending", d.Decision)
	}
	send(t, conn, Event{Kind: KindCallArguments, SessionID: "s1", Seq: 1, Arguments: json.RawMessage(`{}`)})

	// Nobody decides. The daemon's own timer answers instead, unasked.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := mcp.NewReader(conn).ReadRaw()
	if err != nil {
		t.Fatalf("the approval timer never answered: %v", err)
	}
	var final Decision
	if err := json.Unmarshal(raw, &final); err != nil {
		t.Fatal(err)
	}
	if final.Decision != journal.DecisionRejected {
		t.Errorf("timeout decision = %q, want rejected", final.Decision)
	}

	j := openJournal(t, dbPath)
	waitFor(t, j, 3)
	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Decision != journal.DecisionRejected {
		t.Errorf("journaled calls = %+v, want one rejected", calls)
	}
}

// A session that ends while one of its calls is still pending leaves nobody
// who could ever decide it: the daemon rejects it itself rather than making
// the approval timer do the same job later for an answer nothing is waiting
// for any more.
func TestASessionThatEndsWhilePendingIsRejected(t *testing.T) {
	cfg, dbPath := start(t)
	askRule(t, cfg, "send_email", "", "")

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})
	ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "send_email", Digest: "d", OccurredAt: now()})
	send(t, conn, Event{Kind: KindCallArguments, SessionID: "s1", Seq: 1, Arguments: json.RawMessage(`{}`)})
	send(t, conn, Event{Kind: KindSessionEnd, SessionID: "s1", OccurredAt: now()})

	j := openJournal(t, dbPath)
	waitFor(t, j, 4) // session.start, rule.add, call.request(rejected), session.end
	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Decision != journal.DecisionRejected {
		t.Errorf("journaled calls = %+v, want one rejected", calls)
	}
}

// A relay that dies outright leaves no session.end to react to; the
// connection dropping is the daemon's only signal, and it rejects whatever
// that connection's sessions left pending exactly as a graceful end does.
func TestAConnectionThatDropsWhilePendingRejectsWhatItLeftPending(t *testing.T) {
	cfg, dbPath := start(t)
	askRule(t, cfg, "send_email", "", "")

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})
	ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "send_email", Digest: "d", OccurredAt: now()})
	send(t, conn, Event{Kind: KindCallArguments, SessionID: "s1", Seq: 1, Arguments: json.RawMessage(`{}`)})

	conn.Close() // stands in for a killed relay

	j := openJournal(t, dbPath)
	waitFor(t, j, 4) // session.start, rule.add, call.request(rejected), session.end
	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Decision != journal.DecisionRejected {
		t.Errorf("journaled calls = %+v, want one rejected", calls)
	}
}

// The whole point of holding the call rather than digesting it: nim approve
// shows the real arguments. Checked against what approval.list actually
// returns, not against what the daemon merely accepted.
func TestApprovalListShowsTheRealArguments(t *testing.T) {
	cfg, _ := start(t)
	askRule(t, cfg, "send_email", "", "")

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})
	d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "send_email", Digest: "dg", OccurredAt: now()})
	send(t, conn, Event{Kind: KindCallArguments, SessionID: "s1", Seq: 1,
		Arguments: json.RawMessage(`{"to":"ceo@example.com","subject":"quarterly numbers"}`)})

	admin, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	var pending []PendingInfo
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := SendRequest(admin, Request{ID: "l1", Kind: KindApprovalList})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error != "" {
			t.Fatalf("approval.list: %s", resp.Error)
		}
		if len(resp.Pending) > 0 && len(resp.Pending[0].Arguments) > 0 {
			pending = resp.Pending
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("approval.list returned %d pending calls, want 1", len(pending))
	}
	p := pending[0]
	if p.ID != d.Hold || p.Tool != "send_email" {
		t.Errorf("approval.list entry = %+v, want id %q tool send_email", p, d.Hold)
	}
	if string(p.Arguments) != `{"to":"ceo@example.com","subject":"quarterly numbers"}` {
		t.Errorf("approval.list arguments = %s, want the real bytes, not a summary", p.Arguments)
	}
}

// A connection speaking approval.list/approval.decide has committed to that
// purpose, exactly as a policy connection has -- see
// TestARuleCannotNameAnUnenrolledAgentAndPolicyIsItsOwnPurpose. It must not
// be able to pivot to asking for a credential.
func TestAnApprovalConnectionCannotAskForACredential(t *testing.T) {
	cfg, _ := start(t)

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if resp, err := SendRequest(conn, Request{ID: "1", Kind: KindApprovalList}); err != nil || resp.Error != "" {
		t.Fatalf("approval.list on a fresh connection: %v %s", err, resp.Error)
	}
	resp, err := SendRequest(conn, Request{ID: "2", Kind: KindCredentialGet, Target: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != "unauthorized" {
		t.Errorf("an approval connection was allowed to ask for a credential: %q", resp.Error)
	}

	// And the other way round: a credential connection may not pivot to
	// approval.
	other, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	SendRequest(other, Request{ID: "1", Kind: KindCredentialGet, Target: "github"})
	if resp, err := SendRequest(other, Request{ID: "2", Kind: KindApprovalList}); err != nil || resp.Error != "unauthorized" {
		t.Errorf("approval.list on a credential connection returned %q, want unauthorized", resp.Error)
	}
}

// The real arguments exist only in the daemon's memory and in what
// approval.list hands back on request. They must never turn up in the one
// place a journal reader or a log reader actually looks: the database, and
// the daemon's own stderr. Checked with a marker distinctive enough that
// finding it anywhere would be unambiguous.
func TestTheRealArgumentsNeverReachTheJournalOrTheDaemonsLog(t *testing.T) {
	const marker = "sk-live-do-not-log-this-51a4f9"

	var logbuf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logbuf)
	t.Cleanup(func() { log.SetOutput(old) })

	cfg, dbPath := start(t)
	askRule(t, cfg, "send_email", "", "")

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()})
	d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "send_email", Digest: "dg", OccurredAt: now()})
	send(t, conn, Event{Kind: KindCallArguments, SessionID: "s1", Seq: 1,
		Arguments: json.RawMessage(fmt.Sprintf(`{"body":"%s"}`, marker))})

	j := openJournal(t, dbPath)
	waitFor(t, j, 2)

	admin, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if resp, err := SendRequest(admin, Request{
		ID: "a1", Kind: KindApprovalDecide, ApprovalID: d.Hold, ApprovalDecision: journal.DecisionApproved,
	}); err != nil || resp.Error != "" {
		t.Fatalf("approval.decide: %v %s", err, resp.Error)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := mcp.NewReader(conn).ReadRaw(); err != nil {
		t.Fatalf("no final decision: %v", err)
	}
	waitFor(t, j, 3)

	// The database: every entry, every field, formatted the way a careless
	// debug print would.
	entries, err := j.EntriesSince(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(fmt.Sprintf("%+v", e), marker) {
			t.Fatalf("the real arguments reached the journal: %+v", e)
		}
	}

	// The daemon's own log, across everything this flow printed.
	if strings.Contains(logbuf.String(), marker) {
		t.Fatalf("the real arguments reached the daemon's log:\n%s", logbuf.String())
	}
}

// Response.String() must never print a held call's real arguments, for the
// same reason it redacts Env -- see TestRequestStringNeverIncludesTheSecret.
func TestResponseStringNeverIncludesPendingArguments(t *testing.T) {
	resp := Response{ID: "1", Pending: []PendingInfo{
		{ID: "s1-1", Tool: "send_email", Arguments: json.RawMessage(`{"to":"ceo@example.com"}`)},
	}}
	for _, format := range []string{"%v", "%+v", "%s", "%q"} {
		out := fmt.Sprintf(format, resp)
		if strings.Contains(out, "ceo@example.com") {
			t.Fatalf("format %q leaked pending arguments: %s", format, out)
		}
	}
}
