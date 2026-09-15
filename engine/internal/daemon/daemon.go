// Package daemon owns everything shared between shims.
//
// A client spawns one shim per configured MCP server, so decisions, the journal
// and (later) budgets and human approval need a single writer. The shims report
// here; this process is the only one that touches SQLite.
//
// One report is answered, and only one. A shim asks before it forwards a
// tools/call and waits, because a call that has been sent cannot be recalled.
// Everything else -- sessions, outcomes, anomalies -- is still one way and still
// never stalls the relay.
//
// The decision is written before it is sent. A shim is told "allow" only once
// the entry recording that allowance is in the journal, which is what makes a
// forwarded call a recorded call. The converse does not follow: an allow in the
// journal does not mean the call was made, because the shim may have given up
// waiting first. docs/milestones.md sets out that race.
package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/credential"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/peer"
)

// Event is one report from a shim.
type Event struct {
	Kind            string `json:"kind"`
	SessionID       string `json:"session_id"`
	MachineID       string `json:"machine_id,omitempty"`
	Client          string `json:"client,omitempty"`
	Connector       string `json:"connector,omitempty"`
	Seq             int    `json:"seq,omitempty"`
	Tool            string `json:"tool,omitempty"`
	Digest          string `json:"params_digest,omitempty"`
	Decision        string `json:"decision,omitempty"`
	OK              *bool  `json:"ok,omitempty"`
	DurationMS      *int   `json:"duration_ms,omitempty"`
	Anomaly         string `json:"anomaly,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
	OccurredAt      string `json:"occurred_at,omitempty"`
}

// A call is reported twice, as two immutable entries rather than one row that
// gets updated: the chain cannot cover a row that changes after it is written.
//
// An allowed call is reported twice. A refused one is reported once: there is
// no outcome to record for something that never ran, and inventing one would
// put a result in the journal for work nobody did.
const (
	KindSessionStart = journal.KindSessionStart
	KindCallRequest  = journal.KindCallRequest
	KindCallOutcome  = journal.KindCallOutcome
	KindSessionEnd   = journal.KindSessionEnd
	KindAnomaly      = journal.KindAnomaly
)

// KindDecision names the daemon's answer to a call.request.
//
// Not a journal kind: nothing is ever written under it. The decision itself is
// recorded on the call.request entry, which is the thing the chain covers.
const KindDecision = "decision"

// DecisionUndecided answers a call the daemon could not act on at all: the
// attempt to record it failed, so nothing was journaled and nothing was
// actually decided. It travels on the wire only -- journal.Decision* are the
// values a row can hold, and this call never produced one.
const DecisionUndecided = "undecided"

// Decision is the only message the daemon sends back.
//
// It echoes the session and sequence it answers so the shim can check that the
// reply belongs to the question. Without that, one lost or late message puts the
// two out of step and every later answer is attributed to the wrong call --
// which would eventually mean forwarding a call the daemon refused.
type Decision struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	Seq       int    `json:"seq"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason,omitempty"`
}

