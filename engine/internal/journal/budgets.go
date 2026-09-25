package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// BudgetToolAll is the tool value that means "every tool" for a budget's
// scope -- the same idea RuleToolDefault expresses for a rule, and the same
// literal ("*"), but a different table answering a different question. A
// rule's "*" says which effect applies when nothing more specific does; a
// budget's says which calls count against one cap. It exists only through
// `holdcall policy budget <n> --all-tools`, and is otherwise an ordinary tool
// name to MatchingBudgets, for the same reason RuleToolDefault is to
// MatchingRules -- see docs/decisions/0003's note on "*" for the argument,
// which applies here unchanged.
const BudgetToolAll = "*"

// Budget is a cap on the number of ALLOWED calls one session may make,
// scoped like a rule: Agent (nil: every agent, enrolled or not -- see
// MatchingBudgets), Connector (nil: every connector), and Tool (an exact
// name, or BudgetToolAll for every tool). It is consulted only once the
// rules have already allowed a call -- docs/decisions/0004-budgets.md -- so
// a budget can only ever narrow what a session may do, never widen it.
//
// "Per session" means one relay run: session.start to session.end. A client
// that restarts its relay starts a new session with a fresh count, because
// the count CountAllowedCalls returns is read straight from that session's
// own call.request entries and nothing carries over.
type Budget struct {
	ID        int64
	Agent     *string
	Connector *string
	Tool      string
	Calls     int64
	CreatedAt time.Time
}

// String names the budget the way Rule.String names a rule.
func (b Budget) String() string {
	tool := "every tool"
	if b.Tool != BudgetToolAll {
		tool = "tool " + b.Tool
	}
	scope := "every agent"
	if b.Agent != nil {
		scope = "agent " + *b.Agent
	}
	where := "every connector"
	if b.Connector != nil {
		where = "connector " + *b.Connector
	}
	return fmt.Sprintf("%d calls for %s for %s on %s", b.Calls, tool, scope, where)
}

// The two ways a budget change can be a change to nothing -- the same shape
// AddRule and RemoveRule refuse for a rule, and for the same reason: a scope
// holds at most one budget, so adding a second one for the same scope and
// removing one that is not there are both operator mistakes worth naming
// rather than absorbing silently.
var (
	ErrBudgetExists = errors.New("that budget already exists")
	ErrNoSuchBudget = errors.New("no such budget")
)

