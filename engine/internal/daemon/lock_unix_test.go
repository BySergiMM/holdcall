//go:build unix

package daemon

import (
	"errors"
	"net"
	"sync"
	"testing"
)

// This is the exact scenario acquireStartupLock exists for: several daemons
// starting at once after a crash left a stale socket behind. Without
// serializing the check-and-reclaim sequence, more than one of these can
// observe the socket as stale and one can unlink the socket another just
// bound, leaving two live daemons on what looks like one socket path.
func TestListenSerializesConcurrentStaleSocketReclaim(t *testing.T) {
	path := tempSocketPath(t)
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("creating a stale socket: %v", err)
	}
	stale.Close() // the file stays on disk; nothing is bound to it anymore

	const racers = 8
	var wg sync.WaitGroup
	errs := make(chan error, racers)
	listeners := make(chan net.Listener, racers)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			ln, err := listen(path)
			if err != nil {
				errs <- err
				return
			}
			listeners <- ln
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	close(listeners)

	var winners, alreadyRunning int
	for err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, errAlreadyRunning):
			alreadyRunning++
		default:
			t.Errorf("unexpected listen() error: %v", err)
		}
	}
	for ln := range listeners {
		ln.Close()
	}

	if winners != 1 {
		t.Fatalf("got %d winning listeners across %d concurrent racers, want exactly 1", winners, racers)
	}
	if alreadyRunning != racers-1 {
		t.Errorf("got %d losers reporting errAlreadyRunning, want %d", alreadyRunning, racers-1)
	}
}
