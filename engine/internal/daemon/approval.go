package daemon

// Human approval (M6): a call whose winning rule is ask is held here, in
// memory only, until a human decides it with nim approve/nim reject, the
// approval timer runs out, or its session ends first. Nothing about a held
// call -- not even that it happened -- reaches the journal until one of
// those settles it, and its real arguments never reach the journal at all:
// the call.request entry gets the digest, exactly as any other call's does.
// docs/decisions/0005-human-approval.md is the argument for the whole
// design; this file is its implementation.
import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
)

// pendingCall is one call on hold. Every field but Arguments and the
// resolution bookkeeping is fixed at creation, taken from the call.request
// that produced it; Arguments arrives a moment later on the same
// connection, in the call.arguments event the relay sends once it reads
// DecisionPending.
type pendingCall struct {
	SessionID string
	Seq       int
	Tool      string
	Agent     string
	Connector string
	Digest    string
	StartedAt time.Time

	// conn is the connection whose call.request this answers. Once the
	// daemon has replied DecisionPending on it, that connection's own
	// handling loop never writes to it again on this call's account -- it
	// goes back to reading, waiting for call.arguments and then for nothing
	// at all -- so resolve can write the final Decision to it from whatever
	// goroutine gets there first (a human's decision, the timer, or a
	// session ending) without racing the loop that owns it.
	conn net.Conn

	mu        sync.Mutex
	resolved  bool
	arguments json.RawMessage
	// argumentsKnown separates "the relay reported no arguments" from "the
	// relay has not reported yet": the two look the same in arguments, and
	// only the first is something a human can approve. Found by review --
	// an approve could be recorded before the daemon held a single byte of
	// what was being approved.
	argumentsKnown bool
	timer          *time.Timer
}

// id is the string nim approve lists a call under and nim approve/reject
// takes back: session and seq are already how call.request and
// call.outcome correlate, so this reuses that rather than minting a second
// name for the same call.
func (p *pendingCall) id() string { return holdID(p.SessionID, p.Seq) }

func holdID(sessionID string, seq int) string { return fmt.Sprintf("%s-%d", sessionID, seq) }

// setArguments records the one report the relay makes. The first report is
// the one: a later one for the same call is ignored rather than replacing
// what a human may already have read, so the bytes shown are the bytes
// approved. The relay sends exactly one, so a second is a bug or an
// impostor, and either way not something to act on.
func (p *pendingCall) setArguments(raw json.RawMessage) {
	p.mu.Lock()
	if !p.argumentsKnown {
		p.arguments, p.argumentsKnown = raw, true
	}
	p.mu.Unlock()
}

func (p *pendingCall) getArguments() (json.RawMessage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.arguments, p.argumentsKnown
}

// errAlreadyResolved means a call's decision was already made by whichever
// of nim reject/approve, the approval timer or a session ending got there
// first. The others are not errors from the operator's point of view -- the
// call was decided, which is what they wanted -- but resolve still reports
// it so a caller like handleApprovalDecide can say so.
var errAlreadyResolved = errors.New("that call was already decided")

// pendingRegistry is the daemon-wide set of calls currently held for a
// human -- nim approve's whole view of the world, and the only place any of
// this lives. It is never written to SQLite; only the eventual decision is,
// through resolve.
type pendingRegistry struct {
	mu   sync.Mutex
	byID map[string]*pendingCall
}

func newPendingRegistry() *pendingRegistry {
	return &pendingRegistry{byID: make(map[string]*pendingCall)}
}

// hold registers a call an ask rule is holding, before the DecisionPending
// reply is sent -- so a race between the reply reaching the relay and a
// human reacting to it (which cannot really happen this fast, but should
// not be assumed impossible) always finds the call already in the registry.
func (reg *pendingRegistry) hold(p *pendingCall) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.byID[p.id()] = p
}

