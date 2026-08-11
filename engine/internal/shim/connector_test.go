package shim

import (
	"encoding/json"
	"net"
	"sort"
	"testing"

	"github.com/BySergiMM/nim/engine/internal/daemon"
)

// fakeDaemon answers exactly one Request with resp and returns the client
// half of the pipe, closing both ends on test cleanup.
func fakeDaemon(t *testing.T, resp daemon.Response) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go func() {
		var req daemon.Request
		if err := json.NewDecoder(server).Decode(&req); err != nil {
			return
		}
		json.NewEncoder(server).Encode(resp)
	}()
	return client
}

func TestFetchConnectorOnNilConnIsFailOpen(t *testing.T) {
	inj, err := fetchConnector(nil, "github")
	if err != nil || inj.env != nil {
		t.Fatalf("got (%v, %v), want (nil, nil) -- no daemon connection must never block the spawn", inj.env, err)
	}
}

func TestFetchConnectorOnUnconfiguredTargetIsFailOpen(t *testing.T) {
	conn := fakeDaemon(t, daemon.Response{ID: "x", Found: false})
	inj, err := fetchConnector(conn, "github")
	if err != nil || inj.env != nil {
		t.Fatalf("got (%v, %v), want (nil, nil) for an unconfigured target", inj.env, err)
	}
}

func TestFetchConnectorReturnsKeyValuePairs(t *testing.T) {
	conn := fakeDaemon(t, daemon.Response{ID: "x", Found: true, Env: map[string]string{"GITHUB_TOKEN": "ghp_x"}, Command: []string{"server"}})
	inj, err := fetchConnector(conn, "github")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inj.env) != 1 || inj.env[0] != "GITHUB_TOKEN=ghp_x" {
		t.Fatalf("got %v, want [GITHUB_TOKEN=ghp_x]", inj.env)
	}
}

func TestFetchConnectorWithMultipleKeysReturnsAllOfThem(t *testing.T) {
	conn := fakeDaemon(t, daemon.Response{ID: "x", Found: true, Env: map[string]string{
		"GITHUB_TOKEN": "ghp_x", "GITHUB_ORG": "acme",
	}, Command: []string{"server"}})
	inj, err := fetchConnector(conn, "github")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(inj.env)
	want := []string{"GITHUB_ORG=acme", "GITHUB_TOKEN=ghp_x"}
	if len(inj.env) != 2 || inj.env[0] != want[0] || inj.env[1] != want[1] {
		t.Fatalf("got %v, want %v", inj.env, want)
	}
}

// The fail-closed guarantee, seen from the shim's side: a connector IS
// configured but its secret could not be retrieved, so Run must refuse to
// spawn rather than start the downstream without it.
func TestFetchConnectorOnBrokenSecretFailsClosed(t *testing.T) {
	conn := fakeDaemon(t, daemon.Response{ID: "x", Found: true, Error: "keychain locked"})
	inj, err := fetchConnector(conn, "github")
	if err == nil {
		t.Fatal("a configured connector with an unretrievable secret must return an error")
	}
	if inj.env != nil {
		t.Fatalf("no env should be returned on the fail-closed path, got %v", inj.env)
	}
}

// The non-negotiable requirement, exercised at the point it actually
// matters: the credential must reach the downstream only through cmd.Env,
// never cmd.Args, where it would be visible to any local user via ps.
func TestInjectedCredentialNeverAppearsInCmdArgs(t *testing.T) {
	env := []string{"GITHUB_TOKEN=ghp_extremely_secret_9f3a"}
	cmd := buildDownstreamCmd([]string{"echo", "hello"}, env)
	for _, arg := range cmd.Args {
		if arg == "ghp_extremely_secret_9f3a" || arg == env[0] {
			t.Fatalf("the credential appeared in argv: %v", cmd.Args)
		}
	}
	found := false
	for _, e := range cmd.Env {
		if e == env[0] {
			found = true
		}
	}
	if !found {
		t.Fatalf("the credential did not reach cmd.Env: %v", cmd.Env)
	}
}
