//go:build darwin

package peer

import (
	"os"
	"syscall"
	"testing"
)

// The self-check is what makes the rest of image_darwin.go safe to rely on. It
// takes an internal ABI -- the proc_info call number is not in the SDK, and
// the struct offsets are computed from a header -- and turns it into something
// verified at runtime against a process whose identity is already known.
//
// If it stops holding on a healthy system, isSelfImpl silently falls back to
// the weaker path comparison, so it is worth asserting directly rather than
// only through its consequences.
func TestSelfImageIsTrustworthyAndCorrect(t *testing.T) {
	id, trustworthy := selfImageID()
	if !trustworthy {
		t.Fatal("the vnode mechanism did not resolve our own pid to our own binary")
	}

	path, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if id.Dev() != uint64(st.Dev) || id.Ino() != st.Ino {
		t.Fatalf("self image dev=%d ino=%d, but our binary is dev=%d ino=%d",
			id.Dev(), id.Ino(), st.Dev, st.Ino)
	}
}
