//go:build darwin

package peer

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// This file answers, on darwin, the question a peer check exists for: which
// file is a process actually executing?
//
// os.Stat on a path cannot answer it. kern.procargs2 yields the path a process
// was launched from, and the file at that path belongs to whoever owns the
// directory -- so a peer replaces it with a link to our binary, connects, and
// the comparison lands on the wrong file. No race is needed. Linux avoids this
// entirely by stat-ing /proc/<pid>/exe, which the kernel resolves to the inode
// the process is running.
//
// The equivalent here is the vnode behind the process's own mapping of its
// main image. proc_info's PROC_PIDREGIONPATHINFO returns, for the region at a
// given address, a struct vnode_info_path whose vinfo_stat carries vst_dev and
// vst_ino. Those come from the kernel's vnode for the mapped file, not from
// re-resolving a name, so replacing the file at the path does not change them.
//
// Verified, not assumed. Against a live peer (`nc` reached through a symlink
// the test controls):
//
//	before swap: region vnode -> /usr/bin/nc, ino 1152921500312572437
//	after  swap: os.Stat(path) -> OUR ino (this is the bypass)
//	after  swap: region vnode -> /usr/bin/nc, ino unchanged
//
// The address used is 0, which returns the process's lowest mapped region. On
// arm64 that is the main image: __PAGEZERO below it is reserved, and an
// attempt to place a file-backed executable mapping underneath was rejected by
// the kernel at every address tried (EINVAL at 0x1000, ENOMEM above it), so a
// peer cannot relocate what this reads. That was tested rather than reasoned
// about.
//
// What this establishes: the peer's main mapped image is the same file as
// ours, by device and inode. It is executable identity, kernel-provided. It is
// NOT code-signing identity: it says nothing about who signed the binary, and
// a rebuild of Nim is a different inode and so a different process as far as
// this is concerned -- the same property os.SameFile already gave.

// Layout constants.
//
// The flavor and every struct field come from the installed SDK header,
// /usr/include/sys/proc_info.h. The call number does not: PROC_INFO_CALL_PIDINFO
// lives in the kernel-private part of that header and Apple does not ship it,
// so it is the one value here taken from XNU rather than from the SDK. That is
// exactly why selfImage below verifies the whole mechanism against a process
// whose identity we already know before anything is allowed to depend on it.
const (
	procInfoCallPIDInfo   = 2 // PROC_INFO_CALL_PIDINFO
	procPIDRegionPathInfo = 8 // PROC_PIDREGIONPATHINFO

	// sizeof(struct proc_regioninfo): four uint32, one uint64, fourteen
	// uint32, then two uint64 = 16 + 8 + 56 + 16.
	regionInfoLen = 96

	// Offsets into struct vnode_info_path, which follows proc_regioninfo:
	// vinfo_stat begins with vst_dev (uint32) then vst_mode/vst_nlink
	// (uint16 each) then vst_ino (uint64).
	vstDevOffset = regionInfoLen + 0
	vstInoOffset = regionInfoLen + 8

	// sizeof(struct proc_regionwithpathinfo) = proc_regioninfo (96) +
	// vnode_info_path, which is vinfo_stat (136) + vi_type/vi_pad/vi_fsid (16)
	// + vip_path[MAXPATHLEN] (1024) = 1176. Total 1272.
	//
	// Confirmed against the kernel rather than only computed: proc_info
	// returns ENOMEM for a buffer of 1271, and writes exactly 1272 for 1272 or
	// more. The layout above is therefore the one this OS is using.
	regionWithPathLen = 1272

	// What is actually passed. The kernel refuses a buffer smaller than the
	// struct, so asking for more than the size known today means a future
	// macOS that grows the struct still gets a satisfiable request instead of
	// a hard failure -- and a hard failure here would be an outage, since a
	// peer whose image cannot be read is denied. What came back is validated
	// below rather than assumed.
	regionBufLen = 4096
)

