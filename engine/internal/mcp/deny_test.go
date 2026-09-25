package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// An id is copied, never understood.
//
// JSON numbers decode to float64, and 9007199254740993 is the first integer
// float64 cannot hold: it comes back as ...992. A client matching responses to
// requests by id would never find this one, and would wait for an answer that
// had already arrived under a different name.
func TestALargeIDSurvivesVerbatim(t *testing.T) {
	const id = "9007199254740993"

	line := DenyResponse(json.RawMessage(id), DeniedByPolicy, "")
	if !strings.Contains(string(line), `"id":`+id) {
		t.Fatalf("the id was rewritten: %s", line)
	}

	// And through a real decode, since that is what a client does.
	var got struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.ID) != id {
		t.Errorf("id = %s, want %s", got.ID, id)
	}
}

// The shape MCP uses for a tool that failed: a successful response whose result
// carries isError, not a JSON-RPC protocol error.
func TestDenyResponseIsAToolError(t *testing.T) {
	line := DenyResponse(json.RawMessage(`7`), DeniedByPolicy, "")

	var got struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   json.RawMessage `json:"error"`
		Result  struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("a denial must be valid JSON-RPC: %v", err)
	}
	if got.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q", got.JSONRPC)
	}
	if len(got.Error) != 0 {
		t.Error("a refused tool call is a tool error, not a protocol error")
	}
	if !got.Result.IsError {
		t.Error("result.isError must be true, or the client reads the refusal as success")
	}
	if len(got.Result.Content) != 1 || got.Result.Content[0].Text != DeniedByPolicy {
		t.Errorf("content = %+v", got.Result.Content)
	}
	if !strings.HasSuffix(string(line), "\n") {
		t.Error("the transport is line-delimited; the newline is part of the message")
	}
}

// The two reasons are different sentences. An agent that cannot tell a rule from
// a failure will retry the wrong one.
func TestTheTwoRefusalsReadDifferently(t *testing.T) {
	if DeniedByPolicy == DeniedNoDecision {
		t.Fatal("the two refusals must not be the same sentence")
	}
	for _, text := range []string{DeniedByPolicy, DeniedNoDecision, DeniedBatch} {
		if !strings.Contains(strings.ToLower(text), "retry") &&
			!strings.Contains(strings.ToLower(text), "individually") {
			t.Errorf("a refusal should say what not to do next: %q", text)
		}
	}
}

