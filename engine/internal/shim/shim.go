// Package shim is the relay a client actually spawns.
//
// It sits between the client's stdio and a downstream MCP server. It recognises
// tools/call, asks the daemon whether the call may proceed, and pairs each
// allowed request with its response by id to learn the outcome and duration.
//
// A tools/call is the one thing that waits. The relay asks before forwarding
// and blocks until it has an answer, because a call cannot be unsent. Every way
// of not getting one -- no daemon, a slow daemon, a closed socket, an answer to
// a different question -- is a denial. There is no path on which a tools/call
// reaches a connector without a decision behind it.
//
// One answer takes longer than the rest: a rule that says "ask" holds the
// call for a human, and the relay's wait extends from decisionTimeout to
// approval_timeout for that one call, on the same connection -- see
// reporter.awaitApproval and docs/decisions/0005-human-approval.md. Every
// other rule in this paragraph still applies to it: a way of not getting a
// final answer within that longer bound is still a denial.
//
// Everything else still flows without waiting. Other methods are relayed
// untouched and never consult the daemon, and sessions, outcomes and anomalies
// are still reported one way, so bookkeeping cannot stall the relay.
//
// A denial that happens because the daemon is unreachable leaves no journal
// entry, since the only writer is exactly what could not be reached. Those are
// counted as lost events and reported on stderr, which is as close to a record
// as there can be.
package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/mcp"
	"github.com/BySergiMM/nim/engine/internal/peer"
)

// decisionTimeout bounds how long a tools/call waits for the daemon.
//
// Measured rather than picked, and re-derivable rather than remembered. The
// numbers and the commands that produce them are in docs/benchmarks.md; in
// short, the round trip including the durable journal write is p99 0.27 ms for
// one relay and 1.6 ms with sixteen contending (Apple M5, darwin/arm64). Two
// seconds is three orders of magnitude past that, so it cannot fire because
// the daemon is busy -- only because it is wedged or gone.
//
// An earlier version of this comment quoted 0.157 ms from a measurement that
// left nothing behind to re-run. It could not be checked, and the benchmark
// that replaced it reports a different figure. That is the argument for the
// benchmark existing rather than for the number being interesting.
//
// It is shorter than SQLite's five-second busy timeout, which leaves a window:
// a pathologically contended write can still land after the relay has given up
// and denied, putting an allow in the journal for a call that never happened.
// That call reads as pending, never as executed. docs/milestones.md sets it out.
const decisionTimeout = 2 * time.Second

// Options describes one relayed server.
type Options struct {
	Connector string   // name of the downstream server, e.g. "github"
	Client    string   // best-effort label for the MCP client, may be empty
	Command   []string // the downstream server and its arguments
	Config    config.Config
}

type pending struct {
	tool    string
	digest  string
	seq     int
	started time.Time
}

type Shim struct {
	opts      Options
	sessionID string
	reporter  *reporter

	// out is everything Nim sends the client, from both pumps. See clientOut.
	out *clientOut

	mu       sync.Mutex
	inFlight map[string]pending
	seq      int

	initializeKey   string // id of the initialize request, while it is in flight
	discoverKey     string // id of the server/discover request, while it is in flight
	proposedVersion string // the version that server/discover proposed
	protocolVersion string // what the client and server actually agreed on

	finished sync.Once
	refused  sync.Once

	// stopped guards stopDownstream the way finished guards finish: the
	// signal handler and the normal exit can both reach it, and two
	// concurrent Waits on one Cmd are a data race the race detector found.
	stopped sync.Once
	stopErr error
}

// stop terminates the connector once, whichever path gets there first, and
// hands every caller the same result.
func (s *Shim) stop(cmd *exec.Cmd) error {
	s.stopped.Do(func() { s.stopErr = stopDownstream(cmd) })
	return s.stopErr
}

// clientOut serialises everything Nim writes to the client.
//
// Two goroutines write here: the one relaying the server's responses, and the
// one answering a call that was refused. A write to a pipe larger than the
// kernel's atomic size can be split, and a relayed 512 KiB result with a denial
// spliced through the middle of it is a corrupt message -- measured, not
// assumed.
//
// Go's *os.File happens to take a lock of its own, which is why this does not
// already break. That is an implementation detail of one type, and what the
// relay writes to is an io.Writer.
type clientOut struct {
	mu sync.Mutex
	w  io.Writer
}

func (c *clientOut) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.w.Write(p)
}

