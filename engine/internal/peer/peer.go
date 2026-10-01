package peer

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Verdict is what the kernel said about the process on the other end of a
// connection, read once.
//
// It carries the reason alongside the answer because a refusal has to say
// which check refused: "that is not Holdcall" and "that is Holdcall, run by
// another user" call for different things from whoever reads the log, and
// telling them apart afterwards from a pid would mean reading the connection a
// second time, which IsSelfPID's doc comment explains is unsafe.
type Verdict struct {
	// Supported is false on platforms with no way to ask the OS this at all
	// (Windows AF_UNIX exposes no peer-credential API, unlike Linux's
	// SO_PEERCRED or Darwin's LOCAL_PEERCRED) -- see peer_windows.go. Callers
	// must decide what "not supported" means for their own request; it is not
	// automatically an allow, and for releasing a secret it is a refusal.
	Supported bool

	// Same is true only when the peer is running this same holdcall binary AND
	// is running as the same user as this process. Never true unless both were
	// established by the kernel; every lookup that fails leaves it false.
	Same bool

	// PID is the peer's, as the kernel reports it, or 0 when that could not be
	// read. Never meaningful without Supported.
	PID int

	// WrongUser is true when the kernel says the peer's effective user id is
	// not ours. PeerUID and SelfUID are set with it, for the sentence a
	// refusal prints; they carry no meaning otherwise.
	//
	// The user is checked before the executable, and a mismatch ends the check.
	// Running this binary is not enough to be trusted with the socket. The
	// socket's directory and mode are what keep other users off it; this is the
	// second check behind them, for the day one of them is wrong -- a directory
	// that was looser than it should be, a socket path in a shared temp
	// directory -- and not a substitute for either.
	WrongUser bool
	PeerUID   int
	SelfUID   int
}

// Verified reports whether the platform could answer and the answer was yes.
// It is the question a secret's release has to ask: not "was the peer refused"
// but "was the peer confirmed", which are different things exactly where the
// platform cannot ask.
func (v Verdict) Verified() bool { return v.Supported && v.Same }

// Reason is the clause a refusal is built from: why the peer is not accepted.
// Empty for a peer that is. Wording only -- the decision was made by Same.
func (v Verdict) Reason() string {
	switch {
	case !v.Supported:
		return "this platform cannot tell who is on the other end of the socket"
	case v.WrongUser:
		return fmt.Sprintf("it runs as uid %d and this process runs as uid %d: Holdcall only talks to processes of its own user",
			v.PeerUID, v.SelfUID)
	case !v.Same:
		return "it is not running this binary"
	}
	return ""
}

// Check asks the OS, not the connecting process, who is on the other end of
// conn: which user it runs as, and whether it is running this same holdcall
// binary.
//
// This is what keeps everything that is not Holdcall off the daemon socket, and
// so away from credential.get and every connector.* operation: socket file
// permissions only prove "same OS user", which is not enough once a
// downstream MCP server -- code the operator did not write and should not be
// assumed to trust -- can open the same socket. A claim inside a Request ("my
// target is X") is exactly as easy for an attacker to write as for the real
// shim, so nothing self-reported can be the basis for a decision here. Peer
// identity, read from the kernel's own connection-tracking state, cannot be
// forged by the connecting process. It is a floor, not the authorization
// model: anything able to execute this binary as this user passes it, which
// is why the connector's registered command, not the caller, decides what a
// secret is handed to.
//
// Both halves come from the kernel and both are required. The user is what
// SO_PEERCRED (linux) and LOCAL_PEERCRED (darwin) report: the peer's
// effective uid when it connected, compared with this process's effective uid.
// The binary is the running image -- see Image.
//
// Not supported is not the same as refused, and callers must say which they
// mean: see Verdict.Supported.
func Check(conn net.Conn) Verdict { return checkImpl(conn) }

