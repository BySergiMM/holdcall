//go:build !windows

package daemon

import (
	"net"
	"testing"
	"time"

	"github.com/BySergiMM/holdcall/engine/internal/peer"
)

// The other half of peer_unverified_test.go: a peer the kernel confirmed is
// given the credential. Without this, the refusal would be satisfied by a daemon
// that never releases anything, and every relay with a connector would break on
// the platforms that are meant to work.
//
// It is a real unix socket and handle(), not handleCredentialGet with a state
// written by hand, so that what is checked is the verdict peer.Check really
// reaches on this platform becoming the state a request is served under. The
// test process is both ends: the same binary, run by the same user, which is
// exactly what a relay is to the daemon.
func TestACredentialIsReleasedToAPeerTheKernelConfirmed(t *testing.T) {
	j := freshJournal(t)
	store := newFakeStore()
	if err := store.Set("github", unverifiedTestSecret); err != nil {
		t.Fatal(err)
	}
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}

	sock := tempSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listening on %s: %v", sock, err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		handle(c, j, store, newTargetLocks(), newSessionRegistry(), newPendingRegistry(), time.Minute)
	}()

	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	defer func() {
		client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the daemon's handler did not return after the connection closed")
		}
	}()
	client.SetDeadline(time.Now().Add(10 * time.Second))

	// The premise, asserted rather than assumed: if the kernel cannot confirm
	// this process to itself, the rest of the test would be about something else.
	if v := peer.Check(client); !v.Verified() {
		t.Fatalf("the premise of this test is that the platform confirms a process to itself, and peer.Check says %+v", v)
	}

	resp, err := SendRequest(client, Request{ID: "1", Kind: KindCredentialGet, Target: "github"})
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	if resp.Error != "" || !resp.Found {
		t.Fatalf("a peer the kernel confirmed was refused: %+v", resp)
	}
	if resp.Env["GITHUB_TOKEN"] != unverifiedTestSecret {
		t.Errorf("the confirmed peer was not given the credential: env %v", resp.Env)
	}
	if len(resp.Command) != 1 || resp.Command[0] != "server" {
		t.Errorf("the confirmed peer was not given the registered command: %v", resp.Command)
	}
}
