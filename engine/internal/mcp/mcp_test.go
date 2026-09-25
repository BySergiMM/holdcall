package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// The relay's whole promise is that a message comes out exactly as it went in.
func TestReadRawIsByteExact(t *testing.T) {
	// Deliberately awkward: odd key order, wide unicode, escapes, sloppy spacing.
	messages := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`,
		`{  "method" : "notifications/initialized" ,  "jsonrpc":"2.0" }`,
		`{"result":{"text":"café ñandú 日本語 🚀 \" \\ \n"},"id":"abc","jsonrpc":"2.0"}`,
		`{"method":"unknown/future-thing","params":{"whatever":[1,2,3]}}`,
		`not json at all`,
	}
	input := strings.Join(messages, "\n") + "\n"

	r := NewReader(strings.NewReader(input))
	var out bytes.Buffer
	for {
		raw, err := r.ReadRaw()
		if err != nil {
			break
		}
		out.Write(raw)
	}
	if out.String() != input {
		t.Fatalf("relay altered the stream\n got: %q\nwant: %q", out.String(), input)
	}
}

// bufio.Scanner would fail here; tool results routinely exceed its limit.
func TestReadRawHandlesHugeMessages(t *testing.T) {
	payload := strings.Repeat("x", 4<<20) // 4 MiB
	line := `{"id":1,"result":{"text":"` + payload + `"}}` + "\n"

	raw, err := NewReader(strings.NewReader(line)).ReadRaw()
	if err != nil {
		t.Fatalf("reading a 4 MiB message: %v", err)
	}
	if string(raw) != line {
		t.Fatalf("message truncated: got %d bytes, want %d", len(raw), len(line))
	}
}

func TestReadRawKeepsFinalMessageWithoutNewline(t *testing.T) {
	line := `{"id":1}`
	raw, err := NewReader(strings.NewReader(line)).ReadRaw()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(raw) != line {
		t.Fatalf("got %q, want %q", raw, line)
	}
}

func TestParseRecognisesToolCalls(t *testing.T) {
	env, ok := Parse([]byte(`{"id":7,"method":"tools/call","params":{"name":"send_email","arguments":{"to":"a@b.c"}}}`))
	if !ok {
		t.Fatal("did not parse")
	}
	if !env.IsToolCall() {
		t.Error("tools/call not recognised")
	}
	if env.IsResponse() {
		t.Error("a request must not look like a response")
	}
	call, err := env.Call()
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if call.Name != "send_email" {
		t.Errorf("tool name = %q", call.Name)
	}
	if env.Key() != "7" {
		t.Errorf("key = %q", env.Key())
	}
}

func TestParseTolueratesAnythingElse(t *testing.T) {
	// Holdcall only has to understand tools/call. Everything else must survive.
	//
	// A JSON-RPC batch used to be listed here as another harmless shape. It is
	// not harmless: see TestBatchIsAnAnomalyRatherThanNothing.
	for _, raw := range []string{`garbage`, `null`, `{"method":"future/method"}`} {
		env, ok := Parse([]byte(raw))
		if ok && env.IsToolCall() {
			t.Errorf("%q was mistaken for a tools/call", raw)
		}
	}
}

// A batch is an array, so Envelope parsing never sees the tools/call inside it
// and the message is relayed with no record that it happened.
//
// Nothing is blocked here -- that needs the strict reader that comes with
// enforcement. What must not happen is the earlier behaviour: a tool call
// reaching a server having left no trace at all.
func TestBatchIsAnAnomalyRatherThanNothing(t *testing.T) {
	raw := []byte(`[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_repository"}}]`)

	// The old path is blind to it, which is the reason the classifier exists.
	if env, ok := Parse(raw); ok && env.IsToolCall() {
		t.Fatal("Parse suddenly understands batches; this test needs rewriting")
	}

	env, anomaly := Classify(raw)
	if anomaly != AnomalyBatch {
		t.Fatalf("anomaly = %q, want %q: a tools/call inside a batch went unnoticed", anomaly, AnomalyBatch)
	}
	if env.IsToolCall() {
		t.Error("the envelope should be empty: the batch was not unwrapped")
	}

	// An empty array is still a batch, and still not a shape Holdcall can account for.
	if _, a := Classify([]byte(`[]`)); a != AnomalyBatch {
		t.Errorf("empty batch classified as %q", a)
	}
}

func TestMalformedJSONIsAnAnomaly(t *testing.T) {
	for _, raw := range []string{`garbage`, `{"unterminated":`, `{"a":1}}`} {
		if _, a := Classify([]byte(raw)); a != AnomalyMalformedJSON {
			t.Errorf("Classify(%q) = %q, want %q", raw, a, AnomalyMalformedJSON)
		}
	}
}

// The transport is one message per line. Two values in one frame means Holdcall and
// the server downstream may not agree on how many messages arrived.
func TestTwoValuesInOneFrameIsAnAnomaly(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"x"}}`)
	if _, a := Classify(raw); a != AnomalyFraming {
		t.Fatalf("anomaly = %q, want %q", a, AnomalyFraming)
	}
}

func TestClassifyLeavesOrdinaryMessagesAlone(t *testing.T) {
	cases := []struct {
		raw      string
		toolCall bool
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`, true},
		{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, false},
		{`{"jsonrpc":"2.0","id":2,"result":{}}`, false},
		{`{"method":"unknown/future-thing"}`, false},
		{`null`, false},
		{`  `, false},
	}
	for _, c := range cases {
		env, anomaly := Classify([]byte(c.raw))
		if anomaly != AnomalyNone {
			t.Errorf("Classify(%q) reported %q; ordinary traffic must not be flagged", c.raw, anomaly)
		}
		if env.IsToolCall() != c.toolCall {
			t.Errorf("Classify(%q) tool call = %v, want %v", c.raw, env.IsToolCall(), c.toolCall)
		}
	}
}

