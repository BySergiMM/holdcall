package peer

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestAPathSwapDoesNotDefeatPeerIdentity is the question every peer check has
// to answer: is it comparing the process that is actually running, or a file
// that merely used to be it?
//
// Both implementations must resolve the peer to the image it is *running*,
// never to a name. Linux stats /proc/<pid>/exe, which the kernel resolves to
// the running inode. Darwin reads the vnode behind the peer's own mapping of
// its main image, which the kernel likewise supplies from the mapping rather
// than by re-resolving a path.
//
// The failure this guards against is stat-ing a path string: the file at a
// peer's launch path belongs to the peer, so it can replace it with a link to
// our binary and be compared against the wrong file. This test ran red on
// darwin until the vnode check replaced the path comparison.
//
// No race is required. The attacker launches from a path it owns, replaces
// the file there with a link to the real binary, and only then connects: the
// kernel still reports the original path, and the file at it is now the thing
// being compared against.
func TestAPathSwapDoesNotDefeatPeerIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("peer identity is unsupported on windows by design (see peer_windows.go); IsSelf " +
			"would report supported=false and this test would self-skip anyway once it got a peer " +
			"connected, but doing that depends on nc or python3 being present in this exact way on " +
			"the runner, which is not guaranteed -- skip up front rather than risk a hard failure in " +
			"aForeignProgram for a property this platform cannot demonstrate regardless")
	}
	peerProg, peerArgs := aForeignProgram(t)

	dir, err := os.MkdirTemp("", "nimswap")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// The attacker's path, which it controls: a symlink to some program that
	// is not this binary. Launching through it makes the kernel record this
	// path while the process that runs is genuinely something else. A symlink
	// rather than a copy because macOS will not exec a copy of a system
	// binary.
	attacker := filepath.Join(dir, "peer")
	if err := os.Symlink(peerProg, attacker); err != nil {
		t.Fatalf("linking the attacker path: %v", err)
	}

	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	cmd := exec.Command(attacker, peerArgs(sock)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the attacker: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	// Accept with a bound: if the attacker never connects, the test has proved
	// nothing and must say so rather than hang.
	type accepted struct {
		conn net.Conn
		err  error
	}
	ch := make(chan accepted, 1)
	go func() { c, err := ln.Accept(); ch <- accepted{c, err} }()
	var conn net.Conn
	select {
	case a := <-ch:
		if a.err != nil {
			t.Fatalf("accept: %v", a.err)
		}
		conn = a.conn
	case <-time.After(10 * time.Second):
		t.Skip("the attacker process never connected; nothing was proved")
	}
	t.Cleanup(func() { conn.Close() })

	// Baseline: unmodified, the attacker must be rejected. Without this the
	// test could pass because the check rejects everything.
	supported, same := IsSelf(conn)
	if !supported {
		t.Skip("peer identity is unsupported on this platform")
	}
	if same {
		t.Fatal("baseline is wrong: a copy of nc was accepted as this binary")
	}

	// Re-point the path the attacker was launched from at this binary. The
	// attacker keeps running its own image on the same connection; only the
	// file at its recorded path changes.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if err := os.Remove(attacker); err != nil {
		t.Fatalf("removing the attacker path: %v", err)
	}
	if err := os.Symlink(self, attacker); err != nil {
		t.Fatalf("re-pointing the attacker path at this binary: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	if _, same := IsSelf(conn); same {
		t.Fatal("a process that is not this binary passed peer identity after " +
			"replacing the file at its recorded exec path")
	}
}

// aForeignProgram returns a program that is not this test binary and can hold
// a unix socket connection open, plus how to invoke it against a socket path.
//
// Two candidates rather than one, and a failure rather than a skip if neither
// is present. This test is the only thing that demonstrates the property the
// whole peer check exists for, and a silent skip in CI would mean nobody ever
// finds out it stopped holding. Both are present on the ubuntu and macos
// runners; if that ever changes, the build should say so.
func aForeignProgram(t *testing.T) (string, func(sock string) []string) {
	t.Helper()
	if p, err := exec.LookPath("nc"); err == nil {
		return p, func(sock string) []string { return []string{"-U", sock} }
	}
	if p, err := exec.LookPath("python3"); err == nil {
		script := "import socket,sys,time\n" +
			"s=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)\n" +
			"s.connect(sys.argv[1])\n" +
			"time.sleep(60)\n"
		return p, func(sock string) []string { return []string{"-c", script, sock} }
	}
	t.Fatal("neither nc nor python3 is available, so the peer-identity property " +
		"cannot be demonstrated; this must not pass silently")
	return "", nil
}