// Run relays until the client closes stdin or the downstream server exits.
func Run(opts Options) error {
	// The downstream command is not checked here. A connector can supply it,
	// and which one wins is only known once the daemon has answered.

	// A configuration that cannot work is worth one line on stderr, where the
	// client will show it, rather than a relay that quietly records nothing.
	if err := opts.Config.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "nim: %v\nnim: relaying anyway; calls will not be recorded\n", err)
	}
	if err := opts.Config.EnsureDirs(); err != nil {
		fmt.Fprintf(os.Stderr, "nim: %v\n", err)
	}

	// The one blocking round-trip before anything is spawned: a connector with
	// a credential must have it injected, or the downstream must not start.
	// This uses its own short-lived connection rather than the reporter's,
	// because a connection commits to one purpose on the daemon side and a
	// credential has no business sharing the one that carries events.
	inj, err := fetchConnector(opts.Config, opts.Connector)
	if err != nil {
		return err
	}

	// When a credential is being injected, the daemon decides what receives
	// it. Taking the command from opts instead is what made this a credential
	// oracle: any caller could name a connector and its own command and be
	// handed the secret.
	command := opts.Command
	if len(inj.command) > 0 {
		if len(opts.Command) > 0 && !slices.Equal(opts.Command, inj.command) {
			fmt.Fprintf(os.Stderr,
				"nim: ignoring the command given on the command line; connector %q is registered to run %s\n"+
					"nim: the daemon decides what a credential may be injected into, not the caller\n",
				opts.Connector, strings.Join(inj.command, " "))
		}
		command = inj.command
	}
	if len(command) == 0 {
		return fmt.Errorf(
			"no downstream command: give one after --, or register one with "+
				"nim connector set %s --env KEY -- <command> [args...]", opts.Connector)
	}

	s := &Shim{
		opts:      opts,
		sessionID: config.NewID(),
		reporter:  dialDaemon(opts.Config),
		out:       &clientOut{w: os.Stdout},
		inFlight:  make(map[string]pending),
	}
	defer s.finish()

	// Clients end a session by terminating the relay, not by closing its stdin,
	// so without this almost every session would be recorded as never having
	// ended -- and "never ended" is one of the two shapes the journal uses to
	// report that events went missing. A signal that always fired would make
	// that report worthless.
	//
	// The handler only flushes what is already known and exits; it does not try
	// to keep the relay alive or to clean up the downstream server.
	cmd := buildDownstreamCmd(command, inj.env)
	downIn, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	downOut, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// The downstream server's diagnostics belong to the user, so they are
	// passed straight through rather than captured.
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start %s: %w", command[0], err)
	}

	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stopping
		s.finish()
		// The connector is this relay's child and does not outlive it. Its
		// stdin closes when this process exits, and a connector that reads
		// stdin notices; one that does not -- mid-call, or on its own event
		// loop -- used to be left running, still holding whatever credential
		// it was given, after the journal had recorded the session as over.
		s.stop(cmd)
		// Exiting zero after being asked to stop: the relay did what it was
		// told. Re-raising the signal to reproduce the original exit status
		// would need per-platform code for a status nothing reads.
		os.Exit(0)
	}()

	// Read, never create: replacing a lost identifier would reseed the journal's
	// chain. By now the daemon has had its chance to make one, so on a fresh
	// install this is normally set. When it is not, the field is left empty
	// rather than filled with something invented.
	machineID, _ := config.ReadMachineID()

	s.report(daemon.Event{
		Kind:       daemon.KindSessionStart,
		SessionID:  s.sessionID,
		MachineID:  machineID,
		Client:     opts.Client,
		Connector:  opts.Connector,
		OccurredAt: nowRFC3339(),
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.pumpRequests(os.Stdin, downIn) }()
	go func() { defer wg.Done(); s.pumpResponses(downOut, s.out) }()
	wg.Wait()

	// The client has gone (its stdin closed) or the connector has. Either way
	// the connector's stdin is closed by now; a connector that does not act on
	// that is told, then made to.
	err = s.stop(cmd)
	s.finish()
	return err
}

// stopDownstream waits for the connector to exit on its own, then asks it to,
// then makes it. The waits are short: a connector that has not exited once its
// stdin closed is not going to, and the relay is what a client is waiting on.
//
// The result is cmd.Wait's, so a connector that exited badly is still reported
// as such. A connector this had to terminate reports as an error too, which is
// right: it did not stop when told, and that is worth a line on stderr.
func stopDownstream(cmd *exec.Cmd) error {
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case err := <-exited:
		return err
	case <-time.After(downstreamGrace):
	}
	if cmd.Process != nil {
		terminate(cmd.Process)
	}
	select {
	case err := <-exited:
		return err
	case <-time.After(downstreamGrace):
	}
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
	return <-exited
}

// downstreamGrace is how long a connector gets to exit on its own after its
// stdin closes, and then again after being asked to stop.
const downstreamGrace = 2 * time.Second

// finish closes the session in the record and flushes what is pending. It runs
// once, whether the relay ends because the client went away or because it was
// signalled.
func (s *Shim) finish() {
	s.finished.Do(func() {
		s.mu.Lock()
		version := s.protocolVersion
		s.mu.Unlock()

		s.report(daemon.Event{
			Kind:            daemon.KindSessionEnd,
			SessionID:       s.sessionID,
			ProtocolVersion: version,
			OccurredAt:      nowRFC3339(),
		})
		s.reporter.close()
	})
}