// Run serves until the process is stopped. It returns nil when another daemon
// already holds the socket: two shims racing to start one is normal, and the
// loser has nothing to complain about.
func Run(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	ln, err := listen(cfg.Daemon.Socket)
	if err != nil {
		if errors.Is(err, errAlreadyRunning) {
			log.Printf("daemon already running on %s", cfg.Daemon.Socket)
			return nil
		}
		return err
	}
	defer ln.Close()
	defer os.Remove(cfg.Daemon.Socket)

	// The daemon is the only thing that may create an install identifier, and
	// only when no entries depend on the previous one. Replacing a lost one
	// would reseed the chain and make every existing entry look forged.
	seed, known := config.ReadMachineID()
	j, err := journal.Open(cfg.DatabasePath(), seed)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	defer j.Close()

	if !known {
		length, _, err := j.Head()
		switch {
		case err != nil:
			return fmt.Errorf("journal: %w", err)
		case length == 0:
			id, err := config.CreateMachineID()
			if err != nil {
				return fmt.Errorf("creating an install identifier: %w", err)
			}
			if err := j.AdoptSeed(id); err != nil {
				return fmt.Errorf("seeding the journal: %w", err)
			}
		default:
			log.Printf("machine-id is missing from %s; entries are still recorded and chained "+
				"to each other, but `nim verify` cannot check the first one until it is restored",
				config.MachineIDPath())
		}
	}

	// A missing credential store must not take the whole daemon down.
	// Recording and deciding have nothing to do with connectors and must keep
	// working regardless; only credential.get and connector.* need a store, and
	// they report its absence themselves. Failing to start here would turn a
	// machine with no keyring into one where no agent can call anything, which
	// is a much worse failure than one where no credential can be injected.
	store, err := credential.New()
	if err != nil {
		log.Printf("credential store unavailable, connector commands will fail: %v", err)
		store = nil
	}

	locks := newTargetLocks()
	sessions := newSessionRegistry()

	log.Printf("nim daemon listening on %s (journal: %s)", cfg.Daemon.Socket, cfg.DatabasePath())
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go handle(conn, j, store, locks, sessions)
	}
}

// sessionRegistry is the daemon-wide record of which session ids are open,
// so that a session belongs to the one connection that started it.
//
// The per-connection map in handle() answered "did this connection start
// it", which is the right question for every kind but session.start itself:
// a second connection could announce a start under an id another connection
// was already using, be given its own claim on the same id, and from then on
// report calls, outcomes and an end into the first one's session. Peer
// identity keeps everything that is not Nim off the socket, but session
// ownership is meant to hold on its own -- it is what tells two runs of Nim
// apart, which peer identity cannot -- and it did not.
//
// An id is refused if it is live on any connection, or if the journal has
// ever recorded a start under it: a session id names one session, and one
// that ended is not available for a second story. The check and the claim
// are one critical section, so two connections racing on the same id cannot
// both win.
type sessionRegistry struct {
	mu   sync.Mutex
	live map[string]bool
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{live: make(map[string]bool)}
}

// claim reserves id for the caller. ok is false when the id is taken.
func (r *sessionRegistry) claim(id string, j *journal.Journal) (ok bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live[id] {
		return false, nil
	}
	exists, err := j.SessionExists(id)
	if err != nil || exists {
		return false, err
	}
	r.live[id] = true
	return true, nil
}

func (r *sessionRegistry) release(id string) {
	r.mu.Lock()
	delete(r.live, id)
	r.mu.Unlock()
}

var errAlreadyRunning = errors.New("another daemon holds the socket")

// listen binds the socket, clearing a stale file left by a crashed daemon but
// never one a live daemon is using.
//
// Several daemons can start at once -- a client spawning several shims after a
// crash left a stale socket behind all race to reclaim the same path.
// acquireStartupLock serializes the whole check-and-reclaim sequence across
// those processes on unix, which is what actually closes the race: two
// processes can each observe the socket as stale in the same window, and
// without the lock one could unlink the socket the other just bound.
// Reproduced directly -- without it, 8 daemons racing on one stale socket
// produced 2-5 simultaneous "winners"; with it, always exactly 1.
//
// The retry loop remains underneath as the fallback on platforms where that
// lock is a no-op (see lock_windows.go) -- weaker, but better than a single
// attempt, and a daemon that ultimately loses backs off cleanly on its next
// dial rather than unlinking a live socket forever.
func listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	unlock, err := acquireStartupLock(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("acquiring the startup lock: %w", err)
	}
	defer unlock()

	const attempts = 3
	var lastErr error
	for i := 0; i < attempts; i++ {
		ln, err := net.Listen("unix", path)
		if err == nil {
			// net.Listen creates the socket file with whatever the process
			// umask leaves it -- often group/other-readable, and so
			// connectable by any local user (verified: a 022 umask gives
			// srwxr-xr-x). That was a low-severity gap while this socket
			// carried only call metadata. It is not acceptable now that
			// credential.get answers with real secret material on it, and that
			// a decision travels back over it.
			if err := os.Chmod(path, 0o600); err != nil {
				ln.Close()
				return nil, fmt.Errorf("restricting socket permissions: %w", err)
			}
			return ln, nil
		}
		lastErr = err
		// Something is at that path. Ask it whether it is alive.
		if conn, dialErr := net.DialTimeout("unix", path, 500*time.Millisecond); dialErr == nil {
			conn.Close()
			return nil, errAlreadyRunning
		}
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return nil, fmt.Errorf("removing stale socket %s: %w", path, rmErr)
		}
	}
	return nil, fmt.Errorf("could not bind %s: %w", path, lastErr)
}

