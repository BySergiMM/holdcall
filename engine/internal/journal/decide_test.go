package journal

import "testing"

// sa and sc are stand-ins for a named agent or connector: Decide only cares
// whether Rule.Agent/Connector is nil or not, never what the string holds,
// so one shared pointer is enough for every case below.
var sa, sc = ptr("cursor"), ptr("github")

func ptr(s string) *string { return &s }

// The precedence docs/decisions/0003-allow-rules-and-precedence.md states,
// enumerated: specificity first (exact tool over default, named agent or
// connector over unnamed), deny over allow at equal specificity, and no
// candidates at all is left to the caller to read as the M4 baseline. Pure
// and table-driven on purpose: no SQLite, no daemon, just the rule sets a
// database query could plausibly hand back.
func TestDecideAppliesSpecificityThenDenyOverAllow(t *testing.T) {
	cases := []struct {
		name       string
		candidates []Rule
		wantFound  bool
		wantID     int64 // 0 when wantFound is false
	}{
		{
			name:      "no candidates at all leaves the baseline to the caller",
			wantFound: false,
		},
		{
			name:       "the only candidate wins, whatever it is",
			candidates: []Rule{{ID: 1, Tool: "rm", Effect: DecisionAllow}},
			wantFound:  true, wantID: 1,
		},
		{
			name: "an exact tool beats a default naming the same scope",
			candidates: []Rule{
				{ID: 1, Tool: RuleToolDefault, Effect: DecisionDeny},
				{ID: 2, Tool: "rm", Effect: DecisionAllow},
			},
			wantFound: true, wantID: 2,
		},
		{
			name: "an exact tool beats a default naming the same agent and connector, even when the default denies and the tool allows",
			candidates: []Rule{
				{ID: 1, Agent: sa, Connector: sc, Tool: RuleToolDefault, Effect: DecisionDeny},
				{ID: 2, Agent: sa, Connector: sc, Tool: "rm", Effect: DecisionAllow},
			},
			wantFound: true, wantID: 2,
		},
		{
			name: "naming the agent beats not naming it, tool and connector equal",
			candidates: []Rule{
				{ID: 1, Tool: "rm", Effect: DecisionDeny},
				{ID: 2, Agent: sa, Tool: "rm", Effect: DecisionDeny},
			},
			wantFound: true, wantID: 2,
		},
		{
			name: "naming the connector beats not naming it, tool and agent equal",
			candidates: []Rule{
				{ID: 1, Tool: "rm", Effect: DecisionDeny},
				{ID: 2, Connector: sc, Tool: "rm", Effect: DecisionDeny},
			},
			wantFound: true, wantID: 2,
		},
		{
			name: "naming both agent and connector beats naming only one",
			candidates: []Rule{
				{ID: 1, Agent: sa, Tool: "rm", Effect: DecisionDeny},
				{ID: 2, Agent: sa, Connector: sc, Tool: "rm", Effect: DecisionDeny},
			},
			wantFound: true, wantID: 2,
		},
		{
			name: "deny beats allow at equal specificity",
			candidates: []Rule{
				{ID: 1, Agent: sa, Tool: "rm", Effect: DecisionAllow},
				{ID: 2, Agent: sa, Tool: "rm", Effect: DecisionDeny},
			},
			wantFound: true, wantID: 2,
		},
		{
			name: "deny beats allow at equal specificity regardless of which was added first",
			candidates: []Rule{
				{ID: 1, Agent: sa, Tool: "rm", Effect: DecisionDeny},
				{ID: 2, Agent: sa, Tool: "rm", Effect: DecisionAllow},
			},
			wantFound: true, wantID: 1,
		},
		{
			name: "a more specific allow overrides a less specific deny -- the point of M4.5",
			candidates: []Rule{
				{ID: 1, Tool: RuleToolDefault, Effect: DecisionDeny},
				{ID: 2, Agent: sa, Tool: RuleToolDefault, Effect: DecisionAllow},
			},
			wantFound: true, wantID: 2,
		},
		{
			name: "equal specificity and equal effect from two rules naming different things breaks on id",
			candidates: []Rule{
				{ID: 5, Agent: sa, Tool: "rm", Effect: DecisionDeny},
				{ID: 3, Connector: sc, Tool: "rm", Effect: DecisionDeny},
			},
			wantFound: true, wantID: 3,
		},
		{
			name: "order of the candidate slice never changes the winner",
			candidates: []Rule{
				{ID: 2, Agent: sa, Connector: sc, Tool: "rm", Effect: DecisionDeny},
				{ID: 1, Tool: RuleToolDefault, Effect: DecisionAllow},
				{ID: 3, Agent: sa, Tool: "rm", Effect: DecisionAllow},
			},
			wantFound: true, wantID: 2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			winner, found := Decide(c.candidates)
			if found != c.wantFound {
				t.Fatalf("Decide(%s) found = %v, want %v", c.name, found, c.wantFound)
			}
			if found && winner.ID != c.wantID {
				t.Errorf("Decide(%s) picked rule %d, want %d", c.name, winner.ID, c.wantID)
			}
		})
	}
}

// The reverse of the slice above must pick the same winner: Decide is a
// function of the set, not of the order it was handed the set in.
func TestDecideIsOrderIndependent(t *testing.T) {
	candidates := []Rule{
		{ID: 1, Tool: RuleToolDefault, Effect: DecisionDeny},
		{ID: 2, Agent: sa, Tool: "rm", Effect: DecisionAllow},
		{ID: 3, Connector: sc, Tool: "rm", Effect: DecisionDeny},
	}
	forward, _ := Decide(candidates)
	reversed := []Rule{candidates[2], candidates[1], candidates[0]}
	backward, _ := Decide(reversed)
	if forward.ID != backward.ID {
		t.Fatalf("order changed the winner: forward picked %d, backward picked %d", forward.ID, backward.ID)
	}
}
