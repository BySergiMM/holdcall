package shim

import (
	"os/exec"
	"sync"
	"testing"
)

// The signal handler and the normal exit can both reach the connector's
// termination at once -- a shell's Ctrl-C delivers the signal to the whole
// group as the pipes close. Two concurrent Waits on one Cmd are a data race
// the race detector reported, found by review; stop runs it once and hands
// both callers the same answer.
func TestStoppingTheDownstreamTwiceAtOnceWaitsOnce(t *testing.T) {
	for i := 0; i < 5; i++ {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		s := &Shim{}
		var wg sync.WaitGroup
		results := make([]error, 2)
		for k := range results {
			wg.Add(1)
			go func(k int) {
				defer wg.Done()
				results[k] = s.stop(cmd)
			}(k)
		}
		wg.Wait()
		if results[0] != results[1] {
			t.Fatalf("the two callers got different results: %v vs %v", results[0], results[1])
		}
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() && cmd.ProcessState.Success() {
			t.Fatal("the connector was not reaped")
		}
	}
}
