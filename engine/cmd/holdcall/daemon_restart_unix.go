//go:build unix

package main

import (
	"os"
	"syscall"
)

// stopDaemonPID asks the process at pid to exit, the same way an operator's
// own `kill` would: SIGTERM, not SIGKILL, so daemon.Run's own handler gets
// the chance to close the journal and remove the socket file before the
// process actually goes -- see daemon.go's Run, which installs that handler
// specifically so this has somewhere to land.
func stopDaemonPID(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
