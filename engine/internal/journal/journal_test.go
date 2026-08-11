package journal

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Journal {
	t.Helper()
	j, err := Open(filepath.Join(t.TempDir(), "nim.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func TestOpenCreatesAnEmptySchema(t *testing.T) {
	j := open(t)
	n, err := j.CountCalls()
	if err != nil {
		t.Fatalf("CountCalls: %v", err)
	}
	if n != 0 {
		t.Fatalf("fresh journal has %d calls, want 0", n)
	}
}

// foreign_keys and busy_timeout are per-connection pragmas in SQLite, and
// database/sql's pool can open more than one physical connection under
// concurrent load (see Open's doc comment). A one-time Exec after Open only
// reaches whichever connection happens to service it.
//
// A *sql.Tx pins a single physical connection exclusively until it ends, so
// holding n of them open at once forces the pool to hand out n distinct
// connections -- but only if all n are genuinely open at the same moment.
// Launching n goroutines is not enough on its own: without a barrier, a
// goroutine can Begin, query and Rollback before the next one even starts,
// letting them share one connection sequentially and pass without ever
// exercising the pooling bug this guards against. The two-phase wait below
// (ready/proceed) makes that structurally impossible: every goroutine must
// have pinned its own connection before any of them is allowed to query.
type pooledConnState struct {
	foreignKeys int
	busyTimeout int
	journalMode string
}

func TestPerConnectionPragmasApplyOnEveryPooledConnection(t *testing.T) {
	j := open(t)
	j.db.SetMaxOpenConns(8)

	const n = 8
	var ready sync.WaitGroup
	ready.Add(n)
	proceed := make(chan struct{})

	var wg sync.WaitGroup
	results := make(chan pooledConnState, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			tx, err := j.db.Begin()
			if err != nil {
				t.Errorf("Begin: %v", err)
				ready.Done()
				return
			}
			defer tx.Rollback()
			ready.Done()
			<-proceed // every transaction stays open until all n have been pinned

			var s pooledConnState
			if err := tx.QueryRow(`pragma foreign_keys`).Scan(&s.foreignKeys); err != nil {
				t.Errorf("querying foreign_keys pragma: %v", err)
				return
			}
			if err := tx.QueryRow(`pragma busy_timeout`).Scan(&s.busyTimeout); err != nil {
				t.Errorf("querying busy_timeout pragma: %v", err)
				return
			}
			if err := tx.QueryRow(`pragma journal_mode`).Scan(&s.journalMode); err != nil {
				t.Errorf("querying journal_mode pragma: %v", err)
				return
			}
			results <- s
		}()
	}

	ready.Wait()
	if open := j.db.Stats().OpenConnections; open < n {
		t.Fatalf("only %d of %d transactions were pinned to distinct connections; the barrier did not force real concurrency", open, n)
	}
	close(proceed)
	wg.Wait()
	close(results)

	for s := range results {
		if s.foreignKeys != 1 {
			t.Errorf("a pooled connection has foreign_keys = %d, want 1 (enabled)", s.foreignKeys)
		}
		if s.busyTimeout != 5000 {
			t.Errorf("a pooled connection has busy_timeout = %d, want 5000", s.busyTimeout)
		}
		if !strings.EqualFold(s.journalMode, "wal") {
			t.Errorf("a pooled connection has journal_mode = %q, want wal", s.journalMode)
		}
	}
}

func TestOpenIsIdempotentOnAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nim.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := first.StartSession(Session{ID: "s1", MachineID: "m1", Target: "github", StartedAt: time.Now()}); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopening an existing database must not fail: %v", err)
	}
	defer second.Close()
	if err := second.RecordCall(Call{ID: "c1", SessionID: "s1", Seq: 1, Tool: "t", ParamsDigest: "d", Decision: "allow", OccurredAt: time.Now()}); err != nil {
		t.Fatalf("data from before the reopen must still be usable: %v", err)
	}
}

