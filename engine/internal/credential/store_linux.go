//go:build linux

package credential

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/BySergiMM/nim/engine/internal/config"
)

// New returns a Store backed by the freedesktop Secret Service (GNOME
// Keyring, KWallet, or any other implementation), via the `secret-tool`
// command line tool from libsecret. Unlike macOS and Windows, no single
// credential mechanism ships with every Linux system, so this fails loudly
// rather than guessing: a headless box with no keyring daemon and no
// secret-tool gets a clear error here, never a silent plaintext fallback.
func New() (Store, error) {
	if _, err := exec.LookPath("secret-tool"); err != nil {
		return nil, fmt.Errorf(
			"no OS credential store available: secret-tool (libsecret) is not installed, " +
				"and Nim will not fall back to storing secrets in plaintext")
	}
	sum := sha256.Sum256([]byte(config.Home()))
	return linuxStore{service: "nim-" + hex.EncodeToString(sum[:4])}, nil
}

// linuxStore scopes every secret to this install (the same home-hash scheme
// defaultSocket already uses), so two NIM_HOMEs on one machine never collide
// in the shared keyring.
type linuxStore struct{ service string }

func (s linuxStore) Set(target, secret string) error {
	// secret-tool reads the secret from stdin, never from argv.
	cmd := exec.Command("secret-tool", "store",
		"--label", "Nim connector: "+target,
		"service", s.service, "account", target)
	cmd.Stdin = strings.NewReader(secret)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret-tool store: %w", stripSecretToolStderr(err))
	}
	return nil
}

func (s linuxStore) Get(target string) (string, error) {
	cmd := exec.Command("secret-tool", "lookup", "service", s.service, "account", target)
	out, err := cmd.Output()
	if err != nil {
		// secret-tool lookup exits non-zero with no output for a target it
		// cannot find; there is no distinct exit code to key off like
		// macOS's errSecItemNotFound, so absence of output is the signal.
		if len(out) == 0 {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("secret-tool lookup: %w", stripSecretToolStderr(err))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func (s linuxStore) Delete(target string) error {
	cmd := exec.Command("secret-tool", "clear", "service", s.service, "account", target)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret-tool clear: %w", stripSecretToolStderr(err))
	}
	// secret-tool clear exits 0 whether or not anything matched, so Delete
	// here is idempotent by the underlying tool's own design; there is no
	// reliable way to report ErrNotFound distinctly.
	return nil
}

// stripSecretToolStderr discards secret-tool's own diagnostics: they never
// contain the secret value, but this package does not depend on that
// staying true, so only the exit status is surfaced.
func stripSecretToolStderr(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("exit status %d", exitErr.ExitCode())
	}
	return err
}
