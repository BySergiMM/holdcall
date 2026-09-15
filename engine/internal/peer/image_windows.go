//go:build windows

package peer

import (
	"fmt"
	"net"
)

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

// FileIdentitySupported reports whether ImageOfFile can answer at all on
// this platform. False here: ImageOfFile always fails below, and that
// failure is a statement about the platform, not about any particular file.
// A caller that persists an Image and later asks whether a file still
// matches it (the console's STALE check among them) must read that as
// "unknown" rather than as "no" -- reporting a platform limitation as a
// finding about the file would tell an operator their enrolment had gone
// stale when nothing was actually checked.
const FileIdentitySupported = false

// ImageOf reports the identity of the file a process is running.
func ImageOf(pid int) (Image, error) { return Image{}, errUnsupported }

// ImageOfFile reports the identity of a file on disk.
func ImageOfFile(path string) (Image, error) { return Image{}, errUnsupported }

// ParentOf reports the pid that spawned pid.
func ParentOf(pid int) (int, error) { return 0, errUnsupported }

// pidOfImpl reports unsupported, as everything else here does.
func pidOfImpl(conn net.Conn) (int, bool, error) { return 0, false, nil }
