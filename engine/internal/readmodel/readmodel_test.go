package readmodel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BySergiMM/nim/engine/internal/journal"
)

func sp(s string) *string { return &s }
func ip(v int64) *int64   { return &v }
func bp(v bool) *bool     { return &v }

// fake is a Source with no database behind it. A projection is a function of
// its input, so testing one should not need a file.
type fake struct {
	entries   []journal.Entry
	sessions  []journal.SessionRow
	head      int64
	headHash  string
	callCount int
	loss      journal.LossReport
	anomalies map[string]int
	verify    journal.VerifyReport
	seedKnown bool
	budgets   []journal.Budget
	err       error
	calls     []struct {
		since int64
		limit int
	}

	rules      []journal.Rule
	agents     []journal.Agent
	connectors []journal.Connector
}

func (f *fake) ListRules() ([]journal.Rule, error)           { return f.rules, f.err }
func (f *fake) ListBudgets() ([]journal.Budget, error)       { return f.budgets, f.err }
func (f *fake) ListAgents() ([]journal.Agent, error)         { return f.agents, f.err }
func (f *fake) ListConnectors() ([]journal.Connector, error) { return f.connectors, f.err }

func (f *fake) Sessions(limit int) ([]journal.SessionRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	if limit > 0 && len(f.sessions) > limit {
		return f.sessions[:limit], nil
	}
	return f.sessions, nil
}

func (f *fake) Session(id string) (journal.SessionRow, bool, error) {
	if f.err != nil {
		return journal.SessionRow{}, false, f.err
	}
	for _, r := range f.sessions {
		if r.ID == id {
			return r, true, nil
		}
	}
	return journal.SessionRow{}, false, nil
}