// arm starts the daemon-side clock on p: if nobody decides it within
// timeout, it is rejected and journaled exactly as an explicit nim reject
// would be, with its own reason -- so the journal never ends up holding a
// call that was neither approved nor rejected, whether or not a relay is
// still there to hear the answer.
func (reg *pendingRegistry) arm(p *pendingCall, timeout time.Duration, j *journal.Journal) {
	p.mu.Lock()
	p.timer = time.AfterFunc(timeout, func() {
		reg.resolve(p, journal.DecisionRejected, "nobody decided within the approval timeout", j)
	})
	p.mu.Unlock()
}

// setArguments records the real bytes a call.arguments event carried for
// the call named by sessionID and seq. It is silently a no-op when no such
// call is held: the call may already be decided, or the report may be
// bogus, and neither is a reason to break the connection the way an
// out-of-order decision reply is -- this is a one-way report, not an
// exchange.
func (reg *pendingRegistry) setArguments(sessionID string, seq int, arguments json.RawMessage) {
	reg.mu.Lock()
	p, ok := reg.byID[holdID(sessionID, seq)]
	reg.mu.Unlock()
	if !ok {
		return
	}
	p.setArguments(arguments)
}

// get looks up a held call by the id nim approve showed for it.
func (reg *pendingRegistry) get(id string) (*pendingCall, bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	p, ok := reg.byID[id]
	return p, ok
}

// list returns every call still held, oldest first -- the order nim approve
// shows them in, so the one an operator is most overdue to look at is
// always on top.
func (reg *pendingRegistry) list() []*pendingCall {
	reg.mu.Lock()
	out := make([]*pendingCall, 0, len(reg.byID))
	for _, p := range reg.byID {
		out = append(out, p)
	}
	reg.mu.Unlock()
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt.Before(out[k].StartedAt) })
	return out
}

// forSession returns every call currently held for one session, so a
// session that is ending can reject everything it leaves behind.
func (reg *pendingRegistry) forSession(sessionID string) []*pendingCall {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	var out []*pendingCall
	for _, p := range reg.byID {
		if p.SessionID == sessionID {
			out = append(out, p)
		}
	}
	return out
}

func (reg *pendingRegistry) remove(id string) {
	reg.mu.Lock()
	delete(reg.byID, id)
	reg.mu.Unlock()
}

