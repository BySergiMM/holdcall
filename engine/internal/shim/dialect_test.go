package shim

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BySergiMM/holdcall/engine/internal/mcp"
)

// resultTypeOf reads result.resultType from a line Holdcall wrote, "" when absent.
func resultTypeOf(t *testing.T, line []byte) string {
	t.Helper()
	var got struct {
		Result struct {
			ResultType string `json:"resultType"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("what Holdcall sent the client is not valid JSON: %v\n%s", err, line)
	}
	return got.Result.ResultType
}

func lastLine(t *testing.T, out string) []byte {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("Holdcall wrote nothing to the client")
	}
	return []byte(lines[len(lines)-1])
}

const (
	discoverRequest = `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}` + "\n"
	discoverAnswer  = `{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"cacheScope":"private","ttlMs":0}}` + "\n"
	discoverRefused = `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}` + "\n"
	initRequest     = `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}` + "\n"
	initAnswer      = `{"jsonrpc":"2.0","id":2,"result":{"protocolVersion":"2025-11-25","capabilities":{}}}` + "\n"
	deniedCall      = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"rm"}}` + "\n"
)

// A session negotiated through server/discover is on a revision under which
// every result names its resultType. A refusal written without it is one such
// a client cannot parse -- the whole point of answering in MCP's tool-error
// shape is lost if the client never sees the text.
func TestARefusalUnderTheDiscoverHandshakeCarriesResultType(t *testing.T) {
	d := newDaemon(t)
	d.answer = deny("by rule")
	g := newRig(t, d)

	g.relay(discoverRequest)
	g.respond(discoverAnswer)
	g.relay(deniedCall)

	if got := resultTypeOf(t, lastLine(t, g.client.String())); got != mcp.ResultTypeComplete {
		t.Errorf("resultType = %q, want %q: a client on 2026-07-28 cannot read this refusal\n%s",
			got, mcp.ResultTypeComplete, g.client.String())
	}
	if strings.Contains(g.connector.String(), "tools/call") {
		t.Error("the refused call reached the connector")
	}
}

// The older handshake gets the older shape, unchanged: nothing says how a
// client on it treats a field it does not know.
func TestARefusalUnderTheInitializeHandshakeDoesNotCarryResultType(t *testing.T) {
	d := newDaemon(t)
	d.answer = deny("by rule")
	g := newRig(t, d)

	g.relay(initRequest)
	g.respond(initAnswer)
	g.relay(deniedCall)

	if got := resultTypeOf(t, lastLine(t, g.client.String())); got != "" {
		t.Errorf("a refusal for a 2025-11-25 client carries resultType %q", got)
	}
}

// A server that does not speak server/discover answers it with an error, and
// the client falls back to initialize. The version the session ends up on is
// the one initialize settled, not the one discover proposed.
func TestAFailedDiscoverFallsBackToWhatInitializeSettles(t *testing.T) {
	d := newDaemon(t)
	d.answer = deny("by rule")
	g := newRig(t, d)

	g.relay(discoverRequest)
	g.respond(discoverRefused)
	g.relay(initRequest)
	g.respond(initAnswer)
	g.relay(deniedCall)

	if got := g.shim.negotiated(); got != "2025-11-25" {
		t.Errorf("negotiated = %q, want what initialize settled", got)
	}
	if got := resultTypeOf(t, lastLine(t, g.client.String())); got != "" {
		t.Errorf("a refusal after the fallback carries resultType %q", got)
	}
}

// Before either handshake has been answered nothing has been negotiated, and a
// call refused then -- a client that skipped the handshake -- gets the shape
// every client can read.
func TestARefusalBeforeAnyHandshakeUsesTheOlderShape(t *testing.T) {
	d := newDaemon(t)
	d.answer = deny("by rule")
	g := newRig(t, d)

	g.relay(deniedCall)

	if got := resultTypeOf(t, lastLine(t, g.client.String())); got != "" {
		t.Errorf("resultType = %q before any handshake", got)
	}
}

// The handshake frames themselves are relayed untouched, in both directions:
// watching them is not the same as answering them.
func TestTheDiscoverHandshakeIsRelayedByteForByte(t *testing.T) {
	g := newRig(t, newDaemon(t))

	g.relay(discoverRequest)
	g.respond(discoverAnswer)

	if g.connector.String() != discoverRequest {
		t.Errorf("the discover request was altered: %q", g.connector.String())
	}
	if g.client.String() != discoverAnswer {
		t.Errorf("the discover answer was altered: %q", g.client.String())
	}
	if n := len(g.daemon.calls()); n != 0 {
		t.Errorf("the daemon was asked about %d handshake messages", n)
	}
}
