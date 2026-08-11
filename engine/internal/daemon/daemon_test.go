package daemon

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
	_ "modernc.org/sqlite"
)

func openJournal(t *testing.T) *journal.Journal {
	t.Helper()
	j, _ := openJournalWithPath(t)
	return j
}

// openJournalWithPath also returns the database file's path, for tests that
// need to inspect columns apply() has no exported way to read back.
func openJournalWithPath(t *testing.T) (*journal.Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nim.db")
	j, err := journal.Open(path)
	if err != nil {
		t.Fatalf("journal.Open: %v", err)
	}
	t.Cleanup(func() { j.Close() })
	return j, path
}

// tempSocketPath keeps the path short: AF_UNIX addresses are capped near 104
// bytes, and t.TempDir()'s nested test-name directories can overflow that.
func tempSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nimd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestApplySessionStartThenCallThenEnd(t *testing.T) {
	j, path := openJournalWithPath(t)

	if err := apply(Event{Kind: KindSessionStart, SessionID: "s1", Target: "github", OccurredAt: nowRFC()}, j); err != nil {
		t.Fatalf("session.start: %v", err)
	}
	// This only succeeds if the session row genuinely exists: nim_calls has a
	// foreign key on session_id (see journal_test.go), so it is a real check
	// that apply() wired SessionStart through to journal.StartSession.
	if err := apply(Event{Kind: KindCall, SessionID: "s1", Seq: 1, Tool: "echo", OccurredAt: nowRFC()}, j); err != nil {
		t.Fatalf("call: %v", err)
	}
	if err := apply(Event{Kind: KindSessionEnd, SessionID: "s1", OccurredAt: nowRFC()}, j); err != nil {
		t.Fatalf("session.end: %v", err)
	}

	n, err := j.CountCalls()
	if err != nil {
		t.Fatalf("CountCalls: %v", err)
	}
	if n != 1 {
		t.Fatalf("got %d calls, want 1", n)
	}

	// nim_calls.id is typed uuid in the Supabase mirror (supabase/migrations);
	// a locally-minted id that does not already look like one fails to sync.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening the journal file directly: %v", err)
	}
	defer db.Close()
	var id string
	if err := db.QueryRow(`select id from nim_calls limit 1`).Scan(&id); err != nil {
		t.Fatalf("querying nim_calls.id: %v", err)
	}
	if !uuidPattern.MatchString(id) {
		t.Errorf("nim_calls.id = %q, does not look like a uuid", id)
	}
}

// The FK-based check above proves a session row exists; it says nothing
// about whether apply() copied the right event field into the right journal
// field (a swap between Client and Target would pass it silently).
func TestApplySessionStartStoresFieldsAndSessionEndSetsEndedAt(t *testing.T) {
	j, path := openJournalWithPath(t)

	if err := apply(Event{
		Kind: KindSessionStart, SessionID: "s1", MachineID: "m1", Client: "claude", Target: "github",
		OccurredAt: nowRFC(),
	}, j); err != nil {
		t.Fatalf("session.start: %v", err)
	}
	endedAt := nowRFC()
	if err := apply(Event{Kind: KindSessionEnd, SessionID: "s1", OccurredAt: endedAt}, j); err != nil {
		t.Fatalf("session.end: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening the journal file directly: %v", err)
	}
	defer db.Close()

	var machineID, client, target string
	var gotEndedAt sql.NullString
	err = db.QueryRow(`select machine_id, client, target, ended_at from nim_sessions where id = ?`, "s1").
		Scan(&machineID, &client, &target, &gotEndedAt)
	if err != nil {
		t.Fatalf("querying nim_sessions: %v", err)
	}
	if machineID != "m1" || client != "claude" || target != "github" {
		t.Errorf("session fields = (machine_id=%q, client=%q, target=%q), want (m1, claude, github)", machineID, client, target)
	}
	if !gotEndedAt.Valid || gotEndedAt.String != endedAt {
		t.Errorf("ended_at = %v, want %q", gotEndedAt, endedAt)
	}
}

func TestApplyRejectsAnUnknownKind(t *testing.T) {
	j := openJournal(t)
	if err := apply(Event{Kind: "bogus", SessionID: "s1"}, j); err == nil {
		t.Fatal("an unrecognised event kind must be reported, not silently dropped")
	}
}

func TestParseTimeFallsBackOnUnparsable(t *testing.T) {
	before := time.Now()
	got := parseTime("not a timestamp")
	if got.Before(before) {
		t.Errorf("parseTime fell back to a time before the call: %v", got)
	}
}

func TestParseTimeRoundTripsRFC3339Nano(t *testing.T) {
	want := time.Date(2026, 3, 5, 12, 0, 0, 123000000, time.UTC)
	got := parseTime(want.Format(time.RFC3339Nano))
	if !got.Equal(want) {
		t.Errorf("parseTime(%s) = %v, want %v", want.Format(time.RFC3339Nano), got, want)
	}
}

func TestListenReclaimsAStaleSocketFile(t *testing.T) {
	path := tempSocketPath(t)
	// Leave a socket file behind with nothing listening on it -- exactly
	// what a crashed daemon does.
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("creating a stale socket: %v", err)
	}
	stale.Close() // the file stays on disk; nothing is bound to it anymore

	ln, err := listen(path)
	if err != nil {
		t.Fatalf("listen did not reclaim a stale socket: %v", err)
	}
	ln.Close()
}

