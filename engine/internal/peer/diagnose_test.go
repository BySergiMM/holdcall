//go:build unix

package peer

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// diagnose is tested directly rather than through Diagnose, because a test
// cannot make os.Executable() report an arbitrary path for the test binary
// itself -- see diagnose's doc comment in peer.go. Passing "self" in lets a
// test recreate the one shape that matters -- a peer launched from exactly
// our own path -- without needing a real upgrade.

// A peer launched from our own path, but running a different file, must be
// told apart from a peer that is simply a different program somewhere else.
// This is the distinction `nim daemon restart`'s remedy depends on: telling
// an operator to restart Nim is only right in the first case, and the F-001
// fix is worthless if the two ever compare equal.
func TestDiagnoseTellsASamePathRebuildApartFromADifferentProgram(t *testing.T) {
	cmd := exec.Command(sleepProgram, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a foreign process: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	pid := cmd.Process.Pid

	peerPath, ok := ExecPathOf(pid)
	if !ok {
		t.Fatal("ExecPathOf could not read the launch path of a live process this test just started")
	}

	// Baseline: the peer really is running a different file at a different
	// path than our own binary, so the DifferentBinary branch below is the
	// ordinary case, not one that happens to pass by never being exercised.
	self := mustExecutable(t)
	if peerPath == self {
		t.Fatalf("test setup is broken: %s (a foreign process) launched from the same path as this test binary", sleepProgram)
	}
	if got := diagnose(pid, self); got != DifferentBinary {
		t.Fatalf("diagnose(foreign pid, our own path) = %v, want DifferentBinary", got)
	}

	// The comparison a real upgrade needs: once "self" genuinely is the
	// peer's own launch path -- which os.Executable() cannot be made to
	// report for this test binary, so the peer's own path stands in for it
	// -- diagnose must read that as SameLaunchPathOlderBuild, not merely as
	// "not DifferentBinary".
	if got := diagnose(pid, peerPath); got != SameLaunchPathOlderBuild {
		t.Fatalf("diagnose(pid, its own launch path) = %v, want SameLaunchPathOlderBuild", got)
	}
}

// A pid that has already exited cannot be diagnosed at all, and must never
// be reported as SameLaunchPathOlderBuild: that would send an operator to
// `nim daemon restart` a process that was never there, let alone Nim.
func TestDiagnoseIsUnavailableForAPidThatHasExited(t *testing.T) {
	cmd := exec.Command(sleepProgram, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	pid := cmd.Process.Pid
	cmd.Process.Kill()
	cmd.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := ExecPathOf(pid); !ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := diagnose(pid, mustExecutable(t)); got != DiagnosisUnavailable {
		t.Fatalf("diagnose(dead pid, anything) = %v, want DiagnosisUnavailable", got)
	}
}

// ExecPathOf itself must be able to resolve a pid it can genuinely inspect
// -- otherwise both tests above would pass for the wrong reason, by having
// ExecPathOf fail on the live peer too.
func TestExecPathOfResolvesALivePeer(t *testing.T) {
	cmd := exec.Command(sleepProgram, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	path, ok := ExecPathOf(cmd.Process.Pid)
	if !ok {
		t.Fatal("ExecPathOf reported a live process as unreadable")
	}
	if path == "" {
		t.Fatal("ExecPathOf reported ok with an empty path")
	}
}

func mustExecutable(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return self
}
