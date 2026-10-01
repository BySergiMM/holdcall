package shim

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/BySergiMM/holdcall/engine/internal/config"
	"github.com/BySergiMM/holdcall/engine/internal/daemon"
	"github.com/BySergiMM/holdcall/engine/internal/peer"
)

// What a relay does with a daemon's answer depends on what the kernel said
// about the daemon, and on linux and macOS the kernel always says something.
// The one case those platforms never meet -- a daemon nobody could verify,
// which on Windows is every daemon -- is built here by hand: peer.Verdict{} is
// exactly what peer_windows.go returns for every connection.

const unverifiedDaemonSecret = "FAKE-secret-for-the-unverified-daemon-tests-not-a-token"

func connectorAnswer() daemon.Response {
	return daemon.Response{
		Found:   true,
		Env:     map[string]string{"GITHUB_TOKEN": unverifiedDaemonSecret},
		Command: []string{"server", "--flag"},
	}
}

func TestAConnectorAnsweredByADaemonNobodyVerifiedIsNotTaken(t *testing.T) {
	unverified := peer.Verdict{}
	verified := peer.Verdict{Supported: true, Same: true, PID: 7}
	const socket = "/run/user/1000/holdcall-test.sock"

	inj, err := injectionFromAnswer(unverified, socket, "github", connectorAnswer())
	if err == nil {
		t.Fatal("a connector's command and credential were taken from a daemon the platform could not verify")
	}
	if len(inj.command) != 0 || len(inj.env) != 0 {
		t.Fatalf("something to run was handed back with the refusal: command %v, env %v", inj.command, inj.env)
	}
	for _, want := range []string{`connector "github"`, "cannot verify", socket, "neither a credential nor a command"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), unverifiedDaemonSecret) {
		t.Fatalf("the refusal repeats the secret: %v", err)
	}

	// An error from the daemon does not make it more trusted, nor does it
	// change the answer: the connector is refused either way.
	withError := connectorAnswer()
	withError.Error = "credential not released"
	if _, err := injectionFromAnswer(unverified, socket, "github", withError); err == nil {
		t.Error("a daemon nobody verified was believed because its answer was an error")
	}

	// The control that gives the refusal its meaning: the same answer from a
	// daemon the platform confirmed is the injection it asks for.
	inj, err = injectionFromAnswer(verified, socket, "github", connectorAnswer())
	if err != nil {
		t.Fatalf("a verified daemon's answer was refused: %v", err)
	}
	if !slices.Equal(inj.command, []string{"server", "--flag"}) {
		t.Errorf("command = %v", inj.command)
	}
	if !slices.Equal(inj.env, []string{"GITHUB_TOKEN=" + unverifiedDaemonSecret}) {
		t.Errorf("env = %v", inj.env)
	}
}

// What a daemon nobody verified is not believed about is a connector, not the
// whole conversation: "no connector here" asks the relay to run nothing it was
// not already told to run, and refusing it would stop every relay on a platform
// that cannot verify anything.
func TestAnAnswerWithNoConnectorIsTakenFromADaemonNobodyVerified(t *testing.T) {
	inj, err := injectionFromAnswer(peer.Verdict{}, "/s/d.sock", "github", daemon.Response{Found: false})
	if err != nil {
		t.Fatalf("a relay for a server with no connector was refused because the daemon could not be verified: %v", err)
	}
	if len(inj.command) != 0 || len(inj.env) != 0 {
		t.Errorf("an answer with no connector produced an injection: %+v", inj)
	}
}

// A secret is not sent to a daemon nobody confirmed. `holdcall connector set` is
// the one request that carries one out.
func TestASecretIsNotSentToADaemonNobodyVerified(t *testing.T) {
	cfg := config.Config{Daemon: config.Daemon{Socket: "/s/d.sock"}}

	for name, v := range map[string]peer.Verdict{
		"the platform cannot ask":                      {},
		"asked, and it is another binary":              {Supported: true, PID: 7},
		"asked, and it is this binary as another user": {Supported: true, PID: 7, WrongUser: true, PeerUID: 1001, SelfUID: 1000},
	} {
		err := secretRefusal(cfg, v)
		if err == nil {
			t.Errorf("%s: a secret would have been sent", name)
			continue
		}
		if !strings.Contains(err.Error(), "does not send it a secret") || !strings.Contains(err.Error(), "/s/d.sock") {
			t.Errorf("%s: the refusal does not say what it did or where: %v", name, err)
		}
	}
	if err := secretRefusal(cfg, peer.Verdict{Supported: true, Same: true, PID: 7}); err != nil {
		t.Errorf("a daemon the platform confirmed was refused a secret: %v", err)
	}
}

// DialDaemonForSecret against a real listener: it hands the connection back
// where the platform can confirm the daemon, and refuses it, having sent
// nothing, where it cannot. The stub daemon is this very process, so on linux
// and macOS it is this binary run by this user, which the kernel confirms.
func TestDialDaemonForSecretFollowsWhatThePlatformCanVerify(t *testing.T) {
	cfg := stubDaemon(t, daemon.Response{})

	conn, err := DialDaemonForSecret(cfg)
	if runtime.GOOS == "windows" {
		if err == nil {
			conn.Close()
			t.Fatal("a connection to a daemon windows cannot verify was handed back for a secret")
		}
		if conn != nil {
			t.Error("a connection came back alongside the refusal")
		}
		if !strings.Contains(err.Error(), "cannot verify") {
			t.Errorf("the refusal does not say why: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("a daemon the kernel confirmed was refused: %v", err)
	}
	conn.Close()
}
