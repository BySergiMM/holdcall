//go:build windows

package peer

import "net"

// checkImpl always reports unsupported: Holdcall asks nothing of the peer of a
// Windows AF_UNIX connection. docs/security.md, "Windows is experimental", is the
// table of what that leaves enforced and what it does not; this is why the
// check is absent, and what was and was not looked at.
//
// What the platform offers. afunix.sys has no peer-credentials call equivalent
// to Linux's SO_PEERCRED or Darwin's LOCAL_PEERCRED: Microsoft's announcement of
// AF_UNIX lists ancillary data, which is what SCM_CREDENTIALS travels in, as
// unsupported (https://devblogs.microsoft.com/commandline/af_unix-comes-to-windows/).
// It does have the equivalent of Darwin's LOCAL_PEERPID: the
// SIO_AF_UNIX_GETPEERPID control code returns the peer's process id. It is
// defined in mingw-w64's afunix.h, reported working on Windows 10 1903 to 2004
// in https://github.com/microsoft/WSL/issues/4676, and not listed in
// Microsoft's Winsock IOCTL reference. An earlier version of this comment, and
// of docs/security.md, said no such thing existed; that was wrong, and what
// follows is what remains true of it.
//
// Nothing here uses that pid. A user and an executable identity would have to be
// derived from it (OpenProcess and a token's user SID for the first;
// QueryFullProcessImageName and GetFileInformationByHandle's volume serial and
// file index for the second, which is a path-based lookup of the weaker kind
// pathswap_test.go is about), and that would be written without a Windows
// machine to run it on, which is how an identity check that reads as working
// without being one gets written. The other candidates, and why each was set
// aside when this file was first written:
//
//   - Named pipes (GetNamedPipeClientProcessId + a per-pipe DACL) are the
//     mechanism that most directly matches darwin/linux's guarantee: kernel-
//     verified PID, then the same open-process/compare-exe-identity check
//     peer_darwin.go and peer_linux.go already do. It is a real architecture
//     change, though, not a fix: Holdcall deliberately uses one unix-socket
//     transport on all three platforms (see docs/milestones.md, M1's Standing
//     Decision) specifically to avoid needing go-winio, and that decision
//     reached every caller of this socket -- the shim, the CLI's
//     dialConnectorDaemon, StartDaemon's detachment logic -- not just this
//     file. M1 made that call before M3's threat model (a potentially-malicious
//     downstream MCP server on the same socket) existed to weigh against it.
//     Reversing it is a product-shaping decision for the architect, not a fix.
//   - An ephemeral capability/token issued by the daemon does not actually
//     solve this on its own: it only moves the question to "how does the
//     daemon know THIS connection deserves a token", which is the same
//     problem restated. A token handed to the legitimate shim at spawn time
//     (e.g. via an env var) is exactly the kind of value a compromised
//     child process (the downstream MCP server the shim itself spawns,
//     inheriting the shim's environment by default) could read straight
//     back out and replay -- it would need its own delivery mechanism with
//     a real, unforgeable root of trust, which on Windows is precisely what
//     is missing.
//   - A file-content or file-ACL-based check (proving the caller can read
//     something only this user can) only reconstructs "same OS user", the
//     exact guarantee target-binding already treats as insufficient once a
//     downstream MCP server is in scope -- it adds nothing over what the
//     socket's own permissions already claim to provide (see
//     config.EnsurePrivateDir, which on Windows only creates the directory:
//     privatedir_other.go).
//
// The practical effect, which the daemon enforces and not this file: a
// connection on Windows is let through, because refusing every connection
// would refuse every relay, and it carries on as one nobody verified
// (Verdict.Verified is false). A connection nobody verified is never given a
// credential: handleCredentialGet answers it with an error before reading the
// store, the relay takes no connector from a daemon it cannot verify, and
// `holdcall connector set` refuses to send one. What is not refused is
// everything that carries no secret: a decision, a journal entry, an approval.
// Windows' confidentiality for this socket therefore rests on the OS's default
// directory ACLs and on nothing Holdcall verifies. This is a known, real,
// load-bearing gap, not a theoretical one.
func checkImpl(conn net.Conn) Verdict {
	return Verdict{}
}

// primeSelfImpl has nothing to resolve ahead of time on this platform: the
// self check reads the running image from the kernel on every call, or is
// unsupported outright. See PrimeSelf.
func primeSelfImpl() {}
