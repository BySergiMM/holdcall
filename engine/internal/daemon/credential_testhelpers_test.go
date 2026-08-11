package daemon

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/peer"
)

// freshJournal is a writable journal in its own temp directory, seeded so the
// chain has a genesis to start from.
func freshJournal(t testing.TB) *journal.Journal {
	t.Helper()
	return openJournal(t, filepath.Join(t.TempDir(), "nim.db"))
}

// tempSocketPath keeps the path short: an AF_UNIX address is capped near 104
// bytes and t.TempDir()'s nested test-name directories can overflow that.
func tempSocketPath(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nimd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

// openDB opens the journal file directly, for tests that need to read columns
// no accessor exposes.
func openDB(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	return db
}

// sessionLabels returns the two things a session.start records about who was
// calling: the label the caller sent, and the agent the daemon derived.
func sessionLabels(t testing.TB, path, sessionID string) (client, agent string) {
	t.Helper()
	db := openDB(t, path)
	defer db.Close()
	var c, a sql.NullString
	if err := db.QueryRow(
		`select client, agent from nim_journal where kind = ? and session_id = ?`,
		"session.start", sessionID).Scan(&c, &a); err != nil {
		t.Fatalf("reading session %s: %v", sessionID, err)
	}
	return c.String, a.String
}

// journalAgent builds an enrolment from an identity the kernel produced.
func journalAgent(name string, img peer.Image) journal.Agent {
	return journal.Agent{
		Name: name, ExecDev: img.Dev(), ExecIno: img.Ino(),
		ExecPath: "/enrolled/by/test", EnrolledAt: time.Now(),
	}
}
