//go:build linux

package peer

import (
	"net"
	"os"
	"syscall"
)

// isSelfImpl uses SO_PEERCRED (a standard Linux getsockopt on
// AF_UNIX sockets, giving the connecting process's pid/uid/gid with no
// cooperation or truthfulness required from that process) and then resolves
// that pid's executable via /proc/<pid>/exe, which the kernel itself
// maintains as a symlink to the process's real binary -- not something the
// process can rewrite. Comparison is by file identity (inode+device), not
// string equality, so a symlink used to launch one side and not the other
// still compares equal.
//
// Not run on real Linux in this environment; SO_PEERCRED and /proc/pid/exe
// are long-standing, widely-relied-on kernel interfaces (used by systemd,
// sudo, and most local IPC authentication on Linux), so this is high
// confidence by inspection, not empirical verification here.
func isSelfImpl(conn net.Conn) (supported, same bool, pid int) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return false, false, 0
	}
	// A *net.UnixConn whose SyscallConn() itself errors is not "this platform
	// cannot check" (that is the type-assertion failure above) -- it is a
	// genuine runtime failure on a socket the daemon does support checking.
	// Reporting unsupported here would make authorized() default-allow a
	// connection this code was unable to verify at all; report it as
	// checked-and-failed instead, the same as every other error below.
	raw, err := uc.SyscallConn()
	if err != nil {
		return true, false, 0
	}

	var ucred *syscall.Ucred
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		ucred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if ctrlErr != nil || sockErr != nil {
		return true, false, 0
	}
	pid = int(ucred.Pid)

	// Both sides go through ImageOf, which stats /proc/<pid>/exe itself rather
	// than reading it and stat-ing the string it yields. That is the whole
	// difference between checking the running image and checking a filename:
	// the file at a path belongs to whoever owns the directory, so an earlier
	// readlink-then-stat was defeated with no race at all -- launch from a
	// path you control, replace the file there with a link to nim, connect.
	// Demonstrated in pathswap_test.go, which runs on both unix platforms.
	peerImage, err := ImageOf(pid)
	if err != nil {
		return true, false, pid
	}
	selfImage, err := ImageOf(os.Getpid())
	if err != nil {
		return true, false, pid
	}
	return true, peerImage.Equal(selfImage), pid
}

// pidOfImpl reads the peer's pid from the kernel's own record of the
// connection. SO_PEERCRED is set when the connection is made and cannot be
// influenced by the process on the other end.
func pidOfImpl(conn net.Conn) (int, bool, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false, nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, true, err
	}
	var ucred *syscall.Ucred
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		ucred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if ctrlErr != nil {
		return 0, true, ctrlErr
	}
	if sockErr != nil {
		return 0, true, sockErr
	}
	return int(ucred.Pid), true, nil
}
