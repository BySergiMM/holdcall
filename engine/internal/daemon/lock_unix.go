//go:build unix

package daemon

import (
	"os"
	"syscall"
)

// acquireStartupLock takes an exclusive, blocking lock on path, creating it
// if needed, and returns a function that releases it.
//
// The kernel releases this lock automatically if the holding process dies
// for any reason, including a crash that skips every defer -- the property a
// lock *file* checked only by whether it exists cannot give, and exactly what
// makes it safe to serialize listen()'s stale-socket reclaim on.
func acquireStartupLock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil // closing the fd releases the flock
}
