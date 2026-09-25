package daemon

import (
	"errors"
	"fmt"
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
)

// Policy: rules the daemon reads when it decides, kept where the standing
// decision says an authorization input belongs -- SQLite, written only by the
// daemon -- and changed only in a way that leaves an entry in the chain.
//
// This replaces the deny list config.toml carried from M2 to here. That list
// was scaffolding and said so: anything able to write the file could empty it,
// and the change left nothing behind. A rule here is added and removed through
// the daemon, over the same peer-verified socket as a connector or an
// enrolment, and each change is one transaction with its rule.add or
// rule.remove entry, so `nim log` shows when the rules changed as it shows when
// the calls were made.
//
// M4.5 adds allow rules and the precedence between them and deny --
// docs/decisions/0003-allow-rules-and-precedence.md -- so policy.deny and
// policy.allow share one handler below, differing only in the effect they
// store, and policy.explain exists so the CLI can show which rule would
// decide a call without ever reading the database itself.
//
// What a rule is worth is bounded by what an enrolment is worth, and that is
// stated rather than implied: anything that can run this binary as this user
// can add or remove a rule, exactly as it can enrol an agent or register a
// connector. The journal entry is what makes that visible afterwards, not
// what prevents it.

func handlePolicyDeny(req Request, j *journal.Journal) Response {
	return handlePolicyAdd(req, journal.DecisionDeny, j)
}

func handlePolicyAllow(req Request, j *journal.Journal) Response {
	return handlePolicyAdd(req, journal.DecisionAllow, j)
}

// handlePolicyAsk is deny and allow's third counterpart, added for M6: a
// call the rule matches is held for a human rather than decided here -- see
// docs/decisions/0005-human-approval.md and the 2026-09-15 addendum to
// docs/decisions/0003.
func handlePolicyAsk(req Request, j *journal.Journal) Response {
	return handlePolicyAdd(req, journal.DecisionAsk, j)
}

// handlePolicyAdd is shared by policy.deny, policy.allow and policy.ask:
// everything but the effect they store -- validating the scope, resolving
// the tool, checking a named agent is enrolled, refusing a duplicate -- is
// one piece of logic that must not drift between the three.
func handlePolicyAdd(req Request, effect string, j *journal.Journal) Response {
	agent, connector, err := ruleScope(req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	tool, err := ruleTool(req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	// A rule may name only an enrolled agent. Rules match on the name the
	// daemon derives, and it only ever derives names that were enrolled, so a
	// rule for a name nobody enrolled would match nothing and look like it
	// applied. Refusing it here is what turns a typo into a message.
	if agent != nil {
		if _, found, err := j.AgentNamed(*agent); err != nil {
			return Response{ID: req.ID, Error: fmt.Sprintf("looking up agent %q: %v", *agent, err)}
		} else if !found {
			return Response{ID: req.ID, Error: fmt.Sprintf(
				"no agent named %q is enrolled; enrol it first with nim agent add %s <path-to-executable>",
				*agent, *agent)}
		}
	}
	r, err := j.AddRule(journal.Rule{Agent: agent, Connector: connector, Tool: tool, Effect: effect})
	if errors.Is(err, journal.ErrRuleExists) {
		return Response{ID: req.ID, Error: fmt.Sprintf(
			"a rule already exists for %s; a scope holds one rule, allow or deny -- remove it first to change the effect",
			scopeDescription(agent, connector, tool))}
	}
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("recording the rule: %v", err)}
	}
	return Response{ID: req.ID, Rules: []RuleInfo{ruleInfo(r)}}
}

// A rule can be removed whether or not its agent is still enrolled: an
// enrolment can be removed first, and a rule that outlived it must still be
// removable, or the list would hold rules nothing can delete.
//
// The effect is not part of what identifies a rule to remove -- a scope and
// tool hold at most one rule, allow or deny, so naming the scope is enough.
func handlePolicyRemove(req Request, j *journal.Journal) Response {
	agent, connector, err := ruleScope(req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	tool, err := ruleTool(req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	r, err := j.RemoveRule(agent, connector, tool)
	if errors.Is(err, journal.ErrNoSuchRule) {
		return Response{ID: req.ID, Error: fmt.Sprintf(
			"no such rule: %s. nim policy list shows the rules that exist, with their exact scope",
			scopeDescription(agent, connector, tool))}
	}
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("removing the rule: %v", err)}
	}
	return Response{ID: req.ID, Rules: []RuleInfo{ruleInfo(r)}}
}

func handlePolicyList(req Request, j *journal.Journal) Response {
	rules, err := j.ListRules()
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("listing rules: %v", err)}
	}
	infos := make([]RuleInfo, len(rules))
	for i, r := range rules {
		infos[i] = ruleInfo(r)
	}
	return Response{ID: req.ID, Rules: infos}
}

