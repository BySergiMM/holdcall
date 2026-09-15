package main

import (
	"strings"
	"testing"
)

// budget adds or changes a budget through the real CLI and checks the CLI
// said so -- deny/allow's counterpart for M5.
func (s *stack) budget(t *testing.T, args ...string) {
	t.Helper()
	out, err := s.run(t, append([]string{"policy", "budget"}, args...)...)
	if err != nil || !strings.Contains(out, "recorded in the journal") {
		t.Fatalf("nim policy budget %v: %v\n%s", args, err, out)
	}
}

// The acceptance test for M5, run the way M4's and M4.5's were: with the
// real binary, a real daemon and a real relay. A budget of 2 calls is set
// through the CLI; the third call through the relay is refused; restarting
// the relay starts a new session, whose count is unrelated to the one the
// first relay exhausted, so its first call is allowed again -- "per
// session, decremented at authorization time" is the milestone's whole
// sentence, proven end to end.
//
// The client itself is told only the same generic policy refusal every
// denial gets -- mcp.DeniedByPolicy, never a rule's or a budget's own
// reason; that is true for a rule-based deny too
// (TestRealRelayForwardsOneCallAndRefusesTheOther above checks the same
// substring), and this milestone does not change it. "Refused with the
// budget text" -- the daemon's Decision.Reason actually naming the budget
// ("budget: 2 calls for tool list_repos ... already allowed this session")
// -- is what the daemon answers over its own protocol, checked directly
// against real values in internal/daemon/budget_test.go's
// TestTheNthPlusOneAllowedCallIsDeniedAndRefusalsDoNotCount.
func TestABudgetOfTwoRefusesTheThirdCallAndANewRelayStartsFresh(t *testing.T) {
	s := build(t)
	s.daemon(t)
	s.budget(t, "2", "--tool", "list_repos")

	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_repos","arguments":{}}}`

	r := s.serve(t)
	for i := 0; i < 2; i++ {
		r.send(t, call)
		if _, isError, _ := decodeLine(t, r.next(t)); isError {
			t.Fatalf("call %d was refused; the budget is 2 and this is call %d", i+1, i+1)
		}
	}
	r.send(t, call)
	id, isError, text := decodeLine(t, r.next(t))
	if id != "1" {
		t.Errorf("id came back as %s", id)
	}
	if !isError {
		t.Fatal("the third call was allowed; the budget is 2")
	}
	if !strings.Contains(text, "policy") {
		t.Errorf("the client was told %q, want the policy refusal", text)
	}
	r.kill()

	// nim log confirms the third call was decided and refused, on the
	// record -- the same check TestRealRelayForwardsOneCallAndRefusesTheOther
	// makes for a rule-based deny.
	if out, err := s.run(t, "log"); err != nil || !strings.Contains(out, "denied") {
		t.Errorf("nim log does not report the refused call as denied: %v\n%s", err, out)
	}

	// What the connector actually saw: two calls, byte-identical, and no
	// trace of a third.
	got := s.received(t)
	if strings.Count(got, `"name":"list_repos"`) != 2 {
		t.Errorf("the connector saw %d list_repos calls, want 2:\n%s", strings.Count(got, `"name":"list_repos"`), got)
	}

	// A new relay is a new session: session.start to session.end, with a
	// fresh count. Its first call, on the same budget, is allowed again.
	r2 := s.serve(t)
	r2.send(t, call)
	if _, isError, _ := decodeLine(t, r2.next(t)); isError {
		t.Fatal("a new relay's first call was refused; a new session must start with a fresh budget")
	}
	r2.kill()

	if out, err := s.run(t, "verify"); err != nil || !strings.Contains(out, "self-consistent") {
		t.Errorf("nim verify: %v\n%s", err, out)
	}
}

// nim policy list shows the budgets it holds, under the rules, with their
// scope and cap.
func TestPolicyListShowsBudgets(t *testing.T) {
	s := build(t)
	s.daemon(t)
	s.budget(t, "3", "--tool", "rm")
	s.budget(t, "7", "--all-tools", "--connector", "gitlab")

	out, err := s.run(t, "policy", "list")
	if err != nil {
		t.Fatalf("nim policy list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "BUDGETS") {
		t.Fatalf("nim policy list does not show a BUDGETS section:\n%s", out)
	}
	if !strings.Contains(out, "3") || !strings.Contains(out, "rm") {
		t.Errorf("nim policy list does not show the rm budget:\n%s", out)
	}
	if !strings.Contains(out, "7") || !strings.Contains(out, "gitlab") {
		t.Errorf("nim policy list does not show the all-tools budget:\n%s", out)
	}
}

// nim policy explain reports the budgets that would apply to a call and
// their caps -- kept simple, with no invented session state, as
// docs/decisions/0004-budgets.md and the milestone brief ask for.
func TestPolicyExplainShowsMatchingBudgets(t *testing.T) {
	s := build(t)
	s.daemon(t)
	s.budget(t, "4", "--tool", "read_file")

	out, err := s.run(t, "policy", "explain", "read_file")
	if err != nil {
		t.Fatalf("nim policy explain read_file: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Budgets that would apply") {
		t.Errorf("nim policy explain does not mention matching budgets:\n%s", out)
	}
	if !strings.Contains(out, "4 calls") {
		t.Errorf("nim policy explain does not name the budget's cap:\n%s", out)
	}

	// A tool no budget names shows none.
	out, err = s.run(t, "policy", "explain", "untouched_tool")
	if err != nil {
		t.Fatalf("nim policy explain untouched_tool: %v\n%s", err, out)
	}
	if strings.Contains(out, "Budgets that would apply") {
		t.Errorf("nim policy explain invented a budget for a tool none applies to:\n%s", out)
	}
}
