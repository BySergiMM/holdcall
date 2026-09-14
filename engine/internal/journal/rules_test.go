package journal

import (
	"errors"
	"testing"
)

func chainLength(t *testing.T, j *Journal) int64 {
	t.Helper()
	n, _, err := j.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	return n
}

// A policy change and the entry recording it are one transaction. Adding a
// rule that already exists writes nothing, and removing one that is not there
// writes nothing: the chain records what changed, never what was attempted.
func TestARuleAndItsEntryAreOneChange(t *testing.T) {
	j, _ := openTemp(t)
	before := chainLength(t, j)

	agent := "cursor"
	r, err := j.AddRule(Rule{Agent: &agent, Tool: "delete_repository"})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if r.ID == 0 || r.CreatedAt.IsZero() {
		t.Fatalf("the stored rule came back incomplete: %+v", r)
	}
	if got := chainLength(t, j); got != before+1 {
		t.Fatalf("adding a rule wrote %d entries, want 1", got-before)
	}
	entries, _ := j.EntriesSince(before, 10)
	e := entries[0]
	if e.Kind != KindRuleAdd || e.SessionID != "" || e.Agent == nil || *e.Agent != "cursor" ||
		e.Connector != nil || e.Tool == nil || *e.Tool != "delete_repository" ||
		e.Decision == nil || *e.Decision != DecisionDeny {
		t.Fatalf("the rule.add entry does not describe the rule: %+v", e)
	}

	if _, err := j.AddRule(Rule{Agent: &agent, Tool: "delete_repository"}); !errors.Is(err, ErrRuleExists) {
		t.Fatalf("adding the same rule twice: %v, want ErrRuleExists", err)
	}
	if got := chainLength(t, j); got != before+1 {
		t.Fatalf("a refused duplicate left %d entries behind", got-before-1)
	}

	removed, err := j.RemoveRule(&agent, nil, "delete_repository")
	if err != nil {
		t.Fatalf("RemoveRule: %v", err)
	}
	if removed.ID != r.ID {
		t.Fatalf("removed rule %d, want %d", removed.ID, r.ID)
	}
	if rules, _ := j.ListRules(); len(rules) != 0 {
		t.Fatalf("%d rules remain after removal", len(rules))
	}
	if got := chainLength(t, j); got != before+2 {
		t.Fatalf("removing a rule wrote %d entries, want 1", got-before-1)
	}
	entries, _ = j.EntriesSince(before+1, 10)
	if entries[0].Kind != KindRuleRemove || *entries[0].Tool != "delete_repository" {
		t.Fatalf("the rule.remove entry does not describe the rule: %+v", entries[0])
	}

	if _, err := j.RemoveRule(&agent, nil, "delete_repository"); !errors.Is(err, ErrNoSuchRule) {
		t.Fatalf("removing a rule that is not there: %v, want ErrNoSuchRule", err)
	}
	if got := chainLength(t, j); got != before+2 {
		t.Fatalf("a refused removal left %d entries behind", got-before-2)
	}
}

// Either half failing rolls back the other. A rule the chain does not know
// about, or an entry describing a rule that was never stored, is exactly the
// lie one transaction exists to make impossible.
func TestAPolicyChangeThatCannotBeRecordedIsNotMade(t *testing.T) {
	j, path := openTemp(t)
	before := chainLength(t, j)

	// The entry cannot be written: the rule must not exist afterwards.
	tamper(t, path, `create trigger no_rule_entries before insert on nim_journal
		when new.kind = 'rule.add' begin select raise(abort, 'no'); end`)
	if _, err := j.AddRule(Rule{Tool: "rm"}); err == nil {
		t.Fatal("AddRule succeeded although its entry could not be written")
	}
	if rules, _ := j.ListRules(); len(rules) != 0 {
		t.Fatalf("a rule was stored with no entry recording it: %+v", rules)
	}
	tamper(t, path, `drop trigger no_rule_entries`)

	// The rule cannot be stored: the chain must not say it was.
	tamper(t, path, `create trigger no_rules before insert on nim_rules
		begin select raise(abort, 'no'); end`)
	if _, err := j.AddRule(Rule{Tool: "rm"}); err == nil {
		t.Fatal("AddRule succeeded although the rule could not be stored")
	}
	if got := chainLength(t, j); got != before {
		t.Fatalf("the chain gained %d entries for a rule that was never stored", got-before)
	}
}