func handle(conn net.Conn, j *journal.Journal, store credential.Store, locks *targetLocks, sessions *sessionRegistry) {
	defer conn.Close()
	state := &requestState{}

	// Sessions opened on this connection that have not been closed yet, each
	// with the connector its start named: a rule can be scoped to a
	// connector, and the connector is a property of the session, so it is
	// read from here at decision time rather than sent again on every call.
	//
	// A shim closes its own session when it exits or is asked to stop, but a
	// client that kills it outright leaves no chance to. The daemon can still
	// tell, because the connection goes with the process, so it closes the
	// session itself rather than leaving one that looks abandoned.
	//
	// What survives this is the case worth keeping: if the *daemon* dies, it
	// writes nothing, and the session stays open in the record -- which is
	// exactly the period during which events were being lost.
	open := map[string]string{}
	defer func() {
		for id := range open {
			e := journal.Entry{
				Kind:       journal.KindSessionEnd,
				SessionID:  id,
				OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
			}
			if err := j.Append(e); err != nil {
				log.Printf("closing session %s after its shim went away: %v", id, err)
			}
			sessions.release(id)
		}
	}()

	// Nothing that is not this binary gets to speak here.
	//
	// Without this the socket was open to any local process, which on a
	// milestone that enforces is worse than it was on one that only observed:
	// a caller could not merely fabricate a record, it could open sessions
	// and drive the decision path. Ported from the branch where credentials
	// forced the question. It answers "is the caller Nim", not "is the caller
	// a shim the operator meant to run" -- anything able to execute this
	// binary still passes -- so it is a floor, not the authorization model.
	//
	// Checked here, at accept, rather than on the first message: a socket
	// keeps delivering buffered bytes after the writer has exited, so a
	// client that writes and leaves could otherwise be read from after its
	// pid was gone and denied for being fast.
	if supported, isSelf := peer.IsSelf(conn); supported && !isSelf {
		log.Printf("refusing a connection from an unverified peer")
		return
	}

	// The agent behind this connection, worked out once, at accept. The
	// peer's pid is fixed for the life of the socket; its parent is read now
	// and is a snapshot, not an invariant -- a client that exits leaves its
	// relay to be reparented -- so the agent is bound to the session when the
	// session starts and never re-derived. Deriving it per message would add
	// ways to fail and a way for the answer to drift within one session.
	agent := deriveAgent(conn, j)

	// bufio.Reader rather than mcp.NewReader: the latter's ReadRaw grows
	// without bound, which is right for the client<->connector relay, where a
	// large tool result is legitimate, and wrong here. Every message this
	// socket carries is small -- a digest is a fixed-length hash, never tool
	// output. Unbounded, one connection sending a line with no newline grew
	// the daemon's RSS by 200 MiB in 0.2s (measured on the other branch).
	//
	// That is not just an availability nuisance on this milestone. The relay
	// fails closed, so a daemon killed this way does not degrade recording,
	// it denies every tools/call on the machine -- one unauthenticated local
	// process disabling every agent's tools.
	br := bufio.NewReaderSize(conn, 4096)
	for {
		raw, err := boundedReadRaw(br, maxLineBytes)
		if err != nil {
			if err != io.EOF {
				log.Printf("shim connection: %v", err)
			}
			return
		}
		// Two message families share this socket, told apart by a peek at
		// "kind" -- the two vocabularies never overlap. Request/Response is
		// structurally separate from Event on purpose: an Event is one-way,
		// gets applied to the journal, and has its fields logged when it is
		// malformed or rejected, and a credential must never be able to reach
		// that path by accident.
		var peek struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			log.Printf("malformed message: %v", err)
			continue
		}

		if requestKinds[peek.Kind] {
			if err := serveRequest(conn, raw, peek.ID, peek.Kind, state, j, store, locks); err != nil {
				log.Printf("writing response: %v", err)
				return
			}
			continue
		}

		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			log.Printf("malformed event: %v", err)
			continue
		}

		// A connection may only speak about sessions it opened. Peer identity
		// cannot tell one run of Nim from another, so without this any shim
		// could report calls -- and receive decisions -- under a session
		// another one opened.
		//
		// The connection is closed rather than the message skipped. A
		// call.request is answered, so skipping one would leave the shim
		// waiting out its timeout for a reply that is never coming; and by
		// this milestone's own reasoning a stream with one unanswered
		// question can no longer be trusted to pair the next answer with the
		// right call. No legitimate shim reaches this.
		_, mine := open[ev.SessionID]
		if ev.SessionID == "" || (ev.Kind != KindSessionStart && !mine) {
			log.Printf("refusing %s for session %q: this connection did not start it", ev.Kind, ev.SessionID)
			return
		}

		// A session id names one session. One that is live on any connection,
		// or that the journal has ever recorded a start for, cannot be started
		// again -- see sessionRegistry. Closed rather than skipped, for the
		// same reason as above: no legitimate shim reaches this, because a
		// genuine relay draws its id at random for each run.
		if ev.Kind == KindSessionStart {
			ok, err := sessions.claim(ev.SessionID, j)
			if err != nil {
				log.Printf("session.start %q: %v", ev.SessionID, err)
				return
			}
			if !ok {
				log.Printf("refusing session.start for %q: that session already exists", ev.SessionID)
				return
			}
		}

		// A call is numbered from 1 within its session; that number is what
		// pairs it with its outcome and what gap detection counts. A report
		// carrying no usable number is not a well-formed call and used to be
		// journaled with a null seq, where it paired with nothing and could
		// mask a real gap. A genuine relay never sends one.
		if (ev.Kind == KindCallRequest || ev.Kind == KindCallOutcome) && ev.Seq <= 0 {
			log.Printf("refusing %s for session %q with seq %d: a call is numbered from 1", ev.Kind, ev.SessionID, ev.Seq)
			return
		}

		// The one report that is answered. A failure to answer ends the
		// connection rather than carrying on: the shim is waiting, and a stream
		// where one question went unanswered can no longer be trusted to pair
		// the next answer with the right call.
		if ev.Kind == KindCallRequest {
			if err := answer(conn, ev, j, agent, open[ev.SessionID]); err != nil {
				log.Printf("answering %s seq %d: %v", ev.SessionID, ev.Seq, err)
				return
			}
			continue
		}

		if err := apply(ev, j, agent); err != nil {
			log.Printf("%s: %v", ev.Kind, err)
			if ev.Kind == KindSessionStart {
				sessions.release(ev.SessionID)
			}
			continue
		}
		switch ev.Kind {
		case KindSessionStart:
			open[ev.SessionID] = ev.Connector
		case KindSessionEnd:
			delete(open, ev.SessionID)
			sessions.release(ev.SessionID)
		}
	}
}

