package journal

import (
	"errors"
	"testing"
)

// A budget change and the entry recording it are one transaction. Adding a
// budget that already exists for its scope writes nothing, and removing one
// that is not there writes nothing: the chain records what changed, never
// what was attempted -- the same shape TestARuleAndItsEntryAreOneChange
// holds a rule to.
func TestABudgetAndItsEntryAreOneChange(t *testing.T) {
	j, _ := openTemp(t)
	before := chainLength(t, j)

	agent := "cursor"
	b, err := j.AddBudget(Budget{Agent: &agent, Tool: "delete_repository", Calls: 5})
	if err != nil {
		t.Fatalf("AddBudget: %v", err)
	}
	if b.ID == 0 || b.CreatedAt.IsZero() {
		t.Fatalf("the stored budget came back incomplete: %+v", b)
	}
	if got := chainLength(t, j); got != before+1 {
		t.Fatalf("adding a budget wrote %d entries, want 1", got-before)
	}
	entries, _ := j.EntriesSince(before, 10)
	e := entries[0]
	if e.Kind != KindBudgetAdd || e.SessionID != "" || e.Agent == nil || *e.Agent != "cursor" ||
		e.Connector != nil || e.Tool == nil || *e.Tool != "delete_repository" ||
		e.BudgetCalls == nil || *e.BudgetCalls != 5 {
		t.Fatalf("the budget.add entry does not describe the budget: %+v", e)
	}

	if _, err := j.AddBudget(Budget{Agent: &agent, Tool: "delete_repository", Calls: 9}); !errors.Is(err, ErrBudgetExists) {
		t.Fatalf("adding a second budget for the same scope: %v, want ErrBudgetExists", err)
	}
	if got := chainLength(t, j); got != before+1 {
		t.Fatalf("a refused duplicate left %d entries behind", got-before-1)
	}

	removed, err := j.RemoveBudget(&agent, nil, "delete_repository")
	if err != nil {
		t.Fatalf("RemoveBudget: %v", err)
	}
	if removed.ID != b.ID || removed.Calls != 5 {
		t.Fatalf("removed budget %+v, want id %d calls 5", removed, b.ID)
	}
	if budgets, _ := j.ListBudgets(); len(budgets) != 0 {
		t.Fatalf("%d budgets remain after removal", len(budgets))
	}
	if got := chainLength(t, j); got != before+2 {
		t.Fatalf("removing a budget wrote %d entries, want 1", got-before-1)
	}
	entries, _ = j.EntriesSince(before+1, 10)
	if entries[0].Kind != KindBudgetRemove || *entries[0].Tool != "delete_repository" || *entries[0].BudgetCalls != 5 {
		t.Fatalf("the budget.remove entry does not describe the removed budget: %+v", entries[0])
	}

	if _, err := j.RemoveBudget(&agent, nil, "delete_repository"); !errors.Is(err, ErrNoSuchBudget) {
		t.Fatalf("removing a budget that is not there: %v, want ErrNoSuchBudget", err)
	}
	if got := chainLength(t, j); got != before+2 {
		t.Fatalf("a refused removal left %d entries behind", got-before-2)
	}
}

// Either half failing rolls back the other, in both directions: a budget the
// chain does not know about, or an entry describing a budget that was never
// stored, is exactly the lie one transaction exists to make impossible --
// the same proof TestAPolicyChangeThatCannotBeRecordedIsNotMade holds a rule
// to.
func TestABudgetChangeThatCannotBeRecordedIsNotMade(t *testing.T) {
	j, path := openTemp(t)
	before := chainLength(t, j)

	// The entry cannot be written: the budget must not exist afterwards.
	tamper(t, path, `create trigger no_budget_entries before insert on nim_journal
		when new.kind = 'budget.add' begin select raise(abort, 'no'); end`)
	if _, err := j.AddBudget(Budget{Tool: "rm", Calls: 3}); err == nil {
		t.Fatal("AddBudget succeeded although its entry could not be written")
	}
	if budgets, _ := j.ListBudgets(); len(budgets) != 0 {
		t.Fatalf("a budget was stored with no entry recording it: %+v", budgets)
	}
	tamper(t, path, `drop trigger no_budget_entries`)

	// The budget cannot be stored: the chain must not say it was.
	tamper(t, path, `create trigger no_budgets before insert on nim_budgets
		begin select raise(abort, 'no'); end`)
	if _, err := j.AddBudget(Budget{Tool: "rm", Calls: 3}); err == nil {
		t.Fatal("AddBudget succeeded although the budget could not be stored")
	}
	if got := chainLength(t, j); got != before {
		t.Fatalf("the chain gained %d entries for a budget that was never stored", got-before)
	}
}

