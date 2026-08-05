// Package config resolves where Nim keeps its state and reads config.toml.
//
// config.toml configures the daemon and nothing else: socket path and data
// directory. Nothing an authorization decision depends on is stored here --
// that lives in SQLite, which is the source of truth.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

// HomeEnvVar overrides the install directory, mainly for tests and for running
// several isolated instances on one machine.
const HomeEnvVar = "NIM_HOME"

// Config is the whole of config.toml.
type Config struct {
	Daemon Daemon `toml:"daemon"`
}

type Daemon struct {
	Socket  string `toml:"socket"`
	DataDir string `toml:"data_dir"`
}

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
