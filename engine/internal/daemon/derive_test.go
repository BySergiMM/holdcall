package daemon

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BySergiMM/holdcall/engine/internal/peer"
)

// The property this step exists for: an agent cannot be claimed.
//
// A relay sends session.start. If any field on that message could set the
// agent, a restricted agent could name an unrestricted one -- which would not
// merely bypass a policy but invert it. So there is no such field, and this
// test fails to compile if one is ever added.
func TestNoWireFieldCanSetTheAgent(t *testing.T) {
	raw, err := json.Marshal(Event{
		Kind: KindSessionStart, SessionID: "s", MachineID: "m", Connector: "github",
		Client: "a label the caller chose", OccurredAt: now(),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "agent") {
		t.Fatalf("Event has an agent field on the wire: %s", raw)
	}

	// And an inbound message that tries anyway is simply not decoded into
	// anything: the field does not exist, so it is dropped.
	var ev Event
	if err := json.Unmarshal(
		[]byte(`{"kind":"session.start","session_id":"s","agent":"claude-code"}`), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Nothing on Event can hold it. If this ever compiles differently, the
	// property is gone.
	if ev.Kind != KindSessionStart || ev.SessionID != "s" {
		t.Fatal("the rest of the message did not decode")
	}
}

// What a caller sends as `client` is recorded, and is not the agent. The two
// sit next to each other on purpose: one is a claim, the other is derived.
func TestTheClientLabelIsNotTheAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holdcall.db")
	j := openJournal(t, path)

	if err := apply(Event{
		Kind: KindSessionStart, SessionID: "s", MachineID: "m", Connector: "github",
		Client: "claude-code", OccurredAt: now(),
	}, j, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}

	client, agent := sessionLabels(t, path, "s")
	if client != "claude-code" {
		t.Fatalf("client = %q, want the label the caller sent", client)
	}
	if agent != "" {
		t.Fatalf("agent = %q, but nothing was derived -- a claim became an identity", agent)
	}
}

// The derived agent is what lands on the entry, whatever the caller said.
func TestTheDerivedAgentIsRecordedOnSessionStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holdcall.db")
	j := openJournal(t, path)

	if err := apply(Event{
		Kind: KindSessionStart, SessionID: "s", MachineID: "m", Connector: "github",
		Client: "a label", OccurredAt: now(),
	}, j, "claude-code"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	client, agent := sessionLabels(t, path, "s")
	if agent != "claude-code" {
		t.Fatalf("agent = %q, want the derived one", agent)
	}
	if client != "a label" {
		t.Fatalf("client = %q, want the caller's own label preserved alongside", client)
	}
}

// The agent belongs on session.start alone, for the same reason connector
// does: it is a property of the session. Repeating it on every call entry
// would let two immutable entries describing one session disagree.
func TestTheAgentIsNotRepeatedOnCallEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holdcall.db")
	j := openJournal(t, path)

	stamp := now()
	if err := apply(Event{
		Kind: KindSessionStart, SessionID: "s", MachineID: "m", Connector: "github", OccurredAt: stamp,
	}, j, "claude-code"); err != nil {
		t.Fatalf("session.start: %v", err)
	}
	if err := apply(Event{
		Kind: KindCallRequest, SessionID: "s", Seq: 1, Tool: "t", Digest: "d",
		Decision: "allow", OccurredAt: stamp,
	}, j, "claude-code"); err != nil {
		t.Fatalf("call.request: %v", err)
	}

	db := openDB(t, path)
	defer db.Close()
	var agent *string
	if err := db.QueryRow(
		`select agent from nim_journal where kind = ? and session_id = ?`,
		KindCallRequest, "s").Scan(&agent); err != nil {
		t.Fatalf("reading the call entry: %v", err)
	}
	if agent != nil {
		t.Fatalf("call.request carried agent %q; it belongs on session.start and is joined", *agent)
	}
}

// Not being able to derive an agent is the ordinary case, not a failure. Until
// something decides on it, an unknown agent must cost nothing -- refusing a
// session because a pid could not be read would turn a record into an outage.
func TestAnUndeterminedAgentIsRecordedAsAbsentNotRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holdcall.db")
	j := openJournal(t, path)

	if err := apply(Event{
		Kind: KindSessionStart, SessionID: "s", MachineID: "m", Connector: "github", OccurredAt: now(),
	}, j, ""); err != nil {
		t.Fatalf("a session with no derivable agent was refused: %v", err)
	}
	_, agent := sessionLabels(t, path, "s")
	if agent != "" {
		t.Fatalf("agent = %q, want absent", agent)
	}
}

// deriveAgent has to answer "no agent" rather than fail when it cannot walk
// the chain from a connection. A net.Pipe is not a unix socket, so the kernel
// has nothing to say about a peer -- the same shape as Windows.
func TestDeriveAgentOnAnUninspectableConnectionYieldsNoAgent(t *testing.T) {
	j := openJournal(t, filepath.Join(t.TempDir(), "holdcall.db"))
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	if got := deriveAgent(server, j); got != "" {
		t.Fatalf("derived %q from a connection the kernel cannot describe", got)
	}
}

// The end-to-end walk over a real socket: the daemon sees the peer, walks to
// its parent, and matches the enrolment. Here the peer is this test binary and
// its parent is `go test`, so enrolling the parent's own executable must be
// what deriveAgent finds -- and enrolling something else must not.
func TestDeriveAgentMatchesTheEnrolledParent(t *testing.T) {
	j := openJournal(t, filepath.Join(t.TempDir(), "holdcall.db"))

	sock := tempSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("no connection accepted")
	}
	defer server.Close()

	// Nothing enrolled yet.
	if got := deriveAgent(server, j); got != "" {
		t.Fatalf("derived %q with nothing enrolled", got)
	}

	// The peer of this connection is this process; its parent is whatever ran
	// the test. Enrol that parent's executable.
	ppid, err := peer.ParentOf(os.Getpid())
	if err != nil {
		t.Skipf("cannot read this process's parent: %v", err)
	}
	img, err := peer.ImageOf(ppid)
	if err != nil {
		t.Skipf("cannot read the parent's image: %v", err)
	}
	if err := j.AddAgent(journalAgent("the-runner", img)); err != nil {
		t.Fatalf("enrolling: %v", err)
	}

	if got := deriveAgent(server, j); got != "the-runner" {
		t.Fatalf("derived %q, want the-runner", got)
	}

	// A different identity must not match. Shifting the inode by one is enough
	// and cannot collide with the real one.
	if err := j.AddAgent(journalAgent("the-runner", peer.NewImage(img.Dev(), img.Ino()+1))); err != nil {
		t.Fatalf("re-enrolling: %v", err)
	}
	if got := deriveAgent(server, j); got != "" {
		t.Fatalf("derived %q after the enrolment stopped matching", got)
	}
}
