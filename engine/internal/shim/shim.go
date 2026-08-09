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
	"github.com/BySergiMM/nim/engine/internal/uuid"
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

	mu       sync.Mutex
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
		sessionID: uuid.New(),
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

	mid, midErr := machineID()
	if midErr != nil {
		// Reported but not fatal: the relay must keep running regardless.
		// The daemon's own NOT NULL constraint on machine_id is the backstop
		// that refuses to record this session under a fabricated identity.
		fmt.Fprintf(os.Stderr, "nim: %v\n", midErr)
	}
	s.report(daemon.Event{
		Kind:       daemon.KindSessionStart,
		SessionID:  s.sessionID,
		MachineID:  mid,
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

// startDaemon launches this same binary in daemon mode, detached so it
// outlives the shim's process group and session -- see daemonSysProcAttr.
// Several shims may race; the daemon that loses the socket exits quietly.
//
// Its diagnostics go to a log file rather than nowhere. A daemon that fails to
// start silently leaves the user believing calls are being recorded when they
// are not, which is the one failure this tool must never have.
func startDaemon() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	log, _ := os.OpenFile(config.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	return startDaemonProcess(self, log) == nil
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// machineID is a stable, opaque identifier for this install. It is not a
// hostname and not a user: the mirror must not carry either.
//
// Several shims for several configured MCP servers routinely start at once
// (see startDaemon's "several shims may race" comment), so on a fresh install
// this can genuinely be called from more than one process concurrently.
// os.WriteFile would let each one truncate and overwrite the file with its
// own id, leaving whichever wrote last as the winner -- silently splitting
// one machine's history across two ids in the mirror. O_EXCL makes only the
// first create succeed; every other racer waits for what the winner writes
// rather than minting a second id of its own.
//
// The one case this cannot paper over is the winner dying between creating
// the file and writing to it: every later caller would find a permanently
// empty file, unable to either read an id from it or O_EXCL-create it afresh.
// Minting a fallback id there would be exactly the bug this function exists
// to prevent -- a second, inconsistent id for one install -- so this reports
// an error instead. Callers must not paper over that with their own fallback
// id either; the caller here reports the row without a machine_id rather than
// inventing one, and the daemon-side NOT NULL constraint refuses to record
// it under a fabricated identity.
func machineID() (string, error) {
	path := config.Home() + string(os.PathSeparator) + "machine-id"
	if id, ok := readMachineID(path); ok {
		return id, nil
	}
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		return "", fmt.Errorf("machine id: %w", err)
	}

	id := uuid.New()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		if _, writeErr := f.Write([]byte(id)); writeErr != nil {
			f.Close()
			os.Remove(path) // do not leave a permanently unwritable file for the next caller
			return "", fmt.Errorf("machine id: writing %s: %w", path, writeErr)
		}
		f.Close()
		return id, nil
	}
	if !os.IsExist(err) {
		return "", fmt.Errorf("machine id: creating %s: %w", path, err)
	}

	// Someone else is creating it. Their write is a handful of bytes and
	// finishes almost immediately; wait for it rather than returning a
	// second, inconsistent id for the same install.
	for i := 0; i < 20; i++ {
		if got, ok := readMachineID(path); ok {
			return got, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return "", fmt.Errorf("machine id: %s exists but was never written; a previous process may have died while creating it", path)
}

func readMachineID(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}
