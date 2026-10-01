//go:build unix

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Directories here are made and loosened by the test itself, never taken from
// the system: EnsurePrivateDir chmods what it is given, and a test that pointed
// it at /tmp while running as root would be taking /tmp away from everybody.

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// asSomeoneElse makes every directory look as if it belonged to another user,
// which is the only way to test the refusal without being root to chown one.
func asSomeoneElse(t *testing.T) {
	t.Helper()
	currentUID = func() int { return os.Geteuid() + 1 }
	t.Cleanup(func() { currentUID = os.Geteuid })
}

// MkdirAll never tightened a directory that was already there, so a directory
// somebody made 0755 before Holdcall did stayed open to every local user: the
// journal in it is created with the umask's default, and the socket in it is
// reachable until it is chmod'ed.
func TestEnsurePrivateDirNarrowsADirectoryWeOwnThatOthersCouldReach(t *testing.T) {
	for _, loose := range []os.FileMode{0o755, 0o750, 0o705, 0o770, 0o777, 0o701} {
		dir := filepath.Join(t.TempDir(), "d")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, loose); err != nil { // Mkdir's mode is masked by the umask; Chmod's is not
			t.Fatal(err)
		}
		inside := filepath.Join(dir, "kept")
		if err := os.WriteFile(inside, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}

		if err := EnsurePrivateDir(dir); err != nil {
			t.Fatalf("a directory of ours at %04o was refused: %v", loose, err)
		}
		if got := modeOf(t, dir); got != 0o700 {
			t.Errorf("a directory at %04o is %04o after EnsurePrivateDir, want 0700", loose, got)
		}
		if got := modeOf(t, inside); got != 0o640 {
			t.Errorf("a file inside was changed from 0640 to %04o; only the directory is ours to narrow", got)
		}
	}
}

func TestEnsurePrivateDirCreatesWhatIsMissingPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, dir); got != 0o700 {
		t.Errorf("a new directory is %04o, want 0700", got)
	}
}

func TestEnsurePrivateDirLeavesAPrivateDirectoryAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, dir); got != 0o700 {
		t.Errorf("a private directory is %04o after EnsurePrivateDir, want 0700", got)
	}
}

// A directory another user owns is refused and left exactly as it was: the
// owner can replace anything in it whatever its mode says, and narrowing the
// mode of somebody else's directory is not ours to do even where root could.
func TestEnsurePrivateDirRefusesADirectoryAnotherUserOwns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "theirs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	asSomeoneElse(t)

	err := EnsurePrivateDir(dir)
	if !errors.Is(err, ErrDirNotOurs) {
		t.Fatalf("a directory owned by another user was not refused: %v", err)
	}
	for _, want := range []string{dir, "uid " + strconv.Itoa(os.Geteuid()), "uid " + strconv.Itoa(os.Geteuid()+1)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if got := modeOf(t, dir); got != 0o755 {
		t.Errorf("the refused directory was changed from 0755 to %04o", got)
	}
}

func TestEnsurePrivateDirRefusesWhatIsNotADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(file); err == nil {
		t.Fatal("a regular file was accepted as a directory")
	}
}

// A home that is a link to another disk is legitimate, and what has to be ours
// is where it leads.
func TestEnsurePrivateDirChecksTheDirectoryALinkLeadsTo(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := EnsurePrivateDir(link); err != nil {
		t.Fatalf("a link to a directory of ours was refused: %v", err)
	}
	if got := modeOf(t, target); got != 0o700 {
		t.Errorf("the directory behind the link is %04o, want 0700", got)
	}

	asSomeoneElse(t)
	if err := EnsurePrivateDir(link); !errors.Is(err, ErrDirNotOurs) {
		t.Errorf("a link to a directory another user owns was not refused: %v", err)
	}
}

// EnsureDirs is what the daemon and every relay call before they start, and it
// has three directories to get right: the home, the data directory the journal
// is in, and the directory the socket is in.
func TestEnsureDirsMakesHomeDataAndSocketDirectoriesPrivate(t *testing.T) {
	root := t.TempDir()
	loosen := func(name string) string {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	home, data, sockDir := loosen("home"), loosen("data"), loosen("run")
	t.Setenv(HomeEnvVar, home)
	cfg := Config{Daemon: Daemon{Socket: filepath.Join(sockDir, "d.sock"), DataDir: data}}

	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"home": home, "data": data, "socket": sockDir} {
		if got := modeOf(t, dir); got != 0o700 {
			t.Errorf("the %s directory is %04o after EnsureDirs, want 0700", name, got)
		}
	}
}

