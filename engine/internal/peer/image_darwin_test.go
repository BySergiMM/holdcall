//go:build darwin

package peer

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// The self-check is what makes the rest of image_darwin.go safe to rely on: it
// takes an internal ABI and turns it into something verified at runtime
// against a process whose identity is already known. If it ever stops holding
// on a healthy system, everything downstream silently falls back to the weaker
// path comparison, so it is worth asserting directly.
func TestSelfImageResolvesToOurOwnBinary(t *testing.T) {
	id, trustworthy := selfImageID()
	if !trustworthy {
		t.Fatal("the vnode mechanism did not resolve our own pid to our own binary")
	}

	path, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if id.dev != uint32(st.Dev) || id.ino != st.Ino {
		t.Fatalf("self image dev=%d ino=%d, but our binary is dev=%d ino=%d",
			id.dev, id.ino, st.Dev, st.Ino)
	}
}

// Every way of failing to read a peer's image must produce an error, never a
// zero identity -- because a zero identity that compared equal to anything
// would be an allow. isSelfImpl turns each of these into a denial.
func TestUnreadablePidsYieldNoIdentity(t *testing.T) {
	self, ok := selfImageID()
	if !ok {
		t.Skip("vnode identity unavailable")
	}

	// A process that has exited. Its pid was valid moments ago, which is the
	// case a peer disconnecting mid-check produces.
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	pid := cmd.Process.Pid
	if _, err := regionImage(pid); err != nil {
		t.Fatalf("a live process should be readable: %v", err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	deadline := time.Now().Add(3 * time.Second)
	var dead imageID
	var derr error
	for time.Now().Before(deadline) {
		dead, derr = regionImage(pid)
		if derr != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if derr == nil {
		t.Error("a dead pid returned an identity")
	}
	if dead == self {
		t.Fatal("a dead pid compared equal to our own image")
	}

	// Pids we are not allowed to inspect (root-owned), and one that does not
	// exist. A peer running as another user lands in the first case.
	for _, pid := range []int{0, 1, 999999} {
		id, err := regionImage(pid)
		if err == nil && id == self {
			t.Fatalf("pid %d compared equal to our own image", pid)
		}
	}

	// The zero value must never be mistaken for a real identity.
	if (imageID{}) == self {
		t.Fatal("the zero identity equals our own image")
	}
}

// A live process that is not us must not compare equal to us. This is the
// ordinary rejection the whole check exists to make, separate from the path
// swap that pathswap_test.go covers.
func TestAForeignLiveProcessIsNotUs(t *testing.T) {
	self, ok := selfImageID()
	if !ok {
		t.Skip("vnode identity unavailable")
	}
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	other, err := regionImage(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("reading a live foreign process: %v", err)
	}
	if other == self {
		t.Fatal("/bin/sleep compared equal to this test binary")
	}
}
