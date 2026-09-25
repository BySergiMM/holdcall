package daemon

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/credential"
)

// fakeStore is an in-memory credential.Store for daemon-level tests: real
// keychain access belongs in internal/credential's own tests, not here.
type fakeStore struct {
	mu        sync.Mutex
	data      map[string]string
	getErr    map[string]error
	deleteErr map[string]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{data: map[string]string{}, getErr: map[string]error{}, deleteErr: map[string]error{}}
}

func (s *fakeStore) Set(target, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[target] = secret
	return nil
}

func (s *fakeStore) Get(target string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.getErr[target]; ok {
		return "", err
	}
	v, ok := s.data[target]
	if !ok {
		return "", credential.ErrNotFound
	}
	return v, nil
}

func (s *fakeStore) Delete(target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.deleteErr[target]; ok {
		return err
	}
	if _, ok := s.data[target]; !ok {
		return credential.ErrNotFound
	}
	delete(s.data, target)
	return nil
}

func (s *fakeStore) has(target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[target]
	return ok
}

// unverifiedState was a connState on the branch these tests came from, where
// peer verification lived inside the request dispatch. On this one the peer
// check runs once at accept, above both message families, so what a request
// carries is only the purpose binding -- see requestState. The tests below
// exercise the handlers themselves; the authorization layer around them has
// its own socket-backed tests in peer_authz_test.go.
func unverifiedState() *requestState { return &requestState{} }

// This is the fail-open guarantee: a target with no connector configured
// must behave exactly like it does without M3 at all.
func TestHandleCredentialGetOnUnconfiguredTargetIsFailOpen(t *testing.T) {
	j := freshJournal(t)
	resp := handleCredentialGet(Request{ID: "1", Kind: KindCredentialGet, Target: "github"}, unverifiedState(), j, newFakeStore(), newTargetLocks())
	if resp.Found {
		t.Fatal("an unconfigured target must report Found=false")
	}
	if resp.Error != "" {
		t.Fatalf("an unconfigured target must not be an error: %q", resp.Error)
	}
}

func TestHandleCredentialGetReturnsTheConfiguredEnv(t *testing.T) {
	j := freshJournal(t)
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	store := newFakeStore()
	if err := store.Set("github", "ghp_real_token"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	resp := handleCredentialGet(Request{ID: "1", Kind: KindCredentialGet, Target: "github"}, unverifiedState(), j, store, newTargetLocks())
	if !resp.Found || resp.Error != "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Env["GITHUB_TOKEN"] != "ghp_real_token" {
		t.Fatalf("Env = %v, want GITHUB_TOKEN=ghp_real_token", resp.Env)
	}
}

// This is the fail-closed guarantee: a connector IS configured but the
// secret cannot be retrieved, so the response must say so as an error
// rather than silently proceeding as if nothing were configured.
func TestHandleCredentialGetOnBrokenSecretIsFailClosed(t *testing.T) {
	j := freshJournal(t)
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	store := newFakeStore()
	// Deliberately no store.Set: the connector is configured in the journal
	// but its secret is missing from the store, e.g. deleted outside Nim.

	resp := handleCredentialGet(Request{ID: "1", Kind: KindCredentialGet, Target: "github"}, unverifiedState(), j, store, newTargetLocks())
	if !resp.Found {
		t.Fatal("a configured connector must report Found=true even when the secret is missing")
	}
	if resp.Error == "" {
		t.Fatal("a configured connector with no retrievable secret must be reported as an error (fail-closed)")
	}
	if len(resp.Env) != 0 {
		t.Fatalf("no env should be returned on the fail-closed path, got %v", resp.Env)
	}
}

