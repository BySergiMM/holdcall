// Package mcp reads and writes the MCP stdio framing without disturbing it.
//
// The transport is newline-delimited JSON: one message per line. Two rules
// govern everything here.
//
// Messages are forwarded as the exact bytes that arrived. Parsing and
// re-encoding JSON reorders keys and rewrites whitespace, which breaks clients
// in ways that are hard to reproduce, so a message is never re-serialised --
// it is inspected on the side and passed through untouched.
//
// Nothing is assumed about a message beyond JSON-RPC's own envelope. Anything
// that fails to parse, or that carries a method Nim does not know, is still
// relayed. Nim only has to understand tools/call; the rest of the protocol is
// not its business and must keep working as the specification evolves.
package mcp

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
)

// MethodToolsCall is the only method Nim needs to recognise.
const MethodToolsCall = "tools/call"

// Reader yields raw messages from an MCP stdio stream.
type Reader struct {
	br *bufio.Reader
}

func NewReader(r io.Reader) *Reader {
	// bufio.Scanner has a fixed maximum token size and tool results routinely
	// exceed it. ReadBytes grows as needed.
	return &Reader{br: bufio.NewReaderSize(r, 64*1024)}
}

// ReadRaw returns the next message including its trailing newline, exactly as
// it arrived. A final message with no newline is returned as-is.
func (r *Reader) ReadRaw() ([]byte, error) {
	line, err := r.br.ReadBytes('\n')
	if len(line) > 0 {
		return line, nil
	}
	return nil, err
}

// Envelope is the small part of a message Nim looks at. Every field is
// optional: absence is normal, not an error.
type Envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Error  json.RawMessage `json:"error"`
	Result json.RawMessage `json:"result"`
}

// Parse reports what little Nim understands about a message. ok is false when
// the bytes are not a JSON object, in which case the caller still forwards them.
func Parse(raw []byte) (Envelope, bool) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, false
	}
	return env, true
}

// IsToolCall reports whether this message is a tools/call request.
func (e Envelope) IsToolCall() bool { return e.Method == MethodToolsCall }

// IsResponse reports whether this message answers an earlier request. Responses
// carry an id and no method.
func (e Envelope) IsResponse() bool { return len(e.ID) > 0 && e.Method == "" }

// Key returns a stable correlation key for a request id. JSON-RPC allows both
// strings and numbers, so the raw encoding is used verbatim.
func (e Envelope) Key() string { return string(e.ID) }

// Failed reports whether a response represents a failure.
//
// There are two ways for a tool call to go wrong and they look nothing alike.
// A protocol fault arrives as a JSON-RPC error. A tool that raises arrives as a
// perfectly successful response carrying result.isError -- by design, so the
// model can read the failure and react. Recording only the first kind marks
// every failed tool call as successful, which is worse than not recording it.
func (e Envelope) Failed() bool {
	if len(e.Error) > 0 {
		return true
	}
	if len(e.Result) == 0 {
		return false
	}
	var r struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(e.Result, &r); err != nil {
		return false
	}
	return r.IsError
}

// ToolName extracts params.name from a tools/call, or "" when absent.
func (e Envelope) ToolName() string {
	if len(e.Params) == 0 {
		return ""
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(e.Params, &p); err != nil {
		return ""
	}
	return p.Name
}

// ArgumentsDigest hashes params.arguments. The arguments themselves never
// leave the machine; only this digest is ever recorded or synced.
func (e Envelope) ArgumentsDigest() string {
	var p struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(e.Params) > 0 {
		_ = json.Unmarshal(e.Params, &p)
	}
	sum := sha256.Sum256(p.Arguments)
	return hex.EncodeToString(sum[:])
}
