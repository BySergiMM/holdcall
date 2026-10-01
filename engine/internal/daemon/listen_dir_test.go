//go:build unix

package daemon

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/BySergiMM/holdcall/engine/internal/config"
)

// A socket directory that was already there, and that other users could reach,
// used to stay that way: MkdirAll(dir, 0o700) does nothing to a directory that
// exists. Other users could then list and enter the directory the socket sits in,
// and the socket itself was only narrowed to 0600 after it was bound.
func TestListenNarrowsAPreExistingSocketDirectory(t *testing.T) {
	dir, err := os.MkdirTemp("", "hcsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil { // what a directory somebody else made looks like
		t.Fatal(err)
	}
	path := filepath.Join(dir, "d.sock")

	ln, err := listen(path)
	if err != nil {
		t.Fatalf("listen in a directory of ours that others could reach: %v", err)
	}
	defer ln.Close()

	if got := modeOfPath(t, dir); got != 0o700 {
		t.Errorf("the socket's directory is %04o after listen, want 0700: other users can still reach it", got)
	}
	if got := modeOfPath(t, path); got != 0o600 {
		t.Errorf("the socket is %04o, want 0600", got)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the socket cannot be dialled by its own user: %v", err)
	}
	conn.Close()
}

// A directory another user owns is one in which that user can replace the
// socket whatever its mode, and a shared directory such as /tmp is one. The
// daemon does not start there; it says why.
//
// / is the directory used, because it belongs to somebody else on every system
// this runs on and listen refuses before it creates anything in it. Where it
// does not -- running as root -- there is nothing to refuse, and the test does
// not go looking for a directory to take away from its owner.
func TestListenRefusesASocketDirectoryAnotherUserOwns(t *testing.T) {
	notOurs := directoryNotOurs(t)
	path := filepath.Join(notOurs, "holdcall-must-never-be-created.sock")

	ln, err := listen(path)
	if err == nil {
		ln.Close()
		os.Remove(path)
		t.Fatalf("listen bound %s in a directory another user owns", path)
	}
	if !errors.Is(err, config.ErrDirNotOurs) {
		t.Errorf("listen refused, but not for the reason: %v", err)
	}
	for _, leftover := range []string{path, path + ".lock"} {
		if _, err := os.Lstat(leftover); err == nil {
			os.Remove(leftover)
			t.Errorf("%s was created before the directory was checked", leftover)
		}
	}
}

// The daemon as a whole, not only listen: with a configuration that puts the
// socket in somebody else's directory it returns the reason and opens no
// journal.
func TestTheDaemonDoesNotStartWithItsSocketInADirectoryAnotherUserOwns(t *testing.T) {
	notOurs := directoryNotOurs(t)
	home := t.TempDir()
	t.Setenv(config.HomeEnvVar, home)
	cfg := config.Config{Daemon: config.Daemon{
		Socket:  filepath.Join(notOurs, "holdcall-must-never-be-created.sock"),
		DataDir: filepath.Join(home, "data"),
	}}

	err := RunWithStop(cfg, make(chan struct{}))
	if !errors.Is(err, config.ErrDirNotOurs) {
		t.Fatalf("RunWithStop = %v, want a refusal because the socket directory belongs to another user", err)
	}
	if _, statErr := os.Stat(cfg.DatabasePath()); statErr == nil {
		t.Error("a journal was opened although the daemon refused to start")
	}
}

// directoryNotOurs returns a directory that belongs to another user on this
// system, or skips. / is root's everywhere; if it is ours, this process is
// root and there is no safe directory to use.
func directoryNotOurs(t *testing.T) string {
	t.Helper()
	fi, err := os.Stat("/")
	if err != nil {
		t.Skipf("cannot stat /: %v", err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) == os.Geteuid() {
		t.Skip("/ belongs to the user running this test, so it cannot stand in for somebody else's directory")
	}
	return "/"
}

func modeOfPath(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}
