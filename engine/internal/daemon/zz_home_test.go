package daemon

import (
	"os"
	"testing"

	"github.com/BySergiMM/holdcall/engine/internal/config"
)

// No test in this package may reach the operator's real install. The daemon
// resolves the machine-id through config.Home(), and on a journal with no
// entries it creates one there; a test that started a daemon without
// redirecting the home wrote a fresh machine-id into the real home on
// 2026-09-25, and the tests that open a journal directly had been reading
// the operator's identifier as their own (F-027). Every helper sets the home per test as well; this is
// the floor under any test that forgets.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "holdcall-test-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv(config.HomeEnvVar, dir)
	// The journal refuses to start without an install identifier, and the
	// tests that open one directly used to find the operator's. Give the
	// package its own.
	if _, err := config.CreateMachineID(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
