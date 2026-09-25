package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BySergiMM/nim/engine/internal/journal"
)

// anExecutable writes a file that looks enough like a program to be enrolled,
// and returns its path.
func anExecutable(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nimagent")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// The property this milestone exists to establish: an enrolment records the
// identity of a file, taken by the daemon from the filesystem, and never a
// device and inode the caller supplied.
func TestEnrolmentRecordsTheFilesOwnIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent enrolment resolves identity via peer.ImageOfFile, which always errors on " +
			"windows by design (see internal/peer/image_windows.go: \"Windows has no implementation " +
			"of any of this yet\"), so enrolling a real file cannot succeed there")
	}
	j := freshJournal(t)
	path := anExecutable(t, "claude")

	resp := handleAgentAdd(Request{
		ID: "1", Kind: KindAgentAdd, AgentName: "claude-code", AgentPath: path,
	}, j)
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	agents, err := j.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	got := agents[0]
	if got.Name != "claude-code" || got.ExecPath != path {
		t.Fatalf("unexpected enrolment: %+v", got)
	}

	// The identity must be the file's, as the operating system reports it.
	dev, ino, err := resolveAgentImage(path)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if got.ExecDev != dev || got.ExecIno != ino {
		t.Fatalf("stored dev=%d ino=%d, but the file is dev=%d ino=%d",
			got.ExecDev, got.ExecIno, dev, ino)
	}
	if got.ExecIno == 0 {
		t.Fatal("stored a zero inode, which would match nothing and mean nothing")
	}
}

// A caller cannot dictate the identity. The request carries a path; there is
// nowhere on it to put a device or inode, and that is deliberate -- the same
// reason credential.get returns a command instead of accepting one.
func TestARequestCannotCarryAnIdentity(t *testing.T) {
	var req Request
	// If either of these ever becomes settable from the wire, this stops
	// compiling, which is the point.
	req.AgentName = "x"
	req.AgentPath = "/bin/sh"
	if req.AgentName == "" || req.AgentPath == "" {
		t.Fatal("unreachable")
	}
}

// Two different files are two different agents, which is the whole basis for
// telling agents apart later.
func TestTwoDifferentExecutablesEnrolAsDifferentIdentities(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent enrolment resolves identity via peer.ImageOfFile, which always errors on " +
			"windows by design (see internal/peer/image_windows.go: \"Windows has no implementation " +
			"of any of this yet\"), so enrolling a real file cannot succeed there")
	}
	j := freshJournal(t)
	a := anExecutable(t, "claude")
	b := anExecutable(t, "cursor")

	for name, path := range map[string]string{"claude-code": a, "cursor": b} {
		if resp := handleAgentAdd(Request{
			ID: "1", Kind: KindAgentAdd, AgentName: name, AgentPath: path,
		}, j); resp.Error != "" {
			t.Fatalf("enrolling %s: %s", name, resp.Error)
		}
	}

	agents, err := j.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
	if agents[0].ExecIno == agents[1].ExecIno && agents[0].ExecDev == agents[1].ExecDev {
		t.Fatal("two different files enrolled as the same identity")
	}
}

// Re-enrolment under the same name replaces, so an agent cannot accumulate a
// second executable that also counts as it. It is also how an operator repairs
// an enrolment after an application updates itself.
func TestReEnrollingReplacesTheIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent enrolment resolves identity via peer.ImageOfFile, which always errors on " +
			"windows by design (see internal/peer/image_windows.go: \"Windows has no implementation " +
			"of any of this yet\"), so enrolling a real file cannot succeed there")
	}
	j := freshJournal(t)
	first := anExecutable(t, "old")
	second := anExecutable(t, "new")

	for _, path := range []string{first, second} {
		if resp := handleAgentAdd(Request{
			ID: "1", Kind: KindAgentAdd, AgentName: "claude-code", AgentPath: path,
		}, j); resp.Error != "" {
			t.Fatalf("enrolling %s: %s", path, resp.Error)
		}
	}

	agents, _ := j.ListAgents()
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1 -- re-enrolment must replace", len(agents))
	}
	wantDev, wantIno, _ := resolveAgentImage(second)
	if agents[0].ExecDev != wantDev || agents[0].ExecIno != wantIno {
		t.Fatal("re-enrolment did not replace the recorded identity")
	}
	if agents[0].ExecPath != second {
		t.Fatalf("recorded path is %s, want %s", agents[0].ExecPath, second)
	}
}