// answer decides one call, records the decision, and only then tells the shim.
//
// The decision is (agent, connector, tool): the agent the daemon derived for
// this connection, the connector the session was started for, and the tool
// the call names, against the rules in SQLite. Two agents calling the same
// tool on the same connector can therefore receive different answers, which
// is the property M4 exists to establish, and the record distinguishes them
// because the agent is on their sessions' start entries.
//
// The order is the point. If the entry cannot be written the answer is deny,
// because allowing a call Nim failed to record would break the one thing this
// milestone guarantees: that a call which reached a connector is a call the
// journal knows about. A rule lookup that fails is the same shape -- the
// daemon could not decide -- and is answered as undecided, not as a verdict.
func answer(conn net.Conn, ev Event, j *journal.Journal, agent, connector string) error {
	decision, reason := journal.DecisionAllow, ""
	rule, found, err := j.RuleFor(agent, connector, ev.Tool)
	if err != nil {
		log.Printf("reading the rules for %s seq %d: %v", ev.SessionID, ev.Seq, err)
		return json.NewEncoder(conn).Encode(Decision{
			Kind: KindDecision, SessionID: ev.SessionID, Seq: ev.Seq,
			Decision: DecisionUndecided, Reason: "the rules could not be read",
		})
	}
	// found may carry either effect: an explicit allow can be the answer too,
	// when it is the more specific rule -- docs/decisions/0003 has the
	// precedence. No matching rule at all is the M4 baseline, decision's zero
	// value above: allow, with nothing to name as the reason.
	if found {
		decision = rule.Effect
		reason = "by rule: " + rule.String()
	}

	ev.Decision = decision
	if err := apply(ev, j, agent); err != nil {
		log.Printf("call.request: %v", err)
		decision = DecisionUndecided
		reason = "the call could not be recorded"
	}

	return json.NewEncoder(conn).Encode(Decision{
		Kind:      KindDecision,
		SessionID: ev.SessionID,
		Seq:       ev.Seq,
		Decision:  decision,
		Reason:    reason,
	})
}

