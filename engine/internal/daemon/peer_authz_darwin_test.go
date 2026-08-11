//go:build darwin

package daemon

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This is the actual attack from the M3 adversarial audit, reproduced
// end-to-end through the real handle() dispatch (not just verifyPeerIsSelf
// in isolation): a process that is not this binary connects directly to the
// daemon socket and asks for a configured target's credential. Before this
// fix, this succeeded and returned the real secret.
func TestHandleDeniesCredentialGetFromANonNimProcess(t *testing.T) {
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not available")
	}
	j := openJournal(t)
	if err := j.SetConnector("github", "GITHUB_TOKEN", time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	store := newFakeStore()
	store.Set("github", "ghp_REAL_SECRET_must_not_leak")

	path := tempSocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	done := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		handle(conn, j, store, newTargetLocks())
		close(done)
	}()

	// The attacker: a real, separate, non-nim process (nc) connecting
	// directly to the socket and speaking the wire protocol by hand.
	attacker := exec.Command("nc", "-U", path)
	stdin, err := attacker.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := attacker.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := attacker.Start(); err != nil {
		t.Fatalf("start attacker: %v", err)
	}
	t.Cleanup(func() { attacker.Process.Kill() })

	req := Request{ID: "attack", Kind: KindCredentialGet, Target: "github"}
	raw, _ := json.Marshal(req)
	if _, err := stdin.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write attack request: %v", err)
	}

	respCh := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 65536)
		n, _ := stdout.Read(buf)
		respCh <- buf[:n]
	}()

	select {
	case respBytes := <-respCh:
		var resp Response
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			t.Fatalf("unmarshal attacker's response: %v (raw: %q)", err, respBytes)
		}
		if resp.Error != "unauthorized" {
			t.Fatalf("SECURITY FAILURE: non-nim process got response %+v, want Error=unauthorized", resp)
		}
		for _, v := range resp.Env {
			if v == "ghp_REAL_SECRET_must_not_leak" {
				t.Fatal("SECURITY FAILURE: the real secret leaked to a non-nim process")
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no response received from the daemon within 3s")
	}

	stdin.Close()
}

// The M3 final audit's live manual testing (nc, Python raw sockets) found
// connector.set/list/remove all correctly denied to a non-nim process, the
// same as credential.get above -- but only the latter had a permanent
// regression test. This closes that gap: a real, separate, non-nim process
// attempting every connector management operation directly on the socket
// must be denied, and a connector.set attack in particular must never reach
// the store (proven here via the fake store's own call log, not just the
// response).
func TestHandleDeniesConnectorOperationsFromANonNimProcess(t *testing.T) {
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not available")
	}
	j := openJournal(t)
	if err := j.SetConnector("github", "GITHUB_TOKEN", time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	store := newFakeStore()
	store.Set("github", "ghp_REAL_SECRET_must_not_leak")

	path := tempSocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	attacks := []Request{
		{ID: "list", Kind: KindConnectorList},
		{ID: "set", Kind: KindConnectorSet, Target: "github", EnvKey: "PWNED", Secret: "attacker_secret"},
		{ID: "remove", Kind: KindConnectorRemove, Target: "github"},
	}

	for _, req := range attacks {
		t.Run(req.Kind, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				handle(conn, j, store, newTargetLocks())
				close(done)
			}()

			attacker := exec.Command("nc", "-U", path)
			stdin, err := attacker.StdinPipe()
			if err != nil {
				t.Fatalf("StdinPipe: %v", err)
			}
			stdout, err := attacker.StdoutPipe()
			if err != nil {
				t.Fatalf("StdoutPipe: %v", err)
			}
			if err := attacker.Start(); err != nil {
				t.Fatalf("start attacker: %v", err)
			}
			t.Cleanup(func() { attacker.Process.Kill() })

			raw, _ := json.Marshal(req)
			if _, err := stdin.Write(append(raw, '\n')); err != nil {
				t.Fatalf("write attack request: %v", err)
			}

			respCh := make(chan []byte, 1)
			go func() {
				buf := make([]byte, 65536)
				n, _ := stdout.Read(buf)
				respCh <- buf[:n]
			}()

			select {
			case respBytes := <-respCh:
				var resp Response
				if err := json.Unmarshal(respBytes, &resp); err != nil {
					t.Fatalf("unmarshal attacker's response: %v (raw: %q)", err, respBytes)
				}
				if resp.Error != "unauthorized" {
					t.Fatalf("SECURITY FAILURE: non-nim process's %s got response %+v, want Error=unauthorized", req.Kind, resp)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no response received from the daemon within 3s")
			}
			stdin.Close()
			<-done
		})
	}

	// The set attack above (Target: "github", EnvKey: "PWNED") must never
	// have reached the store: github's original secret must be unchanged.
	got, err := store.Get("github")
	if err != nil || got != "ghp_REAL_SECRET_must_not_leak" {
		t.Fatalf("github's secret was modified by a denied connector.set: got (%q, %v)", got, err)
	}
}

