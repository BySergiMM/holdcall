//go:build unix

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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
// use where that is Holdcall's to decide, and says plainly where it is not.
//
// Three cases, and what separates them is who made the directory:
//
//   - A directory that does not exist is created, parents included, and the
//     directory itself with mode 0700. That one is Holdcall's own.
//   - A directory that exists is checked, and must belong to this user: one that
//     belongs to anybody else is refused with ErrDirNotOurs, because whoever owns a
//     directory can replace what is in it whatever the modes of the things inside
//     say. A symbolic link is followed to the directory it stands for, and that is
//     the one checked -- a home on another disk is legitimate.
//   - If that directory, owned by this user, gives group or other any access, what
//     happens depends on whose it is. Holdcall's own default runtime directory
//     (see defaultRuntimeDir) is tightened with chmod 0700, and must not be a
//     symbolic link: it sits in a directory that everybody can write to, and a link
//     put there could point chmod at a directory of this user's that was never
//     Holdcall's. Any other directory -- one the operator chose, which Holdcall did
//     not make -- is left exactly as it is, and what is said is a warning. The
//     operator who writes socket = ~/holdcall.sock has named their home directory,
//     and taking away the access their other users and groups have to it is not
//     something a tool starting a socket gets to do. chmod can also fail where the
//     mode is not the owner's to set (drvfs, NFS), and a daemon that refused to
//     start over that would be a worse outcome than the one it was avoiding.
//
// What the warning does not mean: the socket is created and then narrowed to
// 0600 whatever its directory, and the daemon refuses every connection from
// another user and every shim that does not verify it (see internal/peer). What
// stays only as private as the directory is whatever else is kept in it, the
// journal included: SQLite creates it with the umask's default, usually 0644.
//
// Nothing here is for Windows. There a directory's mode is not its permissions
// (see privatedir_other.go).
func EnsurePrivateDir(dir string) error {
	dir = filepath.Clean(dir)
	created, err := mkdirLeaf(dir)
	if err != nil {
		return err
	}
	// The directories that are Holdcall's own are the ones it just made and the
	// one it keeps its default socket in. For those, Lstat: a link is refused
	// rather than followed. For the others, Stat -- see above.
	own := created || dir == defaultRuntimeDir()
	stat := os.Stat
	if own {
		stat = os.Lstat
	}
	fi, err := stat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link, and Holdcall's own directory is never one: "+
			"it would lead wherever whoever made the link chose", dir)
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
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		if !own {
			warnOnce(dir, mode)
			return nil
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("%s is open to other users (mode %04o) and could not be made private: %w",
				dir, mode, err)
		}
	}
	return nil
}

// mkdirLeaf creates dir, with its parents, and says whether it was dir that this
// call created: Mkdir on the last element is what tells a directory made here
// from one that was already there, which MkdirAll cannot.
func mkdirLeaf(dir string) (created bool, err error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return false, err
	}
	switch err := os.Mkdir(dir, 0o700); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrExist):
		return false, nil
	default:
		return false, err
	}
}

// warn says something to the operator. A variable so a test can hear it; in
// production it is the log, which for the daemon is its log file and for the
// relay is the stderr the client shows.
var warn = func(format string, args ...any) { log.Printf("holdcall: warning: "+format, args...) }

// warned remembers which directories this process has already complained about:
// EnsureDirs and the daemon's listen both look at the socket's directory, and
// one complaint is the right number.
var warned sync.Map

func warnOnce(dir string, mode os.FileMode) {
	if _, again := warned.LoadOrStore(dir, struct{}{}); again {
		return
	}
	warn("%s can be used by other users (mode %04o). Holdcall did not create it, so it does not change it. "+
		"The socket stays mode 0600 and the daemon verifies who connects, but the journal and anything else "+
		"kept in this directory are only as private as the directory is: chmod 700 %s, "+
		"or point the setting that names it at a directory of its own.", dir, mode, dir)
}

// defaultRuntimeDir is the directory of this user's own that the default socket
// goes in when XDG_RUNTIME_DIR names none and the temp directory is shared. It is
// the only directory outside Holdcall's home that Holdcall treats as its own
// without having created it in this call, because it is the one place where
// something that is not Holdcall has already been told it is Holdcall's.
func defaultRuntimeDir() string {
	return filepath.Join(os.TempDir(), "holdcall-"+strconv.Itoa(currentUID()))
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
	if tmp := os.TempDir(); privateToUs(tmp) {
		return tmp
	}
	return defaultRuntimeDir()
}
