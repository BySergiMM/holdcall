//go:build unix

package peer

import (
	"fmt"
	"os"
	"syscall"
)

// ImageOfFile returns the identity of a file on disk, for enrolling something
// that is not running yet.
//
// This is the one place a path is legitimately the input. An enrolment names a
// file the operator chose, deliberately, as configuration -- the same standing
// as a connector's registered command. What must never take a path is the
// question asked of a *running process*, which is ImageOf's job: there the
// path belongs to the peer, and the file at it can be replaced.
//
// The two produce comparable identities, which is what makes an enrolment
// usable: a file enrolled here and a process inspected there are the same
// agent exactly when the numbers match.
func ImageOfFile(path string) (Image, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Image{}, err
	}
	if fi.IsDir() {
		return Image{}, fmt.Errorf("%s is a directory, not an executable", path)
	}
	if fi.Mode()&0o111 == 0 {
		return Image{}, fmt.Errorf("%s is not executable", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Image{}, fmt.Errorf("%s has no unix file identity", path)
	}
	return Image{dev: uint64(st.Dev), ino: st.Ino}, nil
}
