package daemon

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTargetLocksSerializesSameTarget(t *testing.T) {
	tl := newTargetLocks()
	var inCriticalSection atomic.Int32
	var maxConcurrent atomic.Int32

	var wg sync.WaitGroup
	const n = 20
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			unlock := tl.Lock("github")
			defer unlock()
			cur := inCriticalSection.Add(1)
			for {
				max := maxConcurrent.Load()
				if cur <= max || maxConcurrent.CompareAndSwap(max, cur) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			inCriticalSection.Add(-1)
		}()
	}
	wg.Wait()

	if got := maxConcurrent.Load(); got != 1 {
		t.Fatalf("max concurrent holders of the same target's lock = %d, want 1", got)
	}
}

func TestTargetLocksDoesNotSerializeDifferentTargets(t *testing.T) {
	tl := newTargetLocks()
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan time.Duration, 2)

	for _, target := range []string{"github", "slack"} {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			<-start
			t0 := time.Now()
			unlock := tl.Lock(target)
			defer unlock()
			time.Sleep(50 * time.Millisecond)
			results <- time.Since(t0)
		}(target)
	}
	close(start)
	wg.Wait()
	close(results)

	for d := range results {
		if d > 100*time.Millisecond {
			t.Fatalf("locking a different target waited %v; different targets must not serialize each other", d)
		}
	}
}