// Which protocol version was agreed is only knowable from the response, and it
// is what decides whether the server accepts batches at all.
func TestProtocolVersionComesFromTheInitializeResponse(t *testing.T) {
	request, _ := Parse([]byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`))
	if !request.IsInitialize() {
		t.Fatal("initialize request not recognised")
	}
	if request.ProtocolVersion() != "" {
		t.Error("the request cannot know the negotiated version")
	}

	response, _ := Parse([]byte(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-06-18"}}`))
	if got := response.ProtocolVersion(); got != "2025-06-18" {
		t.Errorf("protocol version = %q, want 2025-06-18", got)
	}

	other, _ := Parse([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
	if got := other.ProtocolVersion(); got != "" {
		t.Errorf("a non-initialize response reported version %q", got)
	}
}

func TestResponsesAreDistinguishedFromRequests(t *testing.T) {
	response, _ := Parse([]byte(`{"jsonrpc":"2.0","id":3,"result":{}}`))
	if !response.IsResponse() {
		t.Error("result message should be a response")
	}
	failure, _ := Parse([]byte(`{"jsonrpc":"2.0","id":3,"error":{"code":-32000}}`))
	if !failure.IsResponse() || len(failure.Error) == 0 {
		t.Error("error message should be a response carrying an error")
	}
	notification, _ := Parse([]byte(`{"jsonrpc":"2.0","method":"notifications/x"}`))
	if notification.IsResponse() {
		t.Error("a notification has no id and is not a response")
	}
}

// A tool that raises comes back as a *successful* JSON-RPC response carrying
// result.isError. Reading only the JSON-RPC error field records every failed
// tool call as a success, which is worse than recording nothing.
func TestFailedToolCallsAreRecognised(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"protocol error", `{"id":1,"error":{"code":-32601,"message":"no such method"}}`, true},
		{"tool raised", `{"id":1,"result":{"isError":true,"content":[{"type":"text","text":"boom"}]}}`, true},
		{"tool succeeded", `{"id":1,"result":{"isError":false,"content":[{"type":"text","text":"ok"}]}}`, false},
		{"no isError field", `{"id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`, false},
		{"empty result", `{"id":1,"result":{}}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, ok := Parse([]byte(c.raw))
			if !ok {
				t.Fatal("did not parse")
			}
			if got := env.Failed(); got != c.want {
				t.Errorf("Failed() = %v, want %v", got, c.want)
			}
		})
	}
}

// Arguments never leave the machine; only this digest does.
func TestArgumentsDigestIsStableAndDoesNotLeak(t *testing.T) {
	digest := func(raw string) string {
		env, _ := Parse([]byte(raw))
		call, err := env.Call()
		if err != nil {
			t.Fatalf("Call(%s): %v", raw, err)
		}
		return call.Digest
	}
	a := digest(`{"method":"tools/call","params":{"name":"t","arguments":{"secret":"hunter2"}}}`)
	b := digest(`{"method":"tools/call","params":{"name":"t","arguments":{"secret":"hunter2"}}}`)
	c := digest(`{"method":"tools/call","params":{"name":"t","arguments":{"secret":"other"}}}`)

	if a != b {
		t.Error("identical arguments must digest identically")
	}
	if a == c {
		t.Error("different arguments must digest differently")
	}
	if strings.Contains(a, "hunter2") {
		t.Error("the digest leaked the argument")
	}
	if len(a) != 64 {
		t.Errorf("expected a sha256 hex digest, got %d chars", len(a))
	}

	// Stated in docs/journal-format.md, so it is held to here: no arguments key
	// digests to the hash of no bytes, and a null one to the hash of `null`.
	if got := digest(`{"method":"tools/call","params":{"name":"t"}}`); got != sha256Hex("") {
		t.Errorf("absent arguments digest to %s, want the digest of nothing", got)
	}
	if got := digest(`{"method":"tools/call","params":{"name":"t","arguments":null}}`); got != sha256Hex("null") {
		t.Errorf("null arguments digest to %s, want the digest of `null`", got)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---------- objects that do not mean one thing ----------

// The bypass this closes, reproduced against the relay before the fix: Go's
// typed decoder matches keys case-insensitively, lets the last of several
// matches win, and carries on past a member of the wrong type, while Python
// and JavaScript take the last exact key. Every frame below was a tools/call
// to a server and something else to Holdcall.
func TestAnObjectNamingAKeyTwiceIsRefusedNotGuessed(t *testing.T) {
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":1,"method":5,"method":"tools/call","params":{"name":"rm"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","method":"tools/call","params":{"name":"rm"}}`,
		`{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/call","params":{"name":"rm"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a"},"params":{"name":"rm"}}`,
	} {
		env, anomaly := Classify([]byte(raw))
		if anomaly != AnomalyDuplicateKey {
			t.Errorf("Classify(%s) = %q, want %q", raw, anomaly, AnomalyDuplicateKey)
		}
		if env.IsToolCall() {
			t.Errorf("Classify(%s) produced an envelope from an object it should not have read", raw)
		}
		if _, ok := Parse([]byte(raw)); ok {
			t.Errorf("Parse(%s) accepted an object that names a key twice", raw)
		}
	}
}