// handlePolicyExplain answers "which rule decides this call, and why" --
// nim policy explain <tool> [--agent] [--connector] -- by gathering the same
// candidates the decision path would and running them through journal.Decide,
// the one implementation of the precedence. The CLI never reads nim_rules
// itself; this is the only door to it.
//
// Unlike policy.deny/allow/remove, RuleTool here names the tool a call would
// carry, not a rule's own tool -- so RuleToolDefault ("*") is refused: it is
// not something a client sends as params.name for this purpose, only a way a
// rule expresses a default, and explaining "what happens for the literal
// tool *" would answer a different, much rarer question than the one the
// command name promises.
func handlePolicyExplain(req Request, j *journal.Journal) Response {
	agent, connector, err := ruleScope(req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if err := validateTool(req.RuleTool); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if req.RuleTool == journal.RuleToolDefault {
		return Response{ID: req.ID, Error: `"*" is not a tool a call names; nim policy explain takes the exact tool to ask about`}
	}

	a, c := "", ""
	if agent != nil {
		a = *agent
	}
	if connector != nil {
		c = *connector
	}
	candidates, err := j.MatchingRules(a, c, req.RuleTool)
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("reading the rules: %v", err)}
	}
	decision, info, reason := explainDecision(candidates)

	// Budgets are shown too, but kept simple on purpose: explain has no
	// session id, so it reports which budgets would apply and their caps,
	// never a count against them -- inventing session state it was never
	// given would be a guess dressed up as an answer.
	budgets, err := j.MatchingBudgets(a, c, req.RuleTool)
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("reading the budgets: %v", err)}
	}
	budgetInfos := make([]BudgetInfo, len(budgets))
	for i, b := range budgets {
		budgetInfos[i] = budgetInfo(b)
	}

	return Response{ID: req.ID, Explain: &ExplainInfo{
		Agent: a, Connector: c, Tool: req.RuleTool,
		Decision: decision, Rule: info, Reason: reason, Candidates: len(candidates),
		Budgets: budgetInfos,
	}}
}

// explainDecision turns the candidates policy.explain gathered into the
// verdict the decision path would reach, plus a reason a human can read. It
// calls journal.Decide, never a second copy of the precedence, so what this
// says can never drift from what a real call gets.
func explainDecision(candidates []journal.Rule) (decision string, rule *RuleInfo, reason string) {
	winner, found := journal.Decide(candidates)
	if !found {
		return journal.DecisionAllow, nil, "no rule's scope matches this agent, connector and tool -- the M4 baseline applies: allow"
	}
	info := ruleInfo(winner)
	if len(candidates) == 1 {
		return winner.Effect, &info, "the only rule whose scope matches: " + winner.String()
	}
	return winner.Effect, &info, fmt.Sprintf(
		"the most specific of %d matching rules (a tie in specificity goes to deny): %s", len(candidates), winner.String())
}

// ruleScope validates the two fields that name a rule's scope and turns the
// empty ones into nil, which is how "every" is stored. Tool is not part of
// scope -- see ruleTool -- because a default rule (RuleDefault) has no tool
// field of its own to validate.
func ruleScope(req Request) (agent, connector *string, err error) {
	if req.RuleAgent != "" {
		if err := validateAgentName(req.RuleAgent); err != nil {
			return nil, nil, err
		}
		agent = &req.RuleAgent
	}
	if req.RuleConnector != "" {
		if err := validateTarget(req.RuleConnector); err != nil {
			return nil, nil, fmt.Errorf("connector: %w", err)
		}
		connector = &req.RuleConnector
	}
	return agent, connector, nil
}

// ruleTool resolves the tool a policy.deny/allow/remove request names:
// RuleToolDefault for `nim policy default ...` or `nim policy remove
// --default`, or the validated literal tool otherwise.
//
// "*" is refused as an ordinary tool here, on purpose: it exists only to
// express a default, and typing it directly through --agent/--connector
// scoped policy deny/allow would create one by accident, indistinguishable
// at match time from one made through nim policy default. Requiring
// --default keeps there being exactly one way to ask for a default rule.
// This has nothing to do with whether a *client* may call a tool literally
// named "*" -- it can, and such a call matches this rule exactly as it would
// match one written for that literal name; see MatchingRules.
func ruleTool(req Request) (string, error) {
	if req.RuleDefault {
		if req.RuleTool != "" {
			return "", fmt.Errorf("a default rule has no tool of its own; drop the tool, or drop --default and name one")
		}
		return journal.RuleToolDefault, nil
	}
	if err := validateTool(req.RuleTool); err != nil {
		return "", err
	}
	if req.RuleTool == journal.RuleToolDefault {
		return "", fmt.Errorf(`tool "*" is reserved for defaults; use nim policy default deny|allow instead`)
	}
	return req.RuleTool, nil
}

// scopeDescription names a rule's scope and tool without claiming an effect,
// for messages about a rule that does or does not exist -- removal in
// particular does not know in advance whether a missing rule would have
// allowed or denied.
func scopeDescription(agent, connector *string, tool string) string {
	name := tool
	if tool == journal.RuleToolDefault {
		name = "the default"
	}
	scope := "every agent"
	if agent != nil {
		scope = "agent " + *agent
	}
	where := "every connector"
	if connector != nil {
		where = "connector " + *connector
	}
	return fmt.Sprintf("%s for %s on %s", name, scope, where)
}

func ruleInfo(r journal.Rule) RuleInfo {
	info := RuleInfo{Tool: r.Tool, Effect: r.Effect, CreatedAt: r.CreatedAt.Format(time.RFC3339Nano)}
	if r.Agent != nil {
		info.Agent = *r.Agent
	}
	if r.Connector != nil {
		info.Connector = *r.Connector
	}
	return info
}
