package readmodel

import (
	"time"

	"github.com/BySergiMM/holdcall/engine/internal/journal"
	"github.com/BySergiMM/holdcall/engine/internal/peer"
)

// Rule is one rule as a reader sees it. It and journal.Rule agree on scope
// -- an agent and a connector, both nilable meaning "every" -- and on
// effect: deny, allow, or ask, which holds the call for a human. See
// docs/decisions/0002, 0003 and 0005.
type Rule struct {
	Agent     *string `json:"agent,omitempty"`
	Connector *string `json:"connector,omitempty"`
	Tool      string  `json:"tool"`
	// Effect is deny, allow or ask. Since M4.5 a rule can grant as well as
	// refuse, and since M6 it can hold a call for a human; a list that did
	// not say which would be worse than no list.
	Effect    string `json:"effect"`
	CreatedAt string `json:"created_at"`
}

// RuleFrom projects one rule. Copies, like EventFrom: nothing here aliases
// the journal's own memory.
func RuleFrom(r journal.Rule) Rule {
	return Rule{
		Agent:     copyString(r.Agent),
		Connector: copyString(r.Connector),
		Tool:      r.Tool,
		Effect:    r.Effect,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// RulesFrom projects every rule, in the order ListRules already reads them
// in (oldest first, by id).
func RulesFrom(rows []journal.Rule) []Rule {
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, RuleFrom(r))
	}
	return out
}

// Budget is one cap as a reader sees it: the scope a rule would have, and
// the number of allowed calls one session may make within it. Found missing
// by the M5 review: `holdcall policy list` showed budgets and `holdcall status` and
// the console did not, so an operator reading either would have believed a
// session unbounded that was not.
type Budget struct {
	Agent     *string `json:"agent,omitempty"`
	Connector *string `json:"connector,omitempty"`
	Tool      string  `json:"tool"`
	Calls     int64   `json:"calls"`
	CreatedAt string  `json:"created_at"`
}

