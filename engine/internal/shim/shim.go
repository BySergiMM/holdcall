// Package shim is the relay a client actually spawns.
//
// It sits between the client's stdio and a downstream MCP server, forwarding
// every message untouched. It recognises tools/call, pairs each request with
// its response by id to learn the outcome and duration, and reports both to the
// daemon.
//
// The relay's first duty is to be invisible. Reporting is best-effort and
// happens off the critical path: if the daemon is missing or slow, messages
// still flow and the client notices nothing. Nothing is ever blocked here.
//
// Best-effort reporting means the record can be incomplete, so the relay counts
// what it fails to report and says so on stderr. It does not try to recover the
// lost events: this whole reporting path is replaced when calls start being
// authorized, and a repair built on top of it would be thrown away with it.
package shim

import (
	"encoding/json"
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
)

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

	mu       sync.Mutex
	inFlight map[string]pending
	seq      int

	initializeKey   string // id of the initialize request, while it is in flight
	protocolVersion string // what the client and server actually agreed on

	finished sync.Once
}

// Run relays until the client closes stdin or the downstream server exits.
func Run(opts Options) error {
	if len(opts.Command) == 0 {
		return fmt.Errorf("no downstream command given")
	}

	// A configuration that cannot work is worth one line on stderr, where the
	// client will show it, rather than a relay that quietly records nothing.
	if err := opts.Config.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "nim: %v\nnim: relaying anyway; calls will not be recorded\n", err)
	}
	if err := opts.Config.EnsureDirs(); err != nil {
		fmt.Fprintf(os.Stderr, "nim: %v\n", err)
	}

	s := &Shim{
		opts:      opts,
		sessionID: config.NewID(),
		reporter:  dialDaemon(opts.Config),
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
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stopping
		s.finish()
		// Exiting zero after being asked to stop: the relay did what it was
		// told. Re-raising the signal to reproduce the original exit status
		// would need per-platform code for a status nothing reads.
		os.Exit(0)
	}()

	cmd := exec.Command(opts.Command[0], opts.Command[1:]...)
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
		return fmt.Errorf("cannot start %s: %w", opts.Command[0], err)
	}

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
	go func() { defer wg.Done(); s.pumpResponses(downOut, os.Stdout) }()
	wg.Wait()

	err = cmd.Wait()
	s.finish()
	return err
}

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

// pumpRequests carries client -> server, noting every tools/call on the way.
func (s *Shim) pumpRequests(in io.Reader, out io.WriteCloser) {
	defer out.Close()
	r := mcp.NewReader(in)
	for {
		raw, err := r.ReadRaw()
		if err != nil {
			return
		}
		env, anomaly := mcp.Classify(raw)
		switch {
		case anomaly != mcp.AnomalyNone:
			s.noteAnomaly(anomaly)
		case env.IsToolCall():
			s.noteRequest(env)
		case env.IsInitialize():
			s.mu.Lock()
			s.initializeKey = env.Key()
			s.mu.Unlock()
		}
		// Byte for byte, whatever it was.
		if _, err := out.Write(raw); err != nil {
			return
		}
	}
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

func (s *Shim) noteRequest(env mcp.Envelope) {
	key := env.Key()

	s.mu.Lock()
	_, reused := s.inFlight[key]
	s.seq++
	p := pending{tool: env.ToolName(), digest: env.ArgumentsDigest(), seq: s.seq, started: time.Now()}
	if key != "" {
		s.inFlight[key] = p
	}
	s.mu.Unlock()

	// Two calls in flight under one id: the second answer cannot be matched to
	// the right request, so neither can be trusted to describe what happened.
	if reused {
		s.noteAnomaly(anomalyDuplicateID)
	}

	// Recorded on the way out, so a call that never returns is still on record.
	s.report(daemon.Event{
		Kind:       daemon.KindCallRequest,
		SessionID:  s.sessionID,
		Seq:        p.seq,
		Tool:       p.tool,
		Digest:     p.digest,
		Decision:   journal.DecisionObserved, // nothing is authorized here
		OccurredAt: p.started.UTC().Format(time.RFC3339Nano),
	})
}

func (s *Shim) noteResponse(env mcp.Envelope) {
	key := env.Key()

	s.mu.Lock()
	if key != "" && key == s.initializeKey {
		s.protocolVersion = env.ProtocolVersion()
		s.initializeKey = ""
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

// reporter delivers events to the daemon without ever blocking the relay.
type reporter struct {
	conn net.Conn
	ch   chan daemon.Event
	done chan struct{}

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
	r := &reporter{ch: make(chan daemon.Event, 256), done: make(chan struct{})}
	conn, err := net.DialTimeout("unix", cfg.Daemon.Socket, 300*time.Millisecond)
	if err != nil {
		if startDaemon() {
			for i := 0; i < 20 && conn == nil; i++ {
				time.Sleep(100 * time.Millisecond)
				conn, _ = net.DialTimeout("unix", cfg.Daemon.Socket, 300*time.Millisecond)
			}
		}
	}
	r.conn = conn
	go r.loop()
	return r
}

func (r *reporter) loop() {
	defer close(r.done)
	enc := json.NewEncoder(r.conn)
	for ev := range r.ch {
		if r.conn == nil {
			r.miss("no daemon is listening")
			continue
		}
		if err := enc.Encode(ev); err != nil {
			r.conn.Close()
			r.conn = nil
			r.miss("the daemon stopped accepting events mid-session")
		}
	}
	if r.conn != nil {
		r.conn.Close()
	}
	r.summarise()
}

func (r *reporter) send(ev daemon.Event) {
	select {
	case r.ch <- ev:
	default:
		// The queue is full: drop rather than stall a tool call. Counted, so
		// the record does not end up quietly short.
		r.miss("events were produced faster than the daemon accepted them")
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

func (r *reporter) close() {
	close(r.ch)
	<-r.done
}

// startDaemon launches this same binary in daemon mode. Several shims may race;
// the daemon that loses the socket exits quietly.
//
// Its diagnostics go to a log file rather than nowhere. A daemon that fails to
// start silently leaves the user believing calls are being recorded when they
// are not, which is the one failure this tool must never have.
func startDaemon() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	cmd := exec.Command(self, "daemon")
	if log, err := os.OpenFile(config.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = log, log
	}
	return cmd.Start() == nil
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }
