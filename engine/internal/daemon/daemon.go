// Package daemon owns everything shared between shims.
//
// A client spawns one shim per configured MCP server, so budgets, the journal
// and (later) human approval need a single writer. The shims report here; this
// process is the only one that touches SQLite.
//
// The wire protocol is deliberately one-way. A shim never waits for an answer,
// because the relay must never stall on bookkeeping: if the daemon is slow,
// absent or wedged, tool calls still flow.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/mcp"
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
const (
	KindSessionStart = journal.KindSessionStart
	KindCallRequest  = journal.KindCallRequest
	KindCallOutcome  = journal.KindCallOutcome
	KindSessionEnd   = journal.KindSessionEnd
	KindAnomaly      = journal.KindAnomaly
)

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

	log.Printf("nim daemon listening on %s (journal: %s)", cfg.Daemon.Socket, cfg.DatabasePath())
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go handle(conn, j)
	}
}

var errAlreadyRunning = errors.New("another daemon holds the socket")

// listen binds the socket, clearing a stale file left by a crashed daemon but
// never one a live daemon is using.
func listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err == nil {
		return ln, nil
	}
	// Something is at that path. Ask it whether it is alive.
	if conn, dialErr := net.DialTimeout("unix", path, 500*time.Millisecond); dialErr == nil {
		conn.Close()
		return nil, errAlreadyRunning
	}
	if rmErr := os.Remove(path); rmErr != nil {
		return nil, err
	}
	return net.Listen("unix", path)
}

func handle(conn net.Conn, j *journal.Journal) {
	defer conn.Close()

	// Sessions opened on this connection that have not been closed yet.
	//
	// A shim closes its own session when it exits or is asked to stop, but a
	// client that kills it outright leaves no chance to. The daemon can still
	// tell, because the connection goes with the process, so it closes the
	// session itself rather than leaving one that looks abandoned.
	//
	// What survives this is the case worth keeping: if the *daemon* dies, it
	// writes nothing, and the session stays open in the record -- which is
	// exactly the period during which events were being lost.
	open := map[string]bool{}
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
		}
	}()

	r := mcp.NewReader(conn)
	for {
		raw, err := r.ReadRaw()
		if err != nil {
			if err != io.EOF {
				log.Printf("shim connection: %v", err)
			}
			return
		}
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			log.Printf("malformed event: %v", err)
			continue
		}
		if err := apply(ev, j); err != nil {
			log.Printf("%s: %v", ev.Kind, err)
			continue
		}
		switch ev.Kind {
		case KindSessionStart:
			open[ev.SessionID] = true
		case KindSessionEnd:
			delete(open, ev.SessionID)
		}
	}
}

// apply turns a report into one journal entry. Every kind appends; nothing
// updates. A shim that reports the same call twice -- once on the way out and
// once when it returns -- produces two entries, and the pair is joined by
// (session_id, seq) when read back.
func apply(ev Event, j *journal.Journal) error {
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
