//go:build darwin

package credential

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/BySergiMM/holdcall/engine/internal/config"
)

// New returns a Store backed by the macOS keychain, via the `security`
// command line tool -- the same one Keychain Access.app uses. There is no
// cgo-free way to call Security.framework directly from Go, and `security`
// ships with every Mac, so this needs no new dependency.
func New() (Store, error) {
	sum := sha256.Sum256([]byte(config.Home()))
	return darwinStore{service: "holdcall-" + hex.EncodeToString(sum[:4])}, nil
}

// darwinStore scopes every secret to this install (via the same home-hash
// scheme defaultSocket already uses), so two HOLDCALL_HOMEs on one machine never
// collide in the shared keychain.
type darwinStore struct{ service string }

func (s darwinStore) Set(target, secret string) error {
	cmd := buildSetCmd(s, target, secret)
	if err := cmd.Run(); err != nil {
		return exitError("security add-generic-password", err)
	}
	return nil
}

// buildSetCmd is split out from Set so a test can inspect cmd.Args directly
// without performing a real keychain write.
//
// -w with nothing after it makes `security` read the password from stdin
// (twice, for confirmation) instead of taking it as an argument -- a plain
// "-w <value>" would put the secret in this process's own argv, visible to
// any local user via ps. -U makes this an upsert.
func buildSetCmd(s darwinStore, target, secret string) *exec.Cmd {
	cmd := exec.Command("security", "add-generic-password",
		"-a", target, "-s", s.service, "-U", "-w")
	cmd.Stdin = strings.NewReader(secret + "\n" + secret + "\n")
	return cmd
}

func (s darwinStore) Get(target string) (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-a", target, "-s", s.service, "-w")
	out, err := cmd.Output()
	if err != nil {
		if isNotFound(err) {
			return "", ErrNotFound
		}
		return "", exitError("security find-generic-password", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func (s darwinStore) Delete(target string) error {
	cmd := exec.Command("security", "delete-generic-password", "-a", target, "-s", s.service)
	if err := cmd.Run(); err != nil {
		if isNotFound(err) {
			return ErrNotFound
		}
		return exitError("security delete-generic-password", err)
	}
	return nil
}

// errSecItemNotFound is the exit code `security` uses for
// errSecItemNotFound (-25300), the same on find and delete.
const errSecItemNotFound = 44

func isNotFound(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == errSecItemNotFound
}

// exitError wraps a `security` failure without echoing its stderr: none of
// this tool's own diagnostics ever include the secret value, but keeping
// that true is not this package's job to verify, so only the exit status is
// surfaced here.
func exitError(cmd string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &cliError{cmd: cmd, exitCode: exitErr.ExitCode()}
	}
	return &cliError{cmd: cmd, cause: err}
}

type cliError struct {
	cmd      string
	exitCode int
	cause    error
}

func (e *cliError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.cmd, e.cause)
	}
	return fmt.Sprintf("%s: exit status %d", e.cmd, e.exitCode)
}
