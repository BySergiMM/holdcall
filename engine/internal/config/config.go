// Package config resolves where Nim keeps its state and reads config.toml.
//
// config.toml configures the daemon: socket path, data directory, and -- for
// this milestone only -- a list of tool names to refuse.
//
// That list is scaffolding, not the authorization model. It exists so the
// enforcement path can be exercised end to end against something real, and it
// gives up the property the rest of this file was written around: any process
// able to write config.toml can empty it. Authorization belongs somewhere the
// daemon owns, and until that exists nothing here should grow wildcards,
// scopes, per-connector rules or an evaluation order. See docs/milestones.md.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/BurntSushi/toml"
)

// HomeEnvVar overrides the install directory, mainly for tests and for running
// several isolated instances on one machine.
const HomeEnvVar = "NIM_HOME"

// Config is the whole of config.toml.
type Config struct {
	Daemon Daemon `toml:"daemon"`
	Policy Policy `toml:"policy"`
}

type Daemon struct {
	Socket  string `toml:"socket"`
	DataDir string `toml:"data_dir"`
}

// Policy is a list of tool names to refuse, and nothing more.
//
//	[policy]
//	deny = ["dangerous_tool"]
//
// Exact names, compared to params.name as the client sent it. No wildcards, no
// patterns, no ordering, no per-connector rules: a name is on the list or it is
// not, and anything not on it is allowed.
//
// This is the smallest thing that makes a real denial reachable outside a test.
// It is not a policy engine and must not become one.
type Policy struct {
	Deny []string `toml:"deny"`
}

// Denied reports whether a tool name is on the deny list.
func (p Policy) Denied(tool string) bool { return slices.Contains(p.Deny, tool) }

// Home is the single directory Nim owns.
func Home() string {
	if override := os.Getenv(HomeEnvVar); override != "" {
		return override
	}
	switch runtime.GOOS {
	case "windows":
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, "nim")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "nim")
		}
	default:
		if base := os.Getenv("XDG_DATA_HOME"); base != "" {
			return filepath.Join(base, "nim")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", "nim")
		}
	}
	return filepath.Join(os.TempDir(), "nim")
}

func Path() string { return filepath.Join(Home(), "config.toml") }

// Load reads config.toml, filling in defaults for anything absent. A missing
// file is not an error: the defaults are a working configuration.
func Load() (Config, error) {
	cfg := Config{}
	if data, err := os.ReadFile(Path()); err == nil {
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return cfg, err
		}
	} else if !os.IsNotExist(err) {
		return cfg, err
	}
	if cfg.Daemon.DataDir == "" {
		cfg.Daemon.DataDir = filepath.Join(Home(), "data")
	}
	if cfg.Daemon.Socket == "" {
		cfg.Daemon.Socket = defaultSocket()
	}
	return cfg, nil
}

// MaxSocketPath is the practical ceiling for an AF_UNIX path. The kernel
// struct holds 108 bytes including the terminator on Linux and 104 on macOS
// and Windows; anything longer fails with a bare "invalid argument".
const MaxSocketPath = 100

// defaultSocket derives a short name from the install directory.
//
// The socket cannot live inside Home(): that path is chosen by the user and
// may be arbitrarily deep, which silently breaks the daemon. Hashing it gives
// a name that is both short and unique per install, so two homes on one
// machine never collide.
func defaultSocket() string {
	sum := sha256.Sum256([]byte(Home()))
	name := "nim-" + hex.EncodeToString(sum[:4]) + ".sock"
	if runtime.GOOS != "windows" {
		if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
			return filepath.Join(dir, name)
		}
	}
	return filepath.Join(os.TempDir(), name)
}

// Validate rejects a configuration that cannot work, with an explanation.
func (c Config) Validate() error {
	if n := len(c.Daemon.Socket); n > MaxSocketPath {
		return fmt.Errorf(
			"socket path is %d characters and the limit is %d: %s\n"+
				"set a shorter [daemon] socket in %s",
			n, MaxSocketPath, c.Daemon.Socket, Path())
	}
	return nil
}

// MachineIDPath is the file holding this install's identifier.
func MachineIDPath() string { return filepath.Join(Home(), "machine-id") }

// ReadMachineID returns the install's identifier, and whether there was one.
//
// It never creates the file. That distinction matters: the journal seeds its
// hash chain with this value, so inventing a replacement for a lost one makes
// every existing entry fail to verify -- reporting tampering when all that
// happened is that a file went missing. Losing the material you verify with is
// not the same as the thing you were verifying being wrong.
//
// Only CreateMachineID may bring one into existence, and only when nothing is
// chained to the old one yet.
func ReadMachineID() (string, bool) {
	b, err := os.ReadFile(MachineIDPath())
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}

// CreateMachineID writes a new identifier. Callers must first establish that no
// journal entries depend on the previous one.
func CreateMachineID() (string, error) {
	id := NewID()
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(MachineIDPath(), []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// NewID is an opaque random identifier, used for machine and session ids.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// LogPath is where a daemon started in the background writes its diagnostics.
// Without it a failure to start is invisible, which for a tool that exists to
// keep a record is the worst way to fail.
func LogPath() string { return filepath.Join(Home(), "daemon.log") }

func (c Config) DatabasePath() string { return filepath.Join(c.Daemon.DataDir, "nim.db") }

// EnsureDirs creates the layout with owner-only permissions.
func (c Config) EnsureDirs() error {
	for _, dir := range []string{Home(), c.Daemon.DataDir, filepath.Dir(c.Daemon.Socket)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}
