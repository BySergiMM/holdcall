package readmodel

import "github.com/BySergiMM/holdcall/engine/internal/journal"

// ChainState is what can be said about the chain, and no more.
//
// Four states rather than a boolean, because "is it fine?" has more than two
// honest answers: an empty journal has not been checked, and one whose seed is
// missing has been checked except at the start.
type ChainState string

const (
	// ChainEmpty: nothing recorded, so nothing was checked. This is not the
	// same as a journal that passed.
	ChainEmpty ChainState = "empty"
	// ChainSelfConsistent: every entry hashes to what it stores and links to
	// the one before it. It does not mean nothing was removed.
	ChainSelfConsistent ChainState = "self_consistent"
	// ChainPartial: checked from the second entry on. The install identifier
	// that seeds the chain could not be read, so the first entry has nothing to
	// be compared against. Missing verification material, not a fault.
	ChainPartial ChainState = "partial"
	// ChainBroken: an entry does not hash to what it stores, does not link, or
	// is missing.
	ChainBroken ChainState = "broken"
)

// Journal is the state of the record itself.
type JournalState struct {
	Entries       int64      `json:"entries"`
	Head          string     `json:"head"`
	SchemaVersion int64      `json:"schema_version"`
	Chain         ChainState `json:"chain"`
	// Problem is set only when Chain is broken, naming the entry it was found
	// at.
	Problem string `json:"problem,omitempty"`

	// ExpectedHeadAt is where a head passed to Check was found in the chain,
	// or 0 if it was not there. Only meaningful when one was given.
	//
	// It is on this boundary rather than derived by each caller because it is
	// the difference between two readings that must never be confused: a
	// journal that grew past the head you recorded (which contains it, and is
	// therefore intact up to it) and one that no longer contains it at all
	// (which was rewritten). Deciding that in one place is the whole reason
	// Check exists.
	ExpectedHeadAt int64 `json:"expected_head_at,omitempty"`

	// VerificationMaterial reports whether the seed needed to check the first
	// entry was available. False is a statement about what could be read, never
	// about whether the journal is sound.
	VerificationMaterial bool `json:"verification_material"`
}

// Gaps is what the journal can work out about its own incompleteness.
//
// Every field counts something with evidence behind it. None of them is a total
// of what was lost: reporting is asynchronous and one-way, so an event the
// daemon accepted and failed to write leaves nothing to count. Read these as
// "detected", never as "all".
type Gaps struct {
	UnfinishedSessions int `json:"unfinished_sessions"`
	SessionsWithGaps   int `json:"sessions_with_gaps"`
	MissingCallEntries int `json:"missing_call_entries"`
	// CallsWithoutSession are decided calls whose session.start never reached
	// the journal. They are shown without a connector or agent rather than
	// hidden, and counted here so the totals and the listing agree.
	CallsWithoutSession int            `json:"calls_without_session"`
	Anomalies           map[string]int `json:"anomalies"`
	AnomaliesTotal      int            `json:"anomalies_total"`
}

// AnomalyDisposition says what the relay did with a message of this anomaly
// kind: refused it, or relayed it and merely counted it. One place, so `holdcall
// status` and the console cannot describe the same number differently -- they
// did, for a while, both saying "relayed without inspection" about frames the
// relay had refused since M2.
func AnomalyDisposition(kind string) string {
	switch kind {
	case "malformed_json", "framing", "duplicate_key", "unreadable_call":
		return "refused"
	case "batch":
		return "refused if it carried a tools/call, otherwise relayed"
	case "duplicate_id":
		return "relayed; the second answer cannot be matched"
	default:
		return "unknown kind"
	}
}

// Snapshot is everything derivable from the journal in one read.
//
// Deliberately absent: whether the daemon is running. That is not in the
// journal, and a projection of the record should not pretend to know things
// about the world. Whoever wants it asks the daemon.
type Snapshot struct {
	Journal JournalState `json:"journal"`
	// CallsRecorded is how many calls reached the journal. It is not how many
	// were made, and it is not how many were executed.
	CallsRecorded int  `json:"calls_recorded"`
	Gaps          Gaps `json:"gaps"`
}

