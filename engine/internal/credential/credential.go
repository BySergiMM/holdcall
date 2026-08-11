// Package credential is the only place a downstream connector's secret ever
// touches disk. Store is backed by the operating system's own credential
// mechanism (Keychain on macOS, DPAPI on Windows, the Secret Service on
// Linux via secret-tool) -- never plaintext, and never SQLite. If no such
// mechanism is available, New returns an error rather than falling back to
// something weaker: a credential store that silently degrades to plaintext
// is worse than one that refuses to start.
package credential

import "errors"

// ErrNotFound is returned by Get and Delete when target has no stored
// secret.
var ErrNotFound = errors.New("credential not found")

// Store holds one secret per connector target, keyed by name (e.g. "github").
// Every implementation must delegate the actual encryption and storage to
// the host OS; none may write a secret to disk unencrypted.
type Store interface {
	// Set stores secret for target, replacing any existing value.
	Set(target, secret string) error
	// Get returns the secret for target, or ErrNotFound if none is stored.
	Get(target string) (string, error)
	// Delete removes the secret for target. It returns ErrNotFound if none
	// was stored, but callers that only want removal to be idempotent should
	// treat that as success.
	Delete(target string) error
}
