package shim

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
)

// stubDaemon answers credential.get with resp and then swallows the events
// the shim reports, exactly as the real daemon's connection does. It is a
// stub rather than the real daemon because these tests are about what the
// *shim* spawns given an answer; internal/daemon has its own tests for how
// that answer is reached.
func stubDaemon(t *testing.T, resp daemon.Response) config.Config {
	t.Helper()

	dir, err := os.MkdirTemp("", "nimsp")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				dec := json.NewDecoder(conn)
				var req daemon.Request
				if err := dec.Decode(&req); err != nil {
					return
				}
				resp.ID = req.ID
				json.NewEncoder(conn).Encode(resp)
				// Drain whatever the shim reports afterwards.
				for {
					var ignored map[string]any
					if err := dec.Decode(&ignored); err != nil {
						return
					}
				}
			}()
		}
	}()

	return config.Config{Daemon: config.Daemon{Socket: sock, DataDir: dir}}
}

// runShim runs the relay to completion with an empty stdin, capturing what
// the downstream wrote. Replacing os.Stdin/os.Stdout is unpleasant but it is
// what Run reads and writes, and the point of this test is Run's real
// behaviour rather than a refactored-for-testing version of it.
func runShim(t *testing.T, opts Options) (stdout string, err error) {
	t.Helper()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	outFile := filepath.Join(t.TempDir(), "out")
	out, err := os.Create(outFile)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = devNull, out
	runErr := Run(opts)
	os.Stdin, os.Stdout = oldIn, oldOut
	out.Close()

	b, readErr := os.ReadFile(outFile)
	if readErr != nil {
		t.Fatalf("reading captured stdout: %v", readErr)
	}
	return string(b), runErr
}

