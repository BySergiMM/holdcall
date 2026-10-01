//go:build !unix

package config

import "os"

// EnsurePrivateDir only creates dir on this platform, and that is all it can
// honestly do.
//
// Nothing here checks who owns the directory or who else can use it. On Windows
// a directory's mode is not its permissions -- os.Chmod toggles the read-only
// attribute and nothing more -- and access is decided by ACLs that this code
// neither reads nor sets, so the privacy of Holdcall's home, its journal and its
// socket rests on the default ACLs of the directories they are created in
// (under the user's own profile, in the ordinary case) and on nothing Holdcall
// verifies. docs/security.md says so in its list of gaps.
func EnsurePrivateDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

// socketTempDir is the temp directory itself. On Windows that is the per-user
// one under the profile, which is where the socket has always gone; a unix
// system needs more than that, see privatedir_unix.go.
func socketTempDir() string { return os.TempDir() }