// Check walks the chain and says which of the four things happened.
//
// This is the one place the verifier's report becomes a state, so that
// everything showing it -- the console, `holdcall status`, `holdcall verify` -- draws the
// same conclusion from the same evidence. Three callers reaching their own
// verdict from the same report is how they end up disagreeing.
//
// expectHead, when given, is a head recorded earlier. A chain that is
// internally consistent but no longer ends where it did has had entries removed
// and the chain recomputed, which is a break rather than a passing check.
func Check(src SnapshotSource, expectHead string) (JournalState, error) {
	var state JournalState

	length, head, err := src.Head()
	if err != nil {
		return state, err
	}
	state.Entries = length
	state.Head = head
	// The version this build writes, not a constant. It said 1 while the daemon
	// was already writing 2, which is the sort of stale claim a status command
	// exists to not make.
	state.SchemaVersion = journal.CurrentSchemaVersion
	state.VerificationMaterial = src.SeedKnown()

	report, err := src.Verify(expectHead)
	if err != nil {
		return state, err
	}
	state.ExpectedHeadAt = report.ExpectedHeadAt
	switch {
	case report.Empty && report.Problem == "":
		state.Chain = ChainEmpty
	case report.Problem != "":
		state.Chain = ChainBroken
		state.Problem = report.Problem
	case report.Partial:
		state.Chain = ChainPartial
	default:
		state.Chain = ChainSelfConsistent
	}
	return state, nil
}

// Take reads the journal once and projects its state.
func Take(src SnapshotSource) (Snapshot, error) {
	var s Snapshot

	state, err := Check(src, "")
	if err != nil {
		return s, err
	}
	s.Journal = state

	if s.CallsRecorded, err = src.CountCalls(); err != nil {
		return s, err
	}

	loss, err := src.Loss()
	if err != nil {
		return s, err
	}
	s.Gaps.UnfinishedSessions = loss.UnfinishedSessions
	s.Gaps.SessionsWithGaps = loss.SessionsWithGaps
	s.Gaps.MissingCallEntries = loss.MissingCallEntries
	s.Gaps.CallsWithoutSession = loss.CallsWithoutSession

	anomalies, err := src.Anomalies()
	if err != nil {
		return s, err
	}
	s.Gaps.Anomalies = anomalies
	if s.Gaps.Anomalies == nil {
		s.Gaps.Anomalies = map[string]int{}
	}
	for _, n := range anomalies {
		s.Gaps.AnomaliesTotal += n
	}
	return s, nil
}

// CallState is what the journal says happened to one call.
//
// Derived, never stored. A call is two entries, and what it means depends on
// which of them are there: the same missing outcome is a call still running, or
// a call that was refused and never had one to miss. Deriving it in one place is
// the only way `holdcall log` and the console can agree on which.
type CallState string

const (
	// CallCompleted: decided, forwarded, and answered.
	CallCompleted CallState = "completed"
	// CallPending: decided, with no outcome recorded. That covers a call running
	// right now, one whose connector never answered, and one the relay denied
	// locally after the daemon had already recorded the allowance. The journal
	// cannot tell them apart, so this never means the call ran.
	CallPending CallState = "pending"
	// CallDenied: refused, so it never reached a connector and no outcome was
	// ever due. This is not a missing entry.
	CallDenied CallState = "denied"
	// CallInconsistent: a refused call with an outcome. Nothing should produce
	// this. It exists so that if anything does, it shows up instead of being
	// rounded into one of the others.
	CallInconsistent CallState = "inconsistent"
)

// CallStateOf reads the state from a decision and whether an outcome exists.
//
// "observed" is what was written before anything was decided; it reads like an
// allowance because that is what the relay did with those calls -- forwarded
// them. "approved" and "rejected" belong to human approval and are not written
// yet, but a reader that meets one should not fall over, so they map to the
// decision they amount to.
func CallStateOf(decision string, hasOutcome bool) CallState {
	switch decision {
	case journal.DecisionDeny, journal.DecisionRejected:
		if hasOutcome {
			return CallInconsistent
		}
		return CallDenied
	default: // observed, allow, approved
		if hasOutcome {
			return CallCompleted
		}
		return CallPending
	}
}

