package daemon

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// A secret is released only to a peer the platform confirmed. Where the
// platform cannot ask (Windows) the connection is let through, because refusing
// it would refuse every relay there, and it is never given a credential.
//
// These tests build that situation on every platform, so a linux or macOS run
// proves the refusal too: a connection whose type the peer package cannot
// interrogate is "unsupported", which is exactly what Windows reports for every
// connection. The windows-only test next door runs the same question against a
// real daemon on a real socket there.

// readCountingStore is a store that remembers whether anybody asked it for a
// secret. A refusal that happened after the secret was already read would be a
// refusal in name only.
type readCountingStore struct {
	*fakeStore
	mu   sync.Mutex
	gets int
}

func (s *readCountingStore) Get(target string) (string, error) {
	s.mu.Lock()
	s.gets++
	s.mu.Unlock()
	return s.fakeStore.Get(target)
}

func (s *readCountingStore) reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

// const rather than a literal in each test, and obviously fake: the point is
// that this exact string never appears in what an unverified peer is sent.
const unverifiedTestSecret = "FAKE-secret-for-the-unverified-peer-tests-not-a-token"

func TestACredentialIsNotReleasedToAConnectionNobodyVerified(t *testing.T) {
	j := freshJournal(t)
	store := &readCountingStore{fakeStore: newFakeStore()}
	if err := store.Set("github", unverifiedTestSecret); err != nil {
		t.Fatal(err)
	}
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	ask := Request{ID: "1", Kind: KindCredentialGet, Target: "github"}

	// The zero state is a connection nobody verified.
	resp := handleCredentialGet(ask, &requestState{}, j, store, newTargetLocks())

	if !resp.Found || resp.Error == "" {
		t.Fatalf("a configured connector asked for by an unverified peer must be Found with an error, "+
			"the fail-closed shape the shim refuses to spawn on: %+v", resp)
	}
	if !strings.Contains(resp.Error, "could not verify who is asking") {
		t.Errorf("the refusal does not say why: %q", resp.Error)
	}
	if len(resp.Env) != 0 || len(resp.Command) != 0 {
		t.Errorf("an unverified peer was given something to inject: env %v, command %v", resp.Env, resp.Command)
	}
	wire, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), unverifiedTestSecret) {
		t.Fatalf("the secret is in what an unverified peer was sent: %s", wire)
	}
	if n := store.reads(); n != 0 {
		t.Errorf("the secret was read from the store %d time(s) for a peer that was then refused; "+
			"it must not be fetched at all", n)
	}

	// The control that makes the refusal mean something: the same request from
	// a verified connection is answered with the credential.
	ok := handleCredentialGet(ask, verifiedState(), j, store, newTargetLocks())
	if ok.Error != "" || ok.Env["GITHUB_TOKEN"] != unverifiedTestSecret {
		t.Fatalf("the verified control was not released the credential: %+v", ok)
	}
}

// What an unverified peer is refused is a secret, not the whole protocol: a
// relay for a server with no connector must still start on a platform that
// cannot verify its peers, or nothing would run there at all.
func TestAConnectionNobodyVerifiedIsStillToldThatATargetHasNoConnector(t *testing.T) {
	j := freshJournal(t)
	store := &readCountingStore{fakeStore: newFakeStore()}

	resp := handleCredentialGet(Request{ID: "1", Kind: KindCredentialGet, Target: "no-connector-here"},
		&requestState{}, j, store, newTargetLocks())

	if resp.Found || resp.Error != "" {
		t.Fatalf("a target with no connector must answer Found=false with no error to anyone: %+v", resp)
	}
	if n := store.reads(); n != 0 {
		t.Errorf("the store was read %d time(s) for a target with no connector", n)
	}
}

// The whole path, accept to answer: handle is what the daemon runs for every
// connection, and it is where the verdict becomes the state a request is
// served under. A net.Pipe is a connection the peer package cannot ask the
// kernel about -- unsupported, the same verdict Windows gives a real socket --
// so it is let through the accept check and must not be given the secret.
func TestTheDaemonReleasesNoCredentialOverAConnectionItCannotVerify(t *testing.T) {
	j := freshJournal(t)
	store := &readCountingStore{fakeStore: newFakeStore()}
	if err := store.Set("github", unverifiedTestSecret); err != nil {
		t.Fatal(err)
	}
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handle(server, j, store, newTargetLocks(), newSessionRegistry(), newPendingRegistry(), time.Minute)
	}()
	t.Cleanup(func() {
		client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the daemon's handler did not return after the connection closed")
		}
	})
	client.SetDeadline(time.Now().Add(10 * time.Second))

	resp, err := SendRequest(client, Request{ID: "1", Kind: KindCredentialGet, Target: "github"})
	if err != nil {
		t.Fatalf("the connection was refused outright, which would refuse every relay on a platform that cannot verify: %v", err)
	}
	if !resp.Found || !strings.Contains(resp.Error, "could not verify who is asking") {
		t.Fatalf("an unverified connection asking for a credential was answered %+v", resp)
	}
	if len(resp.Env) != 0 || len(resp.Command) != 0 {
		t.Errorf("an unverified connection was given something to inject: env %v, command %v", resp.Env, resp.Command)
	}
	if n := store.reads(); n != 0 {
		t.Errorf("the secret was read from the store %d time(s) for a connection that was refused", n)
	}
}
