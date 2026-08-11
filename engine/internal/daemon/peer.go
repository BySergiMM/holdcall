package daemon

import "net"

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
func verifyPeerIsSelf(conn net.Conn) (supported, same bool) {
	return verifyPeerIsSelfImpl(conn)
}