func TestRecordCallIsIdempotentOnSessionAndSeq(t *testing.T) {
	j := open(t)
	started := time.Now().UTC()
	if err := j.StartSession(Session{ID: "s1", MachineID: "m1", Target: "github", StartedAt: started}); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Recorded once on the way out (no outcome yet, exactly as the shim does
	// when a tools/call request is seen)...
	if err := j.RecordCall(Call{
		ID: "s1-1", SessionID: "s1", Seq: 1, Tool: "echo", ParamsDigest: "abc", Decision: "allow",
		OccurredAt: started,
	}); err != nil {
		t.Fatalf("first RecordCall: %v", err)
	}
	// ...and updated in place once the response arrives. A shim that retries
	// after a dropped connection to the daemon must not produce a second row.
	// The id deliberately differs from the first call: the conflict target is
	// documented as (session_id, seq), not the primary key, and using the
	// same id both times would let a wrong implementation upsert on id
	// instead and still pass.
	ok := true
	ms := 42
	if err := j.RecordCall(Call{
		ID: "s1-1-retry", SessionID: "s1", Seq: 1, Tool: "echo", ParamsDigest: "abc", Decision: "allow",
		OK: &ok, DurationMS: &ms, OccurredAt: started,
	}); err != nil {
		t.Fatalf("second RecordCall: %v", err)
	}

	n, err := j.CountCalls()
	if err != nil {
		t.Fatalf("CountCalls: %v", err)
	}
	if n != 1 {
		t.Fatalf("got %d rows for one (session_id, seq), want 1 -- upserting on id instead would insert a second row", n)
	}

	var gotID string
	var gotOK bool
	var gotMS int
	if err := j.db.QueryRow(`select id, ok, duration_ms from nim_calls where session_id = ? and seq = ?`, "s1", 1).
		Scan(&gotID, &gotOK, &gotMS); err != nil {
		t.Fatalf("querying the upserted row: %v", err)
	}
	if gotID != "s1-1" {
		t.Errorf("id = %q, want the first call's id s1-1 (the conflict clause must not touch id)", gotID)
	}
	if !gotOK || gotMS != 42 {
		t.Errorf("upsert did not apply the response: ok=%v duration_ms=%d", gotOK, gotMS)
	}
}

// A retried request-time report (no outcome yet) arriving after the
// response-time report already landed must not erase the recorded outcome --
// that would turn a successful call back into an unknown one.
func TestRecordCallUpdateIsMonotonic(t *testing.T) {
	j := open(t)
	started := time.Now().UTC()
	if err := j.StartSession(Session{ID: "s1", MachineID: "m1", Target: "github", StartedAt: started}); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	ok := true
	ms := 42
	if err := j.RecordCall(Call{
		ID: "s1-1", SessionID: "s1", Seq: 1, Tool: "echo", ParamsDigest: "abc", Decision: "allow",
		OK: &ok, DurationMS: &ms, OccurredAt: started,
	}); err != nil {
		t.Fatalf("response-time RecordCall: %v", err)
	}
	// A stale/retried request-time report, same shape as the first report a
	// shim sends before a response exists.
	if err := j.RecordCall(Call{
		ID: "s1-1-retry", SessionID: "s1", Seq: 1, Tool: "echo", ParamsDigest: "abc", Decision: "allow",
		OccurredAt: started,
	}); err != nil {
		t.Fatalf("retried request-time RecordCall: %v", err)
	}

	var gotOK bool
	var gotMS int
	if err := j.db.QueryRow(`select ok, duration_ms from nim_calls where session_id = ? and seq = ?`, "s1", 1).
		Scan(&gotOK, &gotMS); err != nil {
		t.Fatalf("querying the row: %v", err)
	}
	if !gotOK || gotMS != 42 {
		t.Errorf("a stale report with no outcome erased the real one: ok=%v duration_ms=%d, want ok=true duration_ms=42", gotOK, gotMS)
	}
}

func TestRecordCallRejectsAnUnknownSession(t *testing.T) {
	j := open(t)
	err := j.RecordCall(Call{ID: "orphan-1", SessionID: "no-such-session", Seq: 1, Tool: "t", ParamsDigest: "d", Decision: "allow", OccurredAt: time.Now()})
	if err == nil {
		t.Fatal("a call referencing a session that was never started must be rejected")
	}
}

func TestEndSessionIsIdempotent(t *testing.T) {
	j := open(t)
	if err := j.StartSession(Session{ID: "s1", MachineID: "m1", Target: "github", StartedAt: time.Now()}); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	first := time.Now().UTC()
	if err := j.EndSession("s1", first); err != nil {
		t.Fatalf("first EndSession: %v", err)
	}
	// A second end.session report (e.g. from a shim that reconnected) must
	// not overwrite the first, real end time.
	later := first.Add(time.Hour)
	if err := j.EndSession("s1", later); err != nil {
		t.Fatalf("second EndSession: %v", err)
	}

	var endedAt string
	if err := j.db.QueryRow(`select ended_at from nim_sessions where id = ?`, "s1").Scan(&endedAt); err != nil {
		t.Fatalf("querying ended_at: %v", err)
	}
	if want := first.Format(time.RFC3339Nano); endedAt != want {
		t.Errorf("ended_at = %q, want the first report %q (not the later one)", endedAt, want)
	}
}

// A session.end for a session this journal never saw a session.start for
// (e.g. the daemon restarted mid-session) must not be treated as an error --
// there is nothing to update, and reporting is best-effort by design.
func TestEndSessionOnAnUnknownSessionIsANoOp(t *testing.T) {
	j := open(t)
	if err := j.EndSession("never-started", time.Now()); err != nil {
		t.Fatalf("EndSession on an unknown session must not error: %v", err)
	}
}

func TestStartSessionStoresAnEmptyClientAsNull(t *testing.T) {
	j := open(t)
	if err := j.StartSession(Session{ID: "s1", MachineID: "m1", Target: "github", StartedAt: time.Now()}); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	var client *string
	if err := j.db.QueryRow(`select client from nim_sessions where id = ?`, "s1").Scan(&client); err != nil {
		t.Fatalf("querying client: %v", err)
	}
	if client != nil {
		t.Errorf("client = %q, want NULL for an unset label", *client)
	}
}

func TestConnectorInfoOnAnUnconfiguredTargetIsNotFoundNotError(t *testing.T) {
	j := open(t)
	_, found, err := j.ConnectorInfo("github")
	if err != nil {
		t.Fatalf("ConnectorInfo: %v", err)
	}
	if found {
		t.Fatal("an unconfigured target must report found=false, not an error")
	}
}

func TestSetConnectorIsAnUpsert(t *testing.T) {
	j := open(t)
	first := time.Now().UTC()
	if err := j.SetConnector("github", "GITHUB_TOKEN", first); err != nil {
		t.Fatalf("first SetConnector: %v", err)
	}
	second := first.Add(time.Hour)
	if err := j.SetConnector("github", "GH_TOKEN", second); err != nil {
		t.Fatalf("second SetConnector: %v", err)
	}

	c, found, err := j.ConnectorInfo("github")
	if err != nil {
		t.Fatalf("ConnectorInfo: %v", err)
	}
	if !found {
		t.Fatal("expected a connector to be found")
	}
	if c.EnvKey != "GH_TOKEN" {
		t.Errorf("env_key = %q, want the second Set's value GH_TOKEN", c.EnvKey)
	}
	if !c.UpdatedAt.Equal(second) {
		t.Errorf("updated_at = %v, want %v", c.UpdatedAt, second)
	}

	list, err := j.ListConnectors()
	if err != nil {
		t.Fatalf("ListConnectors: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d connectors, want 1 (an upsert must not create a second row)", len(list))
	}
}

func TestListConnectorsIsOrderedAndExcludesNothingSecret(t *testing.T) {
	j := open(t)
	now := time.Now().UTC()
	if err := j.SetConnector("slack", "SLACK_TOKEN", now); err != nil {
		t.Fatalf("SetConnector(slack): %v", err)
	}
	if err := j.SetConnector("github", "GITHUB_TOKEN", now); err != nil {
		t.Fatalf("SetConnector(github): %v", err)
	}

	list, err := j.ListConnectors()
	if err != nil {
		t.Fatalf("ListConnectors: %v", err)
	}
	if len(list) != 2 || list[0].Target != "github" || list[1].Target != "slack" {
		t.Fatalf("got %+v, want [github slack] in that order", list)
	}
}

// The whole point of nim_connectors: it is structurally impossible for a
// secret to end up in this table, because there is no column for one.
func TestNimConnectorsHasNoSecretColumn(t *testing.T) {
	j := open(t)
	rows, err := j.db.Query(`select * from nim_connectors`)
	if err != nil {
		t.Fatalf("querying nim_connectors: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("Columns: %v", err)
	}
	want := map[string]bool{"target": true, "env_key": true, "updated_at": true}
	if len(cols) != len(want) {
		t.Fatalf("nim_connectors columns = %v, want exactly %v", cols, want)
	}
	for _, c := range cols {
		if !want[c] {
			t.Errorf("unexpected column %q in nim_connectors", c)
		}
	}
}

func TestDeleteConnectorRemovesIt(t *testing.T) {
	j := open(t)
	if err := j.SetConnector("github", "GITHUB_TOKEN", time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	if err := j.DeleteConnector("github"); err != nil {
		t.Fatalf("DeleteConnector: %v", err)
	}
	_, found, err := j.ConnectorInfo("github")
	if err != nil {
		t.Fatalf("ConnectorInfo: %v", err)
	}
	if found {
		t.Fatal("connector should be gone after DeleteConnector")
	}
}

func TestDeleteConnectorOnAnUnconfiguredTargetIsANoOp(t *testing.T) {
	j := open(t)
	if err := j.DeleteConnector("never-configured"); err != nil {
		t.Fatalf("DeleteConnector on an unconfigured target must not error: %v", err)
	}
}
