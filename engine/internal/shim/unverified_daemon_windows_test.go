//go:build windows

package shim

import (
	"strings"
	"testing"

	"github.com/BySergiMM/holdcall/engine/internal/daemon"
)

// The windows end of unverified_daemon_test.go, through the real relay: a
// daemon that answers credential.get with a connector is not believed here,
// because nothing on this platform can say it is Holdcall. TestTheDaemonsCommandIsSpawnedNotTheCallersEndToEnd
// cannot run on windows -- its fixture is a unix shell, and its premise, that
// the daemon's answer is trusted, is false here by design -- so this is the
// relay's behaviour on the platform it cannot run on.
func TestOnWindowsARelayDoesNotSpawnWhatAnUnverifiedDaemonNamed(t *testing.T) {
	cfg := stubDaemon(t, daemon.Response{
		Found:   true,
		Env:     map[string]string{"GITHUB_TOKEN": unverifiedDaemonSecret},
		Command: []string{"cmd", "/c", "echo REGISTERED-SERVER-RAN"},
	})

	stdout, err := runShim(t, Options{
		Connector: "github",
		Config:    cfg,
		Command:   []string{"cmd", "/c", "echo CALLERS-SERVER-RAN"},
	})
	if err == nil {
		t.Fatal("the relay started a downstream on the word of a daemon it cannot verify")
	}
	if !strings.Contains(err.Error(), "cannot verify") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	for _, leaked := range []string{"REGISTERED-SERVER-RAN", "CALLERS-SERVER-RAN", unverifiedDaemonSecret} {
		if strings.Contains(stdout, leaked) {
			t.Errorf("%q reached the output: a downstream ran or the secret leaked: %q", leaked, stdout)
		}
	}
}
