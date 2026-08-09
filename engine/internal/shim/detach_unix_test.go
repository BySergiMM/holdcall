//go:build unix

package shim

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// This is the M2 property under test: a process started with
// daemonSysProcAttr() must not land in whatever process group its parent
// happens to be in, because that group is exactly what a terminal SIGINT or
// SIGHUP targets when a client's session ends.
//
// A plain child with no SysProcAttr inherits its parent's process group --
// that is the counterfactual this guards against, checked explicitly so the
// test cannot pass by accident if the assumption stops holding. A child
// started with Setsid always becomes the leader of a brand new session, so
// its group can never collide with one a later signal is sent to.
func TestDaemonSysProcAttrStartsANewSession(t *testing.T) {
	ownPgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("getpgid(self): %v", err)
	}

	plain := exec.Command("sleep", "5")
	if err := plain.Start(); err != nil {
		t.Fatalf("start plain child: %v", err)
	}
	defer plain.Wait() // reap only after Kill below has run (defers are LIFO)
	defer plain.Process.Kill()

	plainPgid, err := syscall.Getpgid(plain.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid(plain child): %v", err)
	}
	if plainPgid != ownPgid {
		t.Fatalf("test assumption broken: a plain child landed in group %d, not the test's own group %d", plainPgid, ownPgid)
	}

	daemon := exec.Command("sleep", "5")
	daemon.SysProcAttr = daemonSysProcAttr() // the code under test
	if err := daemon.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer daemon.Wait() // reap only after Kill below has run (defers are LIFO)
	defer daemon.Process.Kill()

	daemonPgid, err := syscall.Getpgid(daemon.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid(daemon): %v", err)
	}
	if daemonPgid == ownPgid {
		t.Error("daemon shares the test's process group -- a session-ending signal to that group would kill it")
	}
	if daemonPgid != daemon.Process.Pid {
		t.Errorf("daemon is not its own session leader: pgid %d, pid %d", daemonPgid, daemon.Process.Pid)
	}
}
