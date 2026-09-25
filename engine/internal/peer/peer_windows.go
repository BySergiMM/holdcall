//go:build windows

package peer

import "net"

// isSelfImpl always reports unsupported: Windows' AF_UNIX
// implementation (afunix.sys) exposes no peer-credential API equivalent to
// Linux's SO_PEERCRED or Darwin's LOCAL_PEERPID/LOCAL_PEERCRED. This was
// investigated again, specifically, during the M3 final audit -- not just
// re-asserted -- and no workaround was found that gives a genuine,
// kernel-verified answer for an AF_UNIX peer on Windows. What was
// considered and why each was rejected:
//
//   - Named pipes (GetNamedPipeClientProcessId + a per-pipe DACL) are the
//     one mechanism that actually matches darwin/linux's guarantee: kernel-
//     verified PID, then the same open-process/compare-exe-identity check
//     peer_darwin.go and peer_linux.go already do. This is a real
//     architecture change, though, not a fix: Holdcall deliberately uses one
//     unix-socket transport on all three platforms (see docs/milestones.md,
//     M1's Standing Decision) specifically to avoid needing go-winio, and
//     that decision reached every caller of this socket -- the shim, the
//     CLI's dialConnectorDaemon, StartDaemon's detachment logic -- not just
//     this file. M1 made that call before M3's threat model (a
//     potentially-malicious downstream MCP server on the same socket)
//     existed to weigh against it. Reversing it is exactly the kind of
//     product-shaping decision the M3 remediation instructions say to stop
//     and ask about, not decide unilaterally -- and it cannot be verified
//     in this environment, which has no real Windows machine, only
//     cross-compilation.
//   - An ephemeral capability/token issued by the daemon does not actually
//     solve this on its own: it only moves the question to "how does the
//     daemon know THIS connection deserves a token", which is the same
//     problem restated. A token handed to the legitimate shim at spawn time
//     (e.g. via an env var) is exactly the kind of value a compromised
//     child process (the downstream MCP server the shim itself spawns,
//     inheriting the shim's environment by default) could read straight
//     back out and replay -- it would need its own delivery mechanism with
//     a real, unforgeable root of trust, which on Windows is precisely what
//     is missing. It does not avoid the architecture change above; a
//     Windows-only capability channel is its own new transport to build and
//     verify, not a smaller version of the named-pipe fix.
//   - A file-content or file-ACL-based check (proving the caller can read
//     something only this user can) only reconstructs "same OS user", the
//     exact guarantee target-binding already treats as insufficient once a
//     downstream MCP server is in scope -- it adds nothing over what the
//     socket's own permissions already claim to provide (see config.go's
//     EnsureDirs, which documents that even that same-user claim is not
//     actually enforced by this codebase on Windows today).
//
// The practical effect: on Windows, credential.get still enforces
// target-binding (see handleCredentialGet) and connector.set/list/remove
// still enforce target/size validation -- both are peer-identity-independent
// and hold on every platform -- but none of them can verify the caller is
// genuinely this binary. Combined with config.go's EnsureDirs gap, Windows'
// confidentiality for this socket rests on the OS's own default directory
// ACLs, not on anything Holdcall itself verifies. This is a known, real,
// load-bearing gap, not a theoretical one -- see docs/milestones.md and the
// M3 final audit report for the READY-WITH-KNOWN-LIMITATION reasoning this
// feeds into.
func isSelfImpl(conn net.Conn) (supported, same bool, pid int) {
	return false, false, 0
}
