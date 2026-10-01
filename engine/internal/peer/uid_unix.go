//go:build unix

package peer

import "os"

// selfUID is the user this process runs as: the effective uid, which is what
// the kernel reports for the other end of a socket (SO_PEERCRED on linux,
// LOCAL_PEERCRED on darwin) and so the only one that can be compared with it.
//
// A variable so a test can stand in for a daemon run by somebody else. Nothing
// outside tests assigns it: the answer to "who am I" is not configuration.
var selfUID = os.Geteuid

// sameUser is the whole comparison between a peer's user and ours. Split out
// so the rule is stated, and tested, apart from the syscall that feeds it.
//
// A negative id is not a user: it is what a value the kernel could not
// translate looks like once widened, and it must never match anything -- not
// even another unknown -- for the same reason a zero Image equals nothing.
func sameUser(peerUID, selfUID int) bool {
	return peerUID >= 0 && selfUID >= 0 && peerUID == selfUID
}
