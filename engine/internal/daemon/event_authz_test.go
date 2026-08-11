package daemon

import (
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
)

// serveOne runs handle() against one accepted connection and returns when it
// is done, so a test can assert on what reached the journal after the daemon
// has finished with the connection rather than racing it.
func serveOne(t *testing.T, j *journal.Journal, ln net.Listener) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		handle(conn, j, newFakeStore(), newTargetLocks())
	}()
	return done
}

// countRows is deliberately raw SQL rather than a journal method: the point
// of these tests is what physically landed in the tables, not what a
// convenience accessor is willing to report.
func countRows(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`select count(*) from ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestANonNimProcessCannotWriteToTheJournal is the second attack found by the
// audit, reproduced end to end through the real handle() dispatch.
//
// Before the fix, peer verification lived inside handleRequest, so it only
// ever covered credential.get and connector.*. Every Event reached apply()
// unauthenticated, and this exact sequence -- sent from python, over a raw
// socket, by a process that is not this binary -- put a fabricated session
// and a fabricated *allowed* call to "delete_repository" into the journal.
//
// On a hash-chained journal that is worse than it looks: the daemon chains
// whatever it is given, so the forged entries verify cleanly and the chain
// launders the forgery instead of detecting it.
func TestANonNimProcessCannotWriteToTheJournal(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	j, path := openJournalWithPath(t)

	sock := tempSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	done := serveOne(t, j, ln)

	// The attacker: a real, separate, non-nim process speaking the event
	// protocol by hand. This is the audit's reproduction, verbatim in shape.
	script := `
import socket, sys, json, datetime
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); s.connect(sys.argv[1])
now = datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00','Z')
s.sendall((json.dumps({"kind":"session.start","session_id":"forged-session",
    "machine_id":"FORGED-MACHINE","client":"attacker","target":"github",
    "occurred_at":now})+"\n").encode())
s.sendall((json.dumps({"kind":"call","session_id":"forged-session","seq":1,
    "tool":"delete_repository","params_digest":"0"*64,"decision":"allow",
    "ok":True,"duration_ms":1,"occurred_at":now})+"\n").encode())
