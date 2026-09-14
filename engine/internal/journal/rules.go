package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Rule is one denial. It refuses Tool for every session in its scope: those
// of one enrolled Agent or of every session when Agent is nil, on one
// Connector or on every connector when Connector is nil.
//
// Rules only deny. The absence of a rule is an allowance, as it was when the
// list lived in config.toml, so a fresh install with no rules behaves exactly
// as before -- and enrolling an agent can only take capabilities away from it,
// never add any. docs/decisions/0002-what-an-unknown-agent-may-do.md is the
// argument for why that is the right shape until there is a policy language.
type Rule struct {
	ID        int64
	Agent     *string
	Connector *string
	Tool      string
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
	return fmt.Sprintf("deny %s for %s on %s", r.Tool, scope, where)
}

// The two ways a policy change can be a change to nothing.
var (
	ErrRuleExists = errors.New("that rule already exists")
	ErrNoSuchRule = errors.New("no such rule")
)

// AddRule stores a denial and the rule.add entry recording it, in one
// transaction: the chain never says a rule exists that does not, and a rule
// never exists that the chain does not know about.
//
// A rule that already exists is refused without writing anything. It is not
// treated as idempotent, because an operator who adds the same rule twice
// usually meant a different scope, and a silent success hides that.
func (j *Journal) AddRule(r Rule) (Rule, error) {
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
		`insert into nim_rules (agent, connector, tool, effect, created_at) values (?, ?, ?, 'deny', ?)`,
		r.Agent, r.Connector, r.Tool, at)
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

// RemoveRule deletes the rule with exactly this scope and tool, and records
// the rule.remove entry in the same transaction. Removing a rule that is not
// there is ErrNoSuchRule, not a success: nothing changed, so nothing is
// written, and the operator learns that the scope they typed matched nothing.
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
		`select id, agent, connector, tool, created_at from nim_rules
		  where ifnull(agent, '') = ifnull(?, '') and ifnull(connector, '') = ifnull(?, '') and tool = ?`,
		agent, connector, tool).Scan(&r.ID, &a, &c, &r.Tool, &created)
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
// from null; a rule belongs to no session and says so.
func ruleEntry(kind string, r Rule, at string) Entry {
	tool, effect := r.Tool, DecisionDeny
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
		`select id, agent, connector, tool, created_at from nim_rules order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rule
	for rows.Next() {
		var r Rule
		var a, c sql.NullString
		var created string
		if err := rows.Scan(&r.ID, &a, &c, &r.Tool, &created); err != nil {
			return nil, err
		}
		r.Agent, r.Connector = nullable(a), nullable(c)
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RuleDenying reports the rule, if any, that refuses tool for a session whose
// derived agent and connector are the ones given.
//
// A rule with no agent applies to every session; one naming an agent applies
// to sessions derived as that agent and to no other. The same for connector.
// An agent of "" -- no enrolment matched -- therefore meets only the rules
// that name no agent, because ” is never stored as a rule's agent. That is
// the whole of what an unknown agent may do, and it is stated in
// docs/decisions/0002-what-an-unknown-agent-may-do.md rather than left to fall
// out of a query.
//
// This is a read on the decision path, once per tools/call, against the one
// table a decision may read. docs/benchmarks.md carries its cost.
func (j *Journal) RuleDenying(agent, connector, tool string) (Rule, bool, error) {
	var r Rule
	var a, c sql.NullString
	var created string
	err := j.db.QueryRow(
		`select id, agent, connector, tool, created_at from nim_rules
		  where tool = ?
		    and (agent is null or agent = ?)
		    and (connector is null or connector = ?)
		  order by (agent is not null) + (connector is not null) desc, id
		  limit 1`,
		tool, agent, connector).Scan(&r.ID, &a, &c, &r.Tool, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, false, nil
	}
	if err != nil {
		return Rule{}, false, err
	}
	r.Agent, r.Connector = nullable(a), nullable(c)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return r, true, nil
}
