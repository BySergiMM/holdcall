package mcp

import (
	"encoding/json"
	"testing"
)

// A server/discover request names the version it proposes in its _meta, under
// a namespaced key. That key, and nothing looser, is what the relay reads.
func TestADiscoverRequestNamesTheVersionItProposes(t *testing.T) {
	for _, tc := range []struct {
		name, frame, want string
	}{
		{"the namespaced key",
			`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
			"2026-07-28"},
		{"an unnamespaced key is not it",
			`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"protocolVersion":"2026-07-28"}}}`,
			""},
		{"no _meta at all",
			`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`,
			""},
		{"no params at all",
			`{"jsonrpc":"2.0","id":1,"method":"server/discover"}`,
			""},
		{"not a string",
			`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":20260728}}}`,
			""},
	} {
		env, ok := Parse([]byte(tc.frame))
		if !ok || !env.IsDiscover() {
			t.Fatalf("%s: not read as a discover request", tc.name)
		}
		if got := env.ProposedProtocolVersion(); got != tc.want {
			t.Errorf("%s: proposed = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The answer to server/discover lists what the server supports; the client
// picks from that list. The relay settles on the proposal when it is listed,
// and otherwise on the newest listed version the client could still choose.
func TestTheNegotiatedVersionIsReadFromTheDiscoverAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, response, proposed, want string
	}{
		{"the proposal is listed",
			`{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{},"cacheScope":"private","ttlMs":0}}`,
			"2026-07-28", "2026-07-28"},
		{"the proposal is not listed, an older one is",
			`{"jsonrpc":"2.0","id":1,"result":{"supportedVersions":["2026-07-28","2027-01-01"]}}`,
			"2027-03-01", "2027-01-01"},
		{"only newer versions are listed, which the client cannot know",
			`{"jsonrpc":"2.0","id":1,"result":{"supportedVersions":["2027-01-01"]}}`,
			"2026-07-28", ""},
		{"a JSON-RPC error settles nothing",
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`,
			"2026-07-28", ""},
		{"a result with no list settles nothing",
			`{"jsonrpc":"2.0","id":1,"result":{}}`,
			"2026-07-28", ""},
	} {
		env, ok := Parse([]byte(tc.response))
		if !ok || !env.IsResponse() {
			t.Fatalf("%s: not read as a response", tc.name)
		}
		if got := env.NegotiatedVersion(tc.proposed); got != tc.want {
			t.Errorf("%s: negotiated = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// resultType became mandatory in the 2026-07-28 revision. Everything before it,
// and a session that never negotiated, gets the older shape.
func TestResultsCarryTypeFromTheRevisionThatRequiresIt(t *testing.T) {
	for version, want := range map[string]bool{
		"":           false,
		"2024-11-05": false,
		"2025-06-18": false,
		"2025-11-25": false,
		"2026-07-28": true,
		"2027-01-01": true,
	} {
		if got := ResultsCarryType(version); got != want {
			t.Errorf("ResultsCarryType(%q) = %v, want %v", version, got, want)
		}
	}
}

// The id of a discover request is what pairs it with its answer, and it is
// copied, never understood -- the same rule every other id follows.
func TestADiscoverRequestKeepsItsIDVerbatim(t *testing.T) {
	env, _ := Parse([]byte(`{"jsonrpc":"2.0","id":9007199254740993,"method":"server/discover","params":{}}`))
	if string(json.RawMessage(env.Key())) != "9007199254740993" {
		t.Errorf("key = %s", env.Key())
	}
}
