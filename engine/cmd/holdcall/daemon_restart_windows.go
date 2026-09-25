//go:build windows

package main

import "fmt"

// stopDaemonPID is never actually called on windows: runDaemonRestart
// refuses before reaching here, because peer identity -- and therefore
// confirming a pid on the socket is genuinely Holdcall before signalling it --
// is not implemented on this platform (see internal/peer/peer_windows.go).
// Defined anyway so this file, like every other platform split in this
// codebase, compiles under `GOOS=windows go vet` rather than only existing
// in the unix build.
func stopDaemonPID(pid int) error {
	return fmt.Errorf("stopping a process by pid is not supported on windows")
}
