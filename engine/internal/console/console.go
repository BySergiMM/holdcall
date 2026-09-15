// Package console serves a local, read-only view of what Nim has recorded.
//
// It is an observability tool and nothing else. It does not decide anything, it
// cannot change anything, and Nim does not know it exists: close it and the
// engine behaves identically. Everything it shows comes from the journal by way
// of readmodel, so there is no second copy of the record to disagree with the
// first.
//
// This is not the control plane. That name belongs to the privileged write path
// -- registering connectors, granting, revoking, approving -- which has to be
// unreachable from where an agent can talk. Nothing here may ever grow a write
// endpoint; the day one is wanted, it belongs somewhere else, with a human in
// front of it.
package console

import (
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/readmodel"
)

//go:embed index.html
var assets embed.FS

// DefaultLimit is how many entries a page request returns when it does not say.
const DefaultLimit = 200

// daemonProbeTimeout bounds the socket check. The console must stay responsive
// when the daemon is wedged, which is exactly when someone is looking at it.
const daemonProbeTimeout = 300 * time.Millisecond

// Server answers the console's requests. It holds a read-only journal and the
// path of the daemon's socket, and nothing else.
type Server struct {
	src    readmodel.Source
	socket string
	mux    *http.ServeMux
}

// New builds the server. src must be a read-only journal: nothing here writes,
// but the guarantee should come from the handle rather than from this promise.
func New(src readmodel.Source, socketPath string) *Server {
	s := &Server{src: src, socket: socketPath, mux: http.NewServeMux()}
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/api/snapshot", s.handleSnapshot)
	s.mux.HandleFunc("/api/events", s.handleEvents)
	s.mux.HandleFunc("/api/sessions/", s.handleSession)
	s.mux.HandleFunc("/api/policy", s.handlePolicy)
	return s
}

// Handler wraps the routes with the three rules that make this safe to leave
// running: nothing but GET, no guessing at content types, and no answering to
// a name that is not loopback.
//
// The last one is what Listen's loopback bind does not give. A page on any
// site can point a script at http://its-own-name:7717 and have DNS answer
// 127.0.0.1 for that name -- rebinding -- after which the browser treats the
// console as the page's own origin and hands it the record: tool names,
// connectors, session ids, digests, the chain head. The bind address never
// sees the difference; the Host header does, so a request that arrived under
// any other name is refused before a route runs.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostIsLoopback(r.Host) {
			http.Error(w, "the console answers only to a loopback name", http.StatusMisdirectedRequest)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "the console only reads", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		s.mux.ServeHTTP(w, r)
	})
}

// hostIsLoopback reports whether a Host header names this machine and nothing
// else: localhost, or a literal loopback address, with or without a port.
func hostIsLoopback(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return isLoopback(strings.Trim(host, "[]"))
}

// Listen binds the console. The address must be loopback: this serves the
// contents of a journal -- tool names, connectors, digests -- and while none of
// that is a credential, it is a record of what an agent did and has no business
// on a network interface.
func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("address must be host:port, got %q", addr)
	}
	if !isLoopback(host) {
		return nil, fmt.Errorf(
			"the console binds to loopback only; %q is not a loopback address", host)
	}
	return net.Listen("tcp", addr)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Exact match only. This serves one embedded page and never reaches the
	// filesystem, so there is no path to traverse, but an unknown path should
	// still say so rather than render the console.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "console page missing from the binary", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// daemonState is the one thing on this page that does not come from the
// journal. Running means the socket accepted a connection just now -- which is
// what `nim status` means by it, and all that can be shown without asking the
// daemon questions its protocol does not answer.
type daemonState struct {
	Running bool   `json:"running"`
	Socket  string `json:"socket"`
}

// snapshotResponse is the console's own shape, not the journal's. The two are
// separate so that changing what a page shows never becomes a reason to change
// how an entry is stored.
type snapshotResponse struct {
	Daemon        daemonState            `json:"daemon"`
	Journal       readmodel.JournalState `json:"journal"`
	CallsRecorded int                    `json:"calls_recorded"`
	Gaps          readmodel.Gaps         `json:"gaps"`
	Sessions      []readmodel.Session    `json:"sessions"`
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	snap, err := readmodel.Take(s.src)
	if err != nil {
		fail(w, err)
		return
	}
	sessions, err := readmodel.Sessions(s.src, readmodel.MaxSessions)
	if err != nil {
		fail(w, err)
		return
	}
	if sessions == nil {
		sessions = []readmodel.Session{}
	}
	writeJSON(w, snapshotResponse{
		Daemon:        daemonState{Running: s.daemonRunning(), Socket: s.socket},
		Journal:       snap.Journal,
		CallsRecorded: snap.CallsRecorded,
		Gaps:          snap.Gaps,
		Sessions:      sessions,
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	since, err := intParam(r, "since", 0)
	if err != nil {
		http.Error(w, "since must be a number", http.StatusBadRequest)
		return
	}
	limit, err := intParam(r, "limit", DefaultLimit)
	if err != nil {
		http.Error(w, "limit must be a number", http.StatusBadRequest)
		return
	}
	if limit <= 0 || limit > journal.MaxEntriesPerRead {
		limit = DefaultLimit
	}

	page, err := readmodel.Stream(s.src, since, int(limit))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, page)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if !validSessionID(id) {
		http.Error(w, "not a session id", http.StatusBadRequest)
		return
	}
	detail, found, err := readmodel.Detail(s.src, id, journal.MaxEntriesPerRead)
	if err != nil {
		fail(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, detail)
}

// handlePolicy serves what may happen: the rules, agents and connectors held
// outside the chain in nim_rules, nim_agents and nim_connectors. Unlike
// everything else this server shows, none of it is a record of something
// that occurred -- it is the configuration a decision reads, which is why it
// gets its own endpoint rather than a field on the snapshot.
func (s *Server) handlePolicy(w http.ResponseWriter, r *http.Request) {
	pol, err := readmodel.TakePolicy(s.src)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, pol)
}

// validSessionID keeps anything surprising out of the query. Ids are opaque
// hex, so this is deliberately narrower than "whatever SQLite would accept":
// the parameterised query is what prevents injection, and this is what stops a
// request from being interesting in the first place.
func validSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func (s *Server) daemonRunning() bool {
	conn, err := net.DialTimeout("unix", s.socket, daemonProbeTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func intParam(r *http.Request, name string, fallback int64) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return // the reader went away
	}
}

// fail reports a read that did not work without leaking where the journal
// lives or what the query was.
func fail(w http.ResponseWriter, err error) {
	http.Error(w, "could not read the journal: "+err.Error(), http.StatusInternalServerError)
}
