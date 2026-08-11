package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/BySergiMM/nim/engine/internal/credential"
	"github.com/BySergiMM/nim/engine/internal/journal"
)

// freshJournal is a writable journal in its own temp directory, seeded so the
// chain has a genesis to start from.
func freshJournal(t testing.TB) *journal.Journal {
	t.Helper()
	return openJournal(t, filepath.Join(t.TempDir(), "nim.db"))
}

// fakeStore is an in-memory credential.Store, so these tests never touch a
// real keychain. It records every call, so a test can assert that a rejected
// request never reached the store at all rather than only that the response
// said no.
type fakeStore struct {
	mu      sync.Mutex
	secrets map[string]string
	calls   []string
}

func newFakeStore() *fakeStore { return &fakeStore{secrets: map[string]string{}} }

func (f *fakeStore) Set(target, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "set:"+target)
	f.secrets[target] = secret
	return nil
}

func (f *fakeStore) Get(target string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "get:"+target)
	s, ok := f.secrets[target]
	if !ok {
		return "", credential.ErrNotFound
	}
	return s, nil
}

func (f *fakeStore) Delete(target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete:"+target)
	if _, ok := f.secrets[target]; !ok {
		return credential.ErrNotFound
	}
	delete(f.secrets, target)
	return nil
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