// The mirror image: a genuinely separate process running this exact test
// binary (standing in for the real nim binary, which every nim serve and
// nim connector invocation is a copy of) must still succeed. Layer 1 is not
// supposed to lock out legitimate access, only distinguish it from
// everything else.
func TestHandleAllowsCredentialGetFromTheSameBinary(t *testing.T) {
	j := openJournal(t)
	if err := j.SetConnector("github", "GITHUB_TOKEN", time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	store := newFakeStore()
	store.Set("github", "ghp_legit_secret")

	path := tempSocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		handle(conn, j, store, newTargetLocks())
	}()

	selfPath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	client := exec.Command(selfPath, "-test.run=TestHelperProcessSendCredentialGet")
	client.Env = append(os.Environ(), "NIM_PEER_TEST_HELPER_SOCKET="+path)
	out, err := client.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "RESPONSE_ENV:ghp_legit_secret") {
		t.Fatalf("legitimate same-binary access was denied or did not receive the secret; helper output: %s", out)
	}
}

// TestHandleTargetBindingSurvivesEverySingleConnectionTrick is the live,
// end-to-end version of the target-binding unit tests in request_test.go:
// it runs against the real daemon (real listener, real handle() goroutine,
// real journal, real fake store) through a genuinely separate OS process
// that passes Layer 1 (peer identity), the only way to reach Layer 2's
// pivot defenses at all -- a raw-socket attacker never gets past Layer 1,
// as TestHandleDeniesCredentialGetFromANonNimProcess above already proves.
// It exercises, on one real connection: legitimate bind, pivot attempt,
// repeat-of-bound-target, kind-mixing, and a duplicate-JSON-key request --
// then opens a second, fresh connection to confirm binding does not leak
// across connections.
func TestHandleTargetBindingSurvivesEverySingleConnectionTrick(t *testing.T) {
	j := openJournal(t)
	for _, target := range []string{"github", "slack"} {
		if err := j.SetConnector(target, strings.ToUpper(target)+"_TOKEN", time.Now()); err != nil {
			t.Fatalf("SetConnector(%s): %v", target, err)
		}
	}
	store := newFakeStore()
	store.Set("github", "ghp_REAL_must_not_leak_to_slack_session")
	store.Set("slack", "xoxb_REAL_slack_secret")

	path := tempSocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn, j, store, newTargetLocks())
		}
	}()

	selfPath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	client := exec.Command(selfPath, "-test.run=TestHelperProcessTargetBindingAttacks")
	client.Env = append(os.Environ(), "NIM_PEER_TEST_HELPER_SOCKET="+path)
	out, err := client.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\noutput: %s", err, out)
	}
	output := string(out)
	t.Logf("helper output:\n%s", output)

	// github's secret legitimately appears once the fresh, unbound second
	// connection asks for it (RECONNECT_FRESH_GITHUB_OK) -- that line proves
	// binding is per-connection, not a blanket ban on ever seeing this
	// value. What must never happen is the FIRST connection, bound to
	// slack, getting it back on its own pivot attempt.
	beforeReconnect, _, _ := strings.Cut(output, "RECONNECT_FRESH_GITHUB_OK")
	if strings.Contains(beforeReconnect, "ghp_REAL_must_not_leak_to_slack_session") {
		t.Fatal("SECURITY FAILURE: github's secret leaked to a connection bound to slack")
	}
	for _, want := range []string{
		"BOUND_SLACK_OK:xoxb_REAL_slack_secret",
		"PIVOT_TO_GITHUB:unauthorized",
		"REPEAT_SLACK_OK:xoxb_REAL_slack_secret",
		"MIX_CONNECTOR_LIST:unauthorized",
		"DUP_KEY_LAST_WINS_SLACK:xoxb_REAL_slack_secret",
		"RECONNECT_FRESH_GITHUB_OK:ghp_REAL_must_not_leak_to_slack_session",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected helper output to contain %q, got:\n%s", want, output)
		}
	}
}