func TestListenReportsAnAlreadyRunningDaemon(t *testing.T) {
	path := tempSocketPath(t)
	first, err := listen(path)
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}
	defer first.Close()

	if _, err := listen(path); !errors.Is(err, errAlreadyRunning) {
		t.Fatalf("second listen on a live socket: got %v, want errAlreadyRunning", err)
	}
}

// M3 non-negotiable: credential.get answers with real secret material on
// this socket, so it must not be connectable by any other local user.
// net.Listen alone leaves the file at whatever the umask allows -- verified
// separately to be group/other-readable under a common 022 umask -- so
// listen() must chmod it explicitly rather than relying on directory
// permissions holding in every environment.
func TestListenRestrictsSocketPermissionsTo0600(t *testing.T) {
	path := tempSocketPath(t)
	ln, err := listen(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket permissions = %v, want 0600", got)
	}
}

// The reclaimed-stale-socket path binds via the same net.Listen call as the
// fresh case, but it is worth confirming explicitly: this is the more
// common real-world path (a crash leaves a socket behind, the next daemon
// reclaims it) and must not regress separately from the fresh-install case.
func TestListenRestrictsPermissionsAfterReclaimingAStaleSocket(t *testing.T) {
	path := tempSocketPath(t)
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("creating a stale socket: %v", err)
	}
	stale.Close()

	ln, err := listen(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket permissions after reclaim = %v, want 0600", got)
	}
}

func TestHandleAppliesEvents(t *testing.T) {
	j := openJournal(t)
	client, server := net.Pipe()
	// Registered before any assertion runs, so a Fatalf here does not leave
	// handle's goroutine blocked in a read forever.
	t.Cleanup(func() { client.Close(); server.Close() })
	done := make(chan struct{})
	go func() { handle(server, j, nil, newTargetLocks()); close(done) }()

	enc := json.NewEncoder(client)
	if err := enc.Encode(Event{Kind: KindSessionStart, SessionID: "s1", Target: "github", OccurredAt: nowRFC()}); err != nil {
		t.Fatalf("encode session.start: %v", err)
	}
	if err := enc.Encode(Event{Kind: KindCall, SessionID: "s1", Seq: 1, Tool: "echo", OccurredAt: nowRFC()}); err != nil {
		t.Fatalf("encode call: %v", err)
	}
	client.Close() // EOF ends handle's loop

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handle did not return after the connection closed")
	}

	n, err := j.CountCalls()
	if err != nil {
		t.Fatalf("CountCalls: %v", err)
	}
	if n != 1 {
		t.Fatalf("got %d calls, want 1", n)
	}
}

// A shim reports events best-effort; the daemon must not let one bad message
// take the whole connection down, or everything after it silently vanishes.
func TestHandleSkipsMalformedEventsAndKeepsReading(t *testing.T) {
	j := openJournal(t)
	client, server := net.Pipe()
	// Registered before any assertion runs, so a Fatalf here does not leave
	// handle's goroutine blocked in a read forever.
	t.Cleanup(func() { client.Close(); server.Close() })
	done := make(chan struct{})
	go func() { handle(server, j, nil, newTargetLocks()); close(done) }()

	if _, err := client.Write([]byte("not json at all\n")); err != nil {
		t.Fatalf("write malformed line: %v", err)
	}
	enc := json.NewEncoder(client)
	if err := enc.Encode(Event{Kind: KindSessionStart, SessionID: "s1", Target: "github", OccurredAt: nowRFC()}); err != nil {
		t.Fatalf("encode session.start: %v", err)
	}
	if err := enc.Encode(Event{Kind: KindCall, SessionID: "s1", Seq: 1, Tool: "echo", OccurredAt: nowRFC()}); err != nil {
		t.Fatalf("encode call: %v", err)
	}
	client.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handle did not return after the connection closed")
	}

	n, err := j.CountCalls()
	if err != nil {
		t.Fatalf("CountCalls: %v", err)
	}
	if n != 1 {
		t.Fatalf("a malformed line must not stop later valid events from being recorded; got %d calls, want 1", n)
	}
}

// The M3 final audit found that handle() used to read connections with an
// unbounded line reader (mcp.Reader), reachable by any process able to open
// the socket -- before peer verification and before MaxRequestBytes ever
// run, since both require the full line first. A single unauthenticated
// connection sending 200 MiB with no newline grew the live daemon's RSS by
// the same amount with no pushback at all. This is the live regression
// test: a real net.Listen/Accept connection sending well over maxLineBytes
// with no newline must be disconnected, not served indefinitely.
func TestHandleDisconnectsAConnectionSendingAnOversizedLine(t *testing.T) {
	j := openJournal(t)
	path := tempSocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	done := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		handle(conn, j, nil, newTargetLocks())
		close(done)
	}()

	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Comfortably over maxLineBytes, with no newline anywhere in it.
	chunk := make([]byte, 64*1024)
	for i := range chunk {
		chunk[i] = 'A'
	}
	go func() {
		for i := 0; i < 64; i++ { // 4 MiB total, 4x maxLineBytes
			if _, err := client.Write(chunk); err != nil {
				return
			}
		}
	}()

	select {
	case <-done:
		// handle() returned on its own: the oversized line was rejected and
		// the connection closed from the daemon side, exactly as intended.
	case <-time.After(5 * time.Second):
		t.Fatal("handle() did not disconnect a connection sending an oversized line -- it is still buffering it")
	}
}
