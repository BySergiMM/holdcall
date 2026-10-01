//go:build unix

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// currentUID is the user this process runs as: the effective uid, which is the
// one that decides what a process may do to a file and the one a directory's
// owner is compared with.
//
// A variable so a test can stand in for a directory that belongs to somebody
// else without having to be root to make one. Nothing outside tests assigns it.
var currentUID = os.Geteuid

// EnsurePrivateDir makes dir a directory only the user running this process can
// use, or says why it cannot be one.
//
// A directory that does not exist is created, parents included, with mode 0700.
// One that does exist must be a directory this user owns -- a symbolic link is
// followed to the directory it stands for, and that one is what is checked --
// and if group or other have any access to it, that access is removed with
// chmod 0700. One that belongs to anybody else is refused with ErrDirNotOurs.
//
// Why the directory and not only what goes in it: os.MkdirAll(dir, 0o700) leaves
// an existing directory exactly as it found it, so a 0755 directory that was
// there first stayed 0755. And the things Holdcall keeps in these directories are
// not created private. A socket is made with whatever the umask leaves it and
// only narrowed afterwards, and SQLite creates the journal with the umask's
// default, usually 0644 -- what keeps other users away from them is the
// directory, so the directory has to be checked rather than assumed.
//
// Once this has returned nil nobody else can reach the directory's entries, nor
// rename or replace the directory itself if it sits in one they cannot write to,
// so what is created inside it afterwards needs no window of care. The one
// directory it cannot protect is a shared one such as /tmp, which is owned by
// root and is refused: see defaultSocket for where the default socket goes
// instead.
//
// Nothing here is for Windows. There a directory's mode is not its permissions
// (see privatedir_other.go).
func EnsurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Stat, not Lstat: a home that is a symlink to another disk is legitimate,
	// and what has to be ours is the directory it leads to. A link planted by
	// somebody else can only lead somewhere we own, where chmod 0700 is
	// harmless, or somewhere we do not, where the owner check below refuses.
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		// Fail closed: a platform that cannot say who owns a directory has not
		// said that it is ours.
		return fmt.Errorf("cannot tell who owns %s on this platform", dir)
	}
	if self := currentUID(); int(st.Uid) != self {
		return fmt.Errorf("%w: %s is owned by uid %d, and this process runs as uid %d",
			ErrDirNotOurs, dir, st.Uid, self)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("%s is open to other users (mode %04o) and could not be made private: %w",
				dir, fi.Mode().Perm(), err)
		}
	}
	return nil
}

// privateToUs reports whether dir exists, belongs to this user and gives nobody
// else any access: a directory the socket can be put in as it is.
func privateToUs(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == currentUID() && fi.Mode().Perm()&0o077 == 0
}

// socketTempDir is where the default socket goes when XDG_RUNTIME_DIR names no
// directory: the temp directory itself if it is already private to this user,
// which macOS's per-user $TMPDIR is, and otherwise a directory of this user's
// own inside it.
//
// The second case is the one that matters. Without XDG_RUNTIME_DIR a Linux temp
// directory is /tmp, which belongs to root and which every local user can
// create entries in, so a socket directly in it could be pre-created, or
// replaced, by anybody -- and a daemon that insisted on owning the directory
// its socket is in could not start there at all. One level down there is a
// directory that can be owned: holdcall-<uid>, created 0700 by EnsurePrivateDir.
// If somebody else got there first, what this user gets is a refusal to start,
// with the reason, never a daemon in a place another user controls.
func socketTempDir() string {
	tmp := os.TempDir()
	if privateToUs(tmp) {
		return tmp
	}
	return filepath.Join(tmp, "holdcall-"+strconv.Itoa(currentUID()))
}
