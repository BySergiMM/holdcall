package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/journal"
)

// start brings up a daemon on a private socket and returns a dialler for it.
func start(t *testing.T) (cfg config.Config, dbPath string) {
	return startWithPolicy(t, config.Policy{})
}

// startWithPolicy is the same, with a deny list.
func startWithPolicy(t *testing.T, policy config.Policy) (cfg config.Config, dbPath string) {
	t.Helper()

	home := t.TempDir()
	// The socket lives outside home on purpose: an AF_UNIX path is capped near
	// 104 bytes and a temp directory is already most of that.
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("nim-test-%d.sock", time.Now().UnixNano()%1e9))
	t.Cleanup(func() { os.Remove(sock) })

	cfg = config.Config{
		Daemon: config.Daemon{Socket: sock, DataDir: filepath.Join(home, "data")},
		Policy: policy,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test socket is unusable: %v", err)
	}

	go func() {
		if err := Run(cfg); err != nil {
			t.Logf("daemon stopped: %v", err)
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return cfg, cfg.DatabasePath()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not come up")
	return cfg, ""
}

func openJournal(t *testing.T, path string) *journal.Journal {
	t.Helper()
	seed, _ := config.ReadMachineID()
	j, err := journal.Open(path, seed)
	if err != nil {
		t.Fatalf("opening the journal: %v", err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func send(t *testing.T, conn net.Conn, events ...Event) {
	t.Helper()
	enc := json.NewEncoder(conn)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatalf("sending %s: %v", ev.Kind, err)
		}
	}
}

func waitFor(t *testing.T, j *journal.Journal, want int64) int64 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var length int64
	for time.Now().Before(deadline) {
		length, _, _ = j.Head()
		if length >= want {
			return length
		}
		time.Sleep(20 * time.Millisecond)
	}
	return length
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// A client that kills the relay leaves it no chance to close its own session.
// The daemon can still tell, because the connection dies with the process, so a
// killed shim must not leave a session that looks abandoned -- otherwise every
// session would, and the signal would mean nothing.
func TestSessionIsClosedWhenTheShimGoesAway(t *testing.T) {
	cfg, dbPath := start(t)

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn,
		Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()},
		Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "echo",
			Digest: "d", Decision: journal.DecisionObserved, OccurredAt: now()},
	)

	j := openJournal(t, dbPath)
	if got := waitFor(t, j, 2); got != 2 {
		t.Fatalf("journal has %d entries, want 2", got)
	}

	// Dropping the connection stands in for a killed relay.
	conn.Close()

	if got := waitFor(t, j, 3); got != 3 {
		t.Fatalf("no session.end was written after the shim went away (%d entries)", got)
	}
	loss, err := j.Loss()
	if err != nil {
		t.Fatal(err)
	}
	if loss.UnfinishedSessions != 0 {
		t.Errorf("the session still counts as unfinished: %+v", loss)
	}
}

// The daemon must not write a second end for a session that closed itself.
func TestASessionThatClosesItselfIsNotClosedTwice(t *testing.T) {
	cfg, dbPath := start(t)

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn,
		Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Connector: "github", OccurredAt: now()},
		Event{Kind: KindSessionEnd, SessionID: "s1", ProtocolVersion: "2025-06-18", OccurredAt: now()},
	)

	j := openJournal(t, dbPath)
	if got := waitFor(t, j, 2); got != 2 {
		t.Fatalf("journal has %d entries, want 2", got)
	}
	conn.Close()

	// Give the daemon room to write a duplicate if it were going to.
	time.Sleep(300 * time.Millisecond)
	if length, _, _ := j.Head(); length != 2 {
		t.Fatalf("journal grew to %d entries: the session was closed twice", length)
	}
}

// Everything appends. A call reported twice must leave two entries, because a
// row that gets updated cannot be part of the chain.
func TestReportsBecomeAppendOnlyEntries(t *testing.T) {
	cfg, dbPath := start(t)

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ok := true
	ms := 7
	send(t, conn,
		Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m", Client: "c", Connector: "github", OccurredAt: now()},
		Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1, Tool: "create_issue",
			Digest: "d", Decision: journal.DecisionObserved, OccurredAt: now()},
		Event{Kind: KindCallOutcome, SessionID: "s1", Seq: 1, OK: &ok, DurationMS: &ms, OccurredAt: now()},
		Event{Kind: KindAnomaly, SessionID: "s1", Anomaly: "batch", OccurredAt: now()},
	)

	j := openJournal(t, dbPath)
	if got := waitFor(t, j, 4); got != 4 {
		t.Fatalf("journal has %d entries, want 4", got)
	}
	rep, err := j.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("the daemon wrote a chain that does not verify: %s", rep.Problem)
	}

	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("nim_calls has %d rows, want 1", len(calls))
	}
	if calls[0].OK == nil || !*calls[0].OK || calls[0].DurationMS == nil || *calls[0].DurationMS != 7 {
		t.Errorf("the outcome was not joined to its request: %+v", calls[0])
	}

	counts, err := j.Anomalies()
	if err != nil {
		t.Fatal(err)
	}
	if counts["batch"] != 1 {
		t.Errorf("anomaly counts = %v", counts)
	}
}
