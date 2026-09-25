package daemon

import (
	"encoding/json"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// python3 is used rather than nc because it is the one that reproduced this
// originally, and because it can hold a connection open while the daemon
// looks at it -- peer identity is read from a live connection.
func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
}

// attack runs script against sock as a genuinely separate, non-holdcall process
// and returns whatever it printed.
//
// A non-zero exit is not a test failure. The daemon closes an unverified
// connection immediately, so the attacker's own second write usually dies
// with a broken pipe -- which is the defence working, not the test breaking.
// What the attack achieved is asserted from the daemon's side.
func attack(t *testing.T, sock, script string) string {
	t.Helper()
	out, _ := exec.Command("python3", "-c", script, sock).CombinedOutput()
	return string(out)
}

// TestANonNimProcessCannotWriteToTheJournal: the socket was open to anything
// running as the same user, so any local process could fabricate a record.
//
// On a milestone that enforces this is worse than it was on one that only
// observed. The journal is what the decision is written to, and a chain that
// covers forged entries laundered the forgery rather than detecting it: the
// daemon chains whatever it is given, so `holdcall verify` reported the result as
// sound.
func TestANonNimProcessCannotWriteToTheJournal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("peer.IsSelf reports unsupported on windows (see internal/peer/peer_windows.go), and " +
			"daemon.go's own accept-time check only refuses a peer when supported && !isSelf -- so an " +
			"unverified peer is let through there, not refused as this test requires")
	}
	requirePython(t)
	cfg, dbPath := start(t)
	j := openJournal(t, dbPath)

	before, _, err := j.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}

	attack(t, cfg.Daemon.Socket, `
import socket, sys, json, datetime, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); s.connect(sys.argv[1])
now = datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00','Z')
s.sendall((json.dumps({"kind":"session.start","session_id":"forged",
    "machine_id":"FORGED","client":"attacker","connector":"github",
    "occurred_at":now})+"\n").encode())
time.sleep(0.5)
s.close()
`)

	time.Sleep(300 * time.Millisecond)
	after, _, err := j.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if after != before {
		t.Fatalf("a non-holdcall process appended %d entries to the journal", after-before)
	}
}

// TestANonNimProcessCannotObtainADecision is the enforcement-specific half.
//
// The daemon answers call.request, so an unauthenticated caller could not
// merely fabricate a record: it could drive the decision path and read back
// what Holdcall would allow. That turns the socket into an oracle for the policy
// as well as a way to write to the record.
func TestANonNimProcessCannotObtainADecision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("peer.IsSelf reports unsupported on windows (see internal/peer/peer_windows.go), and " +
			"daemon.go's own accept-time check only refuses a peer when supported && !isSelf -- so an " +
			"unverified peer is let through there, not refused as this test requires")
	}
	requirePython(t)
	cfg, _ := start(t)

	// The daemon drops an unverified connection immediately, so this can die
	// at either send or come back with EOF at the recv, depending on
	// scheduling. Every one of those is the defence working, so the script
	// reports one definite outcome rather than letting the timing decide
	// whether the test passes.
	out := attack(t, cfg.Daemon.Socket, `
import socket, sys, json, datetime
outcome = "CLOSED"
try:
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); s.connect(sys.argv[1])
    now = datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00','Z')
    s.sendall((json.dumps({"kind":"session.start","session_id":"forged",
        "machine_id":"m","connector":"github","occurred_at":now})+"\n").encode())
    s.sendall((json.dumps({"kind":"call.request","session_id":"forged","seq":1,
        "tool":"delete_repository","params_digest":"0"*64,
        "occurred_at":now})+"\n").encode())
    s.settimeout(2.0)
    data = s.recv(4096)
    # b"" is EOF: the daemon closed the connection. Only real bytes back
    # would be a decision.
    outcome = ("REPLY:" + data.decode()) if data else "CLOSED"
except Exception as e:
    outcome = "REFUSED:" + type(e).__name__
print(outcome)
`)

	if strings.Contains(out, "REPLY:") {
		t.Fatalf("an unverified peer received a decision: %s", out)
	}
	if !strings.Contains(out, "CLOSED") && !strings.Contains(out, "REFUSED:") {
		t.Fatalf("expected the connection to be closed or refused, got: %s", out)
	}
}