// An enrolment whose file has been replaced no longer matches anything. That
// has to be visible in a listing, because otherwise it is discovered later as
// a silent denial with no explanation.
func TestAReplacedFileIsReportedStaleRatherThanFailing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent enrolment resolves identity via peer.ImageOfFile, which always errors on " +
			"windows by design (see internal/peer/image_windows.go: \"Windows has no implementation " +
			"of any of this yet\"), so enrolling a real file cannot succeed there")
	}
	j := freshJournal(t)
	path := anExecutable(t, "claude")

	if resp := handleAgentAdd(Request{
		ID: "1", Kind: KindAgentAdd, AgentName: "claude-code", AgentPath: path,
	}, j); resp.Error != "" {
		t.Fatalf("enrolling: %s", resp.Error)
	}
	if resp := handleAgentList(Request{ID: "2", Kind: KindAgentList}, j); !resp.Agents[0].Current {
		t.Fatal("a freshly enrolled agent was reported stale")
	}

	// Replace the file the way a self-updating application does: write the new
	// one alongside and rename over the old.
	//
	// Not remove-then-create. That is what this test did first, and it passed
	// on APFS and failed on ext4, which reuses the inode number it just freed
	// -- so the "replacement" was the same identity and correctly reported as
	// current. Renaming keeps both files alive at once, which is the only way
	// to be sure of a distinct inode.
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("writing the replacement: %v", err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("renaming over: %v", err)
	}

	resp := handleAgentList(Request{ID: "3", Kind: KindAgentList}, j)
	if resp.Error != "" {
		t.Fatalf("listing must not fail because an enrolment went stale: %s", resp.Error)
	}
	if len(resp.Agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(resp.Agents))
	}
	if resp.Agents[0].Current {
		t.Fatal("a replaced file was still reported as the enrolled one")
	}
	// The recorded identity must not have quietly followed the new file.
	agents, _ := j.ListAgents()
	dev, ino, _ := resolveAgentImage(path)
	if agents[0].ExecDev == dev && agents[0].ExecIno == ino {
		t.Fatal("the enrolment silently adopted the replacement file")
	}
}

func TestEnrolmentRejectsWhatIsNotAnExecutableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent enrolment resolves identity via peer.ImageOfFile, which always errors on " +
			"windows by design (see internal/peer/image_windows.go: \"Windows has no implementation " +
			"of any of this yet\"), so enrolling a real file cannot succeed there")
	}
	j := freshJournal(t)
	dir := t.TempDir()
	notExecutable := filepath.Join(dir, "plain")
	if err := os.WriteFile(notExecutable, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, path, wants string }{
		{"missing", filepath.Join(dir, "nope"), "cannot enrol"},
		{"a directory", dir, "directory"},
		{"not executable", notExecutable, "not executable"},
	} {
		resp := handleAgentAdd(Request{
			ID: "1", Kind: KindAgentAdd, AgentName: "a", AgentPath: tc.path,
		}, j)
		if resp.Error == "" {
			t.Errorf("%s was accepted as an agent", tc.name)
			continue
		}
		if !strings.Contains(resp.Error, tc.wants) {
			t.Errorf("%s: error %q does not explain the problem", tc.name, resp.Error)
		}
	}
	if agents, _ := j.ListAgents(); len(agents) != 0 {
		t.Fatalf("%d enrolments were recorded despite every attempt failing", len(agents))
	}
}

func TestEnrolmentRejectsBadNamesAndRelativePaths(t *testing.T) {
	j := freshJournal(t)
	good := anExecutable(t, "ok")

	for _, tc := range []struct{ name, agent, path string }{
		{"empty name", "", good},
		{"path traversal in name", "../evil", good},
		{"slash in name", "a/b", good},
		{"relative path", "claude-code", "relative/path"},
		{"empty path", "claude-code", ""},
	} {
		if resp := handleAgentAdd(Request{
			ID: "1", Kind: KindAgentAdd, AgentName: tc.agent, AgentPath: tc.path,
		}, j); resp.Error == "" {
			t.Errorf("%s was accepted", tc.name)
		}
	}
	if agents, _ := j.ListAgents(); len(agents) != 0 {
		t.Fatalf("%d enrolments recorded from rejected requests", len(agents))
	}
}

func TestRemovingAnAgentIsIdempotent(t *testing.T) {
	j := freshJournal(t)
	path := anExecutable(t, "claude")
	handleAgentAdd(Request{ID: "1", Kind: KindAgentAdd, AgentName: "claude-code", AgentPath: path}, j)

	for i := 0; i < 2; i++ {
		if resp := handleAgentRemove(Request{
			ID: "1", Kind: KindAgentRemove, AgentName: "claude-code",
		}, j); resp.Error != "" {
			t.Fatalf("remove %d: %s", i+1, resp.Error)
		}
	}
	if agents, _ := j.ListAgents(); len(agents) != 0 {
		t.Fatalf("%d agents remain after removal", len(agents))
	}
}

