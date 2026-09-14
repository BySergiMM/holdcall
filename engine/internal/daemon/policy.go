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
// What a rule is worth is bounded by what an enrolment is worth, and that is
// stated rather than implied: anything that can run this binary as this user
// can add or remove a rule, exactly as it can enrol an agent or register a
// connector. The journal entry is what makes that visible afterwards, not
// what prevents it.

func handlePolicyDeny(req Request, j *journal.Journal) Response {
	agent, connector, err := ruleScope(req)
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
	r, err := j.AddRule(journal.Rule{Agent: agent, Connector: connector, Tool: req.RuleTool})
	if errors.Is(err, journal.ErrRuleExists) {
		return Response{ID: req.ID, Error: fmt.Sprintf("that rule already exists: %s", ruleString(agent, connector, req.RuleTool))}
	}
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("recording the rule: %v", err)}
	}
	return Response{ID: req.ID, Rules: []RuleInfo{ruleInfo(r)}}
}

// A rule can be removed whether or not its agent is still enrolled: an
// enrolment can be removed first, and a rule that outlived it must still be
// removable, or the list would hold rules nothing can delete.
func handlePolicyRemove(req Request, j *journal.Journal) Response {
	agent, connector, err := ruleScope(req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	r, err := j.RemoveRule(agent, connector, req.RuleTool)
	if errors.Is(err, journal.ErrNoSuchRule) {
		return Response{ID: req.ID, Error: fmt.Sprintf(
			"no such rule: %s. nim policy list shows the rules that exist, with their exact scope",
			ruleString(agent, connector, req.RuleTool))}
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

// ruleScope validates the three fields a rule is made of and turns the empty
// ones into nil, which is how "every" is stored.
func ruleScope(req Request) (agent, connector *string, err error) {
	if err := validateTool(req.RuleTool); err != nil {
		return nil, nil, err
	}
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

func ruleString(agent, connector *string, tool string) string {
	return journal.Rule{Agent: agent, Connector: connector, Tool: tool}.String()
}

func ruleInfo(r journal.Rule) RuleInfo {
	info := RuleInfo{Tool: r.Tool, CreatedAt: r.CreatedAt.Format(time.RFC3339Nano)}
	if r.Agent != nil {
		info.Agent = *r.Agent
	}
	if r.Connector != nil {
		info.Connector = *r.Connector
	}
	return info
}
