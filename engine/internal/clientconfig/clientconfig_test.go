package clientconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// isolatedHome points HOME (and, where it matters, XDG_CONFIG_HOME) at a temp
// directory. Discover must never fall back to a real home directory in a test
// process: every case below calls this first, so a bug here would fail
// loudly rather than quietly reading the developer's own machine.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// With no filter, Discover looks at all three clients' default locations,
// none of which need exist yet.
func TestDiscoverListsAllThreeClientsByDefault(t *testing.T) {
	home := isolatedHome(t)
	files, err := Discover("", "", home)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Client] = true
	}
	for _, c := range AllClients {
		if !seen[c] {
			t.Errorf("Discover did not include client %q: %+v", c, files)
		}
	}
}

// --config only makes sense together with --client: otherwise it is
// ambiguous which of the (possibly several) discovered files it replaces.
func TestDiscoverRefusesConfigOverrideWithoutAClientFilter(t *testing.T) {
	isolatedHome(t)
	if _, err := Discover("", "/some/path.json", ""); err == nil {
		t.Fatal("expected an error for --config without --client")
	}
}

func TestDiscoverRefusesAnUnknownClient(t *testing.T) {
	isolatedHome(t)
	if _, err := Discover("not-a-real-client", "", ""); err == nil {
		t.Fatal("expected an error for an unknown client")
	}
}

// A --config override replaces exactly the filtered client's primary file,
// and Discover reports no other clients at all.
func TestDiscoverConfigOverrideReplacesOnlyTheFilteredClient(t *testing.T) {
	isolatedHome(t)
	override := filepath.Join(t.TempDir(), "custom-claude.json")
	files, err := Discover(ClaudeCode, override, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected exactly one file, got %+v", files)
	}
	if files[0].Path != override || files[0].Client != ClaudeCode {
		t.Errorf("got %+v, want path %s for client %s", files[0], override, ClaudeCode)
	}
}

// Claude Code's project-local .mcp.json is only listed when it actually
// exists next to the given working directory -- its absence says nothing
// (most projects have no server of their own), unlike a missing primary
// file, which is worth a "not found" line.
func TestDiscoverIncludesProjectLocalMcpJSONWhenPresent(t *testing.T) {
	isolatedHome(t)
	cwd := t.TempDir()
	local := filepath.Join(cwd, ".mcp.json")
	if err := os.WriteFile(local, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := Discover(ClaudeCode, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range files {
		if f.Path == local {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %s among %+v", local, files)
	}
}

// With no .mcp.json in the working directory, Discover reports only Claude
// Code's primary file.
func TestDiscoverOmitsProjectLocalMcpJSONWhenAbsent(t *testing.T) {
	isolatedHome(t)
	cwd := t.TempDir()

	files, err := Discover(ClaudeCode, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected exactly one file with no .mcp.json present, got %+v", files)
	}
}
