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

// xucredVersion is XUCRED_VERSION from <sys/ucred.h>, the layout version of
// struct xucred that LOCAL_PEERCRED fills in. Not exported by
// golang.org/x/sys/unix.
const xucredVersion = 0

// checkImpl asks the kernel who is on the other end of conn: which user it
// runs as, and whether it is executing the same file we are.
//
// Who, first. LOCAL_PEERCRED is the Darwin getsockopt behind getpeereid(3),
// which Apple documents as returning "the effective user and group IDs of the
// peer connected to a UNIX-domain socket": for the side that accept()ed, those
// the peer had when it called connect(2); for the side that connect()ed, those
// the listener had when it called listen(2). "This mechanism is reliable;
// there is no way for either side to influence the credentials returned to
// its peer except by calling the appropriate system call ... under different
// effective credentials." (getpeereid(3), developer.apple.com archive.) Go has
// no getpeereid, but x/sys/unix wraps the option it is built on --
// GetsockoptXucred(fd, SOL_LOCAL, LOCAL_PEERCRED) -- and so does this, without
// cgo. The effective uid is compared with this process's own, and a difference
// ends the check: see Verdict.WrongUser. libc's getpeereid refuses a struct
// whose cr_version is not XUCRED_VERSION; so does this.
//
// Then what. LOCAL_PEERPID is a Darwin getsockopt on AF_UNIX sockets giving the
// connecting process's pid, with no cooperation or truthfulness required from
// that process. What to do with that pid is the part that matters.
//
// Two mechanisms, in order of strength:
//
//  1. The vnode of the peer's own mapping of its main image (see
//     image_darwin.go). This is what the process is actually executing, taken
//     from the kernel, and it is not affected by anything done to the path it
//     was launched from. This is the equivalent of linux's /proc/<pid>/exe.
//
//  2. Failing that, the path from kern.procargs2, stat'ed and compared by file
//     identity. This is what this platform did before, and it is weaker in a
//     specific and demonstrated way: the path belongs to the peer, so it can
//     replace the file there with a link to our binary and pass. See
//     pathswap_test.go.
//
// The second is reached only when the first cannot be trusted -- when the
// kernel's answer for our own pid is not our own binary, which is how a change
// in an internal ABI would show up. Falling back is not a silent downgrade: it
// is the previous behaviour, and it is the honest response to a mechanism that
// has just failed to describe something we already know.
func checkImpl(conn net.Conn) Verdict {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Verdict{}
	}
	// A *net.UnixConn whose SyscallConn() itself errors is not "this platform
	// cannot check" (that is the type-assertion failure above) -- it is a
	// genuine runtime failure on a socket we do support checking. Reporting
	// unsupported would make callers default-allow a connection this code was
	// unable to verify at all; report it as checked-and-failed instead, the
	// same as every other error below.
	raw, err := uc.SyscallConn()
	if err != nil {
		return Verdict{Supported: true}
	}

	var pid int
	var cred *unix.Xucred
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		pid, sockErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if sockErr != nil {
			return
		}
		cred, sockErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	})
	if ctrlErr != nil || sockErr != nil {
		return Verdict{Supported: true}
	}
	v := Verdict{Supported: true, PID: pid}
	if cred.Version != xucredVersion {
		// A layout this code does not know is not a uid it can compare.
		return v
	}

	if self := selfUID(); !sameUser(int(cred.Uid), self) {
		v.WrongUser, v.PeerUID, v.SelfUID = true, int(cred.Uid), self
		return v
	}

	if self, trustworthy := selfImageID(); trustworthy {
		peer, err := ImageOf(pid)
		if err != nil {
			// The peer exited, or belongs to another user we cannot inspect.
			// Either way this is not an identity we can confirm.
			return v
		}
		v.Same = peer.Equal(self)
		return v
	}

	v.Same = samePathIdentity(pid)
	return v
}

// samePathIdentity is the pre-vnode comparison, kept only as the fallback
// described above. It compares by inode rather than by string, so a symlink
// used to launch one side and not the other still compares equal -- but the
// inode it reaches is whatever is at the peer's launch path now, which is the
// weakness.
func samePathIdentity(pid int) bool {
	peerPath, err := peerExecPath(pid)
	if err != nil {
		return false
	}
	selfPath, err := os.Executable()
	if err != nil {
		return false
	}
	peerInfo, err := os.Stat(peerPath)
	if err != nil {
		return false
	}
	selfInfo, err := os.Stat(selfPath)
	if err != nil {
		return false
	}
	return os.SameFile(peerInfo, selfInfo)
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

// pidOfImpl reads the peer's pid from the kernel's own record of the
// connection. LOCAL_PEERPID is set when the connection is made and cannot be
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
	var pid int
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		pid, sockErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if ctrlErr != nil {
		return 0, true, ctrlErr
	}
	if sockErr != nil {
		return 0, true, sockErr
	}
	return pid, true, nil
}

// primeSelfImpl resolves selfImage now; see PrimeSelf.
func primeSelfImpl() { selfImageID() }
