package mcp

// The only messages Nim writes itself.
//
// Everything else Nim handles was written by the client or by the server and is
// passed on unchanged. These are the exception, and they exist because a
// refused call still has to be answered: a client left waiting for a response
// that will never come is worse than being told no.

import "encoding/json"

// The two reasons a call is refused. They are different sentences on purpose.
//
// One says a rule was applied and will be applied again. The other says no rule
// could be reached, which may not be true a minute from now. An agent told only
// "denied" cannot tell which, and will retry the wrong one.
//
// Both discourage retrying, because an agent that retries a refusal turns one
// denial into a loop.
const (
	DeniedByPolicy   = "Nim denied this call by policy. Do not retry automatically."
	DeniedNoDecision = "Nim could not reach a decision and denied the call. Do not retry automatically."
)

// DeniedUnreadable explains a tools/call Nim refused because it could not read
// which tool it named. Its own sentence, because it is neither a rule nor an
// outage: the frame was the problem, and sending the same bytes again will
// meet the same refusal.
const DeniedUnreadable = "Nim refused this call: it could not read the tool name unambiguously. Do not retry automatically."

// DeniedBatch explains a refused batch.
const DeniedBatch = "Nim refused this batch: it carries a tools/call, and Nim does not decide batch elements one by one. Send the calls individually."

// DenyErrorCode is in JSON-RPC's implementation-defined server error range.
//
// Not -32600 "Invalid Request": the request was well formed and Nim understood
// it perfectly well. It was refused, which is a different statement, and one an
// error code should not misreport.
const DenyErrorCode = -32000

type toolText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ResultTypeComplete is the resultType of an ordinary, finished tool result
// under the revisions that require the field. The specification's own words:
// when the field is absent, "the client MUST treat the absent field as
// "complete"" -- so this is the value the older shape already meant.
const ResultTypeComplete = "complete"

type toolResult struct {
	Content    []toolText `json:"content"`
	IsError    bool       `json:"isError"`
	ResultType string     `json:"resultType,omitempty"`
}

// id is json.RawMessage everywhere below. Decoding and re-encoding it would
// round an integer id past 2^53 to a different number, and a client that cannot
// match a response to its request is left waiting -- the exact failure these
// messages exist to avoid.
type toolResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  toolResult      `json:"result"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   rpcError        `json:"error"`
}

// DenyResponse refuses one tools/call, in the shape MCP uses for a tool that
// failed: a successful JSON-RPC response whose result carries isError.
//
// That is the right shape rather than a protocol error. From the client's side
// the call was delivered and answered; what did not happen is the work. The
// model reads the text and can react to it, which is the whole reason MCP
// reports tool failures this way.
//
// protocolVersion is what the session negotiated, and decides the dialect the
// refusal is written in. Under 2026-07-28 and later every result must name
// its resultType, and a client on that revision refuses to parse one that
// does not: reproduced with fastmcp 4.0.3, whose default handshake is that
// revision, the refusal surfaced as a local validation error rather than the
// tool error it is meant to read as (F-021). An older client is sent the
// older shape, unchanged, because nothing says how it would treat a field it
// does not know.
//
// The returned bytes include the trailing newline the transport needs.
func DenyResponse(id json.RawMessage, text, protocolVersion string) []byte {
	result := toolResult{
		Content: []toolText{{Type: "text", Text: text}},
		IsError: true,
	}
	if ResultsCarryType(protocolVersion) {
		result.ResultType = ResultTypeComplete
	}
	body, err := json.Marshal(toolResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
	if err != nil {
		return nil
	}
	return append(body, '\n')
}

// DenyBatch refuses a whole batch, answering each element that asked for an
// answer.
//
// These are JSON-RPC errors rather than tool results, and this is the case the
// "prefer isError" rule makes an exception for: a batch may carry ping or
// tools/list beside the tools/call, and a ping's result is not a tool result.
// Inventing one would be answering with the wrong kind of message.
//
// Elements with no id are notifications and get nothing back, as JSON-RPC
// requires. A batch of nothing but notifications gets no response at all, which
// JSON-RPC also requires -- so nil here means "correctly silent", not "failed".
func DenyBatch(envs []Envelope, text string) []byte {
	out := make([]errorResponse, 0, len(envs))
	for _, env := range envs {
		if env.IsNotification() {
			continue
		}
		out = append(out, errorResponse{
			JSONRPC: "2.0",
			ID:      env.ID,
			Error:   rpcError{Code: DenyErrorCode, Message: text},
		})
	}
	if len(out) == 0 {
		return nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return append(body, '\n')
}