// resolve is the one path that ends a held call, whoever decided it: a
// human through approval.decide, the approval timer, or the call's session
// ending. It writes the call.request entry -- approved or rejected -- before
// telling anyone, the same order allow and deny already answer in, and only
// then answers the connection that has been waiting on it since it read
// DecisionPending.
//
// Guarded to run once per call: a human deciding and the timer firing can
// race, and only the one that gets here first is real. The loser gets
// errAlreadyResolved and changes nothing.
func (reg *pendingRegistry) resolve(p *pendingCall, decision, reason string, j *journal.Journal) error {
	p.mu.Lock()
	if p.resolved {
		p.mu.Unlock()
		return errAlreadyResolved
	}
	p.resolved = true
	if p.timer != nil {
		p.timer.Stop()
	}
	p.mu.Unlock()
	reg.remove(p.id())

	ev := Event{
		Kind: KindCallRequest, SessionID: p.SessionID, Seq: p.Seq,
		Tool: p.Tool, Digest: p.Digest, Decision: decision,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	sent, sentReason := decision, reason
	if err := apply(ev, j, p.Agent); err != nil {
		// The same fail-closed shape answer already uses for allow and deny:
		// a decision this milestone could not record is not a decision, and
		// must not be reported as one -- allowing a call to forward, or
		// definitively refusing it, on the strength of a write that did not
		// happen would break the guarantee every other decision already
		// keeps.
		log.Printf("recording the decision for %s seq %d: %v", p.SessionID, p.Seq, err)
		sent, sentReason = DecisionUndecided, "the decision could not be recorded"
	}

	if p.conn != nil {
		if err := json.NewEncoder(p.conn).Encode(Decision{
			Kind: KindDecision, SessionID: p.SessionID, Seq: p.Seq, Decision: sent, Reason: sentReason,
		}); err != nil {
			// The relay may already have given up and closed its side of the
			// connection -- its own fail-closed backstop, documented in
			// shim.go, not a reason to treat the decision above as not
			// having happened. It is already in the journal.
			log.Printf("telling the relay about %s seq %d: %v", p.SessionID, p.Seq, err)
		}
	}
	if sent == DecisionUndecided {
		return fmt.Errorf("the decision could not be recorded")
	}
	return nil
}

// rejectSession rejects and journals every call still held for a session
// that is ending, whether by an explicit session.end or because the
// connection that owned it dropped -- so a pending call never outlives the
// session it belongs to, and the journal never holds one that was neither
// approved nor rejected.
func (reg *pendingRegistry) rejectSession(sessionID string, j *journal.Journal) {
	for _, p := range reg.forSession(sessionID) {
		reg.resolve(p, journal.DecisionRejected, "the session ended before a human decided", j)
	}
}

// handleApprovalList answers nim approve with no argument: every call
// currently held, with its real arguments -- never a summary, never
// something a model wrote. docs/decisions/0005-human-approval.md is why
// that is the whole point.
func handleApprovalList(req Request, approvals *pendingRegistry) Response {
	pending := approvals.list()
	infos := make([]PendingInfo, len(pending))
	for i, p := range pending {
		infos[i] = pendingInfo(p)
	}
	return Response{ID: req.ID, Pending: infos}
}

// handleApprovalDecide answers nim approve <id> and nim reject <id>
// [--reason]. The human's reason, if any, is logged here and returned in
// this Response for the CLI to print -- and goes nowhere else: not the
// journal, not the client the call came from. See Request.ApprovalReason.
func handleApprovalDecide(req Request, approvals *pendingRegistry, j *journal.Journal) Response {
	if req.ApprovalDecision != journal.DecisionApproved && req.ApprovalDecision != journal.DecisionRejected {
		return Response{ID: req.ID, Error: fmt.Sprintf(
			"approval_decision must be %q or %q, not %q",
			journal.DecisionApproved, journal.DecisionRejected, req.ApprovalDecision)}
	}
	p, ok := approvals.get(req.ApprovalID)
	if !ok {
		return Response{ID: req.ID, Error: fmt.Sprintf(
			"no call is held under %q -- nim approve lists what is, and a call leaves that list "+
				"once it is decided or its wait runs out", req.ApprovalID)}
	}
	info := pendingInfo(p)

	// An approval is of the arguments, so until the relay has reported them
	// there is nothing to approve: the human would be approving a tool
	// name. A rejection needs no such thing -- refusing what was not even
	// seen is the safe direction -- so it goes through regardless.
	if req.ApprovalDecision == journal.DecisionApproved && !info.ArgumentsKnown {
		return Response{ID: req.ID, Error: fmt.Sprintf(
			"the arguments of %s have not reached the daemon yet, so there is nothing to approve; "+
				"run nim approve again in a moment, or nim reject %s", req.ApprovalID, req.ApprovalID)}
	}

	reason := "a human approved this call"
	if req.ApprovalDecision == journal.DecisionRejected {
		reason = "a human rejected this call"
	}
	if req.ApprovalReason != "" {
		log.Printf("%s seq %d %s, reason: %s", p.SessionID, p.Seq, req.ApprovalDecision, req.ApprovalReason)
	}

	if err := approvals.resolve(p, req.ApprovalDecision, reason, j); err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("recording the decision: %v", err)}
	}
	return Response{ID: req.ID, Pending: []PendingInfo{info}}
}

func pendingInfo(p *pendingCall) PendingInfo {
	arguments, known := p.getArguments()
	return PendingInfo{
		ID: p.id(), Tool: p.Tool, Agent: p.Agent, Connector: p.Connector,
		Arguments: arguments, ArgumentsKnown: known,
		StartedAt: p.StartedAt.UTC().Format(time.RFC3339Nano),
	}
}
