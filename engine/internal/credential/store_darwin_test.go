//go:build darwin

package credential

import (
	"errors"
	"testing"

	"github.com/BySergiMM/holdcall/engine/internal/config"
)

// These exercise the real macOS keychain. If the environment cannot reach it
// (locked keychain, no session), that is an environment limitation, not a
// bug in this package, so failures here are reported with t.Skip rather than
// t.Fatal wherever the cause is plausibly environmental.
func newTestStore(t *testing.T) Store {
	t.Helper()
	t.Setenv(config.HomeEnvVar, t.TempDir()) // isolates the keychain service name per test
	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestSetThenGetRoundTrips(t *testing.T) {
	s := newTestStore(t)
	target := "test-roundtrip"
	t.Cleanup(func() { s.Delete(target) })

	if err := s.Set(target, "sekret-value-1"); err != nil {
		t.Skipf("keychain unavailable in this environment: %v", err)
	}
	got, err := s.Get(target)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sekret-value-1" {
		t.Fatalf("Get = %q, want %q", got, "sekret-value-1")
	}
}

func TestSetIsAnUpsert(t *testing.T) {
	s := newTestStore(t)
	target := "test-upsert"
	t.Cleanup(func() { s.Delete(target) })

	if err := s.Set(target, "first"); err != nil {
		t.Skipf("keychain unavailable in this environment: %v", err)
	}
	if err := s.Set(target, "second"); err != nil {
		t.Fatalf("second Set: %v", err)
	}
	got, err := s.Get(target)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "second" {
		t.Fatalf("Get = %q, want %q (Set must replace, not fail or duplicate)", got, "second")
	}
}

func TestGetOnUnknownTargetIsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get("test-never-set"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on an unset target: got %v, want ErrNotFound", err)
	}
}

func TestDeleteRemovesTheSecret(t *testing.T) {
	s := newTestStore(t)
	target := "test-delete"

	if err := s.Set(target, "to-be-deleted"); err != nil {
		t.Skipf("keychain unavailable in this environment: %v", err)
	}
	if err := s.Delete(target); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(target); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: got %v, want ErrNotFound", err)
	}
}

func TestDeleteOnUnknownTargetIsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("test-never-existed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete on an unset target: got %v, want ErrNotFound", err)
	}
}

// The whole point of this package: a secret handed to Set must never appear
// in this process's own argv, where any local user could read it via ps.
func TestSetNeverPutsTheSecretInArgv(t *testing.T) {
	s := newTestStore(t).(darwinStore)
	const secret = "argv-must-never-see-this-9f3a"
	cmd := buildSetCmd(s, "test-argv", secret)
	for _, arg := range cmd.Args {
		if arg == secret {
			t.Fatalf("the secret appeared directly in argv: %v", cmd.Args)
		}
	}
	t.Cleanup(func() { s.Delete("test-argv") })
}