// TestAnUnauthenticatedConnectionCannotExhaustMemory.
//
// handle() read with mcp.NewReader, whose ReadRaw grows without bound. That
// is right for the client<->connector relay, where a large tool result is
// legitimate, and wrong for this socket, where every message is small.
//
// The severity is specific to enforcement. The relay fails closed, so a
// daemon killed this way does not degrade recording -- it denies every
// tools/call on the machine. One local process could disable every agent's
// tools by sending a long line with no newline in it.
func TestAnUnauthenticatedConnectionCannotExhaustMemory(t *testing.T) {
	cfg, _ := start(t)

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// A single line, never terminated. Unbounded, this grows the daemon's
	// heap by whatever is written; bounded, the connection is dropped.
	chunk := strings.Repeat("A", 64*1024)
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	var written int
	for written < 8*maxLineBytes {
		n, err := conn.Write([]byte(chunk))
		written += n
		if err != nil {
			break // the daemon closed it, which is the point
		}
	}

	// The daemon must still be answering. If it had died, or were still
	// buffering, a fresh connection would not be served.
	conn2, err := net.DialTimeout("unix", cfg.Daemon.Socket, 3*time.Second)
	if err != nil {
		t.Fatalf("the daemon stopped serving after an oversized message: %v", err)
	}
	conn2.Close()
}

// A connection may only report on sessions it opened. Peer identity narrows
// a caller to "something running Holdcall's code", which any local process can be
// by executing the binary; it cannot tell one run of Holdcall from another.
func TestAConnectionCannotSpeakForAnotherConnectionsSession(t *testing.T) {
	cfg, dbPath := start(t)
	j := openJournal(t, dbPath)

	// One connection opens a session and keeps it open.
	owner, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer owner.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	send(t, owner, Event{
		Kind: KindSessionStart, SessionID: "victim", MachineID: "m",
		Connector: "github", OccurredAt: now,
	})
	waitFor(t, j, 1)

	// A second connection tries to report a call under it, and to read back
	// the decision.
	intruder, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer intruder.Close()
	send(t, intruder, Event{
		Kind: KindCallRequest, SessionID: "victim", Seq: 1,
		Tool: "delete_repository", Digest: strings.Repeat("0", 64), OccurredAt: now,
	})

	intruder.SetReadDeadline(time.Now().Add(2 * time.Second))
	var d Decision
	if err := json.NewDecoder(intruder).Decode(&d); err == nil {
		t.Fatalf("the intruder received a decision for someone else's session: %+v", d)
	}

	length, _, err := j.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if length != 1 {
		t.Fatalf("the intruder's call reached the journal: chain length %d, want 1", length)
	}
}

// The ordinary path, so the checks above cannot pass by refusing everything:
// a connection that opens its own session is served normally.
func TestAConnectionsOwnSessionIsServed(t *testing.T) {
	cfg, dbPath := start(t)
	j := openJournal(t, dbPath)

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	send(t, conn,
		Event{Kind: KindSessionStart, SessionID: "mine", MachineID: "m", Connector: "github", OccurredAt: now},
		Event{Kind: KindCallRequest, SessionID: "mine", Seq: 1, Tool: "list_repos",
			Digest: strings.Repeat("0", 64), OccurredAt: now},
	)

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var d Decision
	if err := json.NewDecoder(conn).Decode(&d); err != nil {
		t.Fatalf("a legitimate call was not answered: %v", err)
	}
	if d.Decision != "allow" {
		t.Fatalf("got decision %q, want allow", d.Decision)
	}
	waitFor(t, j, 2)
}
