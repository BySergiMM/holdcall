package peer

import (
	"net"
	"os"
	"path/filepath"
)

// verifyPeerIsSelf asks the OS, not the connecting process, whether the
// process on the other end of conn is running this same nim binary.
//
// This is the actual authorization boundary for credential.get and every
// connector.* operation: socket file permissions only prove "same OS user",
// which is not enough once a downstream MCP server -- code the operator did
// not write and should not be assumed to trust -- can open the same socket.
// A claim inside a Request ("my target is X") is exactly as easy for an
// attacker to write as for the real shim, so nothing self-reported can be
// the basis for a decision here. Peer identity, read from the kernel's own
// connection-tracking state, cannot be forged by the connecting process.
//
// supported is false on platforms with no way to ask the OS this at all
// (Windows AF_UNIX exposes no peer-credential API, unlike Linux's
// SO_PEERCRED or Darwin's LOCAL_PEERPID) -- see peer_windows.go. Callers
// must decide what "not supported" means for their own request; it is not
// automatically an allow.
func IsSelf(conn net.Conn) (supported, same bool) {
	supported, same, _ = isSelfImpl(conn)
	return supported, same
}

// IsSelfPID is IsSelf plus the pid the identity decision was made against,
// for a caller that also needs to diagnose a mismatch (see Diagnose and
// DiagnosePID) and must not read the peer's pid from conn a second time to
// get it.
//
// That second read is not merely wasteful: a peer already known to be
// refused -- the only time a diagnosis is ever wanted -- is, in every real
// caller, one that is itself about to close its end (the daemon's own
// accept-time check returns immediately, and a client that has just decided
// daemonIsGenuine is false closes conn right after). A second
// connection-based read raced that close often enough in testing to
// misreport a genuine upgrade as a plain "unverified peer": PIDOf succeeded
// once, then failed moments later on the same conn. Reading the pid once,
// here, and diagnosing from it afterwards with DiagnosePID -- which touches
// only the pid, never conn -- is what removes the race instead of merely
// narrowing it.
//
// pid is 0 exactly when supported is false or the identity check otherwise
// could not resolve one; it is never meaningful on its own without
// supported also being true.
func IsSelfPID(conn net.Conn) (supported, same bool, pid int) {
	return isSelfImpl(conn)
}

// Diagnosis explains why a peer that already failed IsSelf's check --
// supported=true, same=false -- differs from us. It plays no part in that
// decision and cannot soften it: IsSelf has already refused the connection
// by the time anything calls this (see daemon.go's accept-time check and
// shim.daemonIsGenuine's callers). Its only job is choosing the sentence an
// operator reads next, for F-001: replacing the binary while a daemon runs
// used to leave both sides refusing each other with nothing but "not Nim"
// to go on, indistinguishable from a genuine impostor.
//
// Telling SameLaunchPathOlderBuild apart from DifferentBinary needs a launch
// *path*, which is exactly the weaker, spoofable signal the rest of this
// package exists to avoid using for authorization -- see pathswap_test.go
// and image.go's doc comment. That is fine here: nothing Diagnose produces
// ever feeds back into an allow/deny decision, only into text. An attacker
// who wins this comparison is exactly as refused as one who does not; it
// only changes which explanation they, or a genuinely upgraded Nim, read.
type Diagnosis int

const (
	// DifferentBinary means the peer's launch path is not our own: not Nim,
	// or Nim installed somewhere else. This is the ordinary case for an
	// impostor -- see shim.daemonIsGenuine's doc and impostor_test.go -- and
	// the wording stays "is not Nim", unchanged from before Diagnose existed.
	DifferentBinary Diagnosis = iota
	// SameLaunchPathOlderBuild means the peer was launched from exactly the
	// path our own binary runs from, but its running image is a different
	// file. That is precisely the shape an in-place upgrade or a `go build`
	// over a running daemon leaves behind (F-001): the old process keeps
	// executing the old, now-unlinked inode while the path now names a new
	// one. The peer is an older build of Nim, not an impostor, and the
	// remedy is `nim daemon restart`.
	SameLaunchPathOlderBuild
	// DiagnosisUnavailable means neither launch path could be read at all --
	// an unsupported platform (Windows; see image_windows.go) or a lookup
	// that failed for this particular pid (it has already exited, or
	// belongs to a user this process may not inspect). Never reported as
	// SameLaunchPathOlderBuild: a wrong diagnosis here would send an
	// operator to `nim daemon restart` a process that was never Nim at all.
	DiagnosisUnavailable
)

// Diagnose tells SameLaunchPathOlderBuild apart from DifferentBinary for the
// peer on conn. Callers use it only once IsSelf has already found
// supported=true, same=false; called on any other peer it still answers
// honestly, it is simply not the question anything needs asked then.
//
// This touches conn exactly once, to read the peer's pid, and diagnoses from
// that pid rather than from conn itself -- see DiagnosePID. A peer that has
// just failed a peer-identity check (the only time anything calls this) may
// close its end at any moment, once its own mirroring check -- the daemon's
// accept-time refusal, or the client's own daemonIsGenuine -- reaches the
// same conclusion independently. Calling Diagnose more than once on the same
// conn was observed to disagree with itself across that window: nim status
// and nim doctor briefly reported a genuine upgrade as a plain impostor,
// because the second call raced the peer's own close. A caller that needs
// the diagnosis more than once must call this at most once and keep the
// result, or call PIDOf once and reuse DiagnosePID, never call Diagnose
// itself a second time on the same conn.
func Diagnose(conn net.Conn) Diagnosis {
	pid, supported, err := PIDOf(conn)
	if !supported || err != nil {
		return DiagnosisUnavailable
	}
	return DiagnosePID(pid)
}

// DiagnosePID is Diagnose's pid-based core, exported so a caller that
// already has the peer's pid -- from its own PIDOf(conn) call, taken before
// conn's peer had any chance to close -- can diagnose it without touching
// conn a second time. Unlike Diagnose, this never touches the connection at
// all: it only needs the peer *process* to still be inspectable, which
// outlives any one socket the same process happens to hold open.
func DiagnosePID(pid int) Diagnosis {
	self, err := os.Executable()
	if err != nil {
		return DiagnosisUnavailable
	}
	return diagnose(pid, self)
}

// diagnose is Diagnose's testable core, split out because a test cannot make
// os.Executable() report an arbitrary path for the test binary itself, but
// it can supply one directly -- see diagnose_test.go.
//
// Both paths are resolved through symlinks before comparing, the same way
// doctor.go's checkBinaryOnPath already does for "is the binary on PATH the
// one running": best-effort, and a resolution failure just leaves the
// original string to compare, rather than failing the whole diagnosis over
// something that never blocks the identity decision itself.
func diagnose(pid int, self string) Diagnosis {
	peerPath, ok := ExecPathOf(pid)
	if !ok {
		return DiagnosisUnavailable
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if resolved, err := filepath.EvalSymlinks(peerPath); err == nil {
		peerPath = resolved
	}
	if peerPath == self {
		return SameLaunchPathOlderBuild
	}
	return DifferentBinary
}
