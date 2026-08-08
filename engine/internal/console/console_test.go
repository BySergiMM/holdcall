package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/readmodel"
)

func sp(s string) *string { return &s }
func ip(v int64) *int64   { return &v }
func bp(v bool) *bool     { return &v }

// serve builds a console over a real journal, opened the way the command opens
// it: read-only.
func serve(t *testing.T, write func(*journal.Journal)) (*httptest.Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nim.db")

	w, err := journal.Open(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	if write != nil {
		write(w)
	}
	w.Close()

	reader, err := journal.OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })

	srv := httptest.NewServer(New(reader, filepath.Join(t.TempDir(), "absent.sock")).Handler())
	t.Cleanup(srv.Close)
	return srv, path
}

func session(id, connector string) journal.Entry {
	return journal.Entry{
		Kind: journal.KindSessionStart, SessionID: id,
		MachineID: sp("test-machine"), Client: sp("test-client"), Connector: sp(connector),
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func call(id string, seq int64, tool string) journal.Entry {
	return journal.Entry{
		Kind: journal.KindCallRequest, SessionID: id, Seq: ip(seq), Tool: sp(tool),
		ParamsDigest: sp("digest"), Decision: sp(journal.DecisionObserved),
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func get(t *testing.T, srv *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	res, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	buf := make([]byte, 1<<20)
	n, _ := res.Body.Read(buf)
	return res, buf[:n]
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return v
}

// The console is a viewer. Every route must refuse anything that is not a read,
// so that it cannot grow into the control plane by accident.
func TestNoEndpointAcceptsAWrite(t *testing.T) {
	srv, _ := serve(t, func(j *journal.Journal) { j.Append(session("s1", "github")) })

	paths := []string{"/", "/api/snapshot", "/api/events", "/api/sessions/s1"}
	methods := []string{http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, "TRACE"}

	for _, p := range paths {
		for _, m := range methods {
			req, err := http.NewRequest(m, srv.URL+p, strings.NewReader(`{"x":1}`))
			if err != nil {
				t.Fatal(err)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", m, p, err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s %s returned %d, want 405", m, p, res.StatusCode)
			}
		}
	}
}

// Reading must not change the record.
func TestReadingDoesNotChangeTheJournal(t *testing.T) {
	srv, path := serve(t, func(j *journal.Journal) {
		j.Append(session("s1", "github"))
		j.Append(call("s1", 1, "echo"))
	})

	before, err := journal.OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	lenBefore, headBefore, _ := before.Head()
	before.Close()

	get(t, srv, "/api/snapshot")
	get(t, srv, "/api/events?since=0")
	get(t, srv, "/api/sessions/s1")
	get(t, srv, "/")

	after, err := journal.OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	lenAfter, headAfter, _ := after.Head()

	if lenBefore != lenAfter || headBefore != headAfter {
		t.Fatalf("the journal moved while being read: %d/%s -> %d/%s",
			lenBefore, headBefore[:8], lenAfter, headAfter[:8])
	}
}

func TestEventsRespectTheCursor(t *testing.T) {
	srv, _ := serve(t, func(j *journal.Journal) {
		j.Append(session("s1", "github"))
		for i := 1; i <= 5; i++ {
			j.Append(call("s1", int64(i), "t"))
		}
	})

	_, body := get(t, srv, "/api/events?since=0")
	all := decode[readmodel.Page](t, body)
	if len(all.Events) != 6 {
		t.Fatalf("got %d events, want 6", len(all.Events))
	}
	for i, e := range all.Events {
		if e.ChainSeq != int64(i+1) {
			t.Fatalf("event %d has chain_seq %d: order must follow the journal", i, e.ChainSeq)
		}
	}
	if all.Cursor != 6 {
		t.Errorf("cursor = %d, want 6", all.Cursor)
	}

	// since=N returns strictly what came after N.
	_, body = get(t, srv, "/api/events?since=4")
	after := decode[readmodel.Page](t, body)
	if len(after.Events) != 2 {
		t.Fatalf("since=4 returned %d events, want 2", len(after.Events))
	}
	for _, e := range after.Events {
		if e.ChainSeq <= 4 {
			t.Errorf("since=4 returned chain_seq %d", e.ChainSeq)
		}
	}

	// At the head, nothing, and a cursor that can be fed straight back.
	_, body = get(t, srv, "/api/events?since=6")
	none := decode[readmodel.Page](t, body)
	if len(none.Events) != 0 {
		t.Errorf("expected nothing past the head, got %d", len(none.Events))
	}
	if none.Cursor != 6 {
		t.Errorf("cursor rewound to %d", none.Cursor)
	}
}

func TestEventsLimitIsHonoured(t *testing.T) {
	srv, _ := serve(t, func(j *journal.Journal) {
		j.Append(session("s1", "github"))
		for i := 1; i <= 10; i++ {
			j.Append(call("s1", int64(i), "t"))
		}
	})

	_, body := get(t, srv, "/api/events?since=0&limit=3")
	page := decode[readmodel.Page](t, body)
	if len(page.Events) != 3 {
		t.Fatalf("limit=3 returned %d events", len(page.Events))
	}
	if page.Cursor != 3 {
		t.Errorf("cursor = %d, want 3", page.Cursor)
	}

	// Paging with the returned cursor must not skip or repeat.
	_, body = get(t, srv, "/api/events?since=3&limit=3")
	next := decode[readmodel.Page](t, body)
	if next.Events[0].ChainSeq != 4 {
		t.Errorf("second page starts at %d, want 4", next.Events[0].ChainSeq)
	}
}

func TestBadParametersAreRejected(t *testing.T) {
	srv, _ := serve(t, func(j *journal.Journal) { j.Append(session("s1", "github")) })

	for _, path := range []string{"/api/events?since=abc", "/api/events?limit=xyz"} {
		res, _ := get(t, srv, path)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s returned %d, want 400", path, res.StatusCode)
		}
	}
	// Anything that is not an opaque id is refused before it reaches a query.
	for _, id := range []string{"../../etc/passwd", "a b", "x';drop table nim_journal;--", ""} {
		res, _ := get(t, srv, "/api/sessions/"+id)
		if res.StatusCode == http.StatusOK {
			t.Errorf("session id %q was accepted", id)
		}
	}
}

func TestSnapshotReportsWhatCanBeShown(t *testing.T) {
	srv, _ := serve(t, func(j *journal.Journal) {
		j.Append(session("s1", "github"))
		j.Append(call("s1", 1, "create_issue"))
		j.Append(journal.Entry{Kind: journal.KindCallOutcome, SessionID: "s1", Seq: ip(1),
			OK: bp(true), DurationMS: ip(9), OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)})
	})

	_, body := get(t, srv, "/api/snapshot")
	snap := decode[map[string]any](t, body)

	journalState := snap["journal"].(map[string]any)
	if journalState["chain"] != string(readmodel.ChainSelfConsistent) {
		t.Errorf("chain = %v", journalState["chain"])
	}
	if journalState["verification_material"] != true {
		t.Error("verification material should be available in this fixture")
	}
	if snap["calls_recorded"].(float64) != 1 {
		t.Errorf("calls_recorded = %v", snap["calls_recorded"])
	}
	// The daemon is not running in a test, and the console must say so rather
	// than guess.
	if snap["daemon"].(map[string]any)["running"] != false {
		t.Error("daemon should be reported as not running")
	}

	sessions := snap["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if got := sessions[0].(map[string]any)["state"]; got != string(readmodel.SessionUnfinished) {
		t.Errorf("session state = %v, want unfinished", got)
	}
}

// An empty journal must not be dressed up as one that passed.
func TestEmptyJournalReportsEmpty(t *testing.T) {
	srv, _ := serve(t, nil)

	_, body := get(t, srv, "/api/snapshot")
	snap := decode[map[string]any](t, body)
	journalState := snap["journal"].(map[string]any)

	if journalState["chain"] != string(readmodel.ChainEmpty) {
		t.Errorf("chain = %v, want empty", journalState["chain"])
	}
	if journalState["entries"].(float64) != 0 {
		t.Errorf("entries = %v", journalState["entries"])
	}
	if sessions := snap["sessions"].([]any); len(sessions) != 0 {
		t.Errorf("an empty journal reported %d sessions", len(sessions))
	}
}

// Missing verification material is a statement about what could be read, and
// the API must carry it as such rather than as a verdict on the journal.
func TestMissingSeedIsReportedAsMissingMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")
	w, err := journal.Open(path, "the-machine")
	if err != nil {
		t.Fatal(err)
	}
	w.Append(session("s1", "github"))
	w.Append(call("s1", 1, "t"))
	w.Close()

	// Opened as the console would with no machine-id to read.
	reader, err := journal.OpenReadOnly(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	srv := httptest.NewServer(New(reader, "/nonexistent.sock").Handler())
	defer srv.Close()

	_, body := get(t, srv, "/api/snapshot")
	state := decode[map[string]any](t, body)["journal"].(map[string]any)

	if state["verification_material"] != false {
		t.Error("verification material should be reported as missing")
	}
	if state["chain"] != string(readmodel.ChainPartial) {
		t.Errorf("chain = %v, want partial: a missing seed is not a broken journal", state["chain"])
	}
	if state["chain"] == string(readmodel.ChainBroken) {
		t.Error("a missing seed must never be reported as tampering")
	}
}

// Nothing reaches the browser that the journal does not hold. Arguments and
// response bodies are never recorded, so they must not appear.
func TestNothingIsInventedForTheBrowser(t *testing.T) {
	srv, _ := serve(t, func(j *journal.Journal) {
		j.Append(session("s1", "github"))
		j.Append(call("s1", 1, "create_issue"))
	})

	for _, path := range []string{"/api/snapshot", "/api/events?since=0", "/api/sessions/s1"} {
		_, body := get(t, srv, path)
		text := string(body)
		for _, forbidden := range []string{
			"arguments", "response", "executed", "\"active\"", "allowed", "denied",
			"tamper", "agent_id", "policy", "credential",
		} {
			if strings.Contains(strings.ToLower(text), forbidden) {
				t.Errorf("%s exposed %q, which the journal does not hold: %s", path, forbidden, text)
			}
		}
	}
}

// A journal left behind by a daemon that is gone must still be readable: an
// autopsy is exactly when someone wants this.
func TestConsoleWorksWithNoDaemon(t *testing.T) {
	srv, _ := serve(t, func(j *journal.Journal) {
		j.Append(session("s1", "github"))
		j.Append(call("s1", 1, "t"))
	})

	res, body := get(t, srv, "/api/snapshot")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("snapshot failed with no daemon: %d", res.StatusCode)
	}
	snap := decode[map[string]any](t, body)
	if snap["daemon"].(map[string]any)["running"] != false {
		t.Error("daemon should report as not running")
	}
	if snap["journal"].(map[string]any)["entries"].(float64) != 2 {
		t.Error("history should still be readable without a daemon")
	}
}

func TestIndexIsServedOnlyAtRoot(t *testing.T) {
	srv, _ := serve(t, nil)

	res, body := get(t, srv, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("root returned %d", res.StatusCode)
	}
	if !strings.Contains(string(body), "nim console") {
		t.Error("the console page was not served")
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type = %q", ct)
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("responses should not be sniffable")
	}

	// No other path serves anything, and nothing reaches the filesystem.
	for _, p := range []string{"/index.html", "/../console.go", "/static/x", "/console.go"} {
		res, _ := get(t, srv, p)
		if res.StatusCode == http.StatusOK {
			t.Errorf("%s was served", p)
		}
	}
}

// The console shows a record of what an agent did. That belongs on loopback.
func TestListenRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "192.168.1.10:0", "[::]:0"} {
		if ln, err := Listen(addr); err == nil {
			ln.Close()
			t.Errorf("Listen accepted %s", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		ln, err := Listen(addr)
		if err != nil {
			t.Errorf("Listen rejected loopback %s: %v", addr, err)
			continue
		}
		ln.Close()
	}
	if _, err := Listen("not-an-address"); err == nil {
		t.Error("Listen accepted a malformed address")
	}
}
