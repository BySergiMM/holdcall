//go:build windows

package shim

import (
	"os"
	"os/exec"
	"syscall"
)

// Not defined in the standard syscall package for windows; values are from
// the Win32 API (winbase.h).
const (
	createNewProcessGroup  = 0x00000200
	detachedProcess        = 0x00000008
	createBreakawayFromJob = 0x01000000
)

// daemonSysProcAttr detaches the daemon from the console and process group
// that spawned it, and asks to break away from any job object.
//
// MCP clients on Windows routinely run inside a job object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, which kills every descendant process --
// daemon included -- the moment the client exits. That is the M2 problem
// this exists to fix.
func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess | createBreakawayFromJob,
	}
}

// startDaemonProcess launches the daemon detached. If the parent job object
// does not set JOB_OBJECT_LIMIT_BREAKAWAY_OK, CREATE_BREAKAWAY_FROM_JOB makes
// CreateProcess fail outright rather than being silently ignored, so a second
// attempt without it follows: a daemon still tied to that job at least
// outlives the shim process itself, which is strictly better than no daemon.
func startDaemonProcess(path string, log *os.File) error {
	if log != nil {
		// CreateProcess is synchronous: by the time Start returns (either
		// attempt), the child has its own inherited handle, so the parent's
		// copy can close immediately rather than leak for the shim's lifetime.
		defer log.Close()
	}

	cmd := exec.Command(path, "daemon")
	cmd.SysProcAttr = daemonSysProcAttr()
	if log != nil {
		cmd.Stdout, cmd.Stderr = log, log
	}
	if err := cmd.Start(); err == nil {
		go cmd.Wait() // this process remains the parent regardless of detachment; reap it
		return nil
	}

	cmd = exec.Command(path, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
	if log != nil {
		cmd.Stdout, cmd.Stderr = log, log
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
