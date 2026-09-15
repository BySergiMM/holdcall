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
	"errors"
	"fmt"
	"io"
)

// MethodToolsCall is the only method Nim acts on.
const MethodToolsCall = "tools/call"

// MethodInitialize carries the negotiated protocol version, which decides
// whether the server accepts JSON-RPC batches at all.
const MethodInitialize = "initialize"

// MethodDiscover is the other way a session can be negotiated. The 2026-07-28
// revision replaced initialize with server/discover: the client proposes a
// version in the request's _meta and the server answers with the versions it
// supports. A session negotiated this way never sends initialize, so a relay
// that only watched initialize would never learn the version -- and would
// answer refusals in a shape such a client cannot read (F-021).
const MethodDiscover = "server/discover"

// discoverVersionKey is where a server/discover request names the version it
// proposes: params._meta["io.modelcontextprotocol/protocolVersion"].
const discoverVersionKey = "io.modelcontextprotocol/protocolVersion"

// resultTypeSince is the first protocol revision under which every result
// carries a resultType discriminator, which its clients validate strictly.
// Versions are dates, so string order is chronological.
const resultTypeSince = "2026-07-28"

// ResultsCarryType reports whether a result written for a session negotiated
// at version must carry resultType. Under 2026-07-28 and later the field is
// mandatory and a client refuses to parse a result without it; under the
// initialize-era versions it is unknown, and a client that never negotiated
// (version "") is on those.
func ResultsCarryType(version string) bool {
	return version >= resultTypeSince
}

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

	// AnomalyDuplicateKey is an object, at a level Nim reads, that carries the
	// same key twice. Valid JSON, and the one shape on which parsers
	// legitimately disagree: Go's typed decoder, Python and JavaScript each
	// pick a value by their own rule, so `"method":5,"method":"tools/call"` is
	// a tools/call to a server and was, until this existed, nothing at all to
	// Nim -- relayed with no decision, no entry and no anomaly. Reproduced
	// against the real relay before it was closed.
	AnomalyDuplicateKey Anomaly = "duplicate_key"

	// AnomalyUnreadableCall is a tools/call whose params, or whose name, Nim
	// could not read as exactly one string. No server executes a tool it
	// cannot name either, but the daemon would have decided on a name of ""
	// and the journal would have recorded it, which is a decision about
	// nothing. Refused instead.
	AnomalyUnreadableCall Anomaly = "unreadable_call"
)

// The two ways a frame Nim reads can fail to mean one thing.
var (
	ErrDuplicateKey   = errors.New("an object carries the same key twice")
	ErrUnreadableCall = errors.New("the tools/call cannot be read as a single tool name")
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

	// `null`, a bare number or a string is valid JSON that no server treats as
	// a request; it is relayed as it always was. An object is read strictly:
	// by exact key, refusing a key that appears twice. Nothing else can be
	// trusted to name the same message to Nim and to the server.
	fields, err := objectFields(value)
	if errors.Is(err, ErrDuplicateKey) {
		return Envelope{}, AnomalyDuplicateKey
	}
	if err != nil {
		return Envelope{}, AnomalyNone
	}
	return envelopeOf(fields), AnomalyNone
}

// objectFields reads one JSON object into its members, by the exact bytes of
// each key, refusing an object that names a key twice.
//
// This is deliberately not json.Unmarshal into a struct. That decoder matches
// keys case-insensitively and lets the last of several matches win, and it
// keeps going past a member of the wrong type -- so `"Method":"ping"` after
// `"method":"tools/call"` read as ping, `"method":5,"method":"tools/call"`
// read as an error the caller discarded, and a frame that every mainstream
// server would execute as a tool call was forwarded as something else.
// Reading by exact key gives the same answer Python and JavaScript give for
// every object that names each key once, and a repeated key is refused rather
// than resolved, because the one rule parsers do not share is which
// repetition wins.
//
// errNotAnObject is returned for any other JSON value.
func objectFields(raw []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, errNotAnObject
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("object key is %v, not a string", keyTok)
		}
		if _, seen := fields[key]; seen {
			return nil, ErrDuplicateKey
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	return fields, nil
}

var errNotAnObject = errors.New("not a JSON object")