// pumpRequests carries client -> server, deciding every tools/call on the way.
//
// Three things can stop a message here, and nothing else does: a tools/call the
// daemon refused, a batch carrying one, and a frame Nim could not read. Anything
// else goes on as the bytes that arrived.
func (s *Shim) pumpRequests(in io.Reader, out io.WriteCloser) {
	defer out.Close()
	r := mcp.NewReader(in)
	for {
		raw, err := r.ReadRaw()
		if err != nil {
			return
		}

		env, anomaly := mcp.Classify(raw)
		if anomaly != mcp.AnomalyNone {
			s.noteAnomaly(anomaly)
		}

		switch {
		case anomaly == mcp.AnomalyBatch:
			if !s.batchMayPass(raw) {
				continue
			}

		case anomaly != mcp.AnomalyNone:
			// Includes an object that names a key twice. Valid JSON, but the
			// one shape on which Nim and a server may legitimately read
			// different messages, so it is treated exactly as a frame that
			// could not be read at all: not relayed, and not answered, because
			// which of two ids to answer under is the same question.
			// Unreadable, so unaccountable. Go rejects JSON that other parsers
			// accept -- NaN is the easy example -- so a frame Nim cannot parse
			// may still be a tools/call to the server behind it. There is no id
			// to answer with, and guessing one would be worse than silence.
			s.refuse(refusalName(anomaly))
			continue

		case env.IsToolCall():
			if !s.decide(env) {
				continue // refused; the client has already been told
			}

		case env.IsInitialize():
			s.mu.Lock()
			s.initializeKey = env.Key()
			s.mu.Unlock()

		case env.IsDiscover():
			// The other handshake. A client on the 2026-07-28 revision
			// never sends initialize, so this is the only place the
			// version can be learned -- and the version decides the shape
			// a refusal has to take for that client to read it.
			s.mu.Lock()
			s.discoverKey = env.Key()
			s.proposedVersion = env.ProposedProtocolVersion()
			s.mu.Unlock()
		}

		// Byte for byte, whatever it was.
		if _, err := out.Write(raw); err != nil {
			return
		}
	}
}

// decide asks the daemon and answers the client itself when the call is refused.
//
// Reports whether the original bytes may go on to the server. Nothing is
// rewritten on the way: a call either travels exactly as it arrived, or it does
// not travel.
func (s *Shim) decide(env mcp.Envelope) bool {
	// Before a sequence number is spent or the daemon is asked: a call Nim
	// cannot read as exactly one tool name is refused here, whole. The daemon
	// would otherwise decide on "" or on whichever of two names Go's decoder
	// preferred, and the journal would record that as what was called while
	// the server ran something else. No call.request is written for it, as
	// for a refused batch; the anomaly entry is its record.
	call, err := env.Call()
	if err != nil {
		anomaly := mcp.AnomalyUnreadableCall
		if errors.Is(err, mcp.ErrDuplicateKey) {
			anomaly = mcp.AnomalyDuplicateKey
		}
		s.noteAnomaly(anomaly)
		s.refuse("a tools/call it could not read as one tool name")
		if !env.IsNotification() {
			s.toClient(mcp.DenyResponse(env.ID, mcp.DeniedUnreadable, s.negotiated()))
		}
		return false
	}

	key := env.Key()

	s.mu.Lock()
	_, reused := s.inFlight[key]
	s.seq++
	p := pending{tool: call.Name, digest: call.Digest, seq: s.seq, started: time.Now()}
	s.mu.Unlock()

	// Two calls in flight under one id: the second answer cannot be matched to
	// the right request, so neither can be trusted to describe what happened.
	if reused {
		s.noteAnomaly(anomalyDuplicateID)
	}

	// No decision field: the shim does not get to say what was decided. It
	// asks, and the daemon records its own answer. The real arguments travel
	// with the question only as far as ask(), which sends them on only if
	// the daemon says it is holding this call for a human -- see awaitApproval.
	v := s.reporter.ask(daemon.Event{
		Kind:       daemon.KindCallRequest,
		SessionID:  s.sessionID,
		Seq:        p.seq,
		Tool:       p.tool,
		Digest:     p.digest,
		OccurredAt: p.started.UTC().Format(time.RFC3339Nano),
	}, call.Arguments)

	if v == verdictAllow || v == verdictApproved {
		// In flight only now. A refused call never gets an outcome, so it must
		// not be left waiting for one -- that would look like a call that never
		// came back.
		if key != "" {
			s.mu.Lock()
			s.inFlight[key] = p
			s.mu.Unlock()
		}
		return true
	}

	// A tools/call with no id expects no reply, and MCP does not produce one,
	// but nothing here depends on that being true.
	if !env.IsNotification() {
		text := mcp.DeniedNoDecision
		switch v {
		case verdictDeniedByPolicy:
			text = mcp.DeniedByPolicy
		case verdictRejectedByHuman:
			text = mcp.DeniedByHuman
		case verdictApprovalTimedOut:
			text = mcp.DeniedApprovalTimedOut
		}
		s.toClient(mcp.DenyResponse(env.ID, text, s.negotiated()))
	}
	return false
}

// negotiated is the protocol version the session has agreed on so far, or ""
// before either handshake has been answered. A refusal is written in the
// dialect of that version, so a client reads it as the tool error it is.
func (s *Shim) negotiated() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.protocolVersion
}