// A batch is answered with protocol errors, because it can carry methods whose
// results are not tool results. Answering a ping with a tool result would be
// replying with the wrong kind of message.
func TestDenyBatchAnswersEveryIDAndNoNotification(t *testing.T) {
	envs, ok := BatchElements([]byte(`[
	  {"jsonrpc":"2.0","id":1,"method":"ping"},
	  {"jsonrpc":"2.0","method":"notifications/progress"},
	  {"jsonrpc":"2.0","id":"a","method":"tools/call","params":{"name":"rm"}}
	]`))
	if !ok {
		t.Fatal("that batch is readable")
	}

	var got []struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(DenyBatch(envs, DeniedBatch), &got); err != nil {
		t.Fatalf("a batch refusal must be a valid JSON-RPC batch response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("answered %d elements, want the two that carry an id", len(got))
	}
	if string(got[0].ID) != "1" || string(got[1].ID) != `"a"` {
		t.Errorf("ids = %s, %s", got[0].ID, got[1].ID)
	}
	for _, r := range got {
		if r.Error.Code != DenyErrorCode {
			t.Errorf("code = %d, want %d", r.Error.Code, DenyErrorCode)
		}
		if len(r.Result) != 0 {
			t.Error("an element refused with an error must not also carry a result")
		}
	}
}

// JSON-RPC says a batch of nothing but notifications gets no response. Silence
// is the correct answer, not a failure to produce one.
func TestABatchOfNotificationsIsAnsweredWithSilence(t *testing.T) {
	envs, ok := BatchElements([]byte(`[{"jsonrpc":"2.0","method":"notifications/cancelled"}]`))
	if !ok {
		t.Fatal("that batch is readable")
	}
	if body := DenyBatch(envs, DeniedBatch); body != nil {
		t.Errorf("answered a batch that asked nothing: %s", body)
	}
}

// A present id of null is a request, not a notification: JSON-RPC still
// expects a reply with id: null, however discouraged that id is to send.
func TestDenyBatchAnswersAnExplicitNullID(t *testing.T) {
	envs, ok := BatchElements([]byte(
		`[{"jsonrpc":"2.0","id":null,"method":"tools/call","params":{"name":"rm"}}]`))
	if !ok {
		t.Fatal("that batch is readable")
	}

	var got []struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(DenyBatch(envs, DeniedBatch), &got); err != nil {
		t.Fatalf("a batch refusal must be a valid JSON-RPC batch response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("answered %d elements, want the one carrying an explicit null id", len(got))
	}
	if string(got[0].ID) != "null" {
		t.Errorf("id = %s, want null", got[0].ID)
	}
}

func TestBatchElements(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		ok       bool
		toolCall bool
	}{
		{"two requests", `[{"method":"ping"},{"method":"tools/call"}]`, true, true},
		{"no tool call", `[{"method":"ping"},{"method":"tools/list"}]`, true, false},
		{"empty", `[]`, true, false},
		{"not an array", `{"method":"ping"}`, false, false},
		// Unreadable in part is unaccountable in whole: a tools/call could be
		// hiding in the element that would not parse.
		{"an element that is not an object", `[{"method":"ping"},42]`, false, false},
		{"a string element", `["tools/call"]`, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			envs, ok := BatchElements([]byte(c.raw))
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			found := false
			for _, e := range envs {
				found = found || e.IsToolCall()
			}
			if found != c.toolCall {
				t.Errorf("tools/call found = %v, want %v", found, c.toolCall)
			}
		})
	}
}

func TestIsNotification(t *testing.T) {
	cases := map[string]bool{
		`{"method":"notifications/progress"}`: true,
		// An explicit null id is present, not absent: JSON-RPC still expects a
		// reply with id: null, however discouraged that id is to send.
		`{"id":null,"method":"ping"}`: false,
		`{"id":0,"method":"ping"}`:    false,
		`{"id":"","method":"ping"}`:   false,
		`{"id":1,"method":"ping"}`:    false,
	}
	for raw, want := range cases {
		env, ok := Parse([]byte(raw))
		if !ok {
			t.Fatalf("cannot parse %s", raw)
		}
		if got := env.IsNotification(); got != want {
			t.Errorf("%s: IsNotification = %v, want %v", raw, got, want)
		}
	}
}

// A refusal is written in the dialect the session negotiated. Under the
// 2026-07-28 revision every result must name its resultType and a client
// refuses to parse one that does not; under the older handshake the field is
// unknown and is not sent. Reproduced against fastmcp 4.0.3 before this test
// existed: in its default mode the refusal raised a validation error on the
// client instead of reading as a tool error (F-021).
func TestARefusalCarriesResultTypeOnlyWhenTheProtocolRequiresIt(t *testing.T) {
	for version, want := range map[string]string{
		"":           "",
		"2025-06-18": "",
		"2025-11-25": "",
		"2026-07-28": ResultTypeComplete,
		"2027-01-01": ResultTypeComplete,
	} {
		line := DenyResponse(json.RawMessage(`7`), DeniedByPolicy, version)

		var got struct {
			Result map[string]json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatalf("%q: %v", version, err)
		}
		raw, present := got.Result["resultType"]
		if want == "" {
			if present {
				t.Errorf("%q: a refusal for an older client carries resultType %s; that client does not know the field", version, raw)
			}
			continue
		}
		var typ string
		if err := json.Unmarshal(raw, &typ); err != nil || typ != want {
			t.Errorf("%q: resultType = %s, want %q", version, raw, want)
		}
		if !json.Valid(got.Result["content"]) || string(got.Result["isError"]) != "true" {
			t.Errorf("%q: the rest of the refusal changed shape: %s", version, line)
		}
	}
}
