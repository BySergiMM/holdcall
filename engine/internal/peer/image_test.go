//go:build unix

package peer

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// A zero Image is what every failure path leaves behind, so it must never
// compare equal to anything -- including another zero. A caller that forgets
// to check an error must not thereby discover that two unknowns match.
func TestAZeroImageEqualsNothing(t *testing.T) {
	var zero Image
	if !zero.IsZero() {
		t.Fatal("the zero value does not report itself as zero")
	}
	if zero.Equal(zero) {
		t.Error("a zero Image compared equal to itself")
	}
	if zero.Equal(NewImage(1, 2)) || NewImage(1, 2).Equal(zero) {
		t.Error("a zero Image compared equal to a real one")
	}
	if NewImage(0, 0) != zero {
		t.Error("NewImage(0,0) produced something other than the zero Image")
	}
}

func TestImageEqualityIsByDeviceAndInode(t *testing.T) {
	a := NewImage(10, 20)
	if !a.Equal(NewImage(10, 20)) {
		t.Error("identical device and inode did not compare equal")
	}
	if a.Equal(NewImage(10, 21)) {
		t.Error("a different inode compared equal")
	}
	if a.Equal(NewImage(11, 20)) {
		t.Error("a different device compared equal")
	}
	if a.Dev() != 10 || a.Ino() != 20 {
		t.Errorf("accessors returned dev=%d ino=%d, want 10/20", a.Dev(), a.Ino())
	}
}

// ImageOf must agree with the operating system about what this process is
// running. This is the check that would catch a wrong struct offset or a
// changed ABI on darwin, and a wrong /proc path on linux.
func TestImageOfSelfMatchesOurOwnBinary(t *testing.T) {
	got, err := ImageOf(os.Getpid())
	if err != nil {
		t.Fatalf("ImageOf(self): %v", err)
	}
	if got.IsZero() {
		t.Fatal("ImageOf(self) returned a zero identity with no error")
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	fi, err := os.Stat(self)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Compared through os.SameFile rather than by number, so this test does
	// not depend on how a platform packs a device id.
	same, err := sameFileAsImage(fi, got)
	if err != nil {
		t.Skipf("cannot compare on this platform: %v", err)
	}
	if !same {
		t.Fatalf("ImageOf(self) = dev %d ino %d, which is not %s", got.Dev(), got.Ino(), self)
	}
}

// A live process running a different program must not compare equal to us.
// Without this, a check that returned a constant would pass everything above.
func TestImageOfAForeignProcessDiffersFromOurs(t *testing.T) {
	self, err := ImageOf(os.Getpid())
	if err != nil {
		t.Fatalf("ImageOf(self): %v", err)
	}

	cmd := exec.Command(sleepProgram, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a foreign process: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	other, err := ImageOf(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("ImageOf(foreign): %v", err)
	}
	if other.Equal(self) {
		t.Fatalf("%s compared equal to this test binary", sleepProgram)
	}
}

// Every way of failing must produce an error and a zero Image, because a
// non-zero value that slipped through would be an identity nobody established.
func TestImageOfUnreadablePidsYieldNoIdentity(t *testing.T) {
	self, err := ImageOf(os.Getpid())
	if err != nil {
		t.Fatalf("ImageOf(self): %v", err)
	}

	cmd := exec.Command(sleepProgram, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	pid := cmd.Process.Pid
	cmd.Process.Kill()
	cmd.Wait()

	deadline := time.Now().Add(3 * time.Second)
	var dead Image
	var derr error
	for time.Now().Before(deadline) {
		dead, derr = ImageOf(pid)
		if derr != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if derr == nil {
		t.Error("a dead pid returned an identity")
	}
	if !dead.IsZero() {
		t.Errorf("a failed lookup returned a non-zero Image: dev=%d ino=%d", dead.Dev(), dead.Ino())
	}
	if dead.Equal(self) {
		t.Fatal("a dead pid compared equal to our own image")
	}

	// A pid that cannot exist, and pids owned by root that we may not inspect.
	for _, p := range []int{-1, 0, 1, 999999} {
		id, err := ImageOf(p)
		if err == nil && id.Equal(self) {
			t.Fatalf("pid %d compared equal to our own image", p)
		}
		if err != nil && !id.IsZero() {
			t.Errorf("pid %d failed but returned a non-zero Image", p)
		}
	}
}

// ParentOf is the other half of deriving an agent: a relay is spawned by the
// client, so the client is the relay's parent. A process we spawn ourselves
// must report us as its parent.
func TestParentOfAChildIsUs(t *testing.T) {
	cmd := exec.Command(sleepProgram, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a child: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	ppid, err := ParentOf(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("ParentOf(child): %v", err)
	}
	if ppid != os.Getpid() {
		t.Fatalf("child reports parent %d, want %d", ppid, os.Getpid())
	}

	// And the whole walk: from a pid to the image of whatever spawned it.
	// This is exactly what the daemon will do with a socket peer.
	parentImage, err := ImageOf(ppid)
	if err != nil {
		t.Fatalf("ImageOf(parent): %v", err)
	}
	self, err := ImageOf(os.Getpid())
	if err != nil {
		t.Fatalf("ImageOf(self): %v", err)
	}
	if !parentImage.Equal(self) {
		t.Fatal("walking child -> parent -> image did not arrive back at this binary")
	}
}

func TestParentOfUnreadablePidsFail(t *testing.T) {
	cmd := exec.Command(sleepProgram, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	pid := cmd.Process.Pid
	cmd.Process.Kill()
	cmd.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := ParentOf(pid); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ppid, err := ParentOf(pid); err == nil {
		t.Errorf("a dead pid reported a parent: %d", ppid)
	}
	for _, p := range []int{-1, 999999} {
		if ppid, err := ParentOf(p); err == nil {
			t.Errorf("pid %d reported a parent: %d", p, ppid)
		}
	}
}