// batchMayPass reports whether a JSON-RPC batch may be relayed.
//
// A batch is all or nothing. Deciding its elements one by one would need a
// sequence number for each, entries for calls the record has no shape for, and a
// response reader that understands arrays -- a great deal of machinery for
// something the current MCP specification removed. So a batch carrying a
// tools/call is refused whole, and one carrying none is relayed exactly as it
// arrived, as it always was.
func (s *Shim) batchMayPass(raw []byte) bool {
	envs, ok := mcp.BatchElements(raw)
	if !ok {
		s.refuse("a batch Nim could not read")
		return false
	}
	if !slices.ContainsFunc(envs, mcp.Envelope.IsToolCall) {
		return true
	}
	s.refuse("a batch carrying a tools/call")
	s.toClient(mcp.DenyBatch(envs, mcp.DeniedBatch))
	return false
}

// toClient writes one complete message to the client, or nothing.
func (s *Shim) toClient(b []byte) {
	if len(b) == 0 || s.out == nil {
		return
	}
	s.out.Write(b)
}

// refusalName describes an unreadable frame in the terms the user will
// recognise, because "malformed" and "two messages in one line" are different
// problems on their end.
func refusalName(a mcp.Anomaly) string {
	if a == mcp.AnomalyFraming {
		return "a frame carrying more than one message"
	}
	return "a frame that is not valid JSON"
}

// refuse says once that Nim is dropping frames rather than relaying them.
//
// Once, for the same reason the loss warning is: this is on the relay's path,
// and a line per message would bury the client's own output. The anomaly entries
// carry the count.
func (s *Shim) refuse(what string) {
	s.refused.Do(func() {
		fmt.Fprintf(os.Stderr,
			"nim: not relaying %s\nnim: Nim cannot inspect it, and forwarding it would put a call in front of a server unchecked\n",
			what)
	})
}

// pumpResponses carries server -> client, closing the loop on pending calls.
func (s *Shim) pumpResponses(in io.Reader, out io.Writer) {
	r := mcp.NewReader(in)
	for {
		raw, err := r.ReadRaw()
		if err != nil {
			return
		}
		if env, ok := mcp.Parse(raw); ok && env.IsResponse() {
			s.noteResponse(env)
		}
		if _, err := out.Write(raw); err != nil {
			return
		}
	}
}

func (s *Shim) noteResponse(env mcp.Envelope) {
	key := env.Key()

	s.mu.Lock()
	if key != "" && key == s.initializeKey {
		s.protocolVersion = env.ProtocolVersion()
		s.initializeKey = ""
	}
	if key != "" && key == s.discoverKey {
		// An error here means the client will fall back to initialize,
		// which is watched above; the version stays unknown until then.
		if v := env.NegotiatedVersion(s.proposedVersion); v != "" {
			s.protocolVersion = v
		}
		s.discoverKey, s.proposedVersion = "", ""
	}
	p, found := s.inFlight[key]
	delete(s.inFlight, key)
	s.mu.Unlock()
	if !found {
		return // a response to something that was not a tools/call
	}

	ok := !env.Failed()
	ms := int(time.Since(p.started).Milliseconds())
	s.report(daemon.Event{
		Kind:       daemon.KindCallOutcome,
		SessionID:  s.sessionID,
		Seq:        p.seq,
		OK:         &ok,
		DurationMS: &ms,
		OccurredAt: nowRFC3339(),
	})
}

const anomalyDuplicateID = mcp.Anomaly("duplicate_id")

func (s *Shim) noteAnomaly(a mcp.Anomaly) {
	// No seq: an anomaly is not a call, and giving it one would look like a
	// gap in the call sequence when the journal is read back.
	s.report(daemon.Event{
		Kind:       daemon.KindAnomaly,
		SessionID:  s.sessionID,
		Anomaly:    string(a),
		OccurredAt: nowRFC3339(),
	})
}

func (s *Shim) report(ev daemon.Event) { s.reporter.send(ev) }

// verdict is what a tools/call gets back from the daemon.
//
// The zero value is the safe default: anything that is not an explicit
// allow-shaped or deny-shaped answer is treated as no decision at all, so a
// tools/call refused this way reads as retryable, not as ruled against.
type verdict int

const (
	verdictNoDecision verdict = iota
	verdictAllow
	verdictDeniedByPolicy
	// verdictApproved and verdictRejectedByHuman are allow and deny's
	// counterparts for a call an "ask" rule held: a human decided it,
	// instead of a rule. See awaitApproval and
	// docs/decisions/0005-human-approval.md.
	verdictApproved
	verdictRejectedByHuman
	// verdictApprovalTimedOut is its own outcome, not verdictNoDecision:
	// nobody deciding in time is not the daemon failing to answer, and the
	// client is told so in its own words -- see mcp.DeniedApprovalTimedOut.
	verdictApprovalTimedOut
)

// item is one thing to send. reply is nil for the reports that expect no
// answer. arguments is the call's real params.arguments bytes, carried
// alongside the call.request event but sent on the wire only if the daemon
// answers that it is holding this call for a human -- see awaitApproval.
type item struct {
	ev        daemon.Event
	arguments json.RawMessage
	reply     chan verdict
	deadline  time.Time
}