// envelopeOf builds the envelope from members read by exact key. A method
// that is present but not a string is left empty: it names nothing a server
// can dispatch, and the frame is relayed for the server to reject in its own
// words.
func envelopeOf(fields map[string]json.RawMessage) Envelope {
	env := Envelope{
		ID:     fields["id"],
		Params: fields["params"],
		Error:  fields["error"],
		Result: fields["result"],
	}
	if raw, ok := fields["method"]; ok {
		_ = json.Unmarshal(raw, &env.Method) // not a string: stays ""
	}
	return env
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
		// Strictly, for the same reason Classify is: an element whose method
		// is named twice is a tools/call to a server and nothing to a decoder
		// that picks the other value.
		fields, err := objectFields(item)
		if err != nil {
			return nil, false
		}
		out = append(out, envelopeOf(fields))
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
// the bytes are not a JSON object that names each key once, in which case the
// caller still forwards them.
func Parse(raw []byte) (Envelope, bool) {
	fields, err := objectFields(bytes.TrimSpace(raw))
	if err != nil {
		return Envelope{}, false
	}
	return envelopeOf(fields), true
}

// IsToolCall reports whether this message is a tools/call request.
func (e Envelope) IsToolCall() bool { return e.Method == MethodToolsCall }

// IsInitialize reports whether this message is the initialize request.
func (e Envelope) IsInitialize() bool { return e.Method == MethodInitialize }

// IsDiscover reports whether this message is a server/discover request.
func (e Envelope) IsDiscover() bool { return e.Method == MethodDiscover }

// IsError reports whether a response carries a JSON-RPC error rather than a
// result.
func (e Envelope) IsError() bool { return len(e.Error) > 0 }

// ProposedProtocolVersion reads the version a server/discover request
// proposes, or "" when the request names none. Unlike initialize, the
// agreed version is not in the answer: the answer lists what the server
// supports, and the client picks from that list. See NegotiatedVersion.
func (e Envelope) ProposedProtocolVersion() string {
	if len(e.Params) == 0 {
		return ""
	}
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(e.Params, &p); err != nil {
		return ""
	}
	var version string
	_ = json.Unmarshal(p.Meta[discoverVersionKey], &version) // not a string: stays ""
	return version
}

// NegotiatedVersion is what a session negotiated through server/discover
// settled on, as far as the relay can tell: the version the client proposed
// when the server's answer lists it, otherwise the newest listed version that
// is not newer than the proposal, which is the newest the client can choose
// since a client proposes the newest it knows. "" when the answer is an error
// or lists nothing usable, in which case the client falls back to initialize
// and that handshake is watched as before.
func (e Envelope) NegotiatedVersion(proposed string) string {
	if e.IsError() || len(e.Result) == 0 {
		return ""
	}
	var r struct {
		Supported []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(e.Result, &r); err != nil {
		return ""
	}
	best := ""
	for _, v := range r.Supported {
		if v == proposed {
			return v
		}
		if v < proposed && v > best {
			best = v
		}
	}
	return best
}

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

// ToolCall is what the decision is about: the tool a tools/call names, and a
// digest standing in for the arguments it carries.
type ToolCall struct {
	// Name is params.name, exactly as sent. No trimming, no case folding, no
	// normalisation: the deny list compares bytes, and a server matches bytes.
	Name string
	// Digest is sha256 over the raw params.arguments bytes as they arrived.
	// When the key is absent the digest is of no bytes at all; when it is
	// present and null, of the four bytes `null`. Neither is a secret and both
	// are stated in docs/journal-format.md, because a reader comparing digests
	// has to know that "no arguments" has a fixed value.
	Digest string
}

// Call reads the tools/call out of a request Nim has already recognised as
// one.
//
// Read strictly, and refused rather than approximated when it cannot be:
// params must be an object naming each key once, and name must be a string.
// The daemon decides on the name this returns and the journal records it, so
// it has to be the name the server will act on. A params object that names
// `name` twice, or under two spellings a case-insensitive decoder would merge,
// could put one tool in front of the daemon and another in front of the
// server -- reproduced, before this existed, with the deny list refusing the
// one the journal then said had been called.
//
// The error is ErrDuplicateKey or ErrUnreadableCall, and the relay turns each
// into a refusal and an anomaly rather than a decision about a guess.
func (e Envelope) Call() (ToolCall, error) {
	if len(e.Params) == 0 {
		return ToolCall{}, ErrUnreadableCall
	}
	fields, err := objectFields(e.Params)
	if errors.Is(err, ErrDuplicateKey) {
		return ToolCall{}, ErrDuplicateKey
	}
	if err != nil {
		return ToolCall{}, ErrUnreadableCall
	}
	rawName, ok := fields["name"]
	if !ok {
		return ToolCall{}, ErrUnreadableCall
	}
	var name string
	if err := json.Unmarshal(rawName, &name); err != nil {
		return ToolCall{}, ErrUnreadableCall
	}
	sum := sha256.Sum256(fields["arguments"])
	return ToolCall{Name: name, Digest: hex.EncodeToString(sum[:])}, nil
}
