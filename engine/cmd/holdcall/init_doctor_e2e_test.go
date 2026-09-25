package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runWithHome runs the built binary the way a real user's shell would,
// except HOME (and, defensively, the platform-specific config-dir variables)
// point at a temp directory rather than the machine's real one. holdcall init and
// holdcall doctor both discover client config files through those variables when
// no --config override is given, and this is what keeps that discovery from
// ever reaching the developer's actual ~/.claude.json.
func runWithHome(t *testing.T, s *stack, home, cwd string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(s.holdcall, args...)
	cmd.Env = append(append([]string{}, s.env...),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
	)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// holdcall init followed by holdcall doctor, against the real binary: init rewrites a
// fixture Claude Code config, dry run first, and doctor then has to notice
// the wrapped server on its own, through the same HOME-based discovery a real
// client config would be found by -- not through --config, which only holdcall
// init is given here.
func TestInitThenDoctorSeeTheSameWrappedServer(t *testing.T) {
	s := build(t)

	// A fake OS home, distinct from HOLDCALL_HOME (build already pointed that at
	// its own temp directory): this one stands in for the machine's real home
	// directory, which holdcall init's default discovery and holdcall doctor's
	// client-config check must never touch.
	fakeHome := t.TempDir()
	cwd := t.TempDir() // no .mcp.json here, so Claude Code's project-local file is not in play
	claudeJSON := filepath.Join(fakeHome, ".claude.json")
	fixture := `{
		"mcpServers": {
			"echo": {
				"command": "echo",
				"args": ["hi"],
				"env": {"SOME_TOKEN": "keep-me"}
			}
		}
	}`
	if err := os.WriteFile(claudeJSON, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}

	// Dry run: reports the change, writes nothing.
	out, err := runWithHome(t, s, fakeHome, cwd,
		"init", "--config", claudeJSON, "--client", "claude-code")
	if err != nil {
		t.Fatalf("holdcall init (dry run): %v\n%s", err, out)
	}
	if !strings.Contains(out, "route through Holdcall") {
		t.Errorf("dry run did not report the pending change:\n%s", out)
	}
	if !strings.Contains(out, "Dry run: nothing was written") {
		t.Errorf("dry run did not say nothing was written:\n%s", out)
	}
	unchanged, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unchanged), `"command": "echo"`) {
		t.Errorf("the dry run modified the file on disk:\n%s", unchanged)
	}

	// --write: applies the change, with a backup.
	out, err = runWithHome(t, s, fakeHome, cwd,
		"init", "--config", claudeJSON, "--client", "claude-code", "--write")
	if err != nil {
		t.Fatalf("holdcall init --write: %v\n%s", err, out)
	}
	if !strings.Contains(out, "wrote "+claudeJSON) {
		t.Errorf("did not report writing %s:\n%s", claudeJSON, out)
	}

	entries, err := os.ReadDir(fakeHome)
	if err != nil {
		t.Fatal(err)
	}
	backedUp := false
	for _, e := range entries {
		if strings.Contains(e.Name(), ".holdcall-backup-") {
			backedUp = true
		}
	}
	if !backedUp {
		t.Error("holdcall init --write left no backup file behind")
	}

	written, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"serve"`, `"--connector"`, `"echo"`, `"--client"`, `"claude-code"`, `"keep-me"`} {
		if !strings.Contains(string(written), want) {
			t.Errorf("rewritten file is missing %s:\n%s", want, written)
		}
	}

	// holdcall doctor, against a running daemon, must find the same server through
	// its own discovery -- no --config given this time -- and exit 0.
	s.daemon(t)
	out, err = runWithHome(t, s, fakeHome, cwd, "doctor")
	if err != nil {
		t.Fatalf("holdcall doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, claudeJSON) {
		t.Errorf("holdcall doctor did not report on %s:\n%s", claudeJSON, out)
	}
	if !strings.Contains(out, "1 server(s) through Holdcall") {
		t.Errorf("holdcall doctor did not report the wrapped server:\n%s", out)
	}
}

// holdcall doctor is read-only: with a daemon already running and healthy, it
// must report OK across the board and exit 0, and it must not start a
// daemon on its own when one is not running -- the daemon check reports WARN
// instead of silently bringing one up, which is what makes "not running" an
// observable state at all rather than one doctor erases by looking.
func TestDoctorReportsHealthyWithARunningDaemon(t *testing.T) {
	s := build(t)
	s.daemon(t)

	fakeHome := t.TempDir()
	out, err := runWithHome(t, s, fakeHome, t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("holdcall doctor: %v\n%s", err, out)
	}
	if strings.Contains(out, "[FAIL]") {
		t.Errorf("a healthy install reported a FAIL:\n%s", out)
	}
	if !strings.Contains(out, "daemon -- reachable and genuine") {
		t.Errorf("holdcall doctor did not confirm the running daemon:\n%s", out)
	}
}

// With no daemon running at all, holdcall doctor must say so as a WARN -- not
// start one, and not FAIL the whole command over it.
func TestDoctorWarnsWithoutStartingADaemon(t *testing.T) {
	s := build(t)

	fakeHome := t.TempDir()
	out, err := runWithHome(t, s, fakeHome, t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("holdcall doctor exited nonzero with no daemon running (should be a WARN, not a FAIL): %v\n%s", err, out)
	}
	if !strings.Contains(out, "daemon -- not running") {
		t.Errorf("holdcall doctor did not report the daemon as not running:\n%s", out)
	}
	if !strings.Contains(out, "starts on demand") {
		t.Errorf("holdcall doctor did not say the daemon starts on demand:\n%s", out)
	}

	// The point of the check: doctor must not have started it as a side
	// effect of asking.
	out2, err2 := s.run(t, "status")
	if err2 != nil {
		t.Fatalf("holdcall status: %v\n%s", err2, out2)
	}
	if !strings.Contains(out2, "daemon   not running") {
		t.Errorf("holdcall doctor left a daemon running behind it:\n%s", out2)
	}
}

// A config.toml still carrying a [policy] section is the error config.Load
// returns, and holdcall doctor must surface it verbatim (see checkConfig) rather
// than summarising it -- the message itself names the fix, and is what
// TestAStaleDenyListInConfigIsRefused holds every other command to. This
// FAILs the whole command: it is exactly the state the exit code exists to
// flag.
func TestDoctorFailsVerbatimOnAStalePolicySection(t *testing.T) {
	s := build(t)
	if err := os.WriteFile(filepath.Join(s.home, "config.toml"),
		[]byte("[daemon]\n\n[policy]\ndeny = [\"dangerous_tool\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := s.run(t, "doctor")
	if err == nil {
		t.Fatalf("holdcall doctor exited 0 with a stale [policy] section:\n%s", out)
	}
	if !strings.Contains(out, "[FAIL] config.toml") {
		t.Errorf("holdcall doctor did not FAIL the config check:\n%s", out)
	}
	if !strings.Contains(out, "holdcall policy deny") {
		t.Errorf("holdcall doctor did not surface config.Load's own remedy verbatim:\n%s", out)
	}
}
