package shim

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BySergiMM/holdcall/engine/internal/config"
	"github.com/BySergiMM/holdcall/engine/internal/daemon"
	"github.com/BySergiMM/holdcall/engine/internal/journal"
	"github.com/BySergiMM/holdcall/engine/internal/mcp"
)

// The approval tests in approval_test.go answer the relay from a fake daemon,
// which is what lets them put the daemon's answer exactly where they want it in
// time. That is also their limit: a fake says what its author believes the real
// daemon does. The bug these tests are about lived in the gap between two
// clocks -- the relay's and the daemon's -- and in the order the daemon does
// things in, both of which a fake takes as given. So this one runs the real
// daemon, in this process, against the real reporter, and asks the only
// question that matters: what does a session look like after a call nobody
// decided.

// startRealDaemon runs the actual daemon on a private socket with the given
// approval_timeout, and returns the configuration that reaches it. The daemon
// is stopped, and the journal closed, before the temporary home is removed:
// Windows will not delete a directory with an open file in it.
func startRealDaemon(t *testing.T, approvalTimeout time.Duration) config.Config {
	t.Helper()

	home := t.TempDir()
	// The daemon reads the install identifier from config.Home(), and makes one
	// on an empty journal: without this it would be the real install's home.
	t.Setenv(config.HomeEnvVar, home)
	if _, err := config.CreateMachineID(); err != nil {
		t.Fatalf("creating the test home's machine-id: %v", err)
	}
	// Outside home on purpose: an AF_UNIX path is capped near 104 bytes and a
	// temporary directory is already most of that.
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("holdcall-shim-real-%d.sock", time.Now().UnixNano()%1e9))
	t.Cleanup(func() { os.Remove(sock) })

	cfg := config.Config{Daemon: config.Daemon{
		Socket: sock, DataDir: filepath.Join(home, "data"),
		ApprovalTimeout: config.Duration(approvalTimeout),
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test socket is unusable: %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := daemon.RunWithStop(cfg, stop); err != nil {
			t.Logf("daemon stopped: %v", err)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Logf("the daemon did not stop within 5s")
		}
	})

	// dialDaemon starts a daemon of its own -- this test binary, re-executed --
	// when it cannot connect, so it must never be called before this holds.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return cfg
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the daemon did not come up")
	return cfg
}

// askRuleFor adds an ask rule the way the CLI does, over its own connection:
// the daemon is the only writer of rules.
func askRuleFor(t *testing.T, cfg config.Config, tool string) {
	t.Helper()
	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatalf("dialling for a rule: %v", err)
	}
	defer conn.Close()
	resp, err := daemon.SendRequest(conn, daemon.Request{ID: "rule", Kind: daemon.KindPolicyAsk, RuleTool: tool})
	if err != nil {
		t.Fatalf("policy.ask: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("policy.ask %s: %s", tool, resp.Error)
	}
}

// newRealRig wires a relay to the daemon startRealDaemon brought up, through
// dialDaemon -- the constructor Run uses, so the approval timeout and the
// grace are the ones a real session has, not the ones a test rig picks.
func newRealRig(t *testing.T, cfg config.Config) *rig {
	t.Helper()

	r := dialDaemon(cfg)
	t.Cleanup(func() { r.close() })
	if r.conn == nil {
		t.Fatal("the relay could not reach the daemon this test started")
	}

	client := &syncBuf{}
	s := &Shim{
		sessionID: "s1",
		reporter:  r,
		out:       &clientOut{w: client},
		inFlight:  make(map[string]pending),
	}
	machineID, _ := config.ReadMachineID()
	s.report(daemon.Event{
		Kind: daemon.KindSessionStart, SessionID: s.sessionID, MachineID: machineID,
		Client: "test", Connector: "rig", OccurredAt: nowRFC3339(),
	})
	return &rig{shim: s, connector: &syncBuf{}, client: client}
}