// What a rule's scope means, stated as the cases a decision meets. An agent of
// "" is a session no enrolment matched, and it meets only the rules that name
// no agent -- docs/decisions/0002 is the argument, this is the fact.
func TestRuleScopes(t *testing.T) {
	j, _ := openTemp(t)
	cursor, github := "cursor", "github"
	for _, r := range []Rule{
		{Tool: "rm"},
		{Agent: &cursor, Tool: "delete_repository"},
		{Connector: &github, Tool: "force_push"},
		{Agent: &cursor, Connector: &github, Tool: "merge"},
	} {
		if _, err := j.AddRule(r); err != nil {
			t.Fatalf("AddRule(%s): %v", r, err)
		}
	}

	cases := []struct {
		agent, connector, tool string
		denied                 bool
	}{
		{"", "", "rm", true},
		{"cursor", "slack", "rm", true},
		{"claude-code", "github", "rm", true},
		{"cursor", "github", "delete_repository", true},
		{"cursor", "slack", "delete_repository", true},
		{"claude-code", "github", "delete_repository", false},
		{"", "github", "delete_repository", false}, // unknown agent: agent-scoped rules do not apply
		{"", "github", "force_push", true},
		{"cursor", "slack", "force_push", false},
		{"cursor", "github", "merge", true},
		{"cursor", "slack", "merge", false},
		{"claude-code", "github", "merge", false},
		{"", "", "merge", false},
		{"cursor", "github", "RM", false},  // exact names only
		{"cursor", "github", "rm ", false}, // no trimming
	}
	for _, c := range cases {
		_, denied, err := j.RuleDenying(c.agent, c.connector, c.tool)
		if err != nil {
			t.Fatalf("RuleDenying(%q, %q, %q): %v", c.agent, c.connector, c.tool, err)
		}
		if denied != c.denied {
			t.Errorf("RuleDenying(agent=%q, connector=%q, tool=%q) = %v, want %v",
				c.agent, c.connector, c.tool, denied, c.denied)
		}
	}
}

// A policy change is covered by the chain like a call is: editing it after the
// fact breaks verification.
func TestAlteringAPolicyEntryBreaksTheChain(t *testing.T) {
	j, path := openTemp(t)
	if _, err := j.AddRule(Rule{Tool: "rm"}); err != nil {
		t.Fatal(err)
	}
	if rep, _ := j.Verify(""); !rep.OK {
		t.Fatalf("the chain does not verify with a rule entry in it: %s", rep.Problem)
	}
	tamper(t, path, `update nim_journal set tool = 'ls' where kind = 'rule.add'`)
	rep, err := j.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("a rewritten rule.add entry still verified")
	}
}

// A read-only handle can look at the rules and change none of them.
func TestAReadOnlyJournalCannotChangePolicy(t *testing.T) {
	w, path := openTemp(t)
	if _, err := w.AddRule(Rule{Tool: "rm"}); err != nil {
		t.Fatal(err)
	}
	w.Close()

	j, err := OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if rules, err := j.ListRules(); err != nil || len(rules) != 1 {
		t.Fatalf("ListRules = %v, %v", rules, err)
	}
	if _, err := j.AddRule(Rule{Tool: "ls"}); err == nil {
		t.Error("a read-only journal accepted a rule")
	}
	if _, err := j.RemoveRule(nil, nil, "rm"); err == nil {
		t.Error("a read-only journal removed a rule")
	}
}
