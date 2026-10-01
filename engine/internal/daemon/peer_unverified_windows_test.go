//go:build windows

package daemon

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/BySergiMM/holdcall/engine/internal/peer"
)

// The windows end of peer_unverified_test.go: the same refusal, asked of a real
// daemon over a real socket on the one platform where every connection is an
// unverified one. On linux and macOS this file does not exist, and the pipe
// there stands in for what this one does for real.
//
// The premise is asserted rather than assumed. If Windows ever gains a way to
// ask who is on the other end of an AF_UNIX socket, this test fails and says
// so, which is the cue to give peer_windows.go its implementation, to drop the
// "Windows" section of docs/security.md that says nothing is verified, and to
// delete this file.
func TestOnWindowsARealDaemonReleasesNoCredentialOverARealSocket(t *testing.T) {
	cfg, dbPath := start(t)
	j := openJournal(t, dbPath)
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server"}, time.Now()); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}

	conn, err := net.Dial("unix", cfg.Daemon.Socket)
	if err != nil {
		t.Fatalf("dialling the daemon: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if v := peer.Check(conn); v.Supported || v.Verified() {
		t.Fatalf("this test is about a platform that cannot verify its peers, and peer.Check says %+v: "+
			"windows can now answer, so peer_windows.go and docs/security.md are out of date", v)
	}

	resp, err := SendRequest(conn, Request{ID: "1", Kind: KindCredentialGet, Target: "github"})
	if err != nil {
		t.Fatalf("the daemon refused the connection outright, which would refuse every relay on windows: %v", err)
	}
	if !resp.Found || !strings.Contains(resp.Error, "could not verify who is asking") {
		t.Fatalf("a connection windows cannot verify, asking for a credential, was answered %+v", resp)
	}
	if len(resp.Env) != 0 || len(resp.Command) != 0 {
		t.Errorf("it was given something to inject: env %v, command %v", resp.Env, resp.Command)
	}
}