// The regression test for the flagship bug, against the real daemon.
//
// A call an ask rule holds and nobody decides is ended by the daemon's own
// timer at approval_timeout. The daemon journals the rejection and only then
// answers, so the answer reaches the relay a little after approval_timeout --
// and v0.1.2's relay gave up at exactly approval_timeout. Which of the two got
// there first was a matter of microseconds, and both outcomes were wrong:
//
//   - the daemon's answer arrived first, and was "rejected", which the relay
//     reported to the model as a human having looked at the call and said no;
//   - the relay's clock ran out first, it hung up on the daemon, and hanging
//     up is terminal, so every later call in the session was denied too.
//
// This test fails on v0.1.2 whichever of those happens: the first fails the
// text assertion, the second the assertion that the next call went through.
func TestARealDaemonsApprovalTimeoutIsAnsweredAsATimeoutAndTheSessionGoesOn(t *testing.T) {
	const timeout = 400 * time.Millisecond

	cfg := startRealDaemon(t, timeout)
	askRuleFor(t, cfg, "send_email")
	g := newRealRig(t, cfg)

	held := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_email","arguments":{"to":"ops@example.com"}}}` + "\n"
	next := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"add","arguments":{"a":1,"b":2}}}` + "\n"

	start := time.Now()
	g.relay(held, next)
	elapsed := time.Since(start)

	// The held call: refused as a timeout, which is neither a human's decision
	// nor a daemon that could not be reached.
	lines := clientLines(g)
	if len(lines) == 0 {
		t.Fatal("the held call was never answered")
	}
	got := decodeResponse(t, []byte(lines[0]))
	if string(got.ID) != "1" {
		t.Errorf("the refusal was addressed to id %s, want the held call's 1", got.ID)
	}
	if !got.Result.IsError || len(got.Result.Content) != 1 {
		t.Fatalf("a call nobody decided was not answered as an error: %s", lines[0])
	}
	switch text := got.Result.Content[0].Text; text {
	case mcp.DeniedApprovalTimedOut:
	case mcp.DeniedByHuman:
		t.Error("a call nobody looked at was reported to the model as rejected by a human")
	default:
		t.Errorf("client text = %q, want the approval-timeout text", text)
	}
	if elapsed < timeout {
		t.Errorf("the call was refused after %v, before approval_timeout (%v) had passed: it was not held", elapsed, timeout)
	}

	// The session: still whole. The next call was decided by the same daemon on
	// the same connection, allowed, and forwarded byte for byte; the held one
	// never reached the connector.
	if got := g.connector.String(); got != next {
		t.Errorf("the call after the timeout did not go through:\ngot  %q\nwant %q\nclient saw: %s",
			got, next, g.client.String())
	}
	if n := g.shim.reporter.lostCount(); n != 0 {
		t.Errorf("the relay gave up on the daemon after a timeout it should have heard: %d lost, %v",
			n, g.shim.reporter.causeList())
	}

	// The record: what the daemon wrote, in an order that still verifies. The
	// journal is deliberately unchanged by the fix -- a timeout is recorded as
	// the rejection it always was.
	//
	// Read the way every reader reads it, read-only: the daemon still holds the
	// database open for writing, and a second writer opening it here would be
	// racing the daemon for a lock the test has no business taking.
	seed, _ := config.ReadMachineID()
	j, err := journal.OpenReadOnly(cfg.DatabasePath(), seed)
	if err != nil {
		t.Fatalf("opening the journal: %v", err)
	}
	t.Cleanup(func() { j.Close() })
	calls, err := j.RecentCalls(10)
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[string]string{}
	for _, c := range calls {
		decisions[c.Tool] = c.Decision
	}
	if len(calls) != 2 || decisions["send_email"] != journal.DecisionRejected || decisions["add"] != journal.DecisionAllow {
		t.Errorf("journaled calls = %+v, want send_email rejected and add allow", calls)
	}
	if rep, err := j.Verify(""); err != nil || !rep.OK {
		t.Errorf("the journal after a timeout does not verify: %v %+v", err, rep)
	}
}
