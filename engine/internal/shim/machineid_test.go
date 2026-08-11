package shim

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/BySergiMM/nim/engine/internal/config"
)

// Several shims for several configured MCP servers routinely start at once
// (see StartDaemon's "several shims may race" comment), so a fresh install's
// very first machineID call can genuinely happen from more than one process
// concurrently. All of them must agree on one id -- the mirror groups
// sessions by machine_id, and two different ids for the same install would
// silently split one machine's history in two.
func TestMachineIDIsConsistentUnderConcurrentFirstRun(t *testing.T) {
	t.Setenv(config.HomeEnvVar, t.TempDir())

	const n = 16
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = machineID()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("machineID()[%d] returned an error: %v", i, err)
		}
	}
	want := ids[0]
	if want == "" {
		t.Fatal("machineID() returned an empty id")
	}
	for i, got := range ids {
		if got != want {
			t.Fatalf("machineID() disagreed across concurrent callers: [0]=%q [%d]=%q", want, i, got)
		}
	}
}

// The one thing worse than two different ids for one install is silently
// inventing a new one every time this is called. A winner that creates
// machine-id and dies before writing to it leaves exactly that trap for
// every later caller: the file exists (so O_CREATE|O_EXCL cannot claim it)
// but is permanently empty (so there is nothing to read). This must surface
// as an error, not a freshly minted id that nothing else will ever agree
// with.
func TestMachineIDErrorsRatherThanFabricatingAnIDForAPermanentlyEmptyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.HomeEnvVar, home)

	if err := os.WriteFile(filepath.Join(home, "machine-id"), nil, 0o600); err != nil {
		t.Fatalf("simulating a died-mid-write machine-id file: %v", err)
	}

	id, err := machineID()
	if err == nil {
		t.Fatalf("machineID() returned %q with no error for a permanently empty file; it must report failure instead of inventing an id", id)
	}
	if id != "" {
		t.Errorf("machineID() returned a non-empty id %q alongside an error", id)
	}
}
