package peer

import "net"

// PIDOf returns the pid of the process on the other end of conn, as the kernel
// reports it -- with no cooperation or truthfulness required from that process.
//
// This is the root of every identity question asked of a connection. IsSelf
// uses it to decide whether the peer is running this binary; the daemon uses
// it to walk to the peer's parent, which is the client program that spawned a
// relay.
//
// supported is false where the platform cannot answer at all, which is the
// same distinction IsSelf draws: not knowing is not the same as knowing the
// answer is no, and a caller has to decide what to do about it.
func PIDOf(conn net.Conn) (pid int, supported bool, err error) {
	return pidOfImpl(conn)
}
