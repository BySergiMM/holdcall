//go:build unix

package peer

import (
	"fmt"
	"os"
	"syscall"
)

// sleepProgram is a program that exists on both unix platforms, is not this
// binary, and stays alive long enough to be inspected.
const sleepProgram = "/bin/sleep"

// sameFileAsImage compares an os.FileInfo to an Image without this test having
// to know how a platform packs a device id into the number ImageOf reports.
func sameFileAsImage(fi os.FileInfo, img Image) (bool, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("no unix metadata on this platform")
	}
	return uint64(st.Dev) == img.Dev() && st.Ino == img.Ino(), nil
}
