package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
)

// This is the actual defense: even a future log.Printf("%v", req) or "%+v"
// mistake must not print the secret, because Go's fmt package calls String
// on any operand that implements it, for every verb.
func TestRequestStringNeverIncludesTheSecret(t *testing.T) {
	req := Request{ID: "1", Kind: KindConnectorSet, Target: "github", EnvKey: "GITHUB_TOKEN", Secret: "ghp_extremely_secret_9f3a"}
	for _, format := range []string{"%v", "%+v", "%s", "%q"} {
		out := fmt.Sprintf(format, req)
		if strings.Contains(out, req.Secret) {
			t.Fatalf("format %q leaked the secret: %s", format, out)
		}
	}
}

func TestResponseStringNeverIncludesEnvValues(t *testing.T) {
	resp := Response{ID: "1", Found: true, Env: map[string]string{"GITHUB_TOKEN": "ghp_extremely_secret_9f3a"}}
	for _, format := range []string{"%v", "%+v", "%s", "%q"} {
		out := fmt.Sprintf(format, resp)
		if strings.Contains(out, "ghp_extremely_secret_9f3a") {
			t.Fatalf("format %q leaked an env value: %s", format, out)
		}
	}
}

// A request/response with no secret must not claim one is being withheld --
// String should say <none>, not <redacted>, so an empty connector.list
// response doesn't read as if it's hiding something.
func TestStringDistinguishesAbsentFromRedacted(t *testing.T) {
	empty := Request{ID: "1", Kind: KindConnectorList}
	if strings.Contains(empty.String(), "redacted") {
		t.Errorf("a request with no secret must not say <redacted>: %s", empty.String())
	}
	withSecret := Request{ID: "1", Kind: KindConnectorSet, Secret: "x"}
	if !strings.Contains(withSecret.String(), "redacted") {
		t.Errorf("a request carrying a secret must say <redacted>: %s", withSecret.String())
	}
}

func TestSendRequestRoundTrips(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	want := Response{ID: "42", Found: true, Env: map[string]string{"K": "v"}}
	go func() {
		var req Request
		if err := json.NewDecoder(server).Decode(&req); err != nil {
			return
		}
		json.NewEncoder(server).Encode(want)
	}()

	got, err := SendRequest(client, Request{ID: "42", Kind: KindCredentialGet, Target: "github"})
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	if got.ID != want.ID || !got.Found || got.Env["K"] != "v" {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
