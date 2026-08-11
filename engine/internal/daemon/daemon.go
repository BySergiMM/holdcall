// Package daemon owns everything shared between shims.
//
// A client spawns one shim per configured MCP server, so budgets, the journal
// and (later) human approval need a single writer. The shims report here; this
// process is the only one that touches SQLite, and the only one that talks to
// the OS credential store.
//
// The wire protocol is one-way for Event: a shim never waits for an answer
// reporting a tools/call, because the relay must never stall on bookkeeping.
// Request/Response is the one deliberate exception -- a shim blocks briefly
// on credential.get before spawning a downstream -- and is a structurally
// separate message family so a credential can never flow through the Event
// path, which gets logged and journaled.
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
	"time"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/credential"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/uuid"
)

// Event is one report from a shim.
type Event struct {
	Kind       string `json:"kind"`
	SessionID  string `json:"session_id"`
	MachineID  string `json:"machine_id,omitempty"`
	Client     string `json:"client,omitempty"`
	Target     string `json:"target,omitempty"`
	Seq        int    `json:"seq,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Digest     string `json:"params_digest,omitempty"`
	Decision   string `json:"decision,omitempty"`
	OK         *bool  `json:"ok,omitempty"`
	DurationMS *int   `json:"duration_ms,omitempty"`
	OccurredAt string `json:"occurred_at,omitempty"`
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

	// A missing credential store must not take the whole daemon down: event
	// recording (M1/M2) has nothing to do with connectors, and must keep
	// working regardless. Only credential.get/connector.* requests need
	// store to be non-nil; handleRequest reports its absence to those
	// specifically, once, rather than refusing to start at all.
	store, err := credential.New()
	if err != nil {
		log.Printf("credential store unavailable, connector commands will fail: %v", err)
		store = nil
	}

	locks := newTargetLocks()

	log.Printf("nim daemon listening on %s (journal: %s)", cfg.Daemon.Socket, cfg.DatabasePath())
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go handle(conn, j, store, locks)
	}
}

var errAlreadyRunning = errors.New("another daemon holds the socket")

// listen binds the socket, clearing a stale file left by a crashed daemon but
// never one a live daemon is using.
//
// Several daemons can start at once -- a client spawning several shims after
// a crash left a stale socket behind all race to reclaim the same path.
// acquireStartupLock serializes the whole check-and-reclaim sequence below
// across those processes on unix, which is what actually closes the race: two
// processes can each observe the socket as stale in the same window, and
// without the lock one could unlink the socket the other just bound. The
// retry loop remains underneath it as the fallback on platforms where that
// lock is a no-op (see lock_windows.go) -- weaker, but still better than a
// single attempt, and a daemon that ultimately loses backs off cleanly on its
// next dial rather than unlinking a live socket forever.
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
			// umask leaves it -- often group/other-readable, which is
			// connectable by any local user (verified: 022 umask gives
			// srwxr-xr-x). That was a low-severity gap while this socket
			// only carried call metadata; it is not acceptable now that
			// credential.get answers with real secret material on it.
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

// requestKinds distinguishes a Request from an Event on the wire: both carry
// a "kind" field, but the two vocabularies never overlap, so a cheap peek at
// just that field is enough to route the rest of the message correctly
// without needing an extra discriminator field.
var requestKinds = map[string]bool{
	KindCredentialGet:   true,
	KindConnectorSet:    true,
	KindConnectorList:   true,
	KindConnectorRemove: true,
}

// connState is per-connection authorization state, computed lazily and
// cached for the life of the connection: peer identity does not change
// mid-connection, so it is checked once, not once per request.
//
// kind and boundTarget are the second authorization layer, independent of
// peer identity: a connection is committed to exactly one purpose (either
// asking for its own target's credential, or being a connector-management
// client) on its first request, and exactly one target if that purpose is
// credential.get. A real shim only ever does one or the other; anything
// that tries to mix them, or pivot credential.get to a second target on the
// same connection, gets "unauthorized" instead of a more specific reason --
// specific reasons are exactly the kind of oracle an attacker iterating on
// this protocol would want.
type connState struct {
	conn net.Conn

	peerChecked   bool
	peerSupported bool
	peerIsSelf    bool

	kind        string // "" | "credential" | "connector"
	boundTarget string // meaningful only once kind == "credential"

	// sessions is every session id this connection opened with a
	// session.start. An event naming any other session is refused: see
	// ownsSession.
	sessions map[string]bool
}

// ownsSession reports whether this connection may write an event for id.
//
// A shim opens one connection and keeps it for the whole session (see the
// shim's dialDaemon -- the same conn carries the spawn-time credential
// request and then every event), so a connection legitimately only ever
// reports on sessions it started itself. Without this check any connection
// could append entries under another session's id, which is how a record of
// what one agent did gets attributed to a different one.
//
// This is a companion to the peer check, not a substitute: peer identity
// answers "is this Nim", session ownership answers "is this the same run of
// Nim that opened the session". Neither alone is enough.
func (s *connState) ownsSession(id string) bool { return s.sessions[id] }

func (s *connState) claimSession(id string) {
	if s.sessions == nil {
		s.sessions = make(map[string]bool)
	}
	s.sessions[id] = true
}

// authorized reports whether this connection's peer may say anything on
// this socket at all -- events and requests alike. Where peer verification
// is supported (macOS, Linux), the peer must be running this exact binary --
// a claim inside the message itself proves nothing, since an attacker can
// write the same claim. Where it is not supported (Windows, see
// peer_windows.go), this cannot add anything beyond the socket's own
// permissions; that is a real, documented gap, not a silent one.
//
// What this does NOT establish, on any platform: that the caller is a shim
// the operator meant to run. Any local process can execute this binary, so
// passing this check only narrows the caller to "something running Nim's
// code". Credentials need a stronger answer than that -- see
// handleCredentialGet and the connector command binding it enforces.
func (s *connState) authorized() bool {
	if !s.peerChecked {
		s.peerSupported, s.peerIsSelf = verifyPeerIsSelf(s.conn)
		s.peerChecked = true
	}
	if s.peerSupported {
		return s.peerIsSelf
	}
	return true
}

func handle(conn net.Conn, j *journal.Journal, store credential.Store, locks *targetLocks) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, 4096)
	enc := json.NewEncoder(conn)
	state := &connState{conn: conn}

	// Identify the peer now, while it is certainly still alive, rather than
	// on the first message.
	//
	// Both sides of this are real. A socket keeps delivering buffered bytes
	// after the process that wrote them has exited, so a short-lived
	// legitimate client -- one that writes its events and leaves -- could be
	// read from after its pid was gone, and LOCAL_PEERPID would resolve to
	// nothing: a genuine client denied for being fast. Found by the test
	// below, which failed exactly that way. The mirror is that a pid can be
	// recycled in that same window, so a late check can also describe a
	// different process than the one that connected.
	//
	// Checking at accept closes both. The result is cached on connState, so
	// everything after this reads a decision made when the answer was
	// certain; the branches below still choose what to DO with it, because a
	// Request can be answered with a refusal and an Event cannot.
	state.authorized()

	for {
		raw, err := boundedReadRaw(br, maxLineBytes)
		if err != nil {
			if err != io.EOF {
				log.Printf("shim connection: %v", err)
			}
			return
		}

		// id is captured here too so an oversized-request rejection can
		// still echo it, without the full Request unmarshal below -- which
		// is what would allocate a Go string for a multi-megabyte Secret
		// field this message is about to be rejected for anyway.
		var peek struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			log.Printf("malformed message: %v", err)
			continue
		}

		if requestKinds[peek.Kind] {
			if len(raw) > MaxRequestBytes {
				resp := Response{ID: peek.ID, Error: fmt.Sprintf("request exceeds %d byte limit", MaxRequestBytes)}
				if err := enc.Encode(resp); err != nil {
					log.Printf("writing response: %v", err)
					return
				}
				continue
			}
			var req Request
			if err := json.Unmarshal(raw, &req); err != nil {
				// Never log req here: it failed to fully decode, but a
				// partial/garbled connector.set payload could still carry
				// enough of a secret fragment to be worth not printing.
				log.Printf("malformed request (kind=%s)", peek.Kind)
				continue
			}
			resp := handleRequest(req, state, j, store, locks)
			if err := enc.Encode(resp); err != nil {
				log.Printf("writing response: %v", err)
				return
			}
			continue
		}

		// The event path is authorized exactly like the request path.
		//
		// It was not, until now: verification lived inside handleRequest, so
		// only credential.get and connector.* were ever checked, and every
		// Event reached the journal unauthenticated. Reproduced live during
		// the audit that found it -- a plain python socket client wrote a
		// fabricated session and a fabricated allowed call to
		// "delete_repository" into the journal, from a process that is not
		// this binary. A record anything can write is not a record.
		//
		// Unlike a Request, an Event has nothing to answer with: it is
		// one-way by design, so there is no Response to carry a refusal. The
		// connection is closed instead, which also stops an unauthorized peer
		// from continuing to send.
		if !state.authorized() {
			log.Printf("refusing events from an unverified peer")
			return
		}

		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			log.Printf("malformed event: %v", err)
			continue
		}
		if err := applyOwned(ev, state, j); err != nil {
			log.Printf("%s: %v", ev.Kind, err)
		}
	}
}

// applyOwned enforces session ownership, then applies.
//
// session.start claims the id for this connection; every later event has to
// name a session this connection claimed. A shim satisfies that without
// changing anything -- it opens one connection and starts one session on it.
func applyOwned(ev Event, state *connState, j *journal.Journal) error {
	switch ev.Kind {
	case KindSessionStart:
		if ev.SessionID == "" {
			return fmt.Errorf("session.start with no session id")
		}
		state.claimSession(ev.SessionID)
	case KindCall, KindSessionEnd:
		if !state.ownsSession(ev.SessionID) {
			return fmt.Errorf(
				"refusing %s for session %s: this connection did not start it", ev.Kind, ev.SessionID)
		}
	}
	return apply(ev, j)
}

// handleRequest enforces both authorization layers before dispatching, then
// routes to the specific handler. None of the handlers below may log req or
// any secret-bearing value they compute -- Response.Error is the only
// channel back to the caller, and must describe the failure without ever
// including a credential value.
func handleRequest(req Request, state *connState, j *journal.Journal, store credential.Store, locks *targetLocks) Response {
	var thisKind string
	switch req.Kind {
	case KindCredentialGet:
		thisKind = "credential"
	case KindConnectorSet, KindConnectorList, KindConnectorRemove:
		thisKind = "connector"
	default:
		return Response{ID: req.ID, Error: fmt.Sprintf("unknown request kind %q", req.Kind)}
	}
	if state.kind == "" {
		state.kind = thisKind
	} else if state.kind != thisKind {
		return Response{ID: req.ID, Error: "unauthorized"}
	}
	if !state.authorized() {
		return Response{ID: req.ID, Error: "unauthorized"}
	}

	switch req.Kind {
	case KindCredentialGet:
		return handleCredentialGet(req, state, j, store, locks)
	case KindConnectorSet:
		return handleConnectorSet(req, j, store, locks)
	case KindConnectorList:
		return handleConnectorList(req, j)
	case KindConnectorRemove:
		return handleConnectorRemove(req, j, store, locks)
	default:
		panic("unreachable: thisKind switch above is exhaustive for req.Kind")
	}
}

// handleCredentialGet is the shim's spawn-time lookup. Found=false with no
// error is the ordinary case for a target with no connector configured, and
// the shim must treat it exactly like today: spawn with no env changes.
// Found=true with a non-empty Error is the fail-closed case: a connector IS
// configured but the secret cannot be released, and the shim must refuse to
// spawn rather than start the downstream under a partial configuration.
//
// The answer carries the command the credential may be injected into, and
// the shim spawns that rather than whatever was on its own command line.
// That is the authorization boundary, and it exists because the two checks
// around it are not one.
//
// Peer verification asks "is the caller this binary". Any local process
// satisfies that by executing the binary, so on its own it authorizes
// nothing: reproduced live during the audit that found it, where
//
//	nim serve --target github -- /bin/sh -c 'echo $GITHUB_TOKEN'
//
// printed the real secret. Both documented layers passed -- the caller was
// genuinely this binary, and the connection genuinely asked for one target
// and only one. Neither constrains *which* target may be asked for, or what
// receives the answer. A caller that chooses the command that receives a
// secret has the secret.
//
// So the command is registered with the connector, in SQLite, by whoever
// stored the credential, and the daemon hands it back rather than accepting
// one. An attacker naming another target now gets that target's real
// downstream server spawned, which is not a way to read the secret.
//
// A connection is still bound to the target of its first credential.get and
// may never ask for a different one: that stops a pivot within one
// connection, which remains worth stopping even though it was never the
// whole story.
//
// What this does not close, stated rather than glossed: the registered argv
// is spawned through the usual PATH resolution, so a caller that already
// controls PATH can still put its own binary in front of the registered
// name. Closing that needs the daemon to spawn the downstream itself, which
// is a larger change than this one and is not pretended here.
//
// The metadata lookup and the secret lookup below are two separate reads,
// not one atomic operation, so this takes the same per-target lock
// connector.set/remove use: without it, a connector.set renaming a target's
// env_key concurrently with this call can interleave between the two reads
// and hand back one generation's env_key paired with a different
// generation's secret value -- reproduced live during the M3 final audit
// (thousands of torn reads out of 20000 iterations under -race). The
// secret's value is always a genuine credential for this exact target
// either way, so this was never a cross-target leak, but it is a real
// atomicity bug the lock exists to prevent on the write side already; the
// read side needs the same guarantee.
func handleCredentialGet(req Request, state *connState, j *journal.Journal, store credential.Store, locks *targetLocks) Response {
	if err := validateTarget(req.Target); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if state.boundTarget == "" {
		state.boundTarget = req.Target
	} else if state.boundTarget != req.Target {
		return Response{ID: req.ID, Error: "unauthorized"}
	}

	unlock := locks.Lock(req.Target)
	defer unlock()

	info, found, err := j.ConnectorInfo(req.Target)
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("looking up connector metadata: %v", err)}
	}
	if !found {
		return Response{ID: req.ID, Found: false}
	}
	// A connector with no registered command is not one without a
	// restriction: it is one whose authorized command is unknown, which is
	// the state every connector stored before commands existed is in. There
	// is nothing safe to inject the secret into, so nothing is injected.
	if len(info.Command) == 0 {
		return Response{ID: req.ID, Found: true, Error: fmt.Sprintf(
			"connector %q has no authorized command: it was registered before Nim bound credentials to a command. "+
				"Register it again with the server it belongs to, e.g. "+
				"nim connector set %s --env %s -- <command> [args...]",
			req.Target, req.Target, info.EnvKey)}
	}
	if store == nil {
		return Response{ID: req.ID, Found: true, Error: "credential store unavailable"}
	}
	secret, err := store.Get(req.Target)
	if err != nil {
		return Response{ID: req.ID, Found: true, Error: fmt.Sprintf("retrieving secret for connector %q: %v", req.Target, err)}
	}
	return Response{
		ID:      req.ID,
		Found:   true,
		Env:     map[string]string{info.EnvKey: secret},
		Command: info.Command,
	}
}

func handleConnectorSet(req Request, j *journal.Journal, store credential.Store, locks *targetLocks) Response {
	if err := validateTarget(req.Target); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if err := validateEnvKey(req.EnvKey); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if err := validateSecret(req.Secret); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if err := validateCommand(req.Command); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if store == nil {
		return Response{ID: req.ID, Error: "credential store unavailable"}
	}

	unlock := locks.Lock(req.Target)
	defer unlock()

	if err := store.Set(req.Target, req.Secret); err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("storing secret: %v", err)}
	}
	if err := j.SetConnector(req.Target, req.EnvKey, req.Command, time.Now()); err != nil {
		// The keychain write succeeded but the metadata write did not: undo
		// it so the two do not silently drift apart -- an orphaned keychain
		// entry with no matching metadata would be invisible to
		// connector.list but still retrievable by anyone who guesses the
		// target name.
		_ = store.Delete(req.Target)
		return Response{ID: req.ID, Error: fmt.Sprintf("storing connector metadata: %v", err)}
	}
	return Response{ID: req.ID}
}

func handleConnectorList(req Request, j *journal.Journal) Response {
	list, err := j.ListConnectors()
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("listing connectors: %v", err)}
	}
	infos := make([]ConnectorInfo, len(list))
	for i, c := range list {
		infos[i] = ConnectorInfo{
			Target:    c.Target,
			EnvKey:    c.EnvKey,
			Command:   c.Command,
			UpdatedAt: c.UpdatedAt.Format(time.RFC3339Nano),
		}
	}
	return Response{ID: req.ID, Connectors: infos}
}

// The secret is removed before the metadata, deliberately the mirror image
// of handleConnectorSet's ordering: if the secret delete fails partway
// through, leaving the metadata in place means the connector still shows up
// as configured and the next credential.get correctly fails closed on it,
// rather than the secret quietly surviving in the store with nothing left
// pointing at it. The lock is the same per-target lock connector.set uses,
// so a set and a remove for the same target can never interleave either.
func handleConnectorRemove(req Request, j *journal.Journal, store credential.Store, locks *targetLocks) Response {
	if err := validateTarget(req.Target); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}

	unlock := locks.Lock(req.Target)
	defer unlock()

	if store != nil {
		if err := store.Delete(req.Target); err != nil && !errors.Is(err, credential.ErrNotFound) {
			return Response{ID: req.ID, Error: fmt.Sprintf("removing secret: %v", err)}
		}
	}
	if err := j.DeleteConnector(req.Target); err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("removing connector metadata: %v", err)}
	}
	return Response{ID: req.ID}
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
			// The row is keyed for idempotency by (session_id, seq), not by
			// id (see journal.RecordCall's ON CONFLICT clause), so a fresh id
			// on every report -- request and response alike -- is safe: only
			// the first one survives once the update path takes over.
			ID:           uuid.New(),
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
