package shim

import (
	"strings"
	"testing"
)

// The first cause is what started the loss. Overwriting it with whatever
// happened last describes the aftermath instead: a daemon that stops accepting
// events partway through would otherwise be reported as one that was never
// there, which points at the wrong problem.
func TestLossSummaryKeepsTheFirstCause(t *testing.T) {
	r := &reporter{}

	r.miss("the daemon stopped accepting events mid-session")
	r.miss("no daemon is listening")
	r.miss("no daemon is listening")

	if r.lost != 3 {
		t.Errorf("lost = %d, want 3", r.lost)
	}
	if len(r.causes) != 2 {
		t.Fatalf("causes = %v, want the two distinct ones", r.causes)
	}
	if r.causes[0] != "the daemon stopped accepting events mid-session" {
		t.Errorf("the first cause was not kept: %q", r.causes[0])
	}
	if r.causes[1] != "no daemon is listening" {
		t.Errorf("the later cause was not kept: %q", r.causes[1])
	}
	if strings.Join(r.causes, "; then ") == "no daemon is listening" {
		t.Error("the summary would report only the last cause")
	}
}

// The warning goes out once. It is on the relay's path -- send calls miss when
// the queue is full -- so repeating it per call would put an unbounded number
// of writes to a pipe nobody may be draining in front of tool calls.
func TestOnlyTheFirstMissWarns(t *testing.T) {
	r := &reporter{}
	if r.warned {
		t.Fatal("a fresh reporter must not have warned")
	}
	r.miss("no daemon is listening")
	if !r.warned {
		t.Fatal("the first miss must warn")
	}
	for i := 0; i < 100; i++ {
		r.miss("no daemon is listening")
	}
	if r.lost != 101 {
		t.Errorf("lost = %d, want every miss counted", r.lost)
	}
	if len(r.causes) != 1 {
		t.Errorf("one cause repeated should be recorded once, got %v", r.causes)
	}
}
