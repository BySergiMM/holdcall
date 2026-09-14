package readmodel

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// jsonFields walks a DTO and returns every json name it can put on the wire,
// including nested ones. Reflection rather than a search for forbidden words:
// what matters is which properties the model can express, not which strings
// happen to appear in it.
func jsonFields(t *testing.T, v any) []string {
	t.Helper()
	seen := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" {
				name = f.Name
			}
			if name == "-" {
				continue
			}
			seen[name] = true
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(v))

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The read model's surface is fixed by this list rather than by whatever
// happens to compile.
//
// It is an allow list on purpose. A deny list of words like "allowed" or
// "policy" would pass the moment someone spelled the same idea differently;
// this fails the moment the model can express anything it could not before,
// which forces the question to be asked out loud. Adding a field to a DTO
// without adding it here is the failure, not an inconvenience.
func TestReadModelSurfaceIsFrozen(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  []string
	}{
		// exec_path and exec_id may be present: they are set on agent.add and
		// agent.remove, the path the operator enrolled and the resolved
		// identity, exactly as the journal defines them at schema_version 3.
		// They are added here for the same reason "agent" was -- the journal
		// carries the value and hashes it, so a read model that could not
		// show it would be hiding something the chain commits to.
		{"Event", Event{}, []string{
			"agent", "anomaly", "chain_seq", "client", "connector", "decision", "duration_ms",
			"exec_id", "exec_path", "hash", "kind", "machine_id", "occurred_at", "ok", "params_digest",
			"prev_hash", "protocol_version", "schema_version", "seq", "session_id", "tool",
		}},
		{"Page", Page{}, []string{
			"agent", "anomaly", "chain_seq", "client", "connector", "cursor", "decision",
			"duration_ms", "events", "exec_id", "exec_path", "hash", "kind", "machine_id", "occurred_at",
			"ok", "params_digest", "prev_hash", "protocol_version", "schema_version",
			"seq", "session_id", "tool",
		}},
		{"JournalState", JournalState{}, []string{
			"chain", "entries", "expected_head_at", "head", "problem", "schema_version",
			"verification_material",
		}},
		{"Gaps", Gaps{}, []string{
			"anomalies", "anomalies_total", "calls_without_session", "missing_call_entries",
			"sessions_with_gaps", "unfinished_sessions",
		}},
		{"Snapshot", Snapshot{}, []string{
			"anomalies", "anomalies_total", "calls_recorded", "calls_without_session", "chain", "entries",
			"expected_head_at", "gaps", "head", "journal", "missing_call_entries", "problem",
			"schema_version", "sessions_with_gaps", "unfinished_sessions",
			"verification_material",
		}},
		{"Session", Session{}, []string{
			"agent", "anomalies", "calls_recorded", "chain_seq", "client", "connector",
			"denied", "ended_at", "id", "machine_id", "outcomes", "started_at", "state",
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := jsonFields(t, c.value)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("the shape of %s changed.\n got: %v\nwant: %v\n\n"+
					"If a field was added deliberately, add it here too -- and check first that "+
					"the journal can actually support what it claims.", c.name, got, c.want)
			}
		})
	}
}

// Properties the journal cannot support. None of them can be expressed today,
// and none should become expressible by accident.
//
// The list shrinks only when the journal gains the thing. "denied" left it
// when enforcement arrived; "agent" left it when the daemon began deriving one
// from the kernel and hashing it into session.start -- until then a field by
// that name would have been a claim with nothing behind it, and once the
// journal carried the value, a read model that could not show it was hiding
// something the chain commits to. Everything else here is still a claim
// nothing in the record can back, and "allowed" is deliberately still here --
// the journal holds a decision, not a fact about what then happened, and a
// field called allowed would invite exactly that reading.
//
// This is checked structurally: the question is whether the model has somewhere
// to put such a value, not whether some string appears in the source.
func TestReadModelCannotExpressEnforcement(t *testing.T) {
	forbidden := map[string]string{
		"executed":          "the journal records a decision and an outcome, never that a call ran",
		"active":            "a session with no end may be running or dead; the journal cannot tell",
		"allowed":           "an allow is a decision, not evidence the call was made",
		"blocked":           "Nim refused to forward a call; it did not stop anything else",
		"policy":            "the rules live in nim_rules and are read by the daemon, not projected here",
		"rule":              "the rules live in nim_rules and are read by the daemon, not projected here",
		"agent_id":          "an agent is named by its enrolment, never numbered",
		"identity":          "the journal records which enrolment matched, not an identity",
		"connector_reached": "the journal does not record whether the connector received anything",
		"reached":           "the journal does not record whether the connector received anything",
		"credential":        "credentials are never recorded",
		"credentials":       "credentials are never recorded",
		"loss_free":         "losses that leave no evidence cannot be ruled out",
		"complete":          "the record cannot claim to be complete",
		"verified":          "the chain is self-consistent; that is a weaker claim",
	}

	for _, dto := range []any{Event{}, Page{}, Snapshot{}, JournalState{}, Gaps{}, Session{}, SessionDetail{}} {
		name := reflect.TypeOf(dto).Name()
		for _, field := range jsonFields(t, dto) {
			if why, bad := forbidden[field]; bad {
				t.Errorf("%s has a field %q: %s", name, field, why)
			}
		}
	}
}

// The vocabularies are closed. A fifth chain state or a third session state
// would be a claim about the record, and it should not arrive quietly.
func TestStateVocabulariesAreClosed(t *testing.T) {
	chain := []ChainState{ChainEmpty, ChainSelfConsistent, ChainPartial, ChainBroken}
	want := []string{"empty", "self_consistent", "partial", "broken"}
	for i, c := range chain {
		if string(c) != want[i] {
			t.Errorf("chain state %d is %q, want %q", i, c, want[i])
		}
	}

	if string(SessionEnded) != "ended" || string(SessionUnfinished) != "unfinished" {
		t.Fatalf("session vocabulary changed: %q, %q", SessionEnded, SessionUnfinished)
	}
	for _, s := range []SessionState{SessionEnded, SessionUnfinished} {
		if strings.Contains(string(s), "active") || strings.Contains(string(s), "running") {
			t.Errorf("session state %q claims to know a session is alive", s)
		}
	}
}

// Each projection takes only what it needs. Compilation is the assertion: if
// Stream ever needs to verify, or Take ever needs to read entries, these stop
// building and the widening has to be justified.
func TestProjectionsTakeNarrowInterfaces(t *testing.T) {
	var (
		_ func(EventSource, int64, int) (Page, error)                   = Stream
		_ func(SnapshotSource) (Snapshot, error)                        = Take
		_ func(SnapshotSource, string) (JournalState, error)            = Check
		_ func(SessionSource, int) ([]Session, error)                   = Sessions
		_ func(SessionSource, string, int) (SessionDetail, bool, error) = Detail
	)

	// And the composite is only for wiring: it must be all three and nothing
	// more, so it cannot become a mirror of the journal's read surface.
	src := reflect.TypeOf((*Source)(nil)).Elem()
	sum := 0
	for _, part := range []any{(*EventSource)(nil), (*SnapshotSource)(nil), (*SessionSource)(nil)} {
		sum += reflect.TypeOf(part).Elem().NumMethod()
	}
	if src.NumMethod() != sum {
		t.Errorf("Source has %d methods but its parts have %d: it has grown beyond them",
			src.NumMethod(), sum)
	}
}