// IsSelf is Check reduced to the two booleans older callers use. same means
// the peer runs this binary as this user.
func IsSelf(conn net.Conn) (supported, same bool) {
	v := checkImpl(conn)
	return v.Supported, v.Same
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
// narrowing it. Check exists for the same reason: it reads everything a
// refusal needs in that one look.
//
// pid is 0 exactly when supported is false or the identity check otherwise
// could not resolve one; it is never meaningful on its own without
// supported also being true.
func IsSelfPID(conn net.Conn) (supported, same bool, pid int) {
	v := checkImpl(conn)
	return v.Supported, v.Same, v.PID
}

// PrimeSelf resolves this process's own image identity now, rather than on
// the first connection that gets checked. A daemon calls it before it
// listens.
//
// The order matters on macOS, where the identity is resolved once and
// verified against the file at os.Executable(): a binary rebuilt in place
// before that first resolution makes the file and the running image differ,
// the mechanism is then judged untrustworthy for the life of the process,
// and every later check falls back to comparing launch paths -- which, after
// an in-place upgrade, the new build shares. An old daemon in that state
// accepted the new build's relays as itself, silently, instead of refusing
// them with the F-001 diagnosis (F-026). Resolved at startup, while the path
// still names the running image, the identity is the running image and an
// upgrade after that is seen for what it is.
func PrimeSelf() { primeSelfImpl() }

// ExplainPID says what is known about pid's executable, for the sentence a
// refusal prints: which file it runs and which file this process is, or
// that the path could not be read. It is wording, never a decision: a wrong
// or spoofed answer changes what an operator reads next, not what was
// refused. It exists because a refusal that only said "not Holdcall" left
// an operator, and a CI log, with nothing to tell an impostor from a lookup
// that failed.
func ExplainPID(pid int) string {
	if pid <= 0 {
		return "its pid could not be read"
	}
	path, ok := ExecPathOf(pid)
	self, err := os.Executable()
	switch {
	case !ok && err != nil:
		return fmt.Sprintf("pid %d; its executable path could not be read", pid)
	case !ok:
		return fmt.Sprintf("pid %d; its executable path could not be read; this binary is %s", pid, self)
	case err != nil:
		return fmt.Sprintf("pid %d runs %s", pid, path)
	default:
		return fmt.Sprintf("pid %d runs %s; this binary is %s", pid, path, self)
	}
}

// Diagnosis explains why a peer that already failed IsSelf's check --
// supported=true, same=false -- differs from us. It plays no part in that
// decision and cannot soften it: IsSelf has already refused the connection
// by the time anything calls this (see daemon.go's accept-time check and
// shim.daemonIsGenuine's callers). Its only job is choosing the sentence an
// operator reads next, for F-001: replacing the binary while a daemon runs
// used to leave both sides refusing each other with nothing but "not Holdcall"
// to go on, indistinguishable from a genuine impostor.
//
// Telling SameLaunchPathOlderBuild apart from DifferentBinary needs a launch
// *path*, which is exactly the weaker, spoofable signal the rest of this
// package exists to avoid using for authorization -- see pathswap_test.go
// and image.go's doc comment. That is fine here: nothing Diagnose produces
// ever feeds back into an allow/deny decision, only into text. An attacker
// who wins this comparison is exactly as refused as one who does not; it
// only changes which explanation they, or a genuinely upgraded Holdcall, read.
type Diagnosis int

const (
	// DifferentBinary means the peer's launch path is not our own: not Holdcall,
	// or Holdcall installed somewhere else. This is the ordinary case for an
	// impostor -- see shim.daemonIsGenuine's doc and impostor_test.go -- and
	// the wording stays "is not Holdcall", unchanged from before Diagnose existed.
	DifferentBinary Diagnosis = iota
	// SameLaunchPathOlderBuild means the peer was launched from exactly the
	// path our own binary runs from, but its running image is a different
	// file. That is precisely the shape an in-place upgrade or a `go build`
	// over a running daemon leaves behind (F-001): the old process keeps
	// executing the old, now-unlinked inode while the path now names a new
	// one. The peer is an older build of Holdcall, not an impostor, and the
	// remedy is `holdcall daemon restart`.
	SameLaunchPathOlderBuild
	// DiagnosisUnavailable means neither launch path could be read at all --
	// an unsupported platform (Windows; see image_windows.go) or a lookup
	// that failed for this particular pid (it has already exited, or
	// belongs to a user this process may not inspect). Never reported as
	// SameLaunchPathOlderBuild: a wrong diagnosis here would send an
	// operator to `holdcall daemon restart` a process that was never Holdcall at all.
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
// conn was observed to disagree with itself across that window: holdcall status
// and holdcall doctor briefly reported a genuine upgrade as a plain impostor,
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
