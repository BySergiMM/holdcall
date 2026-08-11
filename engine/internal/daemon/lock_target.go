package daemon

import "sync"

// targetLocks serializes connector.set/connector.remove per target, so the
// secret written to the credential store and the metadata written to
// nim_connectors for one call can never be interleaved with another call's
// writes to the same target. Without this, two concurrent connector.set
// calls for the same target could each write their own secret and their own
// metadata in an order where the metadata ends up describing one call's env
// key while the store holds the other call's secret.
//
// Locking is per target, not global: concurrent operations on different
// targets never wait on each other.
type targetLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newTargetLocks() *targetLocks {
	return &targetLocks{locks: make(map[string]*sync.Mutex)}
}

// Lock acquires the lock for target, creating it if this is the first
// operation ever seen for that name, and returns a function that releases
// it. Locks are never removed once created; the memory this holds is
// bounded by the number of distinct target names ever used, the same growth
// profile as the nim_connectors table itself.
func (t *targetLocks) Lock(target string) (unlock func()) {
	t.mu.Lock()
	l, ok := t.locks[target]
	if !ok {
		l = &sync.Mutex{}
		t.locks[target] = l
	}
	t.mu.Unlock()

	l.Lock()
	return l.Unlock
}
