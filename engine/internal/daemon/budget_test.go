package daemon

import (
	"net"
	"strings"
	"testing"

	"github.com/BySergiMM/nim/engine/internal/journal"
)

// A budget of N calls allows exactly N, and the N+1th is refused with the
// budget named as the reason. Woven into the same call sequence: a call the
// rules themselves deny -- unrelated to the budget's tool -- must not spend
// any of the budget's count, because a session's refusals, by a rule or by a
// budget, never count as allowed.
func TestTheNthPlusOneAllowedCallIsDeniedAndRefusalsDoNotCount(t *testing.T) {
	cfg, _ := start(t)
	deny(t, cfg, "other_tool", "", "")
	policyChange(t, cfg, Request{
		ID: "b1", Kind: KindBudgetSet, BudgetTool: "rm", BudgetCalls: 2,
	})

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m",
		Connector: "github", OccurredAt: now()})

	// A refusal by an unrelated rule first: it must not touch the budget.
	if d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1,
		Tool: "other_tool", Digest: "d", OccurredAt: now()}); d.Decision != journal.DecisionDeny {
		t.Fatalf("other_tool: %s, want deny", d.Decision)
	}

	for i, seq := range []int{2, 3} {
		d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: seq,
			Tool: "rm", Digest: "d", OccurredAt: now()})
		if d.Decision != journal.DecisionAllow {
			t.Fatalf("call %d (seq %d): %s, want allow -- the budget is 2 and this is call %d", i+1, seq, d.Decision, i+1)
		}
	}

	// The third rm call is the budget's third: denied, and the reason names it.
	d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 4,
		Tool: "rm", Digest: "d", OccurredAt: now()})
	if d.Decision != journal.DecisionDeny {
		t.Fatalf("the third rm call was %s, want deny: the budget is 2", d.Decision)
	}
	if !strings.Contains(d.Reason, "budget") || !strings.Contains(d.Reason, "2 calls") {
		t.Errorf("the refusal does not name the budget: %q", d.Reason)
	}

	// A different tool the budget does not name is unaffected.
	if d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 5,
		Tool: "echo", Digest: "d", OccurredAt: now()}); d.Decision != journal.DecisionAllow {
		t.Fatalf("echo (not covered by the rm budget): %s, want allow", d.Decision)
	}
}

// "Per session" means session.start to session.end: a new session id starts
// with a count of zero even though an earlier session under the same scope
// exhausted its own budget -- exactly what a client restarting its relay
// gets, docs/decisions/0004-budgets.md's argument checked at the answer
// level.
func TestANewSessionStartsWithAFreshBudget(t *testing.T) {
	cfg, _ := start(t)
	policyChange(t, cfg, Request{ID: "b1", Kind: KindBudgetSet, BudgetTool: "rm", BudgetCalls: 1})

	call := func(session string, seq int) string {
		t.Helper()
		conn, err := net.Dial("unix", cfg.Daemon.Socket)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		send(t, conn, Event{Kind: KindSessionStart, SessionID: session, MachineID: "m",
			Connector: "github", OccurredAt: now()})
		return ask(t, conn, Event{Kind: KindCallRequest, SessionID: session, Seq: seq,
			Tool: "rm", Digest: "d", OccurredAt: now()}).Decision
	}

	if got := call("s1", 1); got != journal.DecisionAllow {
		t.Fatalf("s1's first rm call was %s, want allow", got)
	}
	// s2 is a different session under the identical scope. If the budget
	// carried over between sessions, its first call would already be denied.
	if got := call("s2", 1); got != journal.DecisionAllow {
		t.Fatalf("s2's first rm call was %s, want allow: a new session must not inherit s1's count", got)
	}
}

// A budget never grants. A rule that denies a tool outright is not
// overridden by a budget with room left -- checkBudgets is never even
// consulted, because it only ever narrows an allow, never widens a deny.
func TestABudgetCannotMakeADeniedCallPass(t *testing.T) {
	cfg, _ := start(t)
	deny(t, cfg, "rm", "", "")
	policyChange(t, cfg, Request{
		ID: "b1", Kind: KindBudgetSet, BudgetTool: "rm", BudgetCalls: 1000000,
	})

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m",
		Connector: "github", OccurredAt: now()})

	d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: 1,
		Tool: "rm", Digest: "d", OccurredAt: now()})
	if d.Decision != journal.DecisionDeny {
		t.Fatalf("a call the rules deny was %s under a budget with headroom left, want deny", d.Decision)
	}
	if !strings.Contains(d.Reason, "by rule") {
		t.Errorf("the reason should still name the rule, not the budget that was never consulted: %q", d.Reason)
	}
}

