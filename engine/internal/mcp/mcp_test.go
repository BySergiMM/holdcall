package mcp

import (
	"bytes"
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
	if got := env.ToolName(); got != "send_email" {
		t.Errorf("tool name = %q", got)
	}
	if env.Key() != "7" {
		t.Errorf("key = %q", env.Key())
	}
}

func TestParseTolueratesAnythingElse(t *testing.T) {
	// Nim only has to understand tools/call. Everything else must survive.
	for _, raw := range []string{`garbage`, `[]`, `null`, `{"method":"future/method"}`} {
		env, ok := Parse([]byte(raw))
		if ok && env.IsToolCall() {
			t.Errorf("%q was mistaken for a tools/call", raw)
		}
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
	a, _ := Parse([]byte(`{"method":"tools/call","params":{"name":"t","arguments":{"secret":"hunter2"}}}`))
	b, _ := Parse([]byte(`{"method":"tools/call","params":{"name":"t","arguments":{"secret":"hunter2"}}}`))
	c, _ := Parse([]byte(`{"method":"tools/call","params":{"name":"t","arguments":{"secret":"other"}}}`))

	if a.ArgumentsDigest() != b.ArgumentsDigest() {
		t.Error("identical arguments must digest identically")
	}
	if a.ArgumentsDigest() == c.ArgumentsDigest() {
		t.Error("different arguments must digest differently")
	}
	if strings.Contains(a.ArgumentsDigest(), "hunter2") {
		t.Error("the digest leaked the argument")
	}
	if len(a.ArgumentsDigest()) != 64 {
		t.Errorf("expected a sha256 hex digest, got %d chars", len(a.ArgumentsDigest()))
	}
}
