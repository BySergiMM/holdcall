package daemon

import (
	"slices"
	"testing"
	"time"
)

// TestACallerCannotNominateWhatReceivesACredential is the first attack found
// by the audit, at the layer that has to stop it.
//
// The live reproduction was one command:
//
//	holdcall serve --target github -- /bin/sh -c 'echo $GITHUB_TOKEN'
//
// and it printed the real secret. Both documented authorization layers
// passed it. Peer verification asks "is the caller this binary", and the
// caller was -- anything on the machine can be, by running it. Target
// binding asks "has this connection asked for a second target", and it had
// not; it asked for exactly the one it wanted, once.
//
// Neither layer ever constrained what the answer would be injected into. The
// command is registered with the connector now, and the daemon hands it back
// rather than accepting one, so the response can only ever direct a
// credential at the server the operator registered it for.
func TestACallerCannotNominateWhatReceivesACredential(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	store.Set("github", "ghp_REAL_SECRET_must_not_leak")

	registered := []string{"npx", "-y", "@modelcontextprotocol/server-github"}
	if err := j.SetConnector("github", "GITHUB_TOKEN", registered, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}

	// The attacker asks for github's credential and nominates its own
	// command, exactly as the live reproduction did.
	state := &requestState{}
	resp := handleCredentialGet(Request{
		ID: "attack", Kind: KindCredentialGet, Target: "github",
		Command: []string{"/bin/sh", "-c", "echo $GITHUB_TOKEN"},
	}, state, j, store, newTargetLocks())

	if resp.Error != "" {
		t.Fatalf("the legitimate lookup should still succeed: %s", resp.Error)
	}
	if !slices.Equal(resp.Command, registered) {
		t.Fatalf("the daemon returned command %v; a caller must not be able to choose it (wanted %v)",
			resp.Command, registered)
	}
	for _, arg := range resp.Command {
		if arg == "echo $GITHUB_TOKEN" || arg == "/bin/sh" {
			t.Fatalf("the attacker's command came back in the response: %v", resp.Command)
		}
	}
}

// A connector registered before commands existed has no statement about what
// may receive its secret. That is not "unrestricted": the daemon refuses,
// and the shim's fail-closed path turns the refusal into a downstream that
// does not start. Anything else would leave every pre-existing connector
// exactly as exploitable as before.
func TestAConnectorWithNoRegisteredCommandReleasesNothing(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	store.Set("legacy", "ghp_REAL_SECRET_must_not_leak")

	if err := j.SetConnector("legacy", "LEGACY_TOKEN", nil, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}

	state := &requestState{}
	resp := handleCredentialGet(
		Request{ID: "1", Kind: KindCredentialGet, Target: "legacy"}, state, j, store, newTargetLocks())

	if !resp.Found {
		t.Error("the connector exists, so Found must be true: the shim needs to tell this apart " +
			"from an unconfigured target, which fails open")
	}
	if resp.Error == "" {
		t.Fatal("a connector with no authorized command must fail closed")
	}
	if len(resp.Env) != 0 {
		t.Fatalf("a secret was released for a connector with no authorized command: %v", resp.Env)
	}
	for _, v := range resp.Env {
		if v == "ghp_REAL_SECRET_must_not_leak" {
			t.Fatal("the real secret leaked")
		}
	}
}

// Registering a connector without a command is refused up front. The failure
// belongs to whoever is configuring it, where it can be acted on, rather
// than to the agent that hits it at spawn time much later.
func TestRegisteringAConnectorWithoutACommandIsRefused(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()

	resp := handleConnectorSet(Request{
		ID: "1", Kind: KindConnectorSet, Target: "github",
		EnvKey: "GITHUB_TOKEN", Secret: "ghp_x",
	}, j, store, newTargetLocks())

	if resp.Error == "" {
		t.Fatal("a connector with no command was accepted")
	}
	if _, found, _ := j.ConnectorInfo("github"); found {
		t.Error("metadata was written despite the refusal")
	}
	if _, err := store.Get("github"); err == nil {
		t.Error("the secret reached the store despite the refusal")
	}
}

// The registered command round-trips through storage unchanged. It decides
// which process receives a credential, so a value that comes back subtly
// different -- re-split on spaces, re-quoted -- is a different authorization
// than the one that was granted.
func TestTheRegisteredCommandRoundTripsExactly(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()

	want := []string{"node", "/opt/my server/index.js", "--config", `{"k":"v with space"}`}
	resp := handleConnectorSet(Request{
		ID: "1", Kind: KindConnectorSet, Target: "github",
		EnvKey: "GITHUB_TOKEN", Secret: "ghp_x", Command: want,
	}, j, store, newTargetLocks())
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	state := &requestState{}
	got := handleCredentialGet(
		Request{ID: "2", Kind: KindCredentialGet, Target: "github"}, state, j, store, newTargetLocks())
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
	if !slices.Equal(got.Command, want) {
		t.Fatalf("got %v, want %v", got.Command, want)
	}
}

// Re-registering replaces the command, so a connector cannot accumulate a
// second server that is also allowed to receive its credential.
func TestReRegisteringReplacesTheAuthorizedCommand(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	locks := newTargetLocks()

	for _, cmd := range [][]string{{"old-server"}, {"new-server", "--flag"}} {
		resp := handleConnectorSet(Request{
			ID: "1", Kind: KindConnectorSet, Target: "github",
			EnvKey: "GITHUB_TOKEN", Secret: "ghp_x", Command: cmd,
		}, j, store, locks)
		if resp.Error != "" {
			t.Fatalf("unexpected error: %s", resp.Error)
		}
	}

	c, _, err := j.ConnectorInfo("github")
	if err != nil {
		t.Fatalf("ConnectorInfo: %v", err)
	}
	if !slices.Equal(c.Command, []string{"new-server", "--flag"}) {
		t.Fatalf("got %v, want the most recent registration only", c.Command)
	}
}

func TestValidateCommandBounds(t *testing.T) {
	if err := validateCommand(nil); err == nil {
		t.Error("an absent command must be refused")
	}
	if err := validateCommand([]string{""}); err == nil {
		t.Error("an empty program name must be refused")
	}
	if err := validateCommand(make([]string, MaxCommandArgs+1)); err == nil {
		t.Error("an over-long argv must be refused")
	}
	long := make([]byte, MaxCommandArgLen+1)
	if err := validateCommand([]string{"server", string(long)}); err == nil {
		t.Error("an over-long argument must be refused")
	}
	if err := validateCommand([]string{"server", "--flag", "value"}); err != nil {
		t.Errorf("an ordinary command was refused: %v", err)
	}
}
