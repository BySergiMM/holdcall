// Package mcp reads and writes the MCP stdio framing without disturbing it.
//
// The transport is newline-delimited JSON: one message per line. Two rules
// govern everything here.
//
// A message that is relayed is relayed as the exact bytes that arrived. Parsing
// and re-encoding JSON reorders keys and rewrites whitespace, which breaks
// clients in ways that are hard to reproduce, so a message is never
// re-serialised -- it is inspected on the side and passed through untouched, or
// it does not pass through at all.
//
// Nothing is assumed about a message beyond JSON-RPC's own envelope. A method
// Nim does not know is still relayed: only tools/call is Nim's business, and
// the rest of the protocol must keep working as the specification evolves.
//
// What is no longer true is that everything is relayed. A frame Nim cannot
// parse may still be a tools/call to a more forgiving parser downstream -- Go
// rejects NaN where Python accepts it, which is enough to put a call in front
// of a server that Nim never saw -- so from M2 an unreadable frame is refused
// instead of forwarded. Classify names the shapes; what happens to them is the
// relay's decision, not this package's.
package mcp

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
)

// MethodToolsCall is the only method Nim acts on.
const MethodToolsCall = "tools/call"

// MethodInitialize carries the negotiated protocol version, which decides
// whether the server accepts JSON-RPC batches at all.
const MethodInitialize = "initialize"

// Anomaly names a message Nim relayed without being able to account for it.
//
// These are not errors in Nim and not, yet, anything it acts on. They are the
// shapes that would let a message reach a server without passing inspection,
// which makes them worth a number rather than a shrug.
type Anomaly string

const (
	AnomalyNone Anomaly = ""

	// AnomalyBatch is a JSON-RPC batch: an array of messages. Envelope parsing
	// expects an object, so a tools/call inside an array is not seen at all.
	AnomalyBatch Anomaly = "batch"

	// AnomalyMalformedJSON is a frame that is not JSON. Nim cannot tell what it
	// asks for, so it cannot tell whether it mattered.
	AnomalyMalformedJSON Anomaly = "malformed_json"

	// AnomalyFraming is more than one JSON value in a single frame. The
	// transport is one message per line; a reader that accumulates instead
	// would see different messages than Nim did.
	AnomalyFraming Anomaly = "framing"
)

// Classify parses a message and names what is odd about it.
//
// A frame Nim cannot parse into an object is not automatically an anomaly:
// `null`, a bare number and a string are all valid JSON that no server treats
// as a request, and reporting them would bury the shapes that do matter.
//
// One real framing case is out of reach here: a single message split across
// several lines arrives as several unparseable frames and is reported as
// malformed JSON. Distinguishing the two needs the strict reader that comes
// with enforcement, and guessing in the meantime would put a number on
// something this cannot actually detect.
func Classify(raw []byte) (Envelope, Anomaly) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return Envelope{}, AnomalyNone
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var value json.RawMessage
	if err := dec.Decode(&value); err != nil {
		return Envelope{}, AnomalyMalformedJSON
	}

	// Anything after the first complete value means Nim and the server may not
	// agree on how many messages arrived. Decoder.More is not enough to notice:
	// it answers for array and object iteration, so a stray `}` looks like the
	// end of something rather than leftovers.
	if rest := bytes.TrimSpace(trimmed[dec.InputOffset():]); len(rest) > 0 {
		var next json.RawMessage
		if json.NewDecoder(bytes.NewReader(rest)).Decode(&next) == nil {
			return Envelope{}, AnomalyFraming // a second message in one frame
		}
		return Envelope{}, AnomalyMalformedJSON // trailing junk
	}

	if trimmed[0] == '[' {
		return Envelope{}, AnomalyBatch
	}

	var env Envelope
	if err := json.Unmarshal(value, &env); err != nil {
		return Envelope{}, AnomalyNone
	}
	return env, AnomalyNone
}

// BatchElements reads the messages a JSON-RPC batch carries.
//
// Inspection only: the array is never re-emitted, and a batch that is allowed
// through travels as the bytes that arrived.
//
// ok is false when the frame is not an array, or when any element is not
// something an envelope can be read from. A batch Nim cannot read completely is
// one it cannot say anything safe about, and the caller is expected to treat
// that as a refusal rather than as an empty batch.
func BatchElements(raw []byte) ([]Envelope, bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(raw), &items); err != nil {
		return nil, false
	}
	out := make([]Envelope, 0, len(items))
	for _, item := range items {
		var env Envelope
		if err := json.Unmarshal(item, &env); err != nil {
			return nil, false
		}
		out = append(out, env)
	}
	return out, true
}

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

// IsInitialize reports whether this message is the initialize request.
func (e Envelope) IsInitialize() bool { return e.Method == MethodInitialize }

// ProtocolVersion reads result.protocolVersion from an initialize response, or
// "" when absent. It is the version the client and server actually agreed on,
// which is not knowable from the request alone.
func (e Envelope) ProtocolVersion() string {
	if len(e.Result) == 0 {
		return ""
	}
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(e.Result, &r); err != nil {
		return ""
	}
	return r.ProtocolVersion
}

// IsResponse reports whether this message answers an earlier request. Responses
// carry an id and no method.
func (e Envelope) IsResponse() bool { return len(e.ID) > 0 && e.Method == "" }

// IsNotification reports whether this message expects no reply.
//
// JSON-RPC defines a notification as a Request object without an id member. A
// present id of null is still a request expecting a reply with id: null --
// unusual, and the spec itself calls it discouraged, but not the same thing
// as absent.
func (e Envelope) IsNotification() bool {
	return len(e.ID) == 0
}

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