func (f *fake) SessionEntries(id string, limit int) ([]journal.Entry, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []journal.Entry
	for _, e := range f.entries {
		if e.SessionID == id {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fake) Head() (int64, string, error) { return f.head, f.headHash, f.err }
func (f *fake) CountCalls() (int, error)     { return f.callCount, f.err }
func (f *fake) Loss() (journal.LossReport, error) {
	return f.loss, f.err
}
func (f *fake) Anomalies() (map[string]int, error) { return f.anomalies, f.err }
func (f *fake) Verify(string) (journal.VerifyReport, error) {
	return f.verify, f.err
}
func (f *fake) SeedKnown() bool { return f.seedKnown }

func (f *fake) EntriesSince(since int64, limit int) ([]journal.Entry, error) {
	f.calls = append(f.calls, struct {
		since int64
		limit int
	}{since, limit})
	if f.err != nil {
		return nil, f.err
	}
	var out []journal.Entry
	for _, e := range f.entries {
		if e.ChainSeq > since {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func entry(seq int64, kind string) journal.Entry {
	return journal.Entry{
		ChainSeq: seq, SchemaVersion: journal.SchemaVersion1, Kind: kind,
		SessionID: "session", OccurredAt: "2026-08-07T10:00:00Z",
		PrevHash: "aa", Hash: "bb",
	}
}

func TestEventCarriesEveryFieldOfAnEntry(t *testing.T) {
	e := journal.Entry{
		ChainSeq: 7, SchemaVersion: 1, Kind: journal.KindCallRequest,
		SessionID: "s1", Seq: ip(3), Connector: sp("github"), Tool: sp("create_issue"),
		ParamsDigest: sp("digest"), Decision: sp(journal.DecisionObserved),
		OK: bp(true), DurationMS: ip(42), Anomaly: sp("batch"),
		OccurredAt: "2026-08-07T10:00:00Z", MachineID: sp("m"), Client: sp("c"),
		ProtocolVersion: sp("2025-06-18"), Agent: sp("claude-code"), PrevHash: "prev", Hash: "hash",
	}
	got := EventFrom(e)

	if got.ChainSeq != 7 || got.Kind != journal.KindCallRequest || got.SessionID != "s1" {
		t.Errorf("core fields lost: %+v", got)
	}
	if got.PrevHash != "prev" || got.Hash != "hash" {
		t.Error("the chain fields must reach a reader: an event detail view shows them")
	}
	for name, ok := range map[string]bool{
		"seq": got.Seq != nil, "connector": got.Connector != nil, "tool": got.Tool != nil,
		"params_digest": got.ParamsDigest != nil, "decision": got.Decision != nil,
		"ok": got.OK != nil, "duration_ms": got.DurationMS != nil, "anomaly": got.Anomaly != nil,
		"machine_id": got.MachineID != nil, "client": got.Client != nil,
		"protocol_version": got.ProtocolVersion != nil, "agent": got.Agent != nil,
	} {
		if !ok {
			t.Errorf("field %s was dropped by the projection", name)
		}
	}
}

// A projection must copy. If it handed back the journal's own pointers, a
// reader could reach through a view and change what it was looking at.
func TestProjectionCopiesRatherThanAliases(t *testing.T) {
	tool := "original"
	e := entry(1, journal.KindCallRequest)
	e.Tool = &tool

	got := EventFrom(e)
	*got.Tool = "changed"

	if tool != "original" {
		t.Fatal("the projection aliased the entry: writing through the event changed the source")
	}
}

// Absent fields are omitted, not sent as null: which fields an entry carries
// depends on its kind, so their absence is normal rather than missing data.
func TestAbsentFieldsAreOmittedFromJSON(t *testing.T) {
	out, err := json.Marshal(EventFrom(entry(1, journal.KindSessionEnd)))
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(out)

	for _, field := range []string{"seq", "connector", "tool", "params_digest",
		"decision", "ok", "duration_ms", "anomaly", "machine_id", "client", "protocol_version"} {
		if strings.Contains(encoded, `"`+field+`"`) {
			t.Errorf("%s should be omitted when absent: %s", field, encoded)
		}
	}
	for _, field := range []string{"chain_seq", "kind", "session_id", "occurred_at", "prev_hash", "hash"} {
		if !strings.Contains(encoded, `"`+field+`"`) {
			t.Errorf("%s must always be present: %s", field, encoded)
		}
	}
}

// false is a value, not an absence: an outcome that failed must say so.
func TestFalseIsNotOmitted(t *testing.T) {
	e := entry(1, journal.KindCallOutcome)
	e.OK = bp(false)

	out, err := json.Marshal(EventFrom(e))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"ok":false`) {
		t.Fatalf("a failed outcome lost its result: %s", out)
	}
}

func TestStreamReturnsACursorThatCanBeFedBack(t *testing.T) {
	src := &fake{entries: []journal.Entry{
		entry(1, journal.KindSessionStart),
		entry(2, journal.KindCallRequest),
		entry(3, journal.KindCallOutcome),
	}}

	page, err := Stream(src, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(page.Events))
	}
	if page.Cursor != 2 {
		t.Fatalf("cursor is %d, want the last event's position", page.Cursor)
	}

	next, err := Stream(src, page.Cursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Events) != 1 || next.Events[0].ChainSeq != 3 {
		t.Fatalf("resuming from the cursor did not continue cleanly: %+v", next.Events)
	}
}

// A caller loops on the cursor, so an empty page must give back something it
// can pass straight in again rather than a zero that would replay everything.
func TestAnEmptyPageKeepsTheCursorWhereItWas(t *testing.T) {
	src := &fake{entries: []journal.Entry{entry(1, journal.KindSessionStart)}}

	page, err := Stream(src, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("expected nothing after the head, got %d", len(page.Events))
	}
	if page.Cursor != 1 {
		t.Fatalf("cursor moved to %d on an empty page; a loop would rewind", page.Cursor)
	}
	if page.Events == nil {
		t.Error("Events should be an empty slice, not nil: it is encoded as JSON")
	}
}

func TestStreamReportsErrorsWithoutMovingTheCursor(t *testing.T) {
	src := &fake{err: errors.New("journal unavailable")}

	page, err := Stream(src, 9, 10)
	if err == nil {
		t.Fatal("Stream hid a read failure")
	}
	if page.Cursor != 9 {
		t.Errorf("cursor moved to %d on failure; the next read would skip entries", page.Cursor)
	}
}

// The Source interface is what keeps a projection from reaching anything else.
// If it ever grows a write method this test should start looking wrong.
func TestSourceIsReadOnlyByShape(t *testing.T) {
	var _ Source = (*fake)(nil)
	var _ Source = (*journal.Journal)(nil)
}
