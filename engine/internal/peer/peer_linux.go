//go:build linux

package peer

import (
	"fmt"
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
func isSelfImpl(conn net.Conn) (supported, same bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return false, false
	}
	// A *net.UnixConn whose SyscallConn() itself errors is not "this platform
	// cannot check" (that is the type-assertion failure above) -- it is a
	// genuine runtime failure on a socket the daemon does support checking.
	// Reporting unsupported here would make authorized() default-allow a
	// connection this code was unable to verify at all; report it as
	// checked-and-failed instead, the same as every other error below.
	raw, err := uc.SyscallConn()
	if err != nil {
		return true, false
	}

	var ucred *syscall.Ucred
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		ucred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if ctrlErr != nil || sockErr != nil {
		return true, false
	}

	peerPath, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", ucred.Pid))
	if err != nil {
		return true, false
	}
	selfPath, err := os.Executable()
	if err != nil {
		return true, false
	}

	peerInfo, err := os.Stat(peerPath)
	if err != nil {
		return true, false
	}
	selfInfo, err := os.Stat(selfPath)
	if err != nil {
		return true, false
	}
	return true, os.SameFile(peerInfo, selfInfo)
}
