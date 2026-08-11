//go:build windows

package peer

import "fmt"

// Windows has no implementation of any of this yet, and saying so is the whole
// content of this file.
//
// A file identity does exist there -- the volume serial number and file index
// from GetFileInformationByHandle are the equivalent of a device and inode --
// and a process's image could be reached through QueryFullProcessImageName.
// Neither is written here, because nothing in this project has ever run on
// Windows and an unverified identity mechanism is worse than an absent one: it
// would read as a working check to everything above it.
//
// Callers get an error rather than a zero Image that might be mistaken for an
// answer. Enrolment therefore fails on Windows with a clear reason, which
// matches the platform's existing posture -- see internal/peer/peer_windows.go
// and the Windows rows in docs/security.md.

var errUnsupported = fmt.Errorf("executable identity is not implemented on windows")

// ImageOf reports the identity of the file a process is running.
func ImageOf(pid int) (Image, error) { return Image{}, errUnsupported }

// ImageOfFile reports the identity of a file on disk.
func ImageOfFile(path string) (Image, error) { return Image{}, errUnsupported }

// ParentOf reports the pid that spawned pid.
func ParentOf(pid int) (int, error) { return 0, errUnsupported }
