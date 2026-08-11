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

	// Stat the magic link itself rather than reading it and stat'ing the
	// string it yields.
	//
	// This is the whole difference between checking the running image and
	// checking a filename. /proc/<pid>/exe is resolved by the kernel to the
	// inode the process is actually executing; a path read out of it is just
	// text, and the file at that path belongs to whoever owns the directory.
	// An earlier version did readlink-then-stat, which a peer defeated
	// without any race at all: launch from a path you control, replace the
	// file there with a link to nim, then connect. The kernel still reports
	// your original path and the stat lands on nim's inode.
	//
	// Demonstrated in pathswap_test.go, which runs on both unix platforms.
	// Darwin reaches the same guarantee by a different route, because it has
	// no /proc -- see image_darwin.go.
	peerInfo, err := os.Stat(fmt.Sprintf("/proc/%d/exe", ucred.Pid))
	if err != nil {
		return true, false
	}
	// /proc/self/exe for the same reason: os.Executable() also returns a
	// path, and comparing a kernel-resolved inode against a path-resolved one
	// would reintroduce the problem on our own side.
	selfInfo, err := os.Stat("/proc/self/exe")
	if err != nil {
		return true, false
	}
	return true, os.SameFile(peerInfo, selfInfo)
}