// A budget scoped with --all-tools counts every tool's allowed calls in one
// session against the same cap.
func TestABudgetScopedToAllToolsCountsAcrossTools(t *testing.T) {
	cfg, _ := start(t)
	policyChange(t, cfg, Request{
		ID: "b1", Kind: KindBudgetSet, BudgetAllTools: true, BudgetCalls: 2,
	})

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(t, conn, Event{Kind: KindSessionStart, SessionID: "s1", MachineID: "m",
		Connector: "github", OccurredAt: now()})

	tools := []string{"rm", "echo", "ls"} // three different tools, one shared cap
	var got []string
	for i, tool := range tools {
		d := ask(t, conn, Event{Kind: KindCallRequest, SessionID: "s1", Seq: i + 1,
			Tool: tool, Digest: "d", OccurredAt: now()})
		got = append(got, d.Decision)
	}
	want := []string{journal.DecisionAllow, journal.DecisionAllow, journal.DecisionDeny}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d (%s) was %s, want %s: an all-tools budget must count every tool together",
				i+1, tools[i], got[i], want[i])
		}
	}
}

// A budget request is validated the same way a rule request is: an
// unenrolled agent is refused by name, and the connection stays usable for
// the rest of the "policy" purpose afterwards.
func TestABudgetCannotNameAnUnenrolledAgent(t *testing.T) {
	cfg, dbPath := start(t)

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	resp, err := SendRequest(conn, Request{
		ID: "1", Kind: KindBudgetSet, BudgetTool: "rm", BudgetCalls: 3, BudgetAgent: "nobody",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == "" || !strings.Contains(resp.Error, "nim agent add") {
		t.Fatalf("a budget for an unenrolled agent was accepted or badly refused: %q", resp.Error)
	}

	// The same connection may still manage a rule afterwards: both kinds
	// share the "policy" purpose.
	resp, err = SendRequest(conn, Request{ID: "2", Kind: KindPolicyDeny, RuleTool: "rm"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("a policy connection could not also set a rule after a refused budget request: %s", resp.Error)
	}

	j := openJournal(t, dbPath)
	if budgets, _ := j.ListBudgets(); len(budgets) != 0 {
		t.Fatalf("%d budgets were stored from a refused request", len(budgets))
	}
}

// A budget's cap is bounded the way any other request field is: it must be a
// positive integer within MaxBudgetCalls, and a request naming both --tool
// and --all-tools, or neither, is refused rather than guessed at.
func TestBudgetRequestsAreValidated(t *testing.T) {
	cfg, dbPath := start(t)
	enrol(t, dbPath, "claude-code")

	for _, bad := range []Request{
		{ID: "1", Kind: KindBudgetSet, BudgetTool: "rm", BudgetCalls: 0},
		{ID: "2", Kind: KindBudgetSet, BudgetTool: "rm", BudgetCalls: -1},
		{ID: "3", Kind: KindBudgetSet, BudgetTool: "rm", BudgetCalls: MaxBudgetCalls + 1},
		{ID: "4", Kind: KindBudgetSet, BudgetTool: "rm", BudgetAllTools: true, BudgetCalls: 1},
		{ID: "5", Kind: KindBudgetSet, BudgetCalls: 1}, // neither --tool nor --all-tools
		{ID: "6", Kind: KindBudgetSet, BudgetTool: journal.BudgetToolAll, BudgetCalls: 1},
		{ID: "7", Kind: KindBudgetSet, BudgetTool: "rm", BudgetCalls: 1, BudgetAgent: "a/b"},
	} {
		conn, err := net.Dial("unix", cfg.Daemon.Socket)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := SendRequest(conn, bad)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error == "" {
			t.Errorf("%+v was accepted", bad)
		}
	}

	j := openJournal(t, dbPath)
	if budgets, _ := j.ListBudgets(); len(budgets) != 0 {
		t.Fatalf("%d budgets were stored from rejected requests", len(budgets))
	}
}
