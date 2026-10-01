//go:build unix

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Directories here are made and loosened by the test itself, never taken from
// the system: a test that pointed EnsurePrivateDir at $HOME or /tmp would be
// testing what it is meant to protect.

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

// hearWarnings collects what EnsurePrivateDir says to the operator.
func hearWarnings(t *testing.T) *[]string {
	t.Helper()
	var heard []string
	was := warn
	warn = func(format string, args ...any) { heard = append(heard, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { warn = was })
	return &heard
}

// looseDir makes a directory of ours that others can reach, the way somebody
// else's tool, or the operator, would have: Mkdir's mode is masked by the umask
// and Chmod's is not.
func looseDir(t *testing.T, parent, name string, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	return dir
}

// sharedTempDir is what /tmp looks like -- somebody else's, open to everybody,
// sticky -- and is made the temp directory for the test.
func sharedTempDir(t *testing.T) string {
	t.Helper()
	shared, err := os.MkdirTemp("", "hcshared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(shared) })
	if err := os.Chmod(shared, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", shared)
	return shared
}

// A directory somebody else made, or the operator chose, is not Holdcall's to
// change. socket = ~/holdcall.sock names the home directory, and narrowing it
// to 0700 would take away the access that the user's other users and groups
// have to it; a chmod that fails on a filesystem that has no modes (drvfs, NFS)
// would stop the daemon over a directory it never owned.
func TestEnsurePrivateDirNeverChangesADirectoryItDidNotCreate(t *testing.T) {
	warnings := hearWarnings(t)
	for _, loose := range []os.FileMode{0o755, 0o750, 0o705, 0o770, 0o777, 0o701, 0o775} {
		dir := looseDir(t, t.TempDir(), "d", loose)
		inside := filepath.Join(dir, "kept")
		if err := os.WriteFile(inside, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		before := len(*warnings)

		if err := EnsurePrivateDir(dir); err != nil {
			t.Fatalf("a directory of ours at %04o was refused: %v", loose, err)
		}
		if got := modeOf(t, dir); got != loose {
			t.Errorf("a directory at %04o that Holdcall did not create is %04o after EnsurePrivateDir; it is not Holdcall's to change", loose, got)
		}
		if got := modeOf(t, inside); got != 0o640 {
			t.Errorf("a file inside was changed from 0640 to %04o", got)
		}
		if len(*warnings) != before+1 {
			t.Fatalf("a directory at %04o that others can reach produced %d warnings, want 1", loose, len(*warnings)-before)
		}
		said := (*warnings)[before]
		for _, want := range []string{dir, fmt.Sprintf("%04o", loose), "did not create", "chmod 700"} {
			if !strings.Contains(said, want) {
				t.Errorf("the warning for %04o does not say %q: %s", loose, want, said)
			}
		}
	}
}

// The case that made this the rule: a socket configured as ~/x.sock has the home
// directory as its directory.
func TestEnsurePrivateDirDoesNotTakeAHomeDirectoryAway(t *testing.T) {
	hearWarnings(t)
	home := looseDir(t, t.TempDir(), "home", 0o755)
	if err := EnsurePrivateDir(home); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, home); got != 0o755 {
		t.Errorf("a home directory at 0755 is %04o after EnsurePrivateDir", got)
	}
}

// A directory is complained about once per process: the daemon looks at its
// socket's directory twice, and a line in the log for each is noise that teaches
// an operator to stop reading it.
func TestEnsurePrivateDirWarnsOnlyOncePerDirectory(t *testing.T) {
	warnings := hearWarnings(t)
	dir := looseDir(t, t.TempDir(), "d", 0o755)
	other := looseDir(t, t.TempDir(), "o", 0o755)
	for i := 0; i < 3; i++ {
		if err := EnsurePrivateDir(dir); err != nil {
			t.Fatal(err)
		}
	}
	if len(*warnings) != 1 {
		t.Fatalf("three calls for one directory produced %d warnings, want 1", len(*warnings))
	}
	if err := EnsurePrivateDir(other); err != nil {
		t.Fatal(err)
	}
	if len(*warnings) != 2 {
		t.Errorf("a second directory was not warned about: %d warnings", len(*warnings))
	}
}

// Nothing to say about a directory that is private, or one that was just made.
func TestEnsurePrivateDirSaysNothingAboutAPrivateDirectory(t *testing.T) {
	warnings := hearWarnings(t)
	existing := looseDir(t, t.TempDir(), "d", 0o700)
	for _, dir := range []string{existing, filepath.Join(t.TempDir(), "new")} {
		if err := EnsurePrivateDir(dir); err != nil {
			t.Fatal(err)
		}
		if got := modeOf(t, dir); got != 0o700 {
			t.Errorf("%s is %04o, want 0700", dir, got)
		}
	}
	if len(*warnings) != 0 {
		t.Errorf("a private directory produced warnings: %v", *warnings)
	}
}

// What Holdcall makes is its own and is private whatever its parent is: a
// socket directory nested under a directory that is open to others is made
// 0700, and the loose parent is left alone.
func TestEnsurePrivateDirMakesWhatItCreatesPrivateAndLeavesTheParentAlone(t *testing.T) {
	warnings := hearWarnings(t)
	parent := looseDir(t, t.TempDir(), "parent", 0o755)
	dir := filepath.Join(parent, "run")

	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, dir); got != 0o700 {
		t.Errorf("a directory created inside a 0755 one is %04o, want 0700", got)
	}
	if got := modeOf(t, parent); got != 0o755 {
		t.Errorf("the parent that already existed was changed from 0755 to %04o", got)
	}
	if len(*warnings) != 0 {
		t.Errorf("creating a directory produced warnings: %v", *warnings)
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
// is where it leads. It is the operator's directory like any other, so it is
// followed, checked, and not changed.
func TestEnsurePrivateDirChecksTheDirectoryALinkLeadsTo(t *testing.T) {
	warnings := hearWarnings(t)
	root := t.TempDir()
	target := looseDir(t, root, "target", 0o755)
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := EnsurePrivateDir(link); err != nil {
		t.Fatalf("a link to a directory of ours was refused: %v", err)
	}
	if got := modeOf(t, target); got != 0o755 {
		t.Errorf("the directory behind the link is %04o, want it left at 0755", got)
	}
	if len(*warnings) != 1 {
		t.Errorf("a link to a directory that others can reach produced %d warnings, want 1", len(*warnings))
	}

	asSomeoneElse(t)
	if err := EnsurePrivateDir(link); !errors.Is(err, ErrDirNotOurs) {
		t.Errorf("a link to a directory another user owns was not refused: %v", err)
	}
}

// Holdcall's own default runtime directory, holdcall-<uid> inside a shared
// temp directory, is the one directory that was not made by this call and is
// still Holdcall's: it is tightened if it was left open.
func TestEnsurePrivateDirTightensTheDefaultRuntimeDirectory(t *testing.T) {
	warnings := hearWarnings(t)
	shared := sharedTempDir(t)
	dir := looseDir(t, shared, "holdcall-"+strconv.Itoa(os.Geteuid()), 0o755)

	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, dir); got != 0o700 {
		t.Errorf("the default runtime directory is %04o after EnsurePrivateDir, want 0700", got)
	}
	if len(*warnings) != 0 {
		t.Errorf("tightening the default runtime directory produced warnings: %v", *warnings)
	}
}

// The directory is in /tmp, where anybody can put anything: a symbolic link at
// that name could lead chmod to a directory of this user's that was never
// Holdcall's -- $HOME, say -- and a daemon that followed it would be tightening
// that. It is refused, and what it points to is not touched.
func TestEnsurePrivateDirRefusesALinkAtTheDefaultRuntimeDirectory(t *testing.T) {
	hearWarnings(t)
	shared := sharedTempDir(t)
	victim := looseDir(t, t.TempDir(), "victim", 0o755)
	link := filepath.Join(shared, "holdcall-"+strconv.Itoa(os.Geteuid()))
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	err := EnsurePrivateDir(link)
	if err == nil {
		t.Fatal("a symbolic link at the default runtime directory was followed")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if got := modeOf(t, victim); got != 0o755 {
		t.Errorf("the directory the link led to was changed from 0755 to %04o", got)
	}
}

// Somebody else got to the name first: refused, and left as it was.
func TestEnsurePrivateDirRefusesTheDefaultRuntimeDirectoryAnotherUserOwns(t *testing.T) {
	hearWarnings(t)
	shared := sharedTempDir(t)
	asSomeoneElse(t)
	dir := looseDir(t, shared, "holdcall-"+strconv.Itoa(os.Geteuid()+1), 0o755)

	if err := EnsurePrivateDir(dir); !errors.Is(err, ErrDirNotOurs) {
		t.Fatalf("a runtime directory another user owns was not refused: %v", err)
	}
	if got := modeOf(t, dir); got != 0o755 {
		t.Errorf("the refused directory was changed from 0755 to %04o", got)
	}
}

// EnsureDirs is what the daemon and every relay call before they start, and it
// has three directories to get right: the home, the data directory the journal
// is in, and the directory the socket is in. Those that are not there are made
// private; those that are, and that others can reach, are the operator's and
// are warned about.
func TestEnsureDirsMakesWhatIsMissingPrivateAndLeavesWhatIsThereAlone(t *testing.T) {
	warnings := hearWarnings(t)
	root := t.TempDir()
	home := looseDir(t, root, "home", 0o755)
	data := filepath.Join(root, "data")        // missing: made here
	sockDir := looseDir(t, root, "run", 0o750) // there: left
	t.Setenv(HomeEnvVar, home)
	cfg := Config{Daemon: Daemon{Socket: filepath.Join(sockDir, "d.sock"), DataDir: data}}

	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]struct {
		dir  string
		mode os.FileMode
	}{"home": {home, 0o755}, "data": {data, 0o700}, "socket": {sockDir, 0o750}} {
		if got := modeOf(t, want.dir); got != want.mode {
			t.Errorf("the %s directory is %04o after EnsureDirs, want %04o", name, got, want.mode)
		}
	}
	if len(*warnings) != 2 {
		t.Errorf("EnsureDirs warned %d times, want once for each of the two directories that were open: %v",
			len(*warnings), *warnings)
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

	shared := sharedTempDir(t)

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
