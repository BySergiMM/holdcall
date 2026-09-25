package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A deep install directory silently broke the daemon with "bind: invalid
// argument", and the relay reported nothing wrong. The socket must stay short
// no matter where the user puts HOLDCALL_HOME.
func TestSocketStaysWithinTheAfUnixLimit(t *testing.T) {
	deep := "C:\\Users\\someone\\AppData\\Local\\Temp\\" + strings.Repeat("a-long-directory-name\\", 8) + "holdcall"
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

func TestConfigTomlIsOptional(t *testing.T) {
	t.Setenv(HomeEnvVar, t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("a missing config.toml must not be an error: %v", err)
	}
	if cfg.Daemon.DataDir == "" || cfg.Daemon.Socket == "" {
		t.Fatal("defaults must produce a working configuration")
	}
	if time.Duration(cfg.Daemon.ApprovalTimeout) != DefaultApprovalTimeout {
		t.Errorf("approval_timeout defaulted to %s, want %s", cfg.Daemon.ApprovalTimeout, DefaultApprovalTimeout)
	}
}

// approval_timeout is read as a duration string, because TOML has no
// duration type of its own, and a human who never sets it gets the default
// rather than an unusable zero wait.
func TestApprovalTimeoutIsReadAsADurationString(t *testing.T) {
	t.Setenv(HomeEnvVar, t.TempDir())
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("[daemon]\napproval_timeout = \"90s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("a valid approval_timeout was refused: %v", err)
	}
	if got := time.Duration(cfg.Daemon.ApprovalTimeout); got != 90*time.Second {
		t.Errorf("approval_timeout = %s, want 1m30s", got)
	}
}

// A duration that cannot mean "wait" -- unparseable, zero or negative -- is
// refused at load time rather than turned into an instant or infinite
// timeout nobody asked for.
func TestApprovalTimeoutRejectsWhatIsNotAPositiveDuration(t *testing.T) {
	t.Setenv(HomeEnvVar, t.TempDir())
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"not-a-duration", "0s", "-5m"} {
		body := "[daemon]\napproval_timeout = \"" + bad + "\"\n"
		if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Errorf("approval_timeout = %q was accepted", bad)
		}
	}
}

// The deny list moved to SQLite. A config.toml that still carries it is not
// read past: the operator who wrote it believes those tools are refused, and
// nothing would tell them otherwise.
func TestAConfigWithAPolicySectionIsRefused(t *testing.T) {
	t.Setenv(HomeEnvVar, t.TempDir())
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("[daemon]\n\n[policy]\ndeny = [\"rm\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil {
		t.Fatal("a config.toml with a [policy] section was accepted")
	}
	if !strings.Contains(err.Error(), "holdcall policy deny") {
		t.Errorf("the error does not say where policy went: %v", err)
	}
}

// A key this build does not know is a typo until proven otherwise. [polciy]
// used to be accepted and denied nothing.
func TestUnknownKeysAreRefused(t *testing.T) {
	t.Setenv(HomeEnvVar, t.TempDir())
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"[deamon]\nsocket = \"/tmp/x.sock\"\n",
		"[daemon]\nsockte = \"/tmp/x.sock\"\n",
		"[polciy]\ndeny = [\"rm\"]\n",
	} {
		if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Errorf("accepted:\n%s", body)
		}
	}
	// And the file that is right still loads.
	if err := os.WriteFile(Path(), []byte("[daemon]\ndata_dir = \"/tmp/holdcall-data\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil || cfg.Daemon.DataDir != "/tmp/holdcall-data" {
		t.Fatalf("a valid file failed to load: %v (%+v)", err, cfg)
	}
}

// A machine-id restored by hand usually arrives with a trailing newline. The
// identifier is the text, not the bytes: otherwise the restored file seeds a
// different genesis and the chain is reported as tampered with.
func TestAMachineIDSurvivesATrailingNewline(t *testing.T) {
	t.Setenv(HomeEnvVar, t.TempDir())
	id, err := CreateMachineID()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(MachineIDPath(), []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadMachineID()
	if !ok || got != id {
		t.Fatalf("ReadMachineID = %q, %v; want %q", got, ok, id)
	}
	if err := os.WriteFile(MachineIDPath(), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadMachineID(); ok {
		t.Fatal("a file holding only a newline was read as an identifier")
	}
}
