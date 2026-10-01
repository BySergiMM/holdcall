//go:build unix

package peer

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The comparison itself, apart from the syscall that feeds it.
func TestTheUserComparisonMatchesOnlyTheSameUser(t *testing.T) {
	cases := []struct {
		peer, self int
		want       bool
	}{
		{1000, 1000, true},
		{0, 0, true},
		{1001, 1000, false},
		{0, 1000, false}, // root is not us either
		{1000, 0, false},
		{-1, -1, false}, // two unknowns are not the same user
		{-1, 1000, false},
		{1000, -1, false},
		// What an unmapped uid looks like once widened from uid_t.
		{4294967295, 1000, false},
		{4294967295, 4294967295, true}, // a number is a number; only negatives are "unknown"
	}
	for _, c := range cases {
		if got := sameUser(c.peer, c.self); got != c.want {
			t.Errorf("sameUser(%d, %d) = %v, want %v", c.peer, c.self, got, c.want)
		}
	}
}

// TestAPeerRunningAsAnotherUserIsRefusedEvenIfItRunsThisBinary is the property
// the uid check adds: the image check says a peer runs this binary, and on its
// own that is also true of anyone else who can execute it.
//
// The peer here is a real second process running this very test binary, so the
// uid and the image both come from the kernel for a real connection. What the
// test changes is only which user the *daemon side* believes it is -- the
// comparison's other operand -- so that the same peer, the same connection and
// the same kernel answer are first accepted and then refused for the user alone.
// TestAPeerThatReallyRunsAsAnotherUserIsRefused does it with a real second uid
// where the test is allowed to become root.
func TestAPeerRunningAsAnotherUserIsRefusedEvenIfItRunsThisBinary(t *testing.T) {
	conn := acceptFromAHelperProcess(t, mustExecutable(t), nil)

	// The baseline is what makes the refusal below mean something: without it
	// this test would pass on a check that refused every peer.
	v := Check(conn)
	if !v.Supported {
		t.Fatal("peer verification should be supported on this platform")
	}
	if v.WrongUser || !v.Same || !v.Verified() {
		t.Fatalf("a second process of this binary, as this user, was not accepted: %+v (%s)", v, v.Reason())
	}
	realUID := os.Geteuid()
	if v.PID == 0 {
		t.Error("the verdict carries no pid for a peer it accepted")
	}

	selfUID = func() int { return realUID + 1 }
	t.Cleanup(func() { selfUID = os.Geteuid })

	v = Check(conn)
	if !v.Supported {
		t.Fatal("a refusal for the wrong user must still be a checked answer, not an unsupported one: " +
			"unsupported is what callers treat as not-a-denial")
	}
	if v.Same || v.Verified() {
		t.Fatalf("a peer whose user is not ours was accepted because it runs this binary: %+v", v)
	}
	if !v.WrongUser {
		t.Errorf("the refusal did not say it was about the user: %+v", v)
	}
	if v.PeerUID != realUID || v.SelfUID != realUID+1 {
		t.Errorf("verdict says peer uid %d and own uid %d, want %d (what the kernel reported) and %d",
			v.PeerUID, v.SelfUID, realUID, realUID+1)
	}
	if want := "uid " + strconv.Itoa(realUID); !strings.Contains(v.Reason(), want) {
		t.Errorf("Reason() = %q, want it to name %q", v.Reason(), want)
	}
	if supported, same, _ := IsSelfPID(conn); !supported || same {
		t.Errorf("IsSelfPID = (%v, %v), want (true, false): the older interface must refuse too", supported, same)
	}
	if supported, same := IsSelf(conn); !supported || same {
		t.Errorf("IsSelf = (%v, %v), want (true, false)", supported, same)
	}
}