// BudgetFrom projects one budget. Copies, like RuleFrom.
func BudgetFrom(b journal.Budget) Budget {
	return Budget{
		Agent:     copyString(b.Agent),
		Connector: copyString(b.Connector),
		Tool:      b.Tool,
		Calls:     b.Calls,
		CreatedAt: b.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// BudgetsFrom projects every budget, in the order ListBudgets already reads
// them in (oldest first, by id).
func BudgetsFrom(rows []journal.Budget) []Budget {
	out := make([]Budget, 0, len(rows))
	for _, b := range rows {
		out = append(out, BudgetFrom(b))
	}
	return out
}

// Agent is one enrolment as a reader sees it. ExecDev and ExecIno stay out:
// they are the kernel identity a decision matches on, and a console has no
// more reason to show them than to show a params_digest's preimage.
type Agent struct {
	Name       string `json:"name"`
	ExecPath   string `json:"exec_path"`
	EnrolledAt string `json:"enrolled_at"`

	// Current reports whether the file at ExecPath is still the one that was
	// enrolled, computed the same way `holdcall agent list` computes it (see
	// daemon.stillTheEnrolledFile) -- but through internal/peer directly
	// rather than the daemon package, so a read-only viewer with no socket
	// to the daemon can still say it.
	//
	// Three states, not two. nil means this platform cannot answer the
	// question at all (peer.FileIdentitySupported is false on Windows
	// today), which is not the same as having looked and found a mismatch:
	// reporting nil as false would tell an operator their enrolment had gone
	// stale when nothing was actually checked. false is not a security
	// finding either way -- a device number can change across a reboot and a
	// self-updating program becomes a different file. It means this
	// enrolment will match no running process and should be repeated.
	Current *bool `json:"current"`
}

// stillEnrolled reports whether the file now at a.ExecPath is still the one
// identified by a.ExecDev/a.ExecIno at enrolment time, or nil when this
// platform has no way to ask.
func stillEnrolled(a journal.Agent) *bool {
	if !peer.FileIdentitySupported {
		return nil
	}
	img, err := peer.ImageOfFile(a.ExecPath)
	if err != nil {
		current := false
		return &current
	}
	current := img.Equal(peer.NewImage(a.ExecDev, a.ExecIno))
	return &current
}

// AgentFrom projects one enrolment.
func AgentFrom(a journal.Agent) Agent {
	return Agent{
		Name:       a.Name,
		ExecPath:   a.ExecPath,
		EnrolledAt: a.EnrolledAt.UTC().Format(time.RFC3339Nano),
		Current:    stillEnrolled(a),
	}
}

// AgentsFrom projects every enrolment, in the order ListAgents already reads
// them in (by name).
func AgentsFrom(rows []journal.Agent) []Agent {
	out := make([]Agent, 0, len(rows))
	for _, a := range rows {
		out = append(out, AgentFrom(a))
	}
	return out
}

// Connector is non-secret connector metadata as a reader sees it: which env
// var name a target's credential is injected under, and the argv it may be
// injected into. Never the secret -- there is none in the journal to show,
// and this must never become the place one is added.
type Connector struct {
	Target    string   `json:"target"`
	EnvKey    string   `json:"env_key"`
	Command   []string `json:"command,omitempty"`
	UpdatedAt string   `json:"updated_at"`
}

// ConnectorFrom projects one connector. Command is copied, not aliased:
// journal.Connector.Command comes straight out of a driver-owned buffer for
// the row it just scanned, like every other slice this package puts on the
// wire.
func ConnectorFrom(c journal.Connector) Connector {
	return Connector{
		Target:    c.Target,
		EnvKey:    c.EnvKey,
		Command:   append([]string(nil), c.Command...),
		UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// ConnectorsFrom projects every connector, in the order ListConnectors
// already reads them in (by target).
func ConnectorsFrom(rows []journal.Connector) []Connector {
	out := make([]Connector, 0, len(rows))
	for _, c := range rows {
		out = append(out, ConnectorFrom(c))
	}
	return out
}

// Policy is the configuration view: what an agent is, what a connector may
// receive, what is allowed or denied, and how much of it a session may do.
// Distinct from Snapshot, which is the record of what happened -- a rule or
// a budget is what may happen, an enrolment is as privileged as running
// Holdcall, and a connector's command is the argv authorized to receive its
// credential, none of which is a fact about a call that was made.
type Policy struct {
	Rules      []Rule      `json:"rules"`
	Budgets    []Budget    `json:"budgets"`
	Agents     []Agent     `json:"agents"`
	Connectors []Connector `json:"connectors"`
}

// TakePolicy reads rules, budgets, agents and connectors and projects them
// together, so a page that shows all four -- the console's Policy tab, `holdcall
// status` -- makes one call instead of four scattered across its caller.
func TakePolicy(src PolicySource) (Policy, error) {
	var p Policy

	rules, err := src.ListRules()
	if err != nil {
		return p, err
	}
	p.Rules = RulesFrom(rules)

	budgets, err := src.ListBudgets()
	if err != nil {
		return p, err
	}
	p.Budgets = BudgetsFrom(budgets)

	agents, err := src.ListAgents()
	if err != nil {
		return p, err
	}
	p.Agents = AgentsFrom(agents)

	connectors, err := src.ListConnectors()
	if err != nil {
		return p, err
	}
	p.Connectors = ConnectorsFrom(connectors)

	return p, nil
}

// Explanation is what `holdcall policy explain` says, as a reader sees it: the
// effect a call shaped like (agent, connector, tool) would get, the rule that
// decides it when one does, every rule that matched, and the budgets that
// would be weighed once the rules allow. Decision is what journal.Decide
// returns -- the one function the daemon and the CLI already share -- so
// the console cannot say something a real call would not do.
type Explanation struct {
	Agent     string   `json:"agent"`
	Connector string   `json:"connector"`
	Tool      string   `json:"tool"`
	Decision  string   `json:"decision"`
	ByRule    bool     `json:"by_rule"`
	Rule      *Rule    `json:"rule,omitempty"`
	Matching  []Rule   `json:"matching"`
	Budgets   []Budget `json:"budgets"`
}

// Explain projects the decision for one call shape. "" for agent or
// connector means an unenrolled program or an unnamed connector, exactly as
// the daemon sees a session that no enrolment matched.
func Explain(src PolicySource, agent, connector, tool string) (Explanation, error) {
	out := Explanation{Agent: agent, Connector: connector, Tool: tool, Decision: journal.DecisionAllow,
		Matching: []Rule{}, Budgets: []Budget{}}
	candidates, err := src.MatchingRules(agent, connector, tool)
	if err != nil {
		return out, err
	}
	out.Matching = RulesFrom(candidates)
	if winner, found := journal.Decide(candidates); found {
		r := RuleFrom(winner)
		out.Rule, out.ByRule, out.Decision = &r, true, winner.Effect
	}
	budgets, err := src.MatchingBudgets(agent, connector, tool)
	if err != nil {
		return out, err
	}
	out.Budgets = BudgetsFrom(budgets)
	return out, nil
}
