package shim

import (
	"strings"
	"testing"
	"time"

	"github.com/BySergiMM/holdcall/engine/internal/daemon"
	"github.com/BySergiMM/holdcall/engine/internal/journal"
	"github.com/BySergiMM/holdcall/engine/internal/mcp"
)

// M6, human approval: a call an "ask" rule holds gets a second, longer wait
// for the same call.request, and the shim's client-facing text depends on
// how that wait ends -- approved and forwarded, rejected by a human, or
// nobody deciding in time. docs/decisions/0005-human-approval.md is the
// design; internal/daemon's approval_test.go covers the daemon's half
// (journaling, the timer, purpose binding); these cover the relay's.

// pendingReply answers a call.request with DecisionPending -- what a daemon
// holding a call for a human sends first, before its real arguments have
// even arrived. Its own name, not "pending": that identifier is already the
// shim's own in-flight-call struct.
func pendingReply(hold string) func(daemon.Event) any {
	return func(ev daemon.Event) any {
		return daemon.Decision{
			Kind: daemon.KindDecision, SessionID: ev.SessionID, Seq: ev.Seq,
			Decision: daemon.DecisionPending, Hold: hold,
		}
	}
}

// clientLines is what Holdcall itself wrote to the client, one message per
// element. A test that decodes the whole buffer fails with "not valid JSON" the
// moment a second refusal is in it, which says nothing about what went wrong.
func clientLines(g *rig) []string {
	text := strings.TrimSpace(g.client.String())
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// A call an ask rule holds is forwarded byte for byte once a human approves
// it, exactly as an ordinary allow forwards one -- the model reading the
// result cannot tell the two apart, which is the point: nothing about the
// call changes on its way through the hold.
func TestAnApprovedCallForwardsTheExactBytes(t *testing.T) {
	frame := `{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"send_email","arguments":{"to":"ops@example.com"}}}` + "\n"

	d := newDaemon(t)
	d.answer = pendingReply("s1-1")
	d.afterArguments = func(ev daemon.Event) any {
		return daemon.Decision{
			Kind: daemon.KindDecision, SessionID: ev.SessionID, Seq: ev.Seq, Decision: journal.DecisionApproved,
		}
	}

	g := newRigWithApprovalTimeout(t, d, 5*time.Second)
	g.relay(frame)

	if got := g.connector.String(); got != frame {
		t.Errorf("the connector did not receive the original bytes:\ngot  %q\nwant %q", got, frame)
	}
	if g.client.String() != "" {
		t.Errorf("Holdcall answered a call it was told was approved: %q", g.client.String())
	}

	args := g.daemon.await(t, 1, func(ev daemon.Event) bool { return ev.Kind == daemon.KindCallArguments })
	if len(args) != 1 {
		t.Fatalf("the relay never sent call.arguments for the held call")
	}
	if string(args[0].Arguments) != `{"to":"ops@example.com"}` {
		t.Errorf("call.arguments carried %s, want the real bytes", args[0].Arguments)
	}
}

// A human's rejection is its own sentence, distinct from an ordinary policy
// denial: DeniedByPolicy says a rule refused the call and will again;
// DeniedByHuman says a person looked at this one's real arguments and said
// no.
func TestARejectedApprovalAnswersTheClientWithTheHumanRefusalText(t *testing.T) {
	d := newDaemon(t)
	d.answer = pendingReply("s1-1")
	d.afterArguments = func(ev daemon.Event) any {
		return daemon.Decision{
			Kind: daemon.KindDecision, SessionID: ev.SessionID, Seq: ev.Seq, Decision: journal.DecisionRejected,
		}
	}

	g := newRigWithApprovalTimeout(t, d, 5*time.Second)
	g.relay(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_email","arguments":{}}}` + "\n")

	if n := len(g.connector.Bytes()); n != 0 {
		t.Errorf("a call a human rejected reached the connector (%d bytes)", n)
	}
	got := decodeResponse(t, g.client.Bytes())
	if !got.Result.IsError {
		t.Fatal("a rejected call was not answered as an error")
	}
	if got.Result.Content[0].Text != mcp.DeniedByHuman {
		t.Errorf("client text = %q, want the human-refusal text", got.Result.Content[0].Text)
	}
}

// Nobody deciding within approval_timeout is answered in the client's own
// words, not as an ordinary "could not reach a decision": a human declining
// to look is a different fact from the daemon being unreachable, and an
// agent reading the refusal should be able to tell them apart.
//
// This is the case where not even the daemon's own timeout answer arrives --
// a daemon that accepted the call and then went silent -- so it is the
// relay's backstop that speaks, and it speaks only once approvalGrace has
// passed on top of approval_timeout. Giving up at approval_timeout exactly
// was the bug: the daemon's timer fires there and still has a journal write
// to make before it answers.
func TestAnApprovalTimeoutAnswersTheClientWithItsOwnRefusalText(t *testing.T) {
	const timeout, grace = 200 * time.Millisecond, 500 * time.Millisecond

	d := newDaemon(t)
	d.answer = pendingReply("s1-1")
	// afterArguments left nil: the fake daemon receives call.arguments and
	// never answers it, exactly like an operator who has not looked yet and
	// a daemon whose own timer has not answered either.

	g := newRigWithApprovalWait(t, d, timeout, grace)
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_email","arguments":{}}}` + "\n"

	start := time.Now()
	g.relay(call)
	waited := time.Since(start)

	got := decodeResponse(t, g.client.Bytes())
	if !got.Result.IsError {
		t.Fatal("a call nobody decided in time was not answered as an error")
	}
	if got.Result.Content[0].Text != mcp.DeniedApprovalTimedOut {
		t.Errorf("client text = %q, want the approval-timeout text", got.Result.Content[0].Text)
	}
	if waited < timeout+grace {
		t.Errorf("the relay gave up after %v, before approval_timeout plus its grace (%v): "+
			"it would hang up on a daemon that is still journaling its own timeout", waited, timeout+grace)
	}
	if n := len(g.connector.Bytes()); n != 0 {
		t.Errorf("a call nobody decided reached the connector (%d bytes)", n)
	}

	// A daemon that said nothing even after the grace is unreachable, and an
	// unreachable daemon is terminal for the session, as it is everywhere
	// else in this relay: the rest of it is refused at once.
	if g.shim.reporter.lostCount() == 0 {
		t.Error("the relay gave up on the daemon without counting what that cost")
	}
	start = time.Now()
	g.relay(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo"}}` + "\n")
	if second := time.Since(start); second > 200*time.Millisecond {
		t.Errorf("the next call waited %v: the connection was not dropped", second)
	}
	if n := len(g.connector.Bytes()); n != 0 {
		t.Errorf("a call reached the connector after the daemon was given up on (%d bytes)", n)
	}
}

// The daemon's own timer is what ends an undecided hold, and its answer is
// what the relay has to be still listening for. The timer fires at
// approval_timeout and then journals the rejection before it tells anyone,
// so its answer reaches the relay a little after approval_timeout, never
// before. A relay that hangs up at approval_timeout hangs up on it.
//
// The fake daemon here answers well after approval_timeout and well inside
// the grace: the call is refused as a timeout -- not as a human's rejection,
// which is what the daemon's rejection used to be reported as -- and the
// connection is still there for the next call, which the daemon decides.
func TestADaemonsTimeoutAnswerThatArrivesAfterApprovalTimeoutIsHeardAndTheSessionGoesOn(t *testing.T) {
	// The grace is generous because it costs nothing here -- the answer comes
	// long before it ends -- and a test that sat close to it would measure the
	// machine it runs on.
	const timeout, grace = 200 * time.Millisecond, 3 * time.Second
	const journalling = 400 * time.Millisecond // the daemon's timer fired on time and it is still writing the rejection down

	d := newDaemon(t)
	d.answer = func(ev daemon.Event) any {
		if ev.Tool == "send_email" {
			return pendingReply("s1-1")(ev)
		}
		return daemon.Decision{Kind: daemon.KindDecision, SessionID: ev.SessionID, Seq: ev.Seq, Decision: journal.DecisionAllow}
	}
	d.afterArguments = func(ev daemon.Event) any {
		time.Sleep(timeout + journalling)
		return daemon.Decision{
			Kind: daemon.KindDecision, SessionID: ev.SessionID, Seq: ev.Seq,
			Decision: daemon.DecisionTimedOut, Reason: "nobody decided within the approval timeout",
		}
	}

	g := newRigWithApprovalWait(t, d, timeout, grace)
	held := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_email","arguments":{"to":"ops@example.com"}}}` + "\n"
	next := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"add","arguments":{"a":1,"b":2}}}` + "\n"
	g.relay(held, next)

	lines := clientLines(g)
	if len(lines) == 0 {
		t.Fatal("the held call was never answered")
	}
	got := decodeResponse(t, []byte(lines[0]))
	if !got.Result.IsError {
		t.Fatal("a call the daemon timed out was not answered as an error")
	}
	if text := got.Result.Content[0].Text; text == mcp.DeniedByHuman {
		t.Fatal("a call nobody decided was refused as a human's rejection")
	} else if text != mcp.DeniedApprovalTimedOut {
		t.Errorf("client text = %q, want the approval-timeout text", text)
	}

	if g.shim.reporter.lostCount() != 0 {
		t.Errorf("the session was degraded by a timeout the daemon answered: %v", g.shim.reporter.causeList())
	}
	if got := g.connector.String(); got != next {
		t.Errorf("the next call did not reach the connector after the timeout:\ngot  %q\nwant %q", got, next)
	}
	if len(lines) != 1 {
		t.Errorf("Holdcall answered %d calls itself, want only the held one; the others: %q", len(lines), lines[1:])
	}
}

// A daemon that goes away while it holds a call is not a timeout. Nobody let
// the wait run out: the connection ended, so there is no daemon to decide
// anything. The client is told what it is told for every other daemon the
// relay lost -- it could not reach a decision -- not that nobody approved in
// time, which used to be said here, and the session is over, as it is
// everywhere else in this relay.
func TestADaemonThatDiesWhileHoldingACallIsNotReportedAsAnApprovalTimeout(t *testing.T) {
	d := newDaemon(t)
	d.answer = pendingReply("s1-1")
	d.hangUpOnArguments = true

	// Long waits on purpose: a relay that ignored the closed connection and sat
	// out its deadline would take ten seconds here, and fail the bound below.
	g := newRigWithApprovalWait(t, d, 5*time.Second, 5*time.Second)
	start := time.Now()
	g.relay(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_email","arguments":{}}}` + "\n")
	if waited := time.Since(start); waited > 3*time.Second {
		t.Errorf("the relay waited %v on a connection that had already ended", waited)
	}

	got := decodeResponse(t, g.client.Bytes())
	if !got.Result.IsError {
		t.Fatal("a call held by a daemon that died was not answered as an error")
	}
	if text := got.Result.Content[0].Text; text != mcp.DeniedNoDecision {
		t.Errorf("client text = %q, want the could-not-decide text: the daemon went away, nobody ran a wait out", text)
	}
	if n := len(g.connector.Bytes()); n != 0 {
		t.Errorf("a call held by a daemon that died reached the connector (%d bytes)", n)
	}
	if g.shim.reporter.lostCount() == 0 {
		t.Error("the relay lost its daemon mid-hold without counting what that cost")
	}
}