s.close()
`
	cmd := exec.Command("python3", "-c", script, sock)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("attacker script: %v: %s", err, out)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handle did not return; the connection should have been closed")
	}

	if n := countRows(t, path, "nim_sessions"); n != 0 {
		t.Errorf("a non-nim process wrote %d session rows; the journal is forgeable", n)
	}
	if n := countRows(t, path, "nim_calls"); n != 0 {
		t.Errorf("a non-nim process wrote %d call rows; the journal is forgeable", n)
	}
}

// TestEventsFromTheSameBinaryStillLand is the mirror image: authorizing the
// event path must not lock out the legitimate shim, which is the only thing
// that ever sends events. This test binary stands in for the real nim binary,
// exactly as the credential tests do -- every nim serve is a copy of one
// binary too.
func TestEventsFromTheSameBinaryStillLand(t *testing.T) {
	if os.Getenv("NIM_TEST_EVENT_SENDER") != "" {
		return // re-exec guard; the helper below is the real subprocess
	}
	j, path := openJournalWithPath(t)

	sock := tempSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	done := serveOne(t, j, ln)

	// The helper holds its connection open after writing, and this test kills
	// it once the rows have landed. That is not test scaffolding to make a
	// flaky thing pass: peer identity is read from the kernel's record of a
	// live connection, so a client that writes and exits in the same instant
	// can genuinely be gone before the daemon looks. Every real client stays
	// -- a shim for its whole session, a connector command until its response
	// arrives -- and this one models that rather than the impossible case.
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcessSendEvents")
	cmd.Env = append(os.Environ(), "NIM_TEST_HELPER_SOCKET="+sock, "NIM_TEST_EVENT_SENDER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting helper: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	deadline := time.Now().Add(10 * time.Second)
	for {
		if countRows(t, path, "nim_calls") == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the legitimate binary's events never landed: %d sessions, %d calls",
				countRows(t, path, "nim_sessions"), countRows(t, path, "nim_calls"))
		}
		time.Sleep(10 * time.Millisecond)
	}

	if n := countRows(t, path, "nim_sessions"); n != 1 {
		t.Errorf("the legitimate binary wrote %d sessions, want 1", n)
	}

	cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handle did not return after the peer went away")
	}
}

// TestHelperProcessSendEvents is re-executed as a subprocess by the test
// above. It is not a real test: it is this same binary, genuinely running as
// a separate process, sending the events a shim sends.
func TestHelperProcessSendEvents(t *testing.T) {
	sock := os.Getenv("NIM_TEST_HELPER_SOCKET")
	if sock == "" {
		t.Skip("not the helper process")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ok := true
	ms := 3
	for _, ev := range []Event{
		{Kind: KindSessionStart, SessionID: "real", MachineID: "m", Target: "github", OccurredAt: now},
		{Kind: KindCall, SessionID: "real", Seq: 1, Tool: "list_repos", Digest: "d", Decision: "allow", OK: &ok, DurationMS: &ms, OccurredAt: now},
	} {
		if err := enc.Encode(ev); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	// Stay alive, holding the connection, exactly as a real client does. The
	// parent kills this process once it has seen the events land.
	time.Sleep(30 * time.Second)
}

// TestAConnectionCannotWriteToASessionItDidNotStart covers the second half of
// the fix. Peer identity narrows a caller to "something running Nim's code",
// which any local process can be by executing the binary. Session ownership
// is what stops one such caller appending entries under a session another
// one opened -- the difference between a record of what an agent did and a
// record of what someone said an agent did.
func TestAConnectionCannotWriteToASessionItDidNotStart(t *testing.T) {
	j, path := openJournalWithPath(t)

	// The victim's session exists in the journal already, started elsewhere.
	if err := apply(Event{
		Kind: KindSessionStart, SessionID: "victim", MachineID: "m",
		Target: "github", OccurredAt: nowRFC(),
	}, j); err != nil {
		t.Fatalf("seeding the victim session: %v", err)
	}

	// A second connection, which never started that session, tries to append
	// a call to it.
	state := &connState{}
	state.claimSession("its-own-session")

	err := applyOwned(Event{
		Kind: KindCall, SessionID: "victim", Seq: 1, Tool: "delete_repository",
		Digest: "d", Decision: "allow", OccurredAt: nowRFC(),
	}, state, j)
	if err == nil {
		t.Fatal("a connection appended a call to a session it did not start")
	}

	if n := countRows(t, path, "nim_calls"); n != 0 {
		t.Errorf("%d call rows landed despite the refusal", n)
	}

	// And session.end for someone else's session is refused the same way.
	if err := applyOwned(Event{Kind: KindSessionEnd, SessionID: "victim", OccurredAt: nowRFC()}, state, j); err == nil {
		t.Error("a connection ended a session it did not start")
	}
}

// TestOwnSessionEventsAreAccepted is the ordinary path: one connection
// starts a session and then reports on it, which is exactly what a shim does.
func TestOwnSessionEventsAreAccepted(t *testing.T) {
	j, path := openJournalWithPath(t)
	state := &connState{}

	for _, ev := range []Event{
		{Kind: KindSessionStart, SessionID: "mine", MachineID: "m", Target: "github", OccurredAt: nowRFC()},
		{Kind: KindCall, SessionID: "mine", Seq: 1, Tool: "list_repos", Digest: "d", Decision: "allow", OccurredAt: nowRFC()},
		{Kind: KindSessionEnd, SessionID: "mine", OccurredAt: nowRFC()},
	} {
		if err := applyOwned(ev, state, j); err != nil {
			t.Fatalf("%s on its own session was refused: %v", ev.Kind, err)
		}
	}
	if n := countRows(t, path, "nim_calls"); n != 1 {
		t.Errorf("got %d calls, want 1", n)
	}
}

// TestSessionStartWithNoIDIsRefused: an empty id would otherwise be claimed
// and then match every other event carrying no id, turning the ownership
// check into a no-op for anything that simply omits the field.
func TestSessionStartWithNoIDIsRefused(t *testing.T) {
	j := openJournal(t)
	state := &connState{}

	if err := applyOwned(Event{Kind: KindSessionStart, OccurredAt: nowRFC()}, state, j); err == nil {
		t.Fatal("session.start with no id was accepted")
	}
	if err := applyOwned(Event{Kind: KindCall, Seq: 1, OccurredAt: nowRFC()}, state, j); err == nil {
		t.Fatal("a call with no session id was accepted after an empty start")
	}
}
