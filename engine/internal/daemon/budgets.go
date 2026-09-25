package daemon

import (
	"errors"
	"fmt"
	"time"

	"github.com/BySergiMM/holdcall/engine/internal/journal"
)

// Budgets: a cap on the number of ALLOWED calls one session may make,
// checked only once the rules have already allowed a call -- see
// checkBudgets, called from answer(), and
// docs/decisions/0004-budgets.md. A budget never grants; it only lowers what
// the rules already allow.
//
// nim_budgets sits outside the hash chain, exactly like nim_rules and
// nim_agents: it is configuration, not the record of what happened. Every
// insert or delete is paired, in the same transaction, with a budget.add or
// budget.remove entry in the chain, so a budget's history verifies like a
// rule's does. budget.set and budget.remove share the "policy" purpose with
// policy.deny/allow/remove/list/explain -- a budget is policy, and a
// connection that has committed to managing one may manage the other.

func handleBudgetSet(req Request, j *journal.Journal) Response {
	agent, connector, err := budgetScope(req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	tool, err := budgetTool(req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if err := validateBudgetCalls(req.BudgetCalls); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	// A budget may name only an enrolled agent, for the same reason a rule
	// may: it matches on the name the daemon derives, and a budget for a
	// name nobody enrolled would match nothing and look like it applied.
	if agent != nil {
		if _, found, err := j.AgentNamed(*agent); err != nil {
			return Response{ID: req.ID, Error: fmt.Sprintf("looking up agent %q: %v", *agent, err)}
		} else if !found {
			return Response{ID: req.ID, Error: fmt.Sprintf(
				"no agent named %q is enrolled; enrol it first with holdcall agent add %s <path-to-executable>",
				*agent, *agent)}
		}
	}
	b, err := j.AddBudget(journal.Budget{Agent: agent, Connector: connector, Tool: tool, Calls: int64(req.BudgetCalls)})
	if errors.Is(err, journal.ErrBudgetExists) {
		return Response{ID: req.ID, Error: fmt.Sprintf(
			"a budget already exists for %s; a scope holds one budget -- remove it first to change the cap",
			budgetScopeDescription(agent, connector, tool))}
	}
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("recording the budget: %v", err)}
	}
	return Response{ID: req.ID, Budgets: []BudgetInfo{budgetInfo(b)}}
}

// A budget can be removed whether or not its agent is still enrolled, for
// the same reason a rule can (see handlePolicyRemove).
func handleBudgetRemove(req Request, j *journal.Journal) Response {
	agent, connector, err := budgetScope(req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	tool, err := budgetTool(req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	b, err := j.RemoveBudget(agent, connector, tool)
	if errors.Is(err, journal.ErrNoSuchBudget) {
		return Response{ID: req.ID, Error: fmt.Sprintf(
			"no such budget: %s. holdcall policy list shows the budgets that exist, with their exact scope",
			budgetScopeDescription(agent, connector, tool))}
	}
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("removing the budget: %v", err)}
	}
	return Response{ID: req.ID, Budgets: []BudgetInfo{budgetInfo(b)}}
}

func handleBudgetList(req Request, j *journal.Journal) Response {
	budgets, err := j.ListBudgets()
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("listing budgets: %v", err)}
	}
	infos := make([]BudgetInfo, len(budgets))
	for i, b := range budgets {
		infos[i] = budgetInfo(b)
	}
	return Response{ID: req.ID, Budgets: infos}
}

// budgetScope validates the two fields that name a budget's scope and turns
// the empty ones into nil, which is how "every" is stored -- the same
// validation ruleScope applies to a rule's scope, against the same fields'
// shape on their own Budget* names.
func budgetScope(req Request) (agent, connector *string, err error) {
	if req.BudgetAgent != "" {
		if err := validateAgentName(req.BudgetAgent); err != nil {
			return nil, nil, err
		}
		agent = &req.BudgetAgent
	}
	if req.BudgetConnector != "" {
		if err := validateTarget(req.BudgetConnector); err != nil {
			return nil, nil, fmt.Errorf("connector: %w", err)
		}
		connector = &req.BudgetConnector
	}
	return agent, connector, nil
}

// budgetTool resolves the tool a budget.set/remove request names:
// journal.BudgetToolAll for `holdcall policy budget ... --all-tools`, or the
// validated literal tool otherwise -- the same shape ruleTool resolves for
// a rule's default.
func budgetTool(req Request) (string, error) {
	if req.BudgetAllTools {
		if req.BudgetTool != "" {
			return "", fmt.Errorf("--all-tools and a tool are two ways to name the same budget's scope; use one")
		}
		return journal.BudgetToolAll, nil
	}
	// validateTool's own empty-tool message tells the caller to write a
	// rule; a budget request that named no tool deserves its own sentence.
	if req.BudgetTool == "" {
		return "", fmt.Errorf("a budget needs the tool it caps: holdcall policy budget <n> --tool <tool>, or --all-tools")
	}
	if err := validateTool(req.BudgetTool); err != nil {
		return "", err
	}
	if req.BudgetTool == journal.BudgetToolAll {
		return "", fmt.Errorf(`tool "*" is reserved for --all-tools; use holdcall policy budget <n> --all-tools instead`)
	}
	return req.BudgetTool, nil
}

// budgetScopeDescription names a budget's scope and tool for messages about
// one that does or does not exist -- the budget-flavoured scopeDescription.
func budgetScopeDescription(agent, connector *string, tool string) string {
	name := tool
	if tool == journal.BudgetToolAll {
		name = "every tool"
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

func budgetInfo(b journal.Budget) BudgetInfo {
	info := BudgetInfo{Tool: b.Tool, Calls: b.Calls, CreatedAt: b.CreatedAt.Format(time.RFC3339Nano)}
	if b.Agent != nil {
		info.Agent = *b.Agent
	}
	if b.Connector != nil {
		info.Connector = *b.Connector
	}
	return info
}

// checkBudgets is called from answer(), and only once the rules have already
// allowed a call: a budget never grants, it only lowers what the rules
// already allow (docs/decisions/0004-budgets.md), so there is nothing for it
// to say about a call the rules denied.
//
// It walks every budget whose scope matches (agent, connector, tool) --
// MatchingBudgets, the same match a rule's scope gets -- and, for the first
// one (in id order, the order budgets were added) whose session count has
// already reached its cap, returns the deny decision and a reason naming it.
// A zero-value return with a nil error means no matching budget is
// exhausted, and the rules' allow stands.
func checkBudgets(j *journal.Journal, agent, connector, tool, sessionID string) (decision, reason string, err error) {
	budgets, err := j.MatchingBudgets(agent, connector, tool)
	if err != nil {
		return "", "", err
	}
	for _, b := range budgets {
		countTool := b.Tool
		if countTool == journal.BudgetToolAll {
			countTool = ""
		}
		used, err := j.CountAllowedCalls(sessionID, countTool)
		if err != nil {
			return "", "", err
		}
		if int64(used) >= b.Calls {
			return journal.DecisionDeny, fmt.Sprintf("budget: %s already allowed this session", b.String()), nil
		}
	}
	return "", "", nil
}
