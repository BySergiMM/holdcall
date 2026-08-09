//go:build windows

package daemon

// acquireStartupLock is a no-op on Windows: there is no verified, stdlib-only
// equivalent to unix's flock here, and shipping an unverified Win32 locking
// implementation risks being worse than none. listen()'s retry loop is still
// an improvement over a single attempt, but the stale-socket reclaim race
// (see daemon.go) is not fully closed on this platform.
func acquireStartupLock(path string) (unlock func(), err error) {
	return func() {}, nil
}
