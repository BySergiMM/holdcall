package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RuleToolDefault is the tool value that expresses a default: an effect for
// every tool, for the sessions in the rule's scope. It exists only through
// `holdcall policy default deny|allow` -- see
// docs/decisions/0003-allow-rules-and-precedence.md -- and is otherwise an
// ordinary tool name to the matching query: a client that genuinely calls a
// tool named "*" matches this rule exactly as it would match one written for
// that literal name, because nothing here tells the two apart. That is
// deliberate and is documented rather than hidden -- see MatchingRules.
const RuleToolDefault = "*"

// Rule is one line of policy: Effect (deny, allow or ask) for Tool, for
// every session in its scope -- those of one enrolled Agent or of every
// session when Agent is nil, on one Connector or on every connector when
// Connector is nil. Tool is RuleToolDefault for a default and an exact tool
// name otherwise; there is no other kind of match.
//
// A fresh install has no rules, and no rule matching a call is an allow --
// the M4 baseline, restated for a model that also has allow and ask rules:
// absence is still permissive, only now an explicit allow can be overridden
// by a more specific deny or ask and vice versa. docs/decisions/0003 has the
// precedence (extended for ask in its 2026-09-15 addendum) and
// docs/decisions/0002's last section has D-002 answered for the allow model.
type Rule struct {
	ID        int64
	Agent     *string
	Connector *string
	Tool      string
	Effect    string // DecisionDeny, DecisionAllow or DecisionAsk
	CreatedAt time.Time
}

// String names the rule the way a log line or an error should.
func (r Rule) String() string {
	scope := "every agent"
	if r.Agent != nil {
		scope = "agent " + *r.Agent
	}
	where := "every connector"
	if r.Connector != nil {
		where = "connector " + *r.Connector
	}
	tool := r.Tool
	if tool == RuleToolDefault {
		tool = "every tool (the default)"
	}
	return fmt.Sprintf("%s %s for %s on %s", r.Effect, tool, scope, where)
}

// The two ways a policy change can be a change to nothing.
var (
	ErrRuleExists = errors.New("that rule already exists")
	ErrNoSuchRule = errors.New("no such rule")
)

