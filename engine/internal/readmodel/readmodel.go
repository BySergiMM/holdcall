// Package readmodel projects the journal into the shape things that read it
// want, and holds no state of its own.
//
// Nim is the source of truth. Everything here is a function of the journal at a
// given cursor: nothing is cached, nothing is accumulated, and there is no
// second copy of the record to drift from the first. A reader that disappears
// changes nothing, and one that comes back reads the same answers.
//
// The projections are deliberately separate from journal.Entry. That struct is
// how an entry is stored and hashed; changing it is a schema decision. What a
// console renders should not be able to force such a decision, nor depend on
// one, so it gets its own shape.
package readmodel

import "github.com/BySergiMM/nim/engine/internal/journal"

// The interfaces below are named after projections, not after storage, and each
// one is what a single projection needs and no more.
//
// That is the point rather than tidiness. A projection given only EventSource
// cannot reach verification; one given only SnapshotSource cannot read entries.
// More usefully, a single wide interface would grow every time the journal
// grows, which is precisely how this package would turn into a mirror of it: a
// new capability has to be justified against a named projection before anything
// here can use it.
//
// Every method reads. Nothing given one of these can write, whatever it tries.

// EventSource backs the entry stream.
type EventSource interface {
	EntriesSince(since int64, limit int) ([]journal.Entry, error)
}

// SnapshotSource backs the state of the record.
type SnapshotSource interface {
	Head() (int64, string, error)
	CountCalls() (int, error)
	Loss() (journal.LossReport, error)
	Anomalies() (map[string]int, error)
	Verify(expectHead string) (journal.VerifyReport, error)
	SeedKnown() bool
}

// SessionSource backs the session list and one session's detail.
type SessionSource interface {
	Sessions(limit int) ([]journal.SessionRow, error)
	Session(id string) (journal.SessionRow, bool, error)
	SessionEntries(id string, limit int) ([]journal.Entry, error)
}

// PolicySource backs the rules, budgets, agents and connectors listings:
// what may happen, as opposed to the journal's record of what did.
// nim_rules, nim_budgets, nim_agents and nim_connectors sit outside the
// chain (see journal.go), so this reads them the same way ListRules,
// ListBudgets, ListAgents and ListConnectors already do, and adds nothing
// new to what the journal handle can answer.
type PolicySource interface {
	ListRules() ([]journal.Rule, error)
	ListBudgets() ([]journal.Budget, error)
	ListAgents() ([]journal.Agent, error)
	ListConnectors() ([]journal.Connector, error)
	// The two reads a decision makes, so Explain can show what the daemon
	// would do with a call without a second copy of the precedence: the
	// candidates come from here and journal.Decide picks among them.
	MatchingRules(agent, connector, tool string) ([]journal.Rule, error)
	MatchingBudgets(agent, connector, tool string) ([]journal.Budget, error)
}

// Source is all four, for wiring: a caller holding a journal passes it once
// and each projection takes only the part it needs.
type Source interface {
	EventSource
	SnapshotSource
	SessionSource
	PolicySource
}