// TestHelperProcessTargetBindingAttacks is re-executed as a subprocess by
// TestHandleTargetBindingSurvivesEverySingleConnectionTrick. It is not a
// real test: it is this same nim binary, genuinely running as a separate
// process, deliberately trying every connection-level trick to read
// github's secret from a session that bound itself to slack first.
func TestHelperProcessTargetBindingAttacks(t *testing.T) {
	path := os.Getenv("NIM_PEER_TEST_HELPER_SOCKET")
	if path == "" {
		t.Skip("not invoked as a helper process")
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	bound, err := SendRequest(conn, Request{ID: "bind", Kind: KindCredentialGet, Target: "slack"})
	if err != nil {
		t.Fatalf("bind request: %v", err)
	}
	print("BOUND_SLACK_OK:" + bound.Env["SLACK_TOKEN"] + "\n")

	pivot, err := SendRequest(conn, Request{ID: "pivot", Kind: KindCredentialGet, Target: "github"})
	if err != nil {
		t.Fatalf("pivot request: %v", err)
	}
	print("PIVOT_TO_GITHUB:" + pivot.Error + "\n")

	repeat, err := SendRequest(conn, Request{ID: "repeat", Kind: KindCredentialGet, Target: "slack"})
	if err != nil {
		t.Fatalf("repeat request: %v", err)
	}
	print("REPEAT_SLACK_OK:" + repeat.Env["SLACK_TOKEN"] + "\n")

	mix, err := SendRequest(conn, Request{ID: "mix", Kind: KindConnectorList})
	if err != nil {
		t.Fatalf("mix request: %v", err)
	}
	print("MIX_CONNECTOR_LIST:" + mix.Error + "\n")

	// A hand-crafted raw line with the target key repeated: Go's
	// encoding/json keeps the LAST occurrence for a duplicate object key, so
	// this must be judged as target="slack" (the already-bound target,
	// allowed), never target="github" (the first-listed value). Sent as a
	// raw line rather than through SendRequest/Request, since Request has
	// only one Target field and cannot represent a duplicate key.
	raw := []byte(`{"id":"dupkey","kind":"credential.get","target":"github","target":"slack"}` + "\n")
	if _, err := conn.Write(raw); err != nil {
		t.Fatalf("write raw dup-key request: %v", err)
	}
	dec := json.NewDecoder(conn)
	var dupResp Response
	if err := dec.Decode(&dupResp); err != nil {
		t.Fatalf("decode dup-key response: %v", err)
	}
	print("DUP_KEY_LAST_WINS_SLACK:" + dupResp.Env["SLACK_TOKEN"] + dupResp.Error + "\n")

	conn.Close()

	// A fresh connection must start with no binding at all: github must be
	// reachable here, proving binding is per-connection, not smuggled across
	// reconnects by anything (e.g. a shared client-side cache) outside the
	// daemon's own connState.
	conn2, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	defer conn2.Close()
	fresh, err := SendRequest(conn2, Request{ID: "fresh", Kind: KindCredentialGet, Target: "github"})
	if err != nil {
		t.Fatalf("fresh request: %v", err)
	}
	print("RECONNECT_FRESH_GITHUB_OK:" + fresh.Env["GITHUB_TOKEN"] + "\n")
}

// TestHelperProcessSendCredentialGet is re-executed as a subprocess by
// TestHandleAllowsCredentialGetFromTheSameBinary, using this test binary
// itself as the "genuinely separate nim process" stand-in -- every real
// nim serve and nim connector invocation is a copy of the same binary too.
// It connects to the socket, sends one credential.get, and prints the
// response so the parent can inspect it; it is not a real test.
func TestHelperProcessSendCredentialGet(t *testing.T) {
	path := os.Getenv("NIM_PEER_TEST_HELPER_SOCKET")
	if path == "" {
		t.Skip("not invoked as a helper process")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	resp, err := SendRequest(conn, Request{ID: "helper", Kind: KindCredentialGet, Target: "github"})
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	print("RESPONSE_ENV:" + resp.Env["GITHUB_TOKEN"])
}