// ImageOf returns the identity of the file pid is executing: the vnode behind
// its own mapping of its main image.
//
// It works for any pid the caller is allowed to inspect, not only for socket
// peers, which is what lets the daemon identify the process that spawned a
// relay as well as the relay itself. A pid belonging to another user, or one
// that has exited, yields an error and a zero Image -- never a usable
// identity.
func ImageOf(pid int) (Image, error) {
	buf := make([]byte, regionBufLen)
	n, _, errno := unix.Syscall6(unix.SYS_PROC_INFO,
		procInfoCallPIDInfo, uintptr(pid), procPIDRegionPathInfo,
		0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return Image{}, errno
	}
	// proc_info returns the number of bytes written. Anything shorter than the
	// fields read below means the struct is not the shape this code expects,
	// and must not be read as an identity.
	if int(n) < vstInoOffset+8 {
		return Image{}, fmt.Errorf("proc_info returned %d bytes, too few to contain a vnode identity", n)
	}
	return Image{
		dev: uint64(*(*uint32)(unsafe.Pointer(&buf[vstDevOffset]))),
		ino: *(*uint64)(unsafe.Pointer(&buf[vstInoOffset])),
	}, nil
}

// selfImage is our own running image, resolved once, together with whether the
// mechanism above can be trusted on this system at all.
//
// trustworthy is the guard on an internal ABI. If a future macOS moves a field
// or changes the call number, the values read here stop describing our own
// binary -- and that is detectable, because we know what our own binary is.
// Rather than compare garbage against a peer, the caller falls back to the
// path-based check, which is what this platform did before and is documented
// as weaker. A silently wrong comparison would be worse than a known weak one.
var selfImage struct {
	once        sync.Once
	id          Image
	trustworthy bool
}

func resolveSelfImage() {
	path, err := os.Executable()
	if err != nil {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	got, err := ImageOf(os.Getpid())
	if err != nil {
		return
	}
	// The check that makes the rest safe: the kernel's answer for our own pid
	// has to be our own binary.
	if got.dev != uint64(st.Dev) || got.ino != st.Ino {
		return
	}
	selfImage.id = got
	selfImage.trustworthy = true
}

// selfImageID reports our own image identity, and whether vnode comparison can
// be trusted on this system.
func selfImageID() (Image, bool) {
	selfImage.once.Do(resolveSelfImage)
	return selfImage.id, selfImage.trustworthy
}

// ExecPathOf returns the path pid was launched from, via kern.procargs2 --
// see peerExecPath in peer_darwin.go, which this wraps and which isSelfImpl
// already uses as its own weaker fallback when the vnode mechanism above
// cannot be trusted.
//
// This is a launch path, not a running image: unlike ImageOf, it says
// nothing about what pid is actually executing right now, so it must never
// be used to decide identity -- see Diagnose's doc comment in peer.go, the
// one place this is used, only to tell an in-place upgrade apart from an
// impostor after IsSelf has already decided the connection.
//
// ok is false wherever the pid cannot be inspected at all.
func ExecPathOf(pid int) (path string, ok bool) {
	p, err := peerExecPath(pid)
	if err != nil {
		return "", false
	}
	return p, true
}

// Layout constants for PROC_PIDTBSDINFO, from the same SDK header. Only the
// parent pid is read: struct proc_bsdinfo begins with pbi_flags, pbi_status,
// pbi_xstatus and pbi_pid, four uint32s, so pbi_ppid sits at offset 16.
const (
	procPIDTBSDInfo = 3 // PROC_PIDTBSDINFO
	pbiPPIDOffset   = 16
	bsdInfoBufLen   = 1024 // sizeof(struct proc_bsdinfo) is 232; ask for more, see regionBufLen
)

// ParentOf returns the pid that spawned pid.
//
// This is what makes an agent identity possible without asking anyone to
// declare one: a relay is spawned by the MCP client, so the client's own
// process is the relay's parent, and the daemon can walk from the socket peer
// to its parent and ask what file *that* is executing.
//
// It is read from the kernel, so the process in question has no say in it. It
// is still only a pid: what makes it an identity is passing it to ImageOf,
// which is the part a caller must not skip.
//
// A pid that has exited, or belongs to a user we may not inspect, yields an
// error. So does a parent of 0, which would mean the process has been
// reparented or is one this cannot describe -- treating that as an identity
// would be inventing one.
func ParentOf(pid int) (int, error) {
	buf := make([]byte, bsdInfoBufLen)
	n, _, errno := unix.Syscall6(unix.SYS_PROC_INFO,
		procInfoCallPIDInfo, uintptr(pid), procPIDTBSDInfo,
		0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return 0, errno
	}
	if int(n) < pbiPPIDOffset+4 {
		return 0, fmt.Errorf("proc_info returned %d bytes, too few to contain a parent pid", n)
	}
	ppid := int(*(*uint32)(unsafe.Pointer(&buf[pbiPPIDOffset])))
	if ppid <= 0 {
		return 0, fmt.Errorf("process %d reports no parent", pid)
	}
	return ppid, nil
}
