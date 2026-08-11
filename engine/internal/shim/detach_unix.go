//go:build unix

package shim

import (
	"os"
	"os/exec"
	"syscall"
)

// daemonSysProcAttr puts the daemon in its own session, detached from the
// shim's controlling terminal and process group.
//
// Without this, a terminal SIGINT or SIGHUP is delivered to the whole
// foreground process group -- daemon included -- which defeats the one
// property that matters here: a daemon shared across client sessions must
// outlive any single one of them.
func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// startDaemonProcess launches the daemon detached. Unix has no analogue to
// Windows' job-object breakaway failure, so one attempt is enough.
func startDaemonProcess(path string, log *os.File) error {
	if log != nil {
		// Start is synchronous on unix: by the time it returns, the child
		// has its own duplicated descriptor from fork, so the parent's copy
		// can close immediately rather than leak for the shim's lifetime.
		defer log.Close()
	}
	cmd := exec.Command(path, "daemon")
	cmd.SysProcAttr = daemonSysProcAttr()
	if log != nil {
		cmd.Stdout, cmd.Stderr = log, log
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// The daemon almost always outlives this process, but when it loses the
	// socket race it exits immediately -- and this process is still its
	// parent regardless of Setsid, so it is on the hook to reap it.
	go cmd.Wait()
	return nil
}