// reporter carries events to the daemon and brings decisions back.
//
// One goroutine owns the connection in both directions. That is not tidiness: a
// decision arrives on the same socket the events leave by, so a second writer
// could interleave bytes between a question and its answer, and a second reader
// could take an answer meant for someone else.
type reporter struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *mcp.Reader

	// approvalTimeout bounds the second, longer wait a call an "ask" rule
	// holds gets -- read once, from the same config the daemon reads its own
	// copy from, rather than hardcoded like decisionTimeout: unlike the 2 s
	// bound, an operator is expected to tune this one.
	approvalTimeout time.Duration

	ch   chan item
	done chan struct{}

	// sendMu guards the channel against being closed while something is still
	// putting work on it. A tools/call can be waiting for up to decisionTimeout
	// when a signal arrives and ends the session, and a send on a closed channel
	// is a panic rather than a lost event.
	sendMu sync.Mutex
	closed bool

	mu     sync.Mutex
	lost   int
	warned bool
	// Causes in the order they were first seen. The first one is what started
	// the loss; overwriting it with whatever happened last describes the
	// aftermath instead, which is misleading when the two differ -- a daemon
	// that stops accepting events later reads as one that was never there.
	causes []string
}

func dialDaemon(cfg config.Config) *reporter {
	r := &reporter{
		ch: make(chan item, 256), done: make(chan struct{}),
		approvalTimeout: cfg.ApprovalTimeoutOrDefault(),
	}
	conn, err := net.DialTimeout("unix", cfg.Daemon.Socket, 300*time.Millisecond)
	if err != nil {
		if StartDaemon() {
			for i := 0; i < 20 && conn == nil; i++ {
				time.Sleep(100 * time.Millisecond)
				conn, _ = net.DialTimeout("unix", cfg.Daemon.Socket, 300*time.Millisecond)
			}
		}
	}
	// An impostor on the socket is treated as no daemon at all, which by this
	// relay's own rule means every call is denied. Accepting its answers
	// instead would hand it the decision: it could allow everything the deny
	// list refuses, and read every tool name and argument digest as they went
	// past. Verified before a single byte is sent, so it never learns what
	// this session was going to ask.
	if conn != nil && !daemonIsGenuine(conn) {
		fmt.Fprintf(os.Stderr,
			"nim: the process listening on %s is not Nim\n"+
				"nim: refusing to take decisions from it; every tool call in this session will be denied\n",
			cfg.Daemon.Socket)
		conn.Close()
		conn = nil
	}

	if conn != nil {
		r.attach(conn)
	} else {
		// Worth two lines rather than the usual one. With no daemon there is
		// nothing to record a call against, so every call in this session will
		// be refused -- and a user who is not told that will read the refusals
		// as the tools being broken.
		fmt.Fprintf(os.Stderr,
			"nim: no daemon is listening on %s\nnim: a call Nim cannot record is a call Nim will not forward, so every tool call in this session will be denied\n",
			cfg.Daemon.Socket)
	}
	go r.loop()
	return r
}

// daemonIsGenuine reports whether the process on the other end of conn is this
// same binary.
//
// Verification used to run in one direction only: the daemon checked its
// callers, and nothing checked the daemon. Reproduced live -- a plain python
// process that binds the socket path first becomes the daemon for every shim
// that starts afterwards. As one it could nominate the command a credential is
// injected into and have the relay spawn it, allow every call the deny list
// refuses, and collect every tool name and argument digest that went past. The
// real daemon, finding the path already bound and answering, concludes another
// daemon is running and exits, so the impostor is not even competing with it.
//
// The socket lives in a per-user directory on macOS, but on Linux without
// XDG_RUNTIME_DIR it is /tmp, where any local user can create a path first.
// Either way a downstream MCP server -- code the operator did not write, which
// is the whole reason peer verification exists -- runs as the same user and
// can do this between one session and the next.
//
// Where the platform cannot answer (Windows, see internal/peer), this reports
// true, exactly as the daemon's own check treats unsupported as not-a-denial.
// It cannot invent a guarantee the OS does not offer, and pretending otherwise
// would only move the gap somewhere less visible.
func daemonIsGenuine(conn net.Conn) bool {
	supported, isSelf := peer.IsSelf(conn)
	return !supported || isSelf
}

// attach binds the connection and the codecs that read and write it, so they
// are never out of step with each other.
func (r *reporter) attach(conn net.Conn) {
	r.conn, r.enc, r.dec = conn, json.NewEncoder(conn), mcp.NewReader(conn)
}

func (r *reporter) loop() {
	defer close(r.done)
	for it := range r.ch {
		if it.reply == nil {
			r.post(it.ev)
			continue
		}
		it.reply <- r.exchange(it)
	}
	if r.conn != nil {
		r.conn.Close()
	}
	r.summarise()
}

// post sends one event and expects nothing back.
func (r *reporter) post(ev daemon.Event) {
	if r.conn == nil {
		r.miss("no daemon is listening")
		return
	}
	r.conn.SetWriteDeadline(time.Now().Add(decisionTimeout))
	if err := r.enc.Encode(ev); err != nil {
		r.drop("the daemon stopped accepting events mid-session")
	}
}

