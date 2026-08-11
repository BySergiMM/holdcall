package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BySergiMM/nim/engine/internal/journal"
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