// Found during the M3 final audit: handleCredentialGet reads connector
// metadata (env_key) and the secret in two separate steps. Without sharing
// connector.set's per-target lock, a connector.set renaming a target's
// env_key mid-flight can interleave between those two reads and hand back
// one generation's env_key paired with a different generation's secret --
// reproduced live thousands of times per 20000 iterations under -race
// before the fix. This runs a smaller version of that same race under -race
// as a permanent regression test.
func TestHandleCredentialGetNeverTearsEnvKeyAndSecretUnderConcurrentSet(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	locks := newTargetLocks()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			key, secret := "KEY_A", "secretA"
			if i%2 == 0 {
				key, secret = "KEY_B", "secretB"
			}
			handleConnectorSet(Request{ID: "s", Kind: KindConnectorSet, Target: "t", EnvKey: key, Secret: secret}, j, store, locks)
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	for i := 0; i < 2000; i++ {
		resp := handleCredentialGet(Request{ID: "g", Kind: KindCredentialGet, Target: "t"}, unverifiedState(), j, store, locks)
		if !resp.Found || resp.Error != "" {
			continue
		}
		for k, v := range resp.Env {
			if (k == "KEY_A" && v != "secretA") || (k == "KEY_B" && v != "secretB") {
				t.Fatalf("torn read: env_key=%s paired with secret=%s from a different connector.set generation", k, v)
			}
		}
	}
}

func TestHandleCredentialGetWithNoStoreIsFailClosed(t *testing.T) {
	j := freshJournal(t)
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	resp := handleCredentialGet(Request{ID: "1", Kind: KindCredentialGet, Target: "github"}, unverifiedState(), j, nil, newTargetLocks())
	if !resp.Found || resp.Error == "" {
		t.Fatalf("a configured connector with no credential store available must fail closed: %+v", resp)
	}
}

// The target-binding invariant: once a connection has asked for one
// target's credential, it may never pivot to ask for another on the same
// connection. This is the second authorization layer (see connState),
// independent of and in addition to peer verification.
func TestHandleCredentialGetRejectsPivotingToADifferentTargetOnTheSameConnection(t *testing.T) {
	j := freshJournal(t)
	for _, target := range []string{"github", "slack"} {
		if err := j.SetConnector(target, "TOKEN", []string{"server"}, time.Now()); err != nil {
			t.Fatalf("SetConnector(%s): %v", target, err)
		}
	}
	store := newFakeStore()
	store.Set("github", "ghp_x")
	store.Set("slack", "xoxb_y")

	state := unverifiedState()
	locks := newTargetLocks()
	first := handleCredentialGet(Request{ID: "1", Kind: KindCredentialGet, Target: "github"}, state, j, store, locks)
	if !first.Found || first.Error != "" {
		t.Fatalf("first request (own target) should succeed: %+v", first)
	}

	second := handleCredentialGet(Request{ID: "2", Kind: KindCredentialGet, Target: "slack"}, state, j, store, locks)
	if second.Error != "unauthorized" {
		t.Fatalf("pivoting to a second target on the same connection: got %+v, want Error=unauthorized", second)
	}
	if len(second.Env) != 0 {
		t.Fatalf("a denied pivot must never include Env: %+v", second)
	}

	// Repeating the SAME (already-bound) target must still work.
	third := handleCredentialGet(Request{ID: "3", Kind: KindCredentialGet, Target: "github"}, state, j, store, locks)
	if !third.Found || third.Error != "" {
		t.Fatalf("repeating the already-bound target should still succeed: %+v", third)
	}
}

func TestHandleCredentialGetRejectsAnInvalidTarget(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	for _, target := range []string{"", "  ", ".", "..", "a/b", "a\\b", "a/../b", strings.Repeat("x", MaxTargetLen+1)} {
		state := unverifiedState()
		resp := handleCredentialGet(Request{ID: "1", Kind: KindCredentialGet, Target: target}, state, j, store, newTargetLocks())
		if resp.Error == "" {
			t.Errorf("target %q should have been rejected by validation, got %+v", target, resp)
		}
	}
}

func TestHandleConnectorSetStoresBothMetadataAndSecret(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()

	resp := handleConnectorSet(Request{ID: "1", Kind: KindConnectorSet, Target: "github", EnvKey: "GITHUB_TOKEN", Secret: "ghp_x", Command: []string{"server"}}, j, store, newTargetLocks())
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	c, found, err := j.ConnectorInfo("github")
	if err != nil || !found {
		t.Fatalf("ConnectorInfo: found=%v err=%v", found, err)
	}
	if c.EnvKey != "GITHUB_TOKEN" {
		t.Errorf("env_key = %q, want GITHUB_TOKEN", c.EnvKey)
	}
	got, err := store.Get("github")
	if err != nil || got != "ghp_x" {
		t.Errorf("store.Get(github) = %q, %v, want ghp_x, nil", got, err)
	}
}

