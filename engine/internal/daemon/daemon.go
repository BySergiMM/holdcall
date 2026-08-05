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
	Kind       string  `json:"kind"`
	SessionID  string  `json:"session_id"`
	MachineID  string  `json:"machine_id,omitempty"`
	Client     string  `json:"client,omitempty"`
	Target     string  `json:"target,omitempty"`
	Seq        int     `json:"seq,omitempty"`
	Tool       string  `json:"tool,omitempty"`
	Digest     string  `json:"params_digest,omitempty"`
	Decision   string  `json:"decision,omitempty"`
	OK         *bool   `json:"ok,omitempty"`
	DurationMS *int    `json:"duration_ms,omitempty"`
	OccurredAt string  `json:"occurred_at,omitempty"`
}

const (
	KindSessionStart = "session.start"
	KindCall         = "call"
	KindSessionEnd   = "session.end"
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

	j, err := journal.Open(cfg.DatabasePath())
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	defer j.Close()

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
		}
	}
}

func apply(ev Event, j *journal.Journal) error {
	switch ev.Kind {
	case KindSessionStart:
		return j.StartSession(journal.Session{
			ID:        ev.SessionID,
			MachineID: ev.MachineID,
			Client:    ev.Client,
			Target:    ev.Target,
			StartedAt: parseTime(ev.OccurredAt),
		})
	case KindCall:
		return j.RecordCall(journal.Call{
			ID:           fmt.Sprintf("%s-%d", ev.SessionID, ev.Seq),
			SessionID:    ev.SessionID,
			Seq:          ev.Seq,
			Tool:         ev.Tool,
			ParamsDigest: ev.Digest,
			Decision:     ev.Decision,
			OK:           ev.OK,
			DurationMS:   ev.DurationMS,
			OccurredAt:   parseTime(ev.OccurredAt),
		})
	case KindSessionEnd:
		return j.EndSession(ev.SessionID, parseTime(ev.OccurredAt))
	default:
		return fmt.Errorf("unknown kind %q", ev.Kind)
	}
}

func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Now().UTC()
}
