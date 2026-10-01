package journal

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Open has to wait for a lock another connection holds, not fail on it.
//
// The DSN's pragmas run in the order they are written, on every new
// connection. Switching a database to WAL is a write to its header, so it
// needs the write lock -- and with busy_timeout not yet set, a lock held by
// anyone else makes that switch fail at once with "database is locked", which
// is what a daemon starting while another handle was mid-write, or a test
// opening a second handle on a journal a daemon had just created, did
// intermittently. The timeout has to be armed before the first statement that
// can meet a lock, so it comes first in the DSN.
//
// The other handle holds the database in rollback-journal mode, and holds an
// EXCLUSIVE lock, deliberately. Rollback-journal is the state every SQLite
// file starts in, and the one in which the switch has work to do: a file
// already in WAL has nothing to switch, which is why the failure only ever
// showed on the first open. EXCLUSIVE rather than a plain write lock because
// what busy_timeout can wait out is a lock that stops Open *starting* to read;
// if Open already holds a read lock and is upgrading it to a write lock while
// another connection holds one, SQLite answers "locked" at once by design
// (waiting there could deadlock both) and no timeout changes that.
func TestOpenWaitsForALockAnotherConnectionHoldsInsteadOfFailing(t *testing.T) {
	dir := tempDirForOpenTest(t)
	path := filepath.Join(dir, "holdcall.db")

	holder, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("opening the holder: %v", err)
	}
	t.Cleanup(func() { holder.Close() })
	if _, err := holder.Exec(`create table held (x integer)`); err != nil {
		t.Fatalf("creating a database in rollback-journal mode: %v", err)
	}

	// One connection, one exclusive transaction: from here until COMMIT nothing
	// else can so much as read this file, and Open's first statement is a read.
	ctx := context.Background()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("holder connection: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.ExecContext(ctx, `begin exclusive`); err != nil {
		t.Fatalf("taking the exclusive lock: %v", err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if _, err := conn.ExecContext(ctx, `commit`); err != nil {
			t.Errorf("releasing the exclusive lock: %v", err)
		}
	}
	t.Cleanup(release)

	type result struct {
		j   *Journal
		err error
	}
	opened := make(chan result, 1)
	go func() {
		j, err := Open(path, "test-machine")
		opened <- result{j, err}
	}()

	// While the lock is held Open has nothing it can do but wait. A result
	// before the release is the bug, whatever it says: with the lock held it
	// cannot be a success.
	const held = 400 * time.Millisecond
	select {
	case r := <-opened:
		if r.j != nil {
			r.j.Close()
		}
		t.Fatalf("Open returned while another connection still held the exclusive lock (err: %v); "+
			"it should be waiting on it", r.err)
	case <-time.After(held):
	}

	release()

	select {
	case r := <-opened:
		if r.err != nil {
			t.Fatalf("Open failed once the lock was released, having been made to wait for it: %v", r.err)
		}
		t.Cleanup(func() { r.j.Close() })

		var mode string
		if err := r.j.db.QueryRow(`pragma journal_mode`).Scan(&mode); err != nil {
			t.Fatalf("reading the journal mode: %v", err)
		}
		if mode != "wal" {
			t.Errorf("journal_mode = %q after Open, want wal: the wait must end in the switch, not in skipping it", mode)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Open did not return within 10s of the lock being released")
	}
}

// tempDirForOpenTest is a temporary directory whose removal is best effort on
// windows, where a SQLite file can outlast its closed handles for a moment
// (F-029, see TestConcurrentOpensDoNotCollideOnViews). Every other platform
// must be able to remove it.
func tempDirForOpenTest(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "holdcall-open-busy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			if runtime.GOOS == "windows" {
				t.Logf("windows kept a journal file open past Close: %v (F-029)", err)
				return
			}
			t.Errorf("removing %s: %v", dir, err)
		}
	})
	return dir
}