// AddRule stores a rule and the rule.add entry recording it, in one
// transaction: the chain never says a rule exists that does not, and a rule
// never exists that the chain does not know about.
//
// A rule that already exists is refused without writing anything. It is not
// treated as idempotent, because an operator who adds the same rule twice
// usually meant a different scope, and a silent success hides that. The
// uniqueness is on scope and tool alone, not effect: one (agent, connector,
// tool) holds exactly one rule, allow or deny, because a scope that could
// hold both would need its own precedence rule and there is no reason to
// invent one when "remove the old rule first" already says what the operator
// means.
func (j *Journal) AddRule(r Rule) (Rule, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if r.Effect != DecisionDeny && r.Effect != DecisionAllow && r.Effect != DecisionAsk {
		return Rule{}, fmt.Errorf("a rule's effect must be %q, %q or %q, not %q",
			DecisionDeny, DecisionAllow, DecisionAsk, r.Effect)
	}
	if j.readOnly {
		return Rule{}, errReadOnly
	}
	tx, err := j.db.Begin()
	if err != nil {
		return Rule{}, err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(
		`select count(*) from nim_rules
		  where ifnull(agent, '') = ifnull(?, '') and ifnull(connector, '') = ifnull(?, '') and tool = ?`,
		r.Agent, r.Connector, r.Tool).Scan(&n); err != nil {
		return Rule{}, err
	}
	if n > 0 {
		return Rule{}, ErrRuleExists
	}

	r.CreatedAt = time.Now().UTC()
	at := r.CreatedAt.Format(time.RFC3339Nano)
	res, err := tx.Exec(
		`insert into nim_rules (agent, connector, tool, effect, created_at) values (?, ?, ?, ?, ?)`,
		r.Agent, r.Connector, r.Tool, r.Effect, at)
	if err != nil {
		return Rule{}, err
	}
	if r.ID, err = res.LastInsertId(); err != nil {
		return Rule{}, err
	}
	if err := j.appendTx(tx, ruleEntry(KindRuleAdd, r, at)); err != nil {
		return Rule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// RemoveRule deletes the rule with exactly this scope and tool -- whichever
// effect it holds, since a scope and tool have at most one -- and records the
// rule.remove entry in the same transaction. Removing a rule that is not
// there is ErrNoSuchRule, not a success: nothing changed, so nothing is
// written, and the operator learns that the scope they typed matched
// nothing.
func (j *Journal) RemoveRule(agent, connector *string, tool string) (Rule, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.readOnly {
		return Rule{}, errReadOnly
	}
	tx, err := j.db.Begin()
	if err != nil {
		return Rule{}, err
	}
	defer tx.Rollback()

	var r Rule
	var a, c sql.NullString
	var created string
	err = tx.QueryRow(
		`select id, agent, connector, tool, effect, created_at from nim_rules
		  where ifnull(agent, '') = ifnull(?, '') and ifnull(connector, '') = ifnull(?, '') and tool = ?`,
		agent, connector, tool).Scan(&r.ID, &a, &c, &r.Tool, &r.Effect, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, ErrNoSuchRule
	}
	if err != nil {
		return Rule{}, err
	}
	r.Agent, r.Connector = nullable(a), nullable(c)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)

	if _, err := tx.Exec(`delete from nim_rules where id = ?`, r.ID); err != nil {
		return Rule{}, err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := j.appendTx(tx, ruleEntry(KindRuleRemove, r, at)); err != nil {
		return Rule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// ruleEntry is what a policy change looks like in the chain: the rule's scope
// in the columns a call would use, its effect where a decision goes, and no
// session. session_id is the empty string, which the encoding keeps distinct
// from null; a rule belongs to no session and says so. tool is "*" for a
// default, exactly as it is stored.
func ruleEntry(kind string, r Rule, at string) Entry {
	tool, effect := r.Tool, r.Effect
	return Entry{
		Kind:       kind,
		SessionID:  "",
		Agent:      r.Agent,
		Connector:  r.Connector,
		Tool:       &tool,
		Decision:   &effect,
		OccurredAt: at,
	}
}

// ListRules returns every rule, oldest first.
func (j *Journal) ListRules() ([]Rule, error) {
	rows, err := j.db.Query(
		`select id, agent, connector, tool, effect, created_at from nim_rules order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rule
	for rows.Next() {
		var r Rule
		var a, c sql.NullString
		var created string
		if err := rows.Scan(&r.ID, &a, &c, &r.Tool, &r.Effect, &created); err != nil {
			return nil, err
		}
		r.Agent, r.Connector = nullable(a), nullable(c)
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MatchingRules returns every rule whose scope matches a call from this
// agent and connector, for this tool -- the candidates the precedence in
// Decide picks among. A rule matches when its agent is null or equal, its
// connector is null or equal, and its tool is either this exact tool or
// RuleToolDefault.
//
// agent and connector are the session's derived values, exactly as
// journal.Rule's doc comment and docs/decisions/0002 use them: "" means no
// enrolment matched (for agent) or is simply not a stored rule value (for
// connector), and matches only the rules that name none.
//
// This is a read on the decision path, once per tools/call, against the one
// table a decision may read. docs/benchmarks.md carries its cost. The result
// is small by construction: the unique index on (agent, connector, tool)
// means at most one rule per (named-or-not agent, named-or-not connector,
// exact-or-default tool) combination, so there are at most eight candidates
// for any one call.
func (j *Journal) MatchingRules(agent, connector, tool string) ([]Rule, error) {
	rows, err := j.db.Query(
		`select id, agent, connector, tool, effect, created_at from nim_rules
		  where (tool = ? or tool = ?)
		    and (agent is null or agent = ?)
		    and (connector is null or connector = ?)
		  order by id`,
		tool, RuleToolDefault, agent, connector)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rule
	for rows.Next() {
		var r Rule
		var a, c sql.NullString
		var created string
		if err := rows.Scan(&r.ID, &a, &c, &r.Tool, &r.Effect, &created); err != nil {
			return nil, err
		}
		r.Agent, r.Connector = nullable(a), nullable(c)
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RuleFor is the decision path's one call: the rule that governs a session
// derived as agent, on connector, calling tool, and whether any rule applies
// at all. found is false exactly when candidates is empty, which is the M4
// baseline restated for this model -- no matching rule is an allow, computed
// by the caller from an empty Rule rather than encoded as a sentinel here.
func (j *Journal) RuleFor(agent, connector, tool string) (Rule, bool, error) {
	candidates, err := j.MatchingRules(agent, connector, tool)
	if err != nil {
		return Rule{}, false, err
	}
	r, found := Decide(candidates)
	return r, found, nil
}

// Decide picks the rule that governs a call among candidates whose scope
// already matches it -- see MatchingRules -- by the precedence
// docs/decisions/0003-allow-rules-and-precedence.md states, extended for a
// third effect in this document's 2026-09-15 addendum:
//
//  1. Higher specificity wins. Specificity is 2 for an exact tool match, 0
//     for a default ('*'), plus 1 if the rule names an agent, plus 1 if it
//     names a connector -- so an exact-tool rule always beats a default
//     naming the same agent and connector, and a rule naming both an agent
//     and a connector always beats one naming only one of them.
//  2. At equal specificity, deny beats ask beats allow -- the safer rule
//     wins, and holding a call for a human is safer than letting it through
//     unconditionally but not as safe as refusing it outright.
//  3. candidates being empty is not decided here: it returns found=false, and
//     the M4 baseline -- allow -- is the caller's to apply, exactly as "no
//     rule mentions this tool" always has been.
//
// A tie that survives both -- two rules of equal specificity and effect,
// naming different things, both matching this call -- cannot change the
// decision (their effects agree), so it is broken only for determinism: the
// lower id wins, which is the rule that was added first.
//
// This is deliberately a pure function of the candidate list, with no
// database in it, so the precedence itself has a table-driven test that
// needs no SQLite.
func Decide(candidates []Rule) (Rule, bool) {
	var winner Rule
	found := false
	for _, r := range candidates {
		if !found || outranks(r, winner) {
			winner, found = r, true
		}
	}
	return winner, found
}

// outranks reports whether challenger displaces winner under Decide's
// precedence.
func outranks(challenger, winner Rule) bool {
	cs, ws := specificity(challenger), specificity(winner)
	if cs != ws {
		return cs > ws
	}
	if challenger.Effect != winner.Effect {
		return effectRank(challenger.Effect) > effectRank(winner.Effect)
	}
	return challenger.ID < winner.ID
}

// effectRank orders the three effects for the tie-break at equal specificity:
// deny first, then ask, then allow -- the safer of two matching rules wins,
// and holding a call for a human is safer than an unconditional allow but
// not as safe as refusing it outright. This is Decide's one implementation
// of that ordering; nothing else may recompute it.
func effectRank(effect string) int {
	switch effect {
	case DecisionDeny:
		return 2
	case DecisionAsk:
		return 1
	default: // DecisionAllow
		return 0
	}
}

// specificity is defined in Decide's doc comment; this is its one
// implementation; nothing else may recompute it.
func specificity(r Rule) int {
	s := 0
	if r.Tool != RuleToolDefault {
		s += 2
	}
	if r.Agent != nil {
		s++
	}
	if r.Connector != nil {
		s++
	}
	return s
}