// What a budget's scope means, stated as the cases MatchingBudgets meets --
// the same shape TestRuleScopes holds a rule to. An agent of "" is a session
// no enrolment matched; a tool of BudgetToolAll matches every tool.
func TestBudgetScopeMatching(t *testing.T) {
	j, _ := openTemp(t)
	cursor, github := "cursor", "github"
	for _, b := range []Budget{
		{Tool: "rm", Calls: 3},
		{Agent: &cursor, Tool: "delete_repository", Calls: 2},
		{Connector: &github, Tool: "force_push", Calls: 4},
		{Agent: &cursor, Connector: &github, Tool: "merge", Calls: 1},
		{Tool: BudgetToolAll, Calls: 10},
	} {
		if _, err := j.AddBudget(b); err != nil {
			t.Fatalf("AddBudget(%+v): %v", b, err)
		}
	}

	cases := []struct {
		agent, connector, tool string
		wantIDs                int // how many budgets match
	}{
		{"", "", "rm", 2},            // the global rm budget, and the all-tools one
		{"cursor", "slack", "rm", 2}, // same: neither of these two names an agent match here
		{"claude-code", "github", "rm", 2},
		{"cursor", "github", "delete_repository", 2}, // cursor's own budget, plus all-tools
		{"cursor", "slack", "delete_repository", 2},
		{"claude-code", "github", "delete_repository", 1}, // only all-tools: cursor's budget does not name claude-code
		{"", "github", "delete_repository", 1},            // unenrolled: only all-tools applies
		{"", "github", "force_push", 2},                   // the connector-scoped budget, plus all-tools
		{"cursor", "slack", "force_push", 1},              // wrong connector: only all-tools
		{"cursor", "github", "merge", 2},
		{"cursor", "slack", "merge", 1},
		{"claude-code", "github", "merge", 1},
		{"", "", "merge", 1},
		{"cursor", "github", "RM", 1},  // exact names only: rm's budget does not match, all-tools does
		{"cursor", "github", "rm ", 1}, // no trimming
	}
	for _, c := range cases {
		got, err := j.MatchingBudgets(c.agent, c.connector, c.tool)
		if err != nil {
			t.Fatalf("MatchingBudgets(%q, %q, %q): %v", c.agent, c.connector, c.tool, err)
		}
		if len(got) != c.wantIDs {
			t.Errorf("MatchingBudgets(agent=%q, connector=%q, tool=%q) matched %d, want %d: %+v",
				c.agent, c.connector, c.tool, len(got), c.wantIDs, got)
		}
	}
}

// A budget change is covered by the chain like a call is: editing it after
// the fact breaks verification.
func TestAlteringABudgetEntryBreaksTheChain(t *testing.T) {
	j, path := openTemp(t)
	if _, err := j.AddBudget(Budget{Tool: "rm", Calls: 3}); err != nil {
		t.Fatal(err)
	}
	if rep, _ := j.Verify(""); !rep.OK {
		t.Fatalf("the chain does not verify with a budget entry in it: %s", rep.Problem)
	}
	tamper(t, path, `update nim_journal set budget_calls = 99 where kind = 'budget.add'`)
	rep, err := j.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("a rewritten budget.add entry still verified")
	}
}

// A read-only handle can look at the budgets and change none of them.
func TestAReadOnlyJournalCannotChangeBudgets(t *testing.T) {
	w, path := openTemp(t)
	if _, err := w.AddBudget(Budget{Tool: "rm", Calls: 3}); err != nil {
		t.Fatal(err)
	}
	w.Close()

	j, err := OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if budgets, err := j.ListBudgets(); err != nil || len(budgets) != 1 {
		t.Fatalf("ListBudgets = %v, %v", budgets, err)
	}
	if _, err := j.AddBudget(Budget{Tool: "ls", Calls: 1}); err == nil {
		t.Error("a read-only journal accepted a budget")
	}
	if _, err := j.RemoveBudget(nil, nil, "rm"); err == nil {
		t.Error("a read-only journal removed a budget")
	}
}

// CountAllowedCalls weighs only allow and approved decisions for the tool it
// is asked about, or every tool when asked with "". Deny never counts, and a
// different tool never counts either.
func TestCountAllowedCallsCountsOnlyAllowedDecisionsForTheAskedTool(t *testing.T) {
	j, _ := openTemp(t)
	if err := j.Append(session("s1", "github")); err != nil {
		t.Fatal(err)
	}
	entries := []struct {
		tool     string
		decision string
	}{
		{"rm", DecisionAllow},
		{"rm", DecisionDeny},
		{"rm", DecisionApproved},
		{"echo", DecisionAllow},
		{"rm", DecisionObserved}, // pre-M2 vocabulary: never counts as allowed
	}
	for i, e := range entries {
		seq := int64(i + 1)
		decision := e.decision
		if err := j.Append(Entry{
			Kind: KindCallRequest, SessionID: "s1", Seq: &seq,
			Tool: &e.tool, ParamsDigest: sp("d"), Decision: &decision,
			OccurredAt: nowRFC(),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	if n, err := j.CountAllowedCalls("s1", "rm"); err != nil || n != 2 {
		t.Errorf("CountAllowedCalls(s1, rm) = %d, %v, want 2", n, err)
	}
	if n, err := j.CountAllowedCalls("s1", "echo"); err != nil || n != 1 {
		t.Errorf("CountAllowedCalls(s1, echo) = %d, %v, want 1", n, err)
	}
	if n, err := j.CountAllowedCalls("s1", ""); err != nil || n != 3 {
		t.Errorf("CountAllowedCalls(s1, \"\") = %d, %v, want 3 (every tool)", n, err)
	}
	if n, err := j.CountAllowedCalls("s2", "rm"); err != nil || n != 0 {
		t.Errorf("CountAllowedCalls(s2, rm) = %d, %v, want 0: a different session must start fresh", n, err)
	}
}