// A key that differs only in case is a different key, to Holdcall exactly as to a
// server. `"Method":"ping"` beside `"method":"tools/call"` used to make Holdcall
// read ping.
func TestKeysAreMatchedByExactBytes(t *testing.T) {
	env, anomaly := Classify([]byte(
		`{"jsonrpc":"2.0","id":1,"ID":2,"method":"tools/call","Method":"ping","params":{"name":"delete_repository","Name":"list"}}`))
	if anomaly != AnomalyNone {
		t.Fatalf("anomaly = %q; case-variant keys are distinct keys, not duplicates", anomaly)
	}
	if !env.IsToolCall() {
		t.Fatal("the tools/call was read as something else")
	}
	if env.Key() != "1" {
		t.Errorf("id = %s, want 1: the answer would go out under an id the client never sent", env.Key())
	}
	call, err := env.Call()
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if call.Name != "delete_repository" {
		t.Errorf("tool = %q; the daemon would decide on one tool while the server ran another", call.Name)
	}
}

// A tools/call Holdcall cannot read as one tool name is refused, not decided on a
// guess. The daemon used to see "" for every one of these.
func TestACallWithoutOneReadableNameIsRefused(t *testing.T) {
	cases := []struct {
		raw  string
		want error
	}{
		{`{"id":1,"method":"tools/call"}`, ErrUnreadableCall},
		{`{"id":1,"method":"tools/call","params":"delete_repository"}`, ErrUnreadableCall},
		{`{"id":1,"method":"tools/call","params":{}}`, ErrUnreadableCall},
		{`{"id":1,"method":"tools/call","params":{"name":0}}`, ErrUnreadableCall},
		{`{"id":1,"method":"tools/call","params":{"name":["rm"]}}`, ErrUnreadableCall},
		{`{"id":1,"method":"tools/call","params":{"name":"a","name":"rm"}}`, ErrDuplicateKey},
		{`{"id":1,"method":"tools/call","params":{"name":"rm","arguments":{},"arguments":{"x":1}}}`, ErrDuplicateKey},
	}
	for _, c := range cases {
		env, anomaly := Classify([]byte(c.raw))
		if anomaly != AnomalyNone || !env.IsToolCall() {
			t.Fatalf("%s: not read as a tools/call (anomaly %q)", c.raw, anomaly)
		}
		if _, err := env.Call(); !errors.Is(err, c.want) {
			t.Errorf("%s: Call() = %v, want %v", c.raw, err, c.want)
		}
	}

	// Keys inside arguments are the tool's business, not Holdcall's: they are
	// digested as bytes and never interpreted, so a repeat there is not one.
	env, _ := Classify([]byte(`{"id":1,"method":"tools/call","params":{"name":"rm","arguments":{"a":1,"a":2}}}`))
	if _, err := env.Call(); err != nil {
		t.Errorf("a repeated key inside arguments was refused: %v", err)
	}
}

// A method that is not a string names nothing a server can dispatch; the frame
// is relayed for the server to reject, as it would be with no Holdcall in the way.
func TestANonStringMethodIsNotACall(t *testing.T) {
	env, anomaly := Classify([]byte(`{"jsonrpc":"2.0","id":1,"method":["tools/call"],"params":{"name":"rm"}}`))
	if anomaly != AnomalyNone || env.IsToolCall() {
		t.Fatalf("anomaly = %q, tool call = %v", anomaly, env.IsToolCall())
	}
}

// A batch element is read as strictly as a frame: a tools/call hidden behind a
// repeated key inside an element used to make the whole batch look harmless.
func TestABatchElementNamingAKeyTwiceIsUnreadable(t *testing.T) {
	raw := `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"ping","method":"tools/call","params":{"name":"rm"}}]`
	if _, ok := BatchElements([]byte(raw)); ok {
		t.Fatal("a batch with an ambiguous element was read as readable")
	}
	raw = `[{"jsonrpc":"2.0","id":2,"method":"tools/call","Method":"ping","params":{"name":"rm"}}]`
	envs, ok := BatchElements([]byte(raw))
	if !ok || len(envs) != 1 || !envs[0].IsToolCall() {
		t.Fatal("a case-variant key hid the tools/call in a batch element")
	}
}