// apply turns a report into one journal entry. Every kind appends; nothing
// updates. A shim that reports the same call twice -- once on the way out and
// once when it returns -- produces two entries, and the pair is joined by
// (session_id, seq) when read back.
func apply(ev Event, j *journal.Journal, agent string) error {
	e := journal.Entry{
		Kind:       ev.Kind,
		SessionID:  ev.SessionID,
		OccurredAt: normaliseTime(ev.OccurredAt),
	}
	if ev.Seq > 0 {
		seq := int64(ev.Seq)
		e.Seq = &seq
	}

	switch ev.Kind {
	case KindSessionStart:
		e.MachineID = optional(ev.MachineID)
		e.Client = optional(ev.Client)
		e.Connector = optional(ev.Connector)
		// On session.start only, for the same reason connector is: it is a
		// property of the session, and two immutable entries that both
		// describe one thing can contradict each other where a join cannot.
		//
		// Taken from the derivation, never from ev. Client is next to it and
		// is the opposite kind of thing -- a label the caller sent, recorded
		// because it is useful and never because it is trusted.
		e.Agent = optional(agent)
	case KindCallRequest:
		e.Tool = optional(ev.Tool)
		e.ParamsDigest = optional(ev.Digest)
		e.Decision = optional(ev.Decision)
	case KindCallOutcome:
		e.OK = ev.OK
		if ev.DurationMS != nil {
			ms := int64(*ev.DurationMS)
			e.DurationMS = &ms
		}
	case KindSessionEnd:
		e.ProtocolVersion = optional(ev.ProtocolVersion)
	case KindAnomaly:
		e.Anomaly = optional(ev.Anomaly)
	default:
		return fmt.Errorf("unknown kind %q", ev.Kind)
	}
	return j.Append(e)
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// normaliseTime keeps occurred_at in one shape, because it is hashed as the
// string it is stored as.
func normaliseTime(s string) string {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(time.RFC3339Nano)
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}