// AddBudget stores a budget and the budget.add entry recording it, in one
// transaction -- the same pattern AddRule uses and for the same reason: the
// chain never says a budget exists that does not, and no budget exists that
// the chain does not know about.
//
// A budget that already exists for this scope is refused without writing
// anything, exactly as AddRule refuses a duplicate rule: an operator who
// wants a different cap removes the old budget first, which is also what
// keeps a scope from ever holding two numbers that disagree about it.
func (j *Journal) AddBudget(b Budget) (Budget, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if b.Calls < 0 {
		return Budget{}, fmt.Errorf("a budget's calls must not be negative, got %d", b.Calls)
	}
	if j.readOnly {
		return Budget{}, errReadOnly
	}
	tx, err := j.db.Begin()
	if err != nil {
		return Budget{}, err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(
		`select count(*) from nim_budgets
		  where ifnull(agent, '') = ifnull(?, '') and ifnull(connector, '') = ifnull(?, '') and tool = ?`,
		b.Agent, b.Connector, b.Tool).Scan(&n); err != nil {
		return Budget{}, err
	}
	if n > 0 {
		return Budget{}, ErrBudgetExists
	}

	b.CreatedAt = time.Now().UTC()
	at := b.CreatedAt.Format(time.RFC3339Nano)
	res, err := tx.Exec(
		`insert into nim_budgets (agent, connector, tool, calls, created_at) values (?, ?, ?, ?, ?)`,
		b.Agent, b.Connector, b.Tool, b.Calls, at)
	if err != nil {
		return Budget{}, err
	}
	if b.ID, err = res.LastInsertId(); err != nil {
		return Budget{}, err
	}
	if err := j.appendTx(tx, budgetEntry(KindBudgetAdd, b, at)); err != nil {
		return Budget{}, err
	}
	if err := tx.Commit(); err != nil {
		return Budget{}, err
	}
	return b, nil
}

// RemoveBudget deletes the budget with exactly this scope and tool -- and
// records the budget.remove entry in the same transaction -- mirroring
// RemoveRule. Removing a budget that is not there is ErrNoSuchBudget, not a
// success: nothing changed, so nothing is written, and the operator learns
// that the scope they typed matched nothing.
func (j *Journal) RemoveBudget(agent, connector *string, tool string) (Budget, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.readOnly {
		return Budget{}, errReadOnly
	}
	tx, err := j.db.Begin()
	if err != nil {
		return Budget{}, err
	}
	defer tx.Rollback()

	var b Budget
	var a, c sql.NullString
	var created string
	err = tx.QueryRow(
		`select id, agent, connector, tool, calls, created_at from nim_budgets
		  where ifnull(agent, '') = ifnull(?, '') and ifnull(connector, '') = ifnull(?, '') and tool = ?`,
		agent, connector, tool).Scan(&b.ID, &a, &c, &b.Tool, &b.Calls, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, ErrNoSuchBudget
	}
	if err != nil {
		return Budget{}, err
	}
	b.Agent, b.Connector = nullable(a), nullable(c)
	b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)

	if _, err := tx.Exec(`delete from nim_budgets where id = ?`, b.ID); err != nil {
		return Budget{}, err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := j.appendTx(tx, budgetEntry(KindBudgetRemove, b, at)); err != nil {
		return Budget{}, err
	}
	if err := tx.Commit(); err != nil {
		return Budget{}, err
	}
	return b, nil
}

// budgetEntry is what a budget change looks like in the chain: the budget's
// scope in the columns a call would use, its cap in budget_calls, and no
// session -- session_id is the empty string, which the encoding keeps
// distinct from null, exactly as ruleEntry and agentEntry leave it, because
// a budget belongs to no one session.
func budgetEntry(kind string, b Budget, at string) Entry {
	tool, calls := b.Tool, b.Calls
	return Entry{
		Kind:        kind,
		SessionID:   "",
		Agent:       b.Agent,
		Connector:   b.Connector,
		Tool:        &tool,
		BudgetCalls: &calls,
		OccurredAt:  at,
	}
}

// ListBudgets returns every budget, oldest first.
func (j *Journal) ListBudgets() ([]Budget, error) {
	rows, err := j.db.Query(
		`select id, agent, connector, tool, calls, created_at from nim_budgets order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Budget
	for rows.Next() {
		var b Budget
		var a, c sql.NullString
		var created string
		if err := rows.Scan(&b.ID, &a, &c, &b.Tool, &b.Calls, &created); err != nil {
			return nil, err
		}
		b.Agent, b.Connector = nullable(a), nullable(c)
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, b)
	}
	return out, rows.Err()
}

// MatchingBudgets returns every budget whose scope matches a call from this
// agent and connector, for this tool -- the same match MatchingRules makes
// for rules, against a different table. A budget matches when its agent is
// null or equal, its connector is null or equal, and its tool is either this
// exact tool or BudgetToolAll.
//
// Unlike MatchingRules there is no precedence to apply among the results:
// every matching budget is an independent cap, and daemon.checkBudgets walks
// all of them rather than picking a winner.
func (j *Journal) MatchingBudgets(agent, connector, tool string) ([]Budget, error) {
	rows, err := j.db.Query(
		`select id, agent, connector, tool, calls, created_at from nim_budgets
		  where (tool = ? or tool = ?)
		    and (agent is null or agent = ?)
		    and (connector is null or connector = ?)
		  order by id`,
		tool, BudgetToolAll, agent, connector)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Budget
	for rows.Next() {
		var b Budget
		var a, c sql.NullString
		var created string
		if err := rows.Scan(&b.ID, &a, &c, &b.Tool, &b.Calls, &created); err != nil {
			return nil, err
		}
		b.Agent, b.Connector = nullable(a), nullable(c)
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, b)
	}
	return out, rows.Err()
}

// CountAllowedCalls is the count a budget check weighs against Budget.Calls:
// how many call.request entries this session already has with a decision of
// allow or approved. tool restricts that to one tool's calls; "" counts
// every tool, for a budget scoped to BudgetToolAll.
//
// Decremented at authorization time, not execution time: this counts
// decisions the daemon already wrote, not outcomes -- a call the relay gave
// up waiting on still counted the moment it was allowed, exactly as
// docs/journal-format.md's "an allow is not evidence the call was made"
// applies here too. A denied call is never allow or approved, so a
// session's own refusals -- by a rule or by this same budget -- never count
// against it.
//
// The cost: nim_journal_entry_idx, the unique index on (session_id, seq,
// kind), lets SQLite narrow to this session's rows by its leading column
// before it has to look at kind or decision at all, so the read costs is
// proportional to the calls one session has made, not to the size of the
// journal -- an indexed read on (session_id, seq, kind), as the design
// calls for.
func (j *Journal) CountAllowedCalls(sessionID, tool string) (int, error) {
	var n int
	err := j.db.QueryRow(
		`select count(*) from nim_journal
		  where session_id = ? and kind = ? and decision in (?, ?)
		    and (? = '' or tool = ?)`,
		sessionID, KindCallRequest, DecisionAllow, DecisionApproved, tool, tool,
	).Scan(&n)
	return n, err
}
