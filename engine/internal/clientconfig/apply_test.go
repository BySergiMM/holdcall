package clientconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stdioFixture = `{
	"mcpServers": {
		"github": {"command": "npx", "args": ["-y", "server-github"]}
	}
}`

// Computing a Result must never touch disk. This is what makes a dry run a
// dry run: nim init's CLI layer only calls Apply when --write is given, and
// BuildResult itself has no write path to accidentally take.
func TestInitDryRunWritesNothing(t *testing.T) {
	path := writeFixture(t, "claude.json", stdioFixture)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := BuildResult(File{Client: ClaudeCode, Path: path, Kind: KindFlat}, nimPathFor(t), ClaudeCode, false); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the file changed even though Apply was never called")
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the original file in the directory, found %d entries", len(entries))
	}
}

// --write must back up the original bytes before changing anything, write
// the new content atomically (no partial file, no stray temp file left
// behind), and the backup must be owner-only.
func TestInitWriteCreatesABackupAndWritesAtomically(t *testing.T) {
	path := writeFixture(t, "claude.json", stdioFixture)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	res := mustBuildResult(t, path, nimPathFor(t))
	if !res.Changed {
		t.Fatal("expected a change to apply")
	}

	backup, err := Apply(res)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	backupData, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if string(backupData) != string(original) {
		t.Error("the backup does not hold the original content")
	}
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup permissions = %o, want 0600", perm)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(res.Rewritten) {
		t.Error("the file on disk does not match what BuildResult computed")
	}

	// No leftover temp file from the rename-based write.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nim-init-tmp-") {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}
}

// Applying a Result that needed no change is refused: there is nothing to
// back up or write, and doing so anyway would leave a pointless backup file.
func TestInitApplyRefusesAResultWithNoChanges(t *testing.T) {
	path := writeFixture(t, "claude.json", `{"mcpServers": {}}`)
	res := mustBuildResult(t, path, nimPathFor(t))
	if res.Changed {
		t.Fatal("expected no change for an empty mcpServers")
	}
	if _, err := Apply(res); err == nil {
		t.Fatal("expected Apply to refuse a Result with nothing to change")
	}
}

// nim init --undo restores exactly the bytes that were backed up, and
// recovers which file to restore from the backup's own name.
func TestInitUndoRestoresABackup(t *testing.T) {
	path := writeFixture(t, "claude.json", stdioFixture)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	res := mustBuildResult(t, path, nimPathFor(t))
	backup, err := Apply(res)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm the file actually changed before undoing, so a bug that made
	// Apply a no-op could not make this test pass by accident.
	changed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(changed) == string(original) {
		t.Fatal("the file did not actually change before undo")
	}

	restoredPath, err := RestoreBackup(backup)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if restoredPath != path {
		t.Errorf("restored path = %s, want %s", restoredPath, path)
	}

	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Errorf("restored content does not match the original:\ngot:  %s\nwant: %s", restored, original)
	}
}

// A path that was never a nim backup must be refused rather than guessed at.
func TestInitUndoRefusesAPathThatIsNotABackup(t *testing.T) {
	path := writeFixture(t, "claude.json", stdioFixture)
	if _, err := RestoreBackup(path); err == nil {
		t.Fatal("expected an error for a path with no .nim-backup- marker")
	}
}
