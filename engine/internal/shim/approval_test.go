package shim

import (
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
func TestAnApprovalTimeoutAnswersTheClientWithItsOwnRefusalText(t *testing.T) {
	d := newDaemon(t)
	d.answer = pendingReply("s1-1")
	// afterArguments left nil: the fake daemon receives call.arguments and
	// never answers it, exactly like an operator who has not looked yet.

	g := newRigWithApprovalTimeout(t, d, 200*time.Millisecond)
	g.relay(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_email","arguments":{}}}` + "\n")

	got := decodeResponse(t, g.client.Bytes())
	if !got.Result.IsError {
		t.Fatal("a call nobody decided in time was not answered as an error")
	}
	if got.Result.Content[0].Text != mcp.DeniedApprovalTimedOut {
		t.Errorf("client text = %q, want the approval-timeout text", got.Result.Content[0].Text)
	}
}
