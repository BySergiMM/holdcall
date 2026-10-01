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

// A socket directory that was already there is the operator's, not the
// daemon's. socket = ~/holdcall.sock makes the home directory the socket's
// directory, and a daemon that narrowed it to 0700 would have taken away the
// access of the user's other users and groups to their home -- and one that
// stopped because a chmod failed (drvfs, NFS) would not have started at all. The
// socket is private by its own mode and the peer check, which is what covers it.
func TestListenLeavesAPreExistingSocketDirectoryAsItIs(t *testing.T) {
	dir, err := os.MkdirTemp("", "hcsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil { // what the operator's own directory looks like
		t.Fatal(err)
	}
	path := filepath.Join(dir, "d.sock")

	ln, err := listen(path)
	if err != nil {
		t.Fatalf("listen in a directory of ours that others could reach: %v", err)
	}
	defer ln.Close()

	if got := modeOfPath(t, dir); got != 0o755 {
		t.Errorf("the socket's directory is %04o after listen; it existed at 0755 and is not the daemon's to change", got)
	}
	if got := modeOfPath(t, path); got != 0o600 {
		t.Errorf("the socket is %04o, want 0600: it is what keeps other users out of a directory they can enter", got)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the socket cannot be dialled by its own user: %v", err)
	}
	conn.Close()
}

// What the daemon does make, it makes private: a socket directory that is not
// there yet is created 0700 however loose the directory around it is.
func TestListenCreatesAMissingSocketDirectoryPrivate(t *testing.T) {
	parent, err := os.MkdirTemp("", "hcsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "run")
	path := filepath.Join(dir, "d.sock")

	ln, err := listen(path)
	if err != nil {
		t.Fatalf("listen with a socket directory that does not exist yet: %v", err)
	}
	defer ln.Close()

	if got := modeOfPath(t, dir); got != 0o700 {
		t.Errorf("the directory listen created is %04o, want 0700", got)
	}
	if got := modeOfPath(t, parent); got != 0o755 {
		t.Errorf("the parent that was already there was changed from 0755 to %04o", got)
	}
	if got := modeOfPath(t, path); got != 0o600 {
		t.Errorf("the socket is %04o, want 0600", got)
	}
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
