// Package shim is the relay a client actually spawns.
//
// It sits between the client's stdio and a downstream MCP server, forwarding
// every message untouched. It recognises tools/call, pairs each request with
// its response by id to learn the outcome and duration, and reports both to the
// daemon.
//
// The relay's first duty is to be invisible. Reporting is best-effort and
// happens off the critical path: if the daemon is missing or slow, messages
// still flow and the client notices nothing. In M1 nothing is ever blocked.
package shim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/mcp"
)

// Options describes one relayed server.
type Options struct {
	Target  string   // name of the downstream server, e.g. "github"
	Client  string   // best-effort label for the MCP client, may be empty
	Command []string // the downstream server and its arguments
	Config  config.Config
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

	mu      sync.Mutex
	inFlight map[string]pending
	seq      int
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
		sessionID: newID(),
		reporter:  dialDaemon(opts.Config),
		inFlight:  make(map[string]pending),
	}
	defer s.reporter.close()

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

	s.report(daemon.Event{
		Kind:       daemon.KindSessionStart,
		SessionID:  s.sessionID,
		MachineID:  machineID(),
		Client:     opts.Client,
		Target:     opts.Target,
		OccurredAt: nowRFC3339(),
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.pumpRequests(os.Stdin, downIn) }()
	go func() { defer wg.Done(); s.pumpResponses(downOut, os.Stdout) }()
	wg.Wait()

	err = cmd.Wait()

	s.report(daemon.Event{
		Kind:       daemon.KindSessionEnd,
		SessionID:  s.sessionID,
		OccurredAt: nowRFC3339(),
	})
	return err
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
		if env, ok := mcp.Parse(raw); ok && env.IsToolCall() {
			s.noteRequest(env)
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
	s.mu.Lock()
	s.seq++
	p := pending{tool: env.ToolName(), digest: env.ArgumentsDigest(), seq: s.seq, started: time.Now()}
	if key := env.Key(); key != "" {
		s.inFlight[key] = p
	}
	s.mu.Unlock()

	// Recorded on the way out, so a call that never returns is still on record.
	s.report(daemon.Event{
		Kind:       daemon.KindCall,
		SessionID:  s.sessionID,
		Seq:        p.seq,
		Tool:       p.tool,
		Digest:     p.digest,
		Decision:   "allow", // M1 never blocks; policy arrives in M4.
		OccurredAt: nowRFC3339(),
	})
}

func (s *Shim) noteResponse(env mcp.Envelope) {
	key := env.Key()
	s.mu.Lock()
	p, found := s.inFlight[key]
	delete(s.inFlight, key)
	s.mu.Unlock()
	if !found {
		return // a response to something that was not a tools/call
	}

	ok := !env.Failed()
	ms := int(time.Since(p.started).Milliseconds())
	s.report(daemon.Event{
		Kind:       daemon.KindCall,
		SessionID:  s.sessionID,
		Seq:        p.seq,
		Tool:       p.tool,
		Digest:     p.digest,
		Decision:   "allow",
		OK:         &ok,
		DurationMS: &ms,
		OccurredAt: p.started.UTC().Format(time.RFC3339Nano),
	})
}

func (s *Shim) report(ev daemon.Event) { s.reporter.send(ev) }

// reporter delivers events to the daemon without ever blocking the relay.
type reporter struct {
	conn net.Conn
	ch   chan daemon.Event
	done chan struct{}
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
			continue // drained and dropped: the relay is what matters
		}
		if err := enc.Encode(ev); err != nil {
			r.conn.Close()
			r.conn = nil
		}
	}
	if r.conn != nil {
		r.conn.Close()
	}
}

func (r *reporter) send(ev daemon.Event) {
	select {
	case r.ch <- ev:
	default: // the queue is full; drop rather than stall a tool call
	}
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

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// machineID is a stable, opaque identifier for this install. It is not a
// hostname and not a user: the mirror must not carry either.
func machineID() string {
	path := config.Home() + string(os.PathSeparator) + "machine-id"
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return string(b)
	}
	id := newID()
	_ = os.MkdirAll(config.Home(), 0o700)
	_ = os.WriteFile(path, []byte(id), 0o600)
	return id
}