// The operator who reads the refusal has a configuration to fix, not a bug to
// report, so it says which directory and which setting.
func TestARefusedDirectoryIsNamedWithTheSettingThatMovesIt(t *testing.T) {
	root := t.TempDir()
	t.Setenv(HomeEnvVar, filepath.Join(root, "home"))
	cfg := Config{Daemon: Daemon{
		Socket:  filepath.Join(root, "run", "d.sock"),
		DataDir: filepath.Join(root, "data"),
	}}
	want := map[string][]string{
		"the home directory":   {"HOLDCALL_HOME", filepath.Join(root, "home")},
		"the data directory":   {"[daemon] data_dir", filepath.Join(root, "data")},
		"the socket directory": {"[daemon] socket", filepath.Join(root, "run")},
	}
	asSomeoneElse(t)

	layout := cfg.layout()
	if len(layout) != len(want) {
		t.Fatalf("the layout has %d directories, this test knows %d", len(layout), len(want))
	}
	for _, d := range layout {
		if err := os.MkdirAll(d.dir, 0o700); err != nil {
			t.Fatal(err)
		}
		err := d.ensure()
		if !errors.Is(err, ErrDirNotOurs) {
			t.Errorf("%s: a directory another user owns was not refused: %v", d.what, err)
			continue
		}
		for _, fragment := range append(want[d.what], d.what, "uid") {
			if !strings.Contains(err.Error(), fragment) {
				t.Errorf("%s: the refusal does not say %q: %v", d.what, fragment, err)
			}
		}
	}

	// And EnsureDirs, which is what the daemon and the relay call, stops at the first.
	if err := cfg.EnsureDirs(); !errors.Is(err, ErrDirNotOurs) {
		t.Errorf("EnsureDirs did not refuse: %v", err)
	}
}

// Without XDG_RUNTIME_DIR a Linux temp directory is /tmp, which belongs to root
// and which every user can write to. The default socket must not be put
// directly in a directory like that -- the daemon would refuse to start in it,
// and rightly -- but in one of this user's own inside it.
func TestTheDefaultSocketIsNeverPutDirectlyInADirectoryOthersCanUse(t *testing.T) {
	t.Setenv(HomeEnvVar, filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	shared, err := os.MkdirTemp("", "hcshared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(shared) })
	if err := os.Chmod(shared, 0o777|os.ModeSticky); err != nil { // what /tmp looks like
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", shared)

	got := defaultSocket()
	want := filepath.Join(shared, "holdcall-"+strconv.Itoa(os.Geteuid()))
	if dir := filepath.Dir(got); dir != want {
		t.Errorf("with a shared temp directory the socket is in %s, want it in %s", dir, want)
	}
	if err := EnsurePrivateDir(filepath.Dir(got)); err != nil {
		t.Fatalf("the directory chosen for the socket cannot be made private: %v", err)
	}
	if mode := modeOf(t, filepath.Dir(got)); mode != 0o700 {
		t.Errorf("the socket's directory is %04o, want 0700", mode)
	}
}

// Where the temp directory is already private to the user -- macOS's $TMPDIR
// is -- the socket stays where it has always been: moving it would leave a
// daemon that an earlier version started unreachable, and gains nothing.
func TestTheDefaultSocketStaysInATempDirectoryThatIsAlreadyPrivate(t *testing.T) {
	t.Setenv(HomeEnvVar, filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	private, err := os.MkdirTemp("", "hcprivate") // 0700, ours
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(private) })
	t.Setenv("TMPDIR", private)

	if dir := filepath.Dir(defaultSocket()); dir != private {
		t.Errorf("with a private temp directory the socket is in %s, want it in %s", dir, private)
	}
}

func TestTheDefaultSocketUsesXDGRuntimeDirWhenThereIsOne(t *testing.T) {
	t.Setenv(HomeEnvVar, filepath.Join(t.TempDir(), "home"))
	runDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runDir)
	if dir := filepath.Dir(defaultSocket()); dir != runDir {
		t.Errorf("the socket is in %s, want XDG_RUNTIME_DIR %s", dir, runDir)
	}
}
