package clientconfig

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// backupMarker separates an original path from the timestamp in a backup
// file's name, and is how RestoreBackup recovers that original path.
const backupMarker = ".nim-backup-"

// BackupPath is where Apply would back up path up before writing to it.
// Exported so callers can report it before Apply is actually called.
func BackupPath(path string) string {
	return path + backupMarker + time.Now().UTC().Format("20060102T150405Z")
}

// Apply writes res.Rewritten to res.File.Path, after first copying the
// original bytes to a timestamped backup.
//
// It refuses a Result that was never going to change anything: backing up
// and rewriting a file nim init would not have touched serves no purpose and
// would only leave a spurious backup behind.
func Apply(res Result) (backupFilePath string, err error) {
	if !res.Found {
		return "", fmt.Errorf("%s does not exist; nothing to write", res.File.Path)
	}
	if !res.Changed {
		return "", fmt.Errorf("%s needs no changes", res.File.Path)
	}

	backupFilePath = BackupPath(res.File.Path)
	// 0600: a client config file can hold nothing secret itself (Nim moved
	// credentials out of these files), but the backup is still a full copy
	// of it and there is no reason to leave it more open than that.
	if err := os.WriteFile(backupFilePath, res.Original, 0o600); err != nil {
		return "", fmt.Errorf("backing up %s: %w", res.File.Path, err)
	}

	mode := res.OriginalMode
	if mode == 0 {
		mode = 0o644
	}
	if err := writeAtomic(res.File.Path, res.Rewritten, mode); err != nil {
		return backupFilePath, fmt.Errorf("writing %s: %w", res.File.Path, err)
	}
	return backupFilePath, nil
}

// RestoreBackup writes a backup's contents back to the path it was copied
// from, atomically. The original path is recovered from the backup's own
// name rather than asked for separately, so `nim init --undo` needs only the
// one argument a user actually has in hand.
func RestoreBackup(backupFilePath string) (restoredPath string, err error) {
	i := strings.LastIndex(backupFilePath, backupMarker)
	if i < 0 {
		return "", fmt.Errorf("%s does not look like a nim backup (missing %q)", backupFilePath, backupMarker)
	}
	restoredPath = backupFilePath[:i]

	data, err := os.ReadFile(backupFilePath)
	if err != nil {
		return "", fmt.Errorf("reading backup %s: %w", backupFilePath, err)
	}
	mode := fs.FileMode(0o644)
	if info, statErr := os.Stat(restoredPath); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := writeAtomic(restoredPath, data, mode); err != nil {
		return "", fmt.Errorf("restoring %s: %w", restoredPath, err)
	}
	return restoredPath, nil
}

// writeAtomic writes data to a temp file in the same directory as path, then
// renames it into place. A crash or a concurrent reader can only ever see
// the old content or the new one in full, never a partial write -- which
// matters here because these are files a client reads on every launch.
func writeAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".nim-init-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	renamed = true
	return nil
}