// A connection commits to one purpose. Agent management is a third purpose
// alongside credentials and connectors, and mixing it with either is the shape
// a process probing this protocol would take.
func TestAConnectionCannotMixAgentAndOtherPurposes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent enrolment resolves identity via peer.ImageOfFile, which always errors on " +
			"windows by design (see internal/peer/image_windows.go: \"Windows has no implementation " +
			"of any of this yet\"), so enrolling a real file cannot succeed there")
	}
	j := freshJournal(t)
	store := newFakeStore()
	locks := newTargetLocks()
	approvals := newPendingRegistry()
	path := anExecutable(t, "claude")

	state := &requestState{}
	if resp := handleRequest(Request{
		ID: "1", Kind: KindAgentAdd, AgentName: "claude-code", AgentPath: path,
	}, state, j, store, locks, approvals); resp.Error != "" {
		t.Fatalf("the first agent request should succeed: %s", resp.Error)
	}
	for _, kind := range []string{KindCredentialGet, KindConnectorList} {
		resp := handleRequest(Request{ID: "2", Kind: kind, Target: "github"}, state, j, store, locks, approvals)
		if resp.Error != "unauthorized" {
			t.Errorf("%s on an agent connection returned %q, want unauthorized", kind, resp.Error)
		}
	}

	// And the other way round.
	other := &requestState{}
	handleRequest(Request{ID: "1", Kind: KindConnectorList}, other, j, store, locks, approvals)
	if resp := handleRequest(Request{
		ID: "2", Kind: KindAgentList,
	}, other, j, store, locks, approvals); resp.Error != "unauthorized" {
		t.Errorf("agent.list on a connector connection returned %q, want unauthorized", resp.Error)
	}
}

// Enrolment now writes into the chain -- the opposite of what this test used
// to assert.
//
// It read "Agents are configuration, and the record of what happened has to
// stay a record of what happened" until F-018: a rule is scoped to an
// enrolled name, and re-enrolling that name moves every rule scoped to it to
// a different executable with nothing in the chain saying so. Once a rule's
// scope depends on an enrolment, the enrolment is no longer pure
// configuration the way a connector is -- it is part of what the rules mean,
// and an audit of the rules without an audit of what they were scoped to is
// not an audit. handleAgentList stays a read: listing must not itself write.
func TestEnrolmentEntersTheChain(t *testing.T) {
	j := freshJournal(t)
	before, _, err := j.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}

	path := anExecutable(t, "claude")
	handleAgentAdd(Request{ID: "1", Kind: KindAgentAdd, AgentName: "claude-code", AgentPath: path}, j)
	handleAgentList(Request{ID: "2", Kind: KindAgentList}, j)
	handleAgentRemove(Request{ID: "3", Kind: KindAgentRemove, AgentName: "claude-code"}, j)

	after, _, err := j.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if after != before+2 {
		t.Fatalf("enrolling and removing one agent wrote %d entries to the chain, want 2", after-before)
	}

	entries, err := j.EntriesSince(before, 10)
	if err != nil {
		t.Fatalf("EntriesSince: %v", err)
	}
	if len(entries) != 2 || entries[0].Kind != journal.KindAgentAdd || entries[1].Kind != journal.KindAgentRemove {
		t.Fatalf("the chain does not show one agent.add followed by one agent.remove: %+v", entries)
	}
}

// The daemon says whose enrolment is in the way, so the operator can decide
// between reusing that name and removing it -- rather than being told a row
// could not be inserted.
func TestASecondNameForOneExecutableIsRefusedNamingTheFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent enrolment resolves identity via peer.ImageOfFile, which is unsupported on windows by design")
	}
	j := freshJournal(t)
	path := anExecutable(t, "claude")
	if resp := handleAgentAdd(Request{ID: "1", Kind: KindAgentAdd, AgentName: "claude-code", AgentPath: path}, j); resp.Error != "" {
		t.Fatalf("enrolling: %s", resp.Error)
	}
	resp := handleAgentAdd(Request{ID: "2", Kind: KindAgentAdd, AgentName: "evil", AgentPath: path}, j)
	if resp.Error == "" {
		t.Fatal("a second name for the same executable was accepted")
	}
	if !strings.Contains(resp.Error, "claude-code") || !strings.Contains(resp.Error, "nim agent remove") {
		t.Fatalf("the refusal does not say whose enrolment is in the way or what to do: %q", resp.Error)
	}
	if agents, _ := j.ListAgents(); len(agents) != 1 || agents[0].Name != "claude-code" {
		t.Fatalf("enrolments after the refusal: %+v", agents)
	}
}