// If the secret lands in the store but the metadata write fails, the two
// must not be left inconsistent -- an orphaned keychain entry with no
// matching metadata would be invisible to connector list but still
// retrievable by guessing the target name.
func TestHandleConnectorSetRollsBackTheSecretIfMetadataWriteFails(t *testing.T) {
	j := freshJournal(t)
	j.Close() // force SetConnector to fail on a closed database
	store := newFakeStore()

	resp := handleConnectorSet(Request{ID: "1", Kind: KindConnectorSet, Target: "github", EnvKey: "GITHUB_TOKEN", Secret: "ghp_x", Command: []string{"server"}}, j, store, newTargetLocks())
	if resp.Error == "" {
		t.Fatal("expected an error when the metadata write fails")
	}
	if store.has("github") {
		t.Fatal("the secret must be rolled back from the store when the metadata write fails")
	}
}

func TestHandleConnectorSetRequiresAllFields(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	for _, req := range []Request{
		{ID: "1", Kind: KindConnectorSet, Target: "", EnvKey: "K", Secret: "v"},
		{ID: "1", Kind: KindConnectorSet, Target: "t", EnvKey: "", Secret: "v"},
		{ID: "1", Kind: KindConnectorSet, Target: "t", EnvKey: "K", Secret: ""},
	} {
		resp := handleConnectorSet(req, j, store, newTargetLocks())
		if resp.Error == "" {
			t.Errorf("expected an error for incomplete request %+v", req)
		}
	}
}

// Causa raiz fix for the Windows path traversal finding: target validation
// happens once, centrally, before any backend -- not sanitized, rejected.
func TestHandleConnectorSetRejectsPathTraversalAndInvalidTargets(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	for _, target := range []string{
		"", "  ", ".", "..", "a/b", "a\\b", "../../../etc/passwd", "a/../b",
		strings.Repeat("x", MaxTargetLen+1),
	} {
		resp := handleConnectorSet(Request{ID: "1", Kind: KindConnectorSet, Target: target, EnvKey: "K", Secret: "v"}, j, store, newTargetLocks())
		if resp.Error == "" {
			t.Errorf("target %q should have been rejected, got %+v", target, resp)
		}
		if store.has(target) {
			t.Errorf("target %q must not have reached the store", target)
		}
	}
}

func TestHandleConnectorSetRejectsOversizedFields(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	cases := []Request{
		{ID: "1", Kind: KindConnectorSet, Target: "github", EnvKey: strings.Repeat("K", MaxEnvKeyLen+1), Secret: "v"},
		{ID: "2", Kind: KindConnectorSet, Target: "github", EnvKey: "K", Secret: strings.Repeat("v", MaxSecretLen+1)},
	}
	for _, req := range cases {
		resp := handleConnectorSet(req, j, store, newTargetLocks())
		if resp.Error == "" {
			t.Errorf("oversized request should have been rejected: %+v", req)
		}
	}
}

func TestHandleConnectorSetAcceptsExactlyTheSizeLimit(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	req := Request{
		ID: "1", Kind: KindConnectorSet,
		Target:  strings.Repeat("a", MaxTargetLen),
		EnvKey:  strings.Repeat("K", MaxEnvKeyLen),
		Secret:  strings.Repeat("v", MaxSecretLen),
		Command: []string{"server"},
	}
	resp := handleConnectorSet(req, j, store, newTargetLocks())
	if resp.Error != "" {
		t.Fatalf("a request exactly at the size limits must be accepted: %s", resp.Error)
	}
}

func TestHandleConnectorListReturnsNoSecretsEverTouchingTheStore(t *testing.T) {
	j := freshJournal(t)
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	resp := handleConnectorList(Request{ID: "1", Kind: KindConnectorList}, j)
	if len(resp.Connectors) != 1 || resp.Connectors[0].Target != "github" {
		t.Fatalf("unexpected connectors: %+v", resp.Connectors)
	}
}

func TestHandleConnectorRemoveDeletesMetadataAndSecret(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	store.Set("github", "ghp_x")

	resp := handleConnectorRemove(Request{ID: "1", Kind: KindConnectorRemove, Target: "github"}, j, store, newTargetLocks())
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if _, found, _ := j.ConnectorInfo("github"); found {
		t.Error("connector metadata should be gone")
	}
	if store.has("github") {
		t.Error("secret should be gone from the store")
	}
}

