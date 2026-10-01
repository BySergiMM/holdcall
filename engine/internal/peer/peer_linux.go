//go:build linux

package peer

import (
	"net"
	"os"
	"syscall"
)

// checkImpl uses SO_PEERCRED (a standard Linux getsockopt on
// AF_UNIX sockets, giving the connecting process's pid, uid and gid with no
// cooperation or truthfulness required from that process) and then resolves
// that pid's executable via /proc/<pid>/exe, which the kernel itself
// maintains as a symlink to the process's real binary -- not something the
// process can rewrite. Comparison is by file identity (inode+device), not
// string equality, so a symlink used to launch one side and not the other
// still compares equal.
//
// The uid is compared first and a difference ends the check: see
// Verdict.WrongUser. unix(7) describes SO_PEERCRED as returning "the credentials
// of the peer process connected to this socket", and says they are "those that
// were in effect at the time of the call to connect(2), listen(2), or
// socketpair(2)": for the side that accepted, the connecting process's; for the
// side that connected, the listener's. unix(7) does not say which of a
// process's uids that is. The kernel's cred_to_ucred (net/core/sock.c) fills
// ucred.uid from cred->euid, so it is the effective uid, and it is compared
// with this process's effective uid. The peer cannot change it after the fact,
// and it is not something a peer can claim: there is no request field for it.
//
// Exercised for real on linux by TestAPeerRunningAsAnotherUserIsRefusedEvenIfItRunsThisBinary
// (peer_uid_test.go) and by the pathswap and daemon tests; SO_PEERCRED and
// /proc/pid/exe are long-standing, widely-relied-on kernel interfaces (used
// by systemd, sudo, and most local IPC authentication on Linux).
func checkImpl(conn net.Conn) Verdict {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Verdict{}
	}
	// A *net.UnixConn whose SyscallConn() itself errors is not "this platform
	// cannot check" (that is the type-assertion failure above) -- it is a
	// genuine runtime failure on a socket the daemon does support checking.
	// Reporting unsupported here would make a caller default-allow a
	// connection this code was unable to verify at all; report it as
	// checked-and-failed instead, the same as every other error below.
	raw, err := uc.SyscallConn()
	if err != nil {
		return Verdict{Supported: true}
	}

	var ucred *syscall.Ucred
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		ucred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if ctrlErr != nil || sockErr != nil {
		return Verdict{Supported: true}
	}
	v := Verdict{Supported: true, PID: int(ucred.Pid)}

	if self := selfUID(); !sameUser(int(ucred.Uid), self) {
		v.WrongUser, v.PeerUID, v.SelfUID = true, int(ucred.Uid), self
		return v
	}

	// Both sides go through ImageOf, which stats /proc/<pid>/exe itself rather
	// than reading it and stat-ing the string it yields. That is the whole
	// difference between checking the running image and checking a filename:
	// the file at a path belongs to whoever owns the directory, so an earlier
	// readlink-then-stat was defeated with no race at all -- launch from a
	// path you control, replace the file there with a link to holdcall, connect.
	// Demonstrated in pathswap_test.go, which runs on both unix platforms.
	peerImage, err := ImageOf(v.PID)
	if err != nil {
		return v
	}
	selfImage, err := ImageOf(os.Getpid())
	if err != nil {
		return v
	}
	v.Same = peerImage.Equal(selfImage)
	return v
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

// primeSelfImpl has nothing to resolve ahead of time on this platform: the
// self check reads the running image from the kernel on every call, or is
// unsupported outright. See PrimeSelf.
func primeSelfImpl() {}
