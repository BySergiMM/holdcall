//go:build darwin

package peer

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// acceptOneConn starts a listener at a fresh temp path, spawns client as the
// connecting process, and returns the accepted connection.
func acceptOneConn(t *testing.T, client *exec.Cmd) net.Conn {
	t.Helper()
	path := tempSocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	client.Args = append(client.Args, path)
	if err := client.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	t.Cleanup(func() { client.Process.Kill() })

	ln.(*net.UnixListener).SetDeadline(time.Now().Add(3 * time.Second))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// This is the actual security property Layer 1 exists for: a connection
// from a process that is not this binary must be identified as such by the
// kernel, not by anything the process itself claims.
func TestVerifyPeerIsSelfRejectsADifferentBinary(t *testing.T) {
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not available")
	}
	// nc -U <path>: a real, separate process that is definitely not nim.
	client := exec.Command("nc", "-U")
	conn := acceptOneConn(t, client)

	supported, same := IsSelf(conn)
	if !supported {
		t.Fatal("peer verification should be supported on darwin")
	}
	if same {
		t.Fatal("nc must never be identified as this binary")
	}
}

// And the positive case: a connection from a genuinely separate process
// running the exact same test binary must be identified as self.
func TestVerifyPeerIsSelfAcceptsTheSameBinary(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// Re-exec this same test binary with a flag that makes it just dial the
	// socket and block, instead of running the test suite again.
	client := exec.Command(self, "-test.run=TestHelperProcessDialAndBlock")
	client.Env = append(os.Environ(), "NIM_PEER_TEST_HELPER=1")
	conn := acceptOneConn(t, client)

	supported, same := IsSelf(conn)
	if !supported {
		t.Fatal("peer verification should be supported on darwin")
	}
	if !same {
		t.Fatal("a second instance of this exact binary must be identified as self")
	}
}

// Closing conn before verifyPeerIsSelf runs forces a getsockopt-level
// failure inside raw.Control (Go's *net.UnixConn.SyscallConn() itself does
// not error here -- Close() does not nil out the underlying fd struct, so
// SyscallConn() still succeeds; the failure only surfaces once Control()
// actually tries to use the closed descriptor). This exercises the
// raw.Control error branch, which was already correct before the M3 final
// audit. The audit also found and fixed a second, adjacent bug: the
// SyscallConn()-erroring branch itself returned (false, false) -- "this
// platform cannot check" -- which is wrong on its own terms (darwin can
// check; that is what this whole file is for) and, more importantly,
// authorized() treats supported=false as an automatic allow. No means to
// force *net.UnixConn.SyscallConn() itself to return a non-nil error was
// found, so that fix is defense-in-depth verified by code reading, not by
// an empirical test -- documented here rather than claimed proven.
func TestVerifyPeerIsSelfFailsClosedOnAClosedConnection(t *testing.T) {
	path := tempSocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		c, err := net.Dial("unix", path)
		if err == nil {
			c.Close()
		}
	}()

	ln.(*net.UnixListener).SetDeadline(time.Now().Add(3 * time.Second))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	conn.Close() // forces the subsequent raw.Control call to error

	supported, same := IsSelf(conn)
	if !supported {
		t.Fatal("a Control() error on darwin must be reported as checked (supported=true), not as unsupported -- unsupported is what authorized() treats as an automatic allow")
	}
	if same {
		t.Fatal("a connection this code could not actually check must never be treated as self")
	}
}

// TestHelperProcessDialAndBlock is not a real test: it is re-executed as a
// subprocess by TestVerifyPeerIsSelfAcceptsTheSameBinary via `go test
// -test.run=...`, using this test binary itself as "a separate process
// running the same nim binary" stand-in.
func TestHelperProcessDialAndBlock(t *testing.T) {
	if os.Getenv("NIM_PEER_TEST_HELPER") != "1" {
		t.Skip("not invoked as a helper process")
	}
	path := os.Args[len(os.Args)-1]
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	time.Sleep(2 * time.Second)
}

// tempSocketPath keeps the path short: an AF_UNIX address is capped near 104
// bytes and t.TempDir()'s nested test-name directories can overflow that.
func tempSocketPath(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nimp")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "p.sock")
}