func TestHandleConnectorRemoveOnUnconfiguredTargetIsANoOp(t *testing.T) {
	j := freshJournal(t)
	resp := handleConnectorRemove(Request{ID: "1", Kind: KindConnectorRemove, Target: "never-configured"}, j, newFakeStore(), newTargetLocks())
	if resp.Error != "" {
		t.Fatalf("removing an unconfigured target must not error: %s", resp.Error)
	}
}

func TestHandleConnectorRemoveRejectsAnInvalidTarget(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	for _, target := range []string{"", "..", "a/../b"} {
		resp := handleConnectorRemove(Request{ID: "1", Kind: KindConnectorRemove, Target: target}, j, store, newTargetLocks())
		if resp.Error == "" {
			t.Errorf("target %q should have been rejected", target)
		}
	}
}

// If the secret can't be removed from the store, the metadata must be left
// in place rather than deleted anyway: a connector that still shows up as
// configured but whose secret is gone correctly fails closed on the next
// credential.get, instead of the secret quietly surviving in the store with
// no metadata left pointing at it.
func TestHandleConnectorRemoveLeavesMetadataIfSecretRemovalFails(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	store.Set("github", "ghp_x")
	store.deleteErr["github"] = errors.New("keychain locked")

	resp := handleConnectorRemove(Request{ID: "1", Kind: KindConnectorRemove, Target: "github"}, j, store, newTargetLocks())
	if resp.Error == "" {
		t.Fatal("expected an error when the secret cannot be removed")
	}
	if _, found, _ := j.ConnectorInfo("github"); !found {
		t.Error("connector metadata must survive a failed secret removal, not vanish alongside it")
	}
}

// End-to-end over a real connection: a Request must get a Response, and an
// Event on the same connection afterward must still be applied exactly as
// before -- the new dispatch in handle() must not disturb the existing path.
// This uses net.Pipe, so peer verification reports "unsupported" (like
// Windows); the authorization-layer-specific tests below use a real unix
// socket, which is what peer verification actually inspects.
func TestHandleDispatchesRequestsAndEventsOnTheSameConnection(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	store.Set("github", "ghp_x")

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	done := make(chan struct{})
	go func() {
		handle(server, j, store, newTargetLocks(), newSessionRegistry(), newPendingRegistry(), testApprovalTimeout)
		close(done)
	}()

	resp, err := SendRequest(client, Request{ID: "1", Kind: KindCredentialGet, Target: "github"})
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	if !resp.Found || resp.Env["GITHUB_TOKEN"] != "ghp_x" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	enc := json.NewEncoder(client)
	if err := enc.Encode(Event{Kind: KindSessionStart, SessionID: "s1", Connector: "github", MachineID: "m", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("encode session.start: %v", err)
	}
	client.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handle did not return after the connection closed")
	}
}

// A connection must commit to exactly one purpose: a real shim only ever
// calls credential.get, a real CLI invocation only ever calls one
// connector.* kind. Mixing credential.get and connector.set on the same
// connection is exactly the shape a compromised process speaking the raw
// protocol directly (bypassing both the shim and the CLI) would produce.
func TestHandleRejectsMixingCredentialAndConnectorRequestsOnOneConnection(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	store.Set("github", "ghp_x")

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	done := make(chan struct{})
	go func() {
		handle(server, j, store, newTargetLocks(), newSessionRegistry(), newPendingRegistry(), testApprovalTimeout)
		close(done)
	}()

	first, err := SendRequest(client, Request{ID: "1", Kind: KindCredentialGet, Target: "github"})
	if err != nil || !first.Found {
		t.Fatalf("first (credential.get) request should succeed: %+v, %v", first, err)
	}

	second, err := SendRequest(client, Request{ID: "2", Kind: KindConnectorList})
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	if second.Error != "unauthorized" {
		t.Fatalf("connector.list after credential.get on the same connection: got %+v, want Error=unauthorized", second)
	}
	if second.Connectors != nil {
		t.Fatalf("a denied request must never include Connectors: %+v", second)
	}
	client.Close()
	<-done
}