// With a second real uid there is nothing to stand in for: the peer is this
// binary -- a hard link to the same inode, so its image is ours by the kernel's
// own account -- started as another user, and only the user differs.
//
// It needs root to start a process as someone else. Everywhere else, including
// CI, it skips and says why; the test above runs there and covers the same
// code path with the kernel's real answer for the peer.
func TestAPeerThatReallyRunsAsAnotherUserIsRefused(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("starting a process as another user needs root; " +
			"TestAPeerRunningAsAnotherUserIsRefusedEvenIfItRunsThisBinary covers the comparison without it")
	}
	const other = 65534 // nobody on every system this runs on; an unmapped number would do as well

	self := mustExecutable(t)
	dir, err := os.MkdirTemp("", "hcuid")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A hard link, not a copy: the same inode, so if the peer is refused it is
	// not because this is a different file. Reachable by the other user, which
	// the directory the test binary was built in may not be.
	link := filepath.Join(dir, "peer.test")
	if err := os.Link(self, link); err != nil {
		t.Skipf("cannot hard-link the test binary into a directory the other user can reach: %v", err)
	}

	conn := acceptFromAHelperProcess(t, link, func(cmd *exec.Cmd, sock string) {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: other, Gid: other}}
		// Connecting to a socket needs write permission on it and search
		// permission on every directory above it, and this one was created by
		// root, in a private directory, under its umask.
		for path, mode := range map[string]os.FileMode{filepath.Dir(sock): 0o755, sock: 0o666} {
			if err := os.Chmod(path, mode); err != nil {
				t.Fatalf("opening %s to the other user: %v", path, err)
			}
		}
	})

	v := Check(conn)
	if !v.Supported {
		t.Fatal("peer verification should be supported on this platform")
	}
	if !v.WrongUser || v.Same || v.Verified() {
		t.Fatalf("a peer running as uid %d was accepted by a daemon running as uid %d: %+v", other, os.Geteuid(), v)
	}
	if v.PeerUID != other || v.SelfUID != 0 {
		t.Errorf("verdict says peer uid %d and own uid %d, want %d and 0", v.PeerUID, v.SelfUID, other)
	}

	// And it was the user alone: the peer's image is this binary's.
	peerImage, err := ImageOf(v.PID)
	if err != nil {
		t.Fatalf("reading the peer's image: %v", err)
	}
	selfImage, err := ImageOf(os.Getpid())
	if err != nil {
		t.Fatalf("reading our own image: %v", err)
	}
	if !peerImage.Equal(selfImage) {
		t.Fatalf("test setup is wrong: the peer does not run this binary (%v vs %v), so the refusal proves nothing about the user",
			peerImage, selfImage)
	}
}

// acceptFromAHelperProcess listens on a fresh private socket, starts binary as
// a peer that connects to it and waits, and returns the accepted connection.
// customise may adjust the command, and is given the socket path, before it is
// started. Everything is cleaned up with the test.
func acceptFromAHelperProcess(t *testing.T, binary string, customise func(cmd *exec.Cmd, sock string)) net.Conn {
	t.Helper()
	// Short, for the same reason every socket in this repository's tests is: an
	// AF_UNIX path is capped near 104 bytes and t.TempDir() is not short.
	dir, err := os.MkdirTemp("", "hcu")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	cmd := exec.Command(binary, "-test.run=^TestPeerUIDHelperProcess$")
	cmd.Env = append(os.Environ(), "HOLDCALL_PEER_UID_HELPER="+sock)
	if customise != nil {
		customise(cmd, sock)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the peer: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	ln.(*net.UnixListener).SetDeadline(time.Now().Add(10 * time.Second))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("the peer never connected: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// TestPeerUIDHelperProcess is not a test. It is what the tests above start as
// their peer -- this same binary, re-executed -- and does nothing unless it is
// asked to: it dials the socket it was given and waits to be killed.
func TestPeerUIDHelperProcess(t *testing.T) {
	sock := os.Getenv("HOLDCALL_PEER_UID_HELPER")
	if sock == "" {
		t.Skip("not started as a helper process")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	time.Sleep(30 * time.Second)
}