// SessionState is what the journal can say about a session's shape.
type SessionState string

const (
	// SessionEnded: a session.end was recorded.
	SessionEnded SessionState = "ended"
	// SessionUnfinished: no session.end. This covers a session running right
	// now and one whose daemon died without writing an end, and the journal
	// cannot tell them apart. It is never "active".
	SessionUnfinished SessionState = "unfinished"
)

// Session is one session as a reader sees it.
type Session struct {
	ChainSeq  int64        `json:"chain_seq"`
	ID        string       `json:"id"`
	State     SessionState `json:"state"`
	StartedAt string       `json:"started_at"`
	EndedAt   *string      `json:"ended_at,omitempty"`

	// Connector and Client are what the shim reported for itself. Self-asserted
	// labels, never facts Holdcall established.
	Connector *string `json:"connector,omitempty"`
	Client    *string `json:"client,omitempty"`
	// Agent is the opposite kind of thing from Client: the enrolled program
	// the daemon derived from the kernel, absent when no enrolment matched.
	Agent     *string `json:"agent,omitempty"`
	MachineID *string `json:"machine_id,omitempty"`

	CallsRecorded int `json:"calls_recorded"`
	// Denied is how many of those were refused. Without it, CallsRecorded and
	// Outcomes differ for two unrelated reasons -- calls still running, and calls
	// that never ran -- and a reader would have to guess which.
	Denied    int `json:"denied"`
	Outcomes  int `json:"outcomes"`
	Anomalies int `json:"anomalies"`
}

// SessionFrom projects one session row.
func SessionFrom(r journal.SessionRow) Session {
	s := Session{
		ChainSeq:      r.ChainSeq,
		ID:            r.ID,
		State:         SessionUnfinished,
		StartedAt:     r.StartedAt,
		EndedAt:       copyString(r.EndedAt),
		Connector:     copyString(r.Connector),
		Client:        copyString(r.Client),
		Agent:         copyString(r.Agent),
		MachineID:     copyString(r.MachineID),
		CallsRecorded: r.CallsRecorded,
		Denied:        r.Denied,
		Outcomes:      r.Outcomes,
		Anomalies:     r.Anomalies,
	}
	if r.EndedAt != nil {
		s.State = SessionEnded
	}
	return s
}

// Sessions lists sessions, newest first.
func Sessions(src SessionSource, limit int) ([]Session, error) {
	rows, err := src.Sessions(limit)
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, SessionFrom(r))
	}
	return out, nil
}

// SessionDetail is one session with everything recorded under it.
type SessionDetail struct {
	Session Session `json:"session"`
	Events  []Event `json:"events"`
}

// Detail reads one session and its entries.
func Detail(src SessionSource, id string, limit int) (SessionDetail, bool, error) {
	entries, err := src.SessionEntries(id, limit)
	if err != nil {
		return SessionDetail{}, false, err
	}
	if len(entries) == 0 {
		return SessionDetail{}, false, nil
	}

	// The summary comes from the same query the list uses, so a session reads
	// the same whichever view asked for it. Looked up by id rather than found
	// in the list: the list is a page of the newest MaxSessions, and a session
	// older than that used to come back with its events beside an empty
	// summary in a state no reader was written to handle.
	row, found, err := src.Session(id)
	if err != nil {
		return SessionDetail{}, false, err
	}
	if !found {
		// Entries with no session.start: the start was lost. Real entries, but
		// not a session the journal can summarise, and pretending otherwise is
		// how an empty state string reached a browser.
		return SessionDetail{}, false, nil
	}
	detail := SessionDetail{Session: SessionFrom(row), Events: make([]Event, 0, len(entries))}
	for _, e := range entries {
		detail.Events = append(detail.Events, EventFrom(e))
	}
	return detail, true, nil
}

// MaxSessions caps a session listing, for the same reason reads of the journal
// are capped: a read should be a page, not the whole file.
const MaxSessions = 200
