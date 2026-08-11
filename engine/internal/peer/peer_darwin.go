//go:build darwin

package peer

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// kernProcargs2 is KERN_PROCARGS2 from <sys/sysctl.h>; not exported by
// golang.org/x/sys/unix.
const kernProcargs2 = 49

// isSelfImpl uses LOCAL_PEERPID (a Darwin-specific getsockopt on
// AF_UNIX sockets, giving the connecting process's pid with no cooperation
// or truthfulness required from that process) and then resolves that pid's
// executable path via the same kern.procargs2 sysctl `ps` itself uses,
// comparing it to our own os.Executable() by file identity (inode+device,
// not string equality, so a symlink used to launch one side and not the
// other still compares equal).
//
// Verified empirically against a real, separately-exec'd process during
// development: LOCAL_PEERPID returned the true peer pid, and the sysctl
// resolved it to that process's real binary path.
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

	var pid int
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		pid, sockErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if ctrlErr != nil || sockErr != nil {
		return true, false
	}

	peerPath, err := peerExecPath(pid)
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

// peerExecPath resolves pid's executable path via kern.procargs2: argc
// (4 bytes), then the NUL-terminated exec path, then argv/envp. Requires the
// same UID as the target process (or root); a same-user socket peer always
// qualifies.
func peerExecPath(pid int) (string, error) {
	data, err := unix.SysctlRaw("kern", kernProcargs2, pid)
	if err != nil {
		return "", err
	}
	if len(data) < 4 {
		return "", os.ErrInvalid
	}
	rest := data[4:]
	end := 0
	for end < len(rest) && rest[end] != 0 {
		end++
	}
	if end == 0 {
		return "", os.ErrInvalid
	}
	return string(rest[:end]), nil
}