// TestTheDaemonsCommandIsSpawnedNotTheCallersEndToEnd is the credential
// oracle, closed at the layer it was open at.
//
// The original reproduction ran the real binary:
//
//	nim serve --target github -- /bin/sh -c 'echo $GITHUB_TOKEN'
//
// and the secret was printed, because Run spawned whatever its caller named
// and injected the credential into it. Here the caller asks for the same
// thing and the registered command runs instead, so the environment carrying
// the secret is handed to a process the caller did not choose.
func TestTheDaemonsCommandIsSpawnedNotTheCallersEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test spawns a real downstream command hardcoded as /bin/echo or /bin/sh -c, " +
			"neither of which exists at that path on windows; the credential-injection logic under " +
			"test is not windows-specific, only this fixture's command strings are")
	}
	const secret = "ghp_REAL_SECRET_must_not_leak"

	cfg := stubDaemon(t, daemon.Response{
		Found:   true,
		Env:     map[string]string{"GITHUB_TOKEN": secret},
		Command: []string{"/bin/echo", "REGISTERED-SERVER-RAN"},
	})

	stdout, err := runShim(t, Options{
		Connector: "github",
		Config:    cfg,
		// The attacker's command: print the credential it was handed.
		Command: []string{"/bin/sh", "-c", "echo LEAKED=$GITHUB_TOKEN"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if strings.Contains(stdout, secret) {
		t.Fatalf("the credential reached the caller's command; output was %q", stdout)
	}
	if strings.Contains(stdout, "LEAKED") {
		t.Fatalf("the caller's command ran at all; output was %q", stdout)
	}
	if !strings.Contains(stdout, "REGISTERED-SERVER-RAN") {
		t.Fatalf("the registered command did not run; output was %q", stdout)
	}
}

// With no connector configured the caller's command is what runs. This is
// the ordinary case -- most targets have no credential -- and it must keep
// working exactly as it did before credentials existed.
func TestWithNoConnectorTheCallersCommandRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test spawns a real downstream command hardcoded as /bin/echo or /bin/sh -c, " +
			"neither of which exists at that path on windows; the credential-injection logic under " +
			"test is not windows-specific, only this fixture's command strings are")
	}
	cfg := stubDaemon(t, daemon.Response{Found: false})

	stdout, err := runShim(t, Options{
		Connector: "github",
		Config:    cfg,
		Command:   []string{"/bin/echo", "CALLERS-SERVER-RAN"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(stdout, "CALLERS-SERVER-RAN") {
		t.Fatalf("the caller's command did not run; output was %q", stdout)
	}
}

// A connector whose credential cannot be released must stop the spawn, not
// start the downstream without it. Fail-closed, seen from the relay: a server
// started without the token it was configured to need would fail in a way
// that looks like the remote service's problem.
func TestAnUnreleasableCredentialStopsTheSpawn(t *testing.T) {
	cfg := stubDaemon(t, daemon.Response{
		Found: true,
		Error: "connector \"github\" has no authorized command",
	})

	stdout, err := runShim(t, Options{
		Connector: "github",
		Config:    cfg,
		Command:   []string{"/bin/echo", "SHOULD-NOT-RUN"},
	})
	if err == nil {
		t.Fatal("Run should have refused to spawn")
	}
	if strings.Contains(stdout, "SHOULD-NOT-RUN") {
		t.Fatalf("the downstream ran despite the refusal; output was %q", stdout)
	}
}

// A daemon that answers with a credential but no command is refused rather
// than fallen back on. Falling back would mean spawning the caller's command
// with a secret in its environment, which is the original hole: a future
// daemon bug must not be able to reopen it quietly.
func TestACredentialWithNoCommandIsRefused(t *testing.T) {
	const secret = "ghp_REAL_SECRET_must_not_leak"
	cfg := stubDaemon(t, daemon.Response{
		Found: true,
		Env:   map[string]string{"GITHUB_TOKEN": secret},
	})

	stdout, err := runShim(t, Options{
		Connector: "github",
		Config:    cfg,
		Command:   []string{"/bin/sh", "-c", "echo LEAKED=$GITHUB_TOKEN"},
	})
	if err == nil {
		t.Fatal("a credential with no authorized command must not be injected anywhere")
	}
	if strings.Contains(stdout, secret) {
		t.Fatalf("the credential leaked; output was %q", stdout)
	}
}

// Neither a registered command nor one from the caller: the relay has
// nothing to spawn and says so, rather than failing somewhere less obvious.
func TestNoCommandAnywhereIsAClearError(t *testing.T) {
	cfg := stubDaemon(t, daemon.Response{Found: false})

	_, err := runShim(t, Options{Connector: "github", Config: cfg})
	if err == nil {
		t.Fatal("expected an error when no command is available from either side")
	}
	if !strings.Contains(err.Error(), "no downstream command") {
		t.Fatalf("the error should say what is missing, got %q", err)
	}
}

// The downstream must not outlive the relay. A connector left running after
// its shim is gone is a process holding a credential with nothing recording
// what it does -- the orphan case, which is also the one that leaves a
// session in the journal with no end.
func TestTheDownstreamDiesWithTheRelay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test spawns a real downstream command hardcoded as /bin/echo or /bin/sh -c, " +
			"neither of which exists at that path on windows; the credential-injection logic under " +
			"test is not windows-specific, only this fixture's command strings are")
	}
	cfg := stubDaemon(t, daemon.Response{Found: false})

	marker := filepath.Join(t.TempDir(), "alive")

	// Writes a marker, then reads stdin forever. Run returns only once this
	// has exited, so if Run returns at all the process is gone.
	script := "echo x > " + marker + "; cat"
	done := make(chan error, 1)
	go func() {
		_, err := runShim(t, Options{
			Connector: "github",
			Config:    cfg,
			Command:   []string{"/bin/sh", "-c", script},
		})
		done <- err
	}()

	select {
	case <-done:
		// Run returned, which means cmd.Wait returned, which means the
		// downstream is not running. stdin was /dev/null, so the relay closed
		// the downstream's stdin immediately and `cat` exited.
	case <-time.After(15 * time.Second):
		t.Fatal("the relay never returned: the downstream outlived it")
	}

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the downstream never ran, so this proved nothing: %v", err)
	}
}