// exchange sends a call.request and waits for the answer to that exact call.
//
// Every failure returns the zero verdict, which is a denial. The queue, the
// socket, the clock and the daemon all get to say no; only one specific reply
// says yes -- or, for a call an ask rule holds, says pending, in which case
// this hands off to awaitApproval for the longer, second wait rather than
// deciding here.
func (r *reporter) exchange(it item) verdict {
	if r.conn == nil {
		r.miss("there was no daemon to decide a call, so it was denied")
		return verdictNoDecision
	}

	r.conn.SetWriteDeadline(it.deadline)
	if err := r.enc.Encode(it.ev); err != nil {
		r.drop("the daemon stopped accepting events mid-session")
		return verdictNoDecision
	}

	r.conn.SetReadDeadline(it.deadline)
	raw, err := r.dec.ReadRaw()
	if err != nil {
		r.drop("the daemon did not decide a call within " + decisionTimeout.String())
		return verdictNoDecision
	}

	d, ok := r.readDecision(raw, it.ev)
	if !ok {
		return verdictNoDecision
	}
	if d.Decision == daemon.DecisionPending {
		return r.awaitApproval(it)
	}
	v, ok := verdictFor(d.Decision)
	if !ok {
		r.drop("the daemon sent a decision Nim does not understand")
		return verdictNoDecision
	}
	return v
}

// readDecision unmarshals one reply and checks it answers the question that
// was actually asked. A reply that does not match means the two are out of
// step, and the next answer would be read as belonging to a different call
// -- which is how a refusal turns into an allowance -- so this drops the
// connection exactly as exchange always has for that case.
func (r *reporter) readDecision(raw []byte, question daemon.Event) (daemon.Decision, bool) {
	var d daemon.Decision
	if err := json.Unmarshal(raw, &d); err != nil {
		r.drop("the daemon sent something that was not a decision")
		return daemon.Decision{}, false
	}
	if d.Kind != daemon.KindDecision || d.SessionID != question.SessionID || d.Seq != question.Seq {
		r.drop("the daemon answered a different call")
		return daemon.Decision{}, false
	}
	return d, true
}

// verdictFor maps the daemon's wire vocabulary to what the shim acts on. ok
// is false for a decision value this build does not know, which the caller
// treats as a protocol break rather than any kind of verdict.
func verdictFor(decision string) (verdict, bool) {
	switch decision {
	case journal.DecisionAllow:
		return verdictAllow, true
	case journal.DecisionDeny:
		return verdictDeniedByPolicy, true
	case journal.DecisionApproved:
		return verdictApproved, true
	case journal.DecisionRejected:
		return verdictRejectedByHuman, true
	case daemon.DecisionUndecided:
		return verdictNoDecision, true
	}
	return verdictNoDecision, false
}

// awaitApproval sends the call's real arguments once the daemon has said it
// is holding the call for a human, then waits up to approvalTimeout for the
// final answer on the same connection -- the same exchange decisionTimeout
// already bounds, only longer, because deciding this one needs a human
// rather than a rule lookup. See docs/decisions/0005-human-approval.md.
//
// A failure here denies the call, exactly as every other path through
// exchange does, and drops the connection: the daemon has its own timer at
// the same bound, armed the moment it started holding the call, so reaching
// this without an answer means even that did not arrive, which is the
// daemon being unreachable, not merely undecided.
func (r *reporter) awaitApproval(it item) verdict {
	deadline := time.Now().Add(r.approvalTimeout)

	r.conn.SetWriteDeadline(deadline)
	if err := r.enc.Encode(daemon.Event{
		Kind: daemon.KindCallArguments, SessionID: it.ev.SessionID, Seq: it.ev.Seq,
		Arguments: it.arguments,
	}); err != nil {
		r.drop("the daemon stopped accepting events mid-session")
		return verdictNoDecision
	}

	r.conn.SetReadDeadline(deadline)
	raw, err := r.dec.ReadRaw()
	if err != nil {
		r.drop("nobody decided a pending call within " + r.approvalTimeout.String())
		return verdictApprovalTimedOut
	}

	d, ok := r.readDecision(raw, it.ev)
	if !ok {
		return verdictNoDecision
	}
	v, ok := verdictFor(d.Decision)
	if !ok {
		r.drop("the daemon sent a decision Nim does not understand")
		return verdictNoDecision
	}
	return v
}

// drop closes the connection for good and counts what it cost.
//
// Terminal on purpose. Reconnecting would mean guessing whether the events on
// either side of the gap belong to the same story, and a relay that keeps trying
// waits the full timeout on every call while denying all of them anyway. Once
// this has happened, the rest of the session denies immediately.
func (r *reporter) drop(reason string) {
	if r.conn != nil {
		r.conn.Close()
	}
	r.conn, r.enc, r.dec = nil, nil, nil
	r.miss(reason)
}

