package config

import (
	"strings"
	"testing"
)

// A deep install directory silently broke the daemon with "bind: invalid
// argument", and the relay reported nothing wrong. The socket must stay short
// no matter where the user puts NIM_HOME.
func TestSocketStaysWithinTheAfUnixLimit(t *testing.T) {
	deep := "C:\\Users\\someone\\AppData\\Local\\Temp\\" + strings.Repeat("a-long-directory-name\\", 8) + "nim"
	t.Setenv(HomeEnvVar, deep)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n := len(cfg.Daemon.Socket); n > MaxSocketPath {
		t.Fatalf("socket path is %d characters, over the %d limit: %s", n, MaxSocketPath, cfg.Daemon.Socket)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a default configuration must always be valid: %v", err)
	}
}

// Two homes on one machine must not fight over the same socket.
func TestSocketIsUniquePerHome(t *testing.T) {
	t.Setenv(HomeEnvVar, "/tmp/home-one")
	one, _ := Load()
	t.Setenv(HomeEnvVar, "/tmp/home-two")
	two, _ := Load()

	if one.Daemon.Socket == two.Daemon.Socket {
		t.Fatalf("both homes resolved to %s", one.Daemon.Socket)
	}
}

func TestSocketIsStableForOneHome(t *testing.T) {
	t.Setenv(HomeEnvVar, "/tmp/stable")
	first, _ := Load()
	second, _ := Load()
	if first.Daemon.Socket != second.Daemon.Socket {
		t.Fatal("the socket path must not change between runs")
	}
}

func TestValidateExplainsAnOverlongSocket(t *testing.T) {
	cfg := Config{Daemon: Daemon{Socket: "/" + strings.Repeat("x", MaxSocketPath+10)}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("an unusable socket path must be rejected")
	}
	// The message has to say what to do, not just that something is wrong.
	if !strings.Contains(err.Error(), "limit") || !strings.Contains(err.Error(), "config.toml") {
		t.Errorf("unhelpful message: %v", err)
	}
}

// The sqlite driver treats the first '?' in a DSN as the start of a query
// string and silently drops everything before it from the path, so a data
// directory containing one would make journal.Open open the wrong file
// instead of failing loudly.
func TestValidateRejectsAQuestionMarkInDataDir(t *testing.T) {
	cfg := Config{Daemon: Daemon{Socket: "/tmp/nim.sock", DataDir: "/tmp/nim?data"}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("a data_dir containing '?' must be rejected")
	}
	if !strings.Contains(err.Error(), "?") {
		t.Errorf("unhelpful message: %v", err)
	}
}

func TestConfigTomlIsOptional(t *testing.T) {
	t.Setenv(HomeEnvVar, t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("a missing config.toml must not be an error: %v", err)
	}
	if cfg.Daemon.DataDir == "" || cfg.Daemon.Socket == "" {
		t.Fatal("defaults must produce a working configuration")
	}
}