// Event is one journal entry as a reader sees it.
//
// chain_seq is the cursor and the order. occurred_at is shown because it is
// useful, but it is a value the entry carries and a value can be wrong; the
// journal's own order is the one to trust.
//
// Absent fields are omitted rather than sent as null: which fields an entry
// carries depends on its kind, and their absence is normal rather than missing
// information.
type Event struct {
	ChainSeq      int64  `json:"chain_seq"`
	SchemaVersion int64  `json:"schema_version"`
	Kind          string `json:"kind"`
	SessionID     string `json:"session_id"`
	OccurredAt    string `json:"occurred_at"`

	Seq          *int64  `json:"seq,omitempty"`
	Connector    *string `json:"connector,omitempty"`
	Tool         *string `json:"tool,omitempty"`
	ParamsDigest *string `json:"params_digest,omitempty"`

	// Decision is what the daemon decided about this call: allow or deny. In
	// journals written before enforcement it is "observed", which recorded that
	// nothing had been decided at all.
	Decision *string `json:"decision,omitempty"`

	OK         *bool   `json:"ok,omitempty"`
	DurationMS *int64  `json:"duration_ms,omitempty"`
	Anomaly    *string `json:"anomaly,omitempty"`

	MachineID       *string `json:"machine_id,omitempty"`
	Client          *string `json:"client,omitempty"`
	ProtocolVersion *string `json:"protocol_version,omitempty"`

	// Agent is the enrolled program the daemon derived a session from, on
	// session.start, and the scope of a rule on rule.add and rule.remove. It
	// is hashed like every other field, so a reader recomputing an entry's
	// hash from this projection needs it; it was missing for a while, which
	// made `nim log --json` an incomplete account of a v2 entry.
	//
	// On agent.add and agent.remove it is instead the enrolment's name.
	Agent *string `json:"agent,omitempty"`

	// ExecPath and ExecID are set on agent.add and agent.remove: the path the
	// operator enrolled and the resolved identity as "<dev>:<ino>" decimal.
	// On agent.add they describe the enrolment made; on agent.remove, the one
	// removed. Both are hashed like every other field, so a reader
	// recomputing a v3 entry's hash needs them.
	ExecPath *string `json:"exec_path,omitempty"`
	ExecID   *string `json:"exec_id,omitempty"`

	// BudgetCalls is set on budget.add and budget.remove: the cap the
	// budget carries, being set or having been removed. It is hashed like
	// every other field, so a reader recomputing a v4 entry's hash needs it
	// -- the same reasoning ExecPath and ExecID were added under for v3.
	BudgetCalls *int64 `json:"budget_calls,omitempty"`

	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
}

// EventFrom projects one entry. It copies; nothing here aliases the journal's
// own memory, so a reader cannot reach back through a projection.
func EventFrom(e journal.Entry) Event {
	return Event{
		ChainSeq:        e.ChainSeq,
		SchemaVersion:   e.SchemaVersion,
		Kind:            e.Kind,
		SessionID:       e.SessionID,
		OccurredAt:      e.OccurredAt,
		Seq:             copyInt(e.Seq),
		Connector:       copyString(e.Connector),
		Tool:            copyString(e.Tool),
		ParamsDigest:    copyString(e.ParamsDigest),
		Decision:        copyString(e.Decision),
		OK:              copyBool(e.OK),
		DurationMS:      copyInt(e.DurationMS),
		Anomaly:         copyString(e.Anomaly),
		MachineID:       copyString(e.MachineID),
		Client:          copyString(e.Client),
		ProtocolVersion: copyString(e.ProtocolVersion),
		Agent:           copyString(e.Agent),
		ExecPath:        copyString(e.ExecPath),
		ExecID:          copyString(e.ExecID),
		BudgetCalls:     copyInt(e.BudgetCalls),
		PrevHash:        e.PrevHash,
		Hash:            e.Hash,
	}
}

// Page is one read of the stream.
type Page struct {
	Events []Event `json:"events"`
	// Cursor is the chain_seq to ask from next time. It is the last event's
	// position, or the cursor that was passed in when nothing came back, so a
	// caller can always feed it straight back without special cases.
	Cursor int64 `json:"cursor"`
}

// Stream reads the entries after a cursor and projects them, oldest first.
//
// The caller drives: it decides how often to ask and how much to take. A full
// page means there is probably more, so ask again before waiting.
func Stream(src EventSource, since int64, limit int) (Page, error) {
	entries, err := src.EntriesSince(since, limit)
	if err != nil {
		return Page{Cursor: since}, err
	}

	page := Page{Cursor: since, Events: make([]Event, 0, len(entries))}
	for _, e := range entries {
		page.Events = append(page.Events, EventFrom(e))
		page.Cursor = e.ChainSeq
	}
	return page, nil
}

func copyString(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

func copyInt(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func copyBool(v *bool) *bool {
	if v == nil {
		return nil
	}
	b := *v
	return &b
}