// ask sends a call.request and waits for the daemon's decision. arguments is
// the call's real params.arguments bytes -- see item and awaitApproval; most
// calls never need it, and it is a no-op to carry for those.
//
// The clock starts here rather than at the write, so time spent queued behind
// other reports counts against the same budget. A caller cannot wait longer
// than decisionTimeout for an ordinary verdict, or decisionTimeout plus
// approvalTimeout for a call an ask rule ends up holding -- the outer bound
// below covers both, because ask does not yet know which this call will be.
func (r *reporter) ask(ev daemon.Event, arguments json.RawMessage) verdict {
	reply := make(chan verdict, 1)
	deadline := time.Now().Add(decisionTimeout)
	outer := deadline.Add(r.approvalTimeout)

	if !r.offer(item{ev: ev, arguments: arguments, reply: reply, deadline: deadline}) {
		// A full queue is not a reason to let a call through, and waiting for
		// room would stall behind whatever filled it. Denying is the only answer
		// that is both bounded and safe.
		r.miss("a call needed a decision and there was no room to ask for one")
		return verdictNoDecision
	}

	select {
	case v := <-reply:
		return v
	case <-time.After(time.Until(outer)):
		// The loop sets its own deadlines -- decisionTimeout for the first
		// reply, then its own approvalTimeout-based one if that reply was
		// pending -- so this should be unreachable. It is here because
		// "should be" is not a bound.
		return verdictNoDecision
	}
}

func (r *reporter) send(ev daemon.Event) {
	if !r.offer(item{ev: ev}) {
		// The queue is full: drop rather than stall a tool call. Counted, so
		// the record does not end up quietly short.
		r.miss("events were produced faster than the daemon accepted them")
	}
}

// offer puts work on the queue without ever blocking, and reports whether it
// got there.
func (r *reporter) offer(it item) bool {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	if r.closed {
		return false
	}
	select {
	case r.ch <- it:
		return true
	default:
		return false
	}
}

// miss records one unreported event and says so once, immediately. Once is
// deliberate: the same warning on every call would be noise in the client's
// output, and the count at the end carries the rest.
func (r *reporter) miss(reason string) {
	r.mu.Lock()
	r.lost++
	first := !r.warned
	r.warned = true
	if !slices.Contains(r.causes, reason) {
		r.causes = append(r.causes, reason)
	}
	r.mu.Unlock()

	if first {
		fmt.Fprintf(os.Stderr,
			"nim: not recording every call -- %s\nnim: the relay is unaffected; the journal for this session will be incomplete\n",
			reason)
	}
}

func (r *reporter) summarise() {
	r.mu.Lock()
	lost, causes := r.lost, slices.Clone(r.causes)
	r.mu.Unlock()
	if lost == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "nim: %d event(s) went unrecorded this session (%s)\n",
		lost, strings.Join(causes, "; then "))
}

// close stops the reporter and waits for what is already queued to be sent.
//
// Anything still on the queue is delivered first, including a call waiting for a
// decision: closing the channel ends the range loop only once it is drained, so
// a call in flight when a signal arrives still gets its answer.
func (r *reporter) close() {
	r.sendMu.Lock()
	if !r.closed {
		r.closed = true
		close(r.ch)
	}
	r.sendMu.Unlock()
	<-r.done
}

// StartDaemon launches this same binary in daemon mode, detached so it
// outlives the shim's process group and session -- see daemonSysProcAttr.
//
// Detachment matters more here than it did when the daemon only recorded. A
// terminal SIGINT reaches the whole foreground process group, and a daemon
// that dies with the client session takes enforcement down with it: every
// shim still running would then deny every call, because that is what an
// unreachable daemon means. The daemon has to outlive any single session for
// the fail-closed default to be a safety net rather than an outage.
//
// Several shims may race; the daemon that loses the socket exits quietly.
// Exported so connector management commands, which also need the daemon
// running, can reuse it instead of duplicating process-spawn logic.
//
// Its diagnostics go to a log file rather than nowhere. A daemon that fails to
// start silently leaves the user believing calls are being recorded when they
// are not, which is the one failure this tool must never have.
func StartDaemon() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	// The log lives in the home directory, which on a fresh install does not
	// exist yet when `nim connector set` is the first thing run. Opening the
	// file then failed, the error was dropped, and the daemon's first words
	// -- the ones that explain why it did not start -- went nowhere.
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "nim: cannot create %s for the daemon's log: %v\n", config.Home(), err)
	}
	log, err := os.OpenFile(config.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nim: cannot open %s: the daemon will start with no log: %v\n", config.LogPath(), err)
		log = nil
	}
	return startDaemonProcess(self, log) == nil
}

// DialDaemon connects for one request/response exchange -- a connector, an
// enrolment or a rule -- starting the daemon if it is not running, and
// refusing to hand back a connection to anything that is not Nim.
//
// The refusal is the point. The relay verified the daemon before sending it a
// byte, but the management commands dialled the socket path and trusted
// whatever answered: `nim connector set` would have written the plaintext
// secret to any process that had bound the path first. Exported so every
// command that talks to the daemon goes through the one check.
func DialDaemon(cfg config.Config) (net.Conn, error) {
	conn, err := dialOrStart(cfg)
	if err != nil {
		return nil, err
	}
	if !daemonIsGenuine(conn) {
		conn.Close()
		return nil, fmt.Errorf(
			"the process listening on %s is not Nim; refusing to talk to it", cfg.Daemon.Socket)
	}
	return conn, nil
}

// ErrDaemonNotReachable means nothing answered the daemon's socket within the
// timeout -- as opposed to something answering that is not Nim, which
// DialRunningDaemon reports as a different error entirely. The distinction
// matters to a caller like nim doctor: the first is the ordinary state of an
// install nobody has started yet, worth a WARN; the second means something is
// impersonating the daemon, worth a FAIL.
var ErrDaemonNotReachable = errors.New("no daemon reachable")

// DialRunningDaemon connects to the daemon's socket without starting one, and
// verifies the peer is genuinely this binary.
//
// Unlike DialDaemon, which brings the daemon up when nothing answers because
// that is the right thing for a command that needs a decision, this is for a
// caller whose whole job is reporting what is true right now: starting the
// thing being checked would make "is it running" unanswerable. nim doctor is
// the one caller today, and it opens one of these per check, exactly as
// DialDaemon's callers open one connection per request kind.
func DialRunningDaemon(cfg config.Config, timeout time.Duration) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", cfg.Daemon.Socket, timeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDaemonNotReachable, err)
	}
	if !daemonIsGenuine(conn) {
		conn.Close()
		return nil, fmt.Errorf("the process listening on %s is not Nim; refusing to talk to it", cfg.Daemon.Socket)
	}
	return conn, nil
}

// dialOrStart reaches the daemon socket, starting a daemon when nothing
// answers. Peer identity is the caller's business: the two callers treat an
// unreachable daemon differently, and both must verify what they reached.
func dialOrStart(cfg config.Config) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", cfg.Daemon.Socket, 300*time.Millisecond)
	if err == nil {
		return conn, nil
	}
	if !StartDaemon() {
		return nil, fmt.Errorf("could not start the daemon")
	}
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		if conn, err = net.DialTimeout("unix", cfg.Daemon.Socket, 300*time.Millisecond); err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("daemon did not become reachable at %s", cfg.Daemon.Socket)
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// injection is what the daemon authorized for one connector: the environment
// to add, and the command that environment may be added to. Both or neither --
// a credential is never separable from the process allowed to receive it.
type injection struct {
	env     []string
	command []string
}

// buildDownstreamCmd is split out so a test can inspect cmd.Args and cmd.Env
// directly: a credential must reach the downstream only through the latter,
// never the former, where it would be visible to any local user via ps.
func buildDownstreamCmd(command []string, env []string) *exec.Cmd {
	cmd := exec.Command(command[0], command[1:]...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd
}

// fetchConnector asks the daemon what to inject for connector, and into what,
// before anything is spawned. Four outcomes, and which of them is open and
// which is closed is argued in docs/decisions/0001-failure-behaviour.md:
//
//   - the daemon cannot be reached at all: treated as "no connector", so the
//     relay still starts. Nothing is leaked by this -- there is no credential
//     to leak -- and every tools/call in that session is denied anyway,
//     because an unreachable daemon is a denial. A server that starts and
//     refuses tools tells the operator what is wrong; one that fails to start
//     does not.
//   - the daemon answers Found=false: no connector configured. The ordinary
//     case for most connectors, and not a failure.
//   - Found=true with Error set: a connector IS configured but its credential
//     cannot be released. Fails closed -- Run refuses to spawn rather than
//     start the downstream under a partial configuration. A connector
//     registered before commands existed lands here, deliberately.
//   - Found=true with no error: the env to inject and the one command it may
//     be injected into. A response carrying a credential but no command is
//     itself refused; that pairing is the whole authorization.
func fetchConnector(cfg config.Config, connector string) (injection, error) {
	conn, err := dialOrStart(cfg)
	if err != nil {
		return injection{}, nil // unreachable: no connector, and every call will be denied
	}
	defer conn.Close()

	// Whoever is on the other end of this decides what command receives a
	// credential, so it has to be Nim. Refusing to spawn is the only safe
	// answer here: an impostor's answer is worse than no answer.
	if !daemonIsGenuine(conn) {
		return injection{}, fmt.Errorf(
			"the process listening on %s is not Nim; refusing to ask it for a credential or a command",
			cfg.Daemon.Socket)
	}

	conn.SetDeadline(time.Now().Add(2 * time.Second))

	if err := json.NewEncoder(conn).Encode(daemon.Request{
		ID:     config.NewID(),
		Kind:   daemon.KindCredentialGet,
		Target: connector,
	}); err != nil {
		return injection{}, nil // could not ask; see above
	}
	var resp daemon.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return injection{}, nil
	}

	if !resp.Found {
		return injection{}, nil
	}
	if resp.Error != "" {
		return injection{}, fmt.Errorf(
			"connector %q is configured but its credential could not be retrieved: %s", connector, resp.Error)
	}
	if len(resp.Env) > 0 && len(resp.Command) == 0 {
		// The daemon should never send this. Refusing rather than falling back
		// to the caller's own command means a future daemon bug cannot quietly
		// reopen the oracle this pairing exists to close.
		return injection{}, fmt.Errorf(
			"connector %q returned a credential with no authorized command; refusing to spawn", connector)
	}

	pairs := make([]string, 0, len(resp.Env))
	for k, v := range resp.Env {
		pairs = append(pairs, k+"="+v)
	}
	return injection{env: pairs, command: resp.Command}, nil
}
