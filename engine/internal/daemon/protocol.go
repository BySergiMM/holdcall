package daemon

import (
	"encoding/json"
	"fmt"
	"net"
)

// Request is a question that expects a Response on the same connection --
// structurally separate from Event on purpose. Event is one-way, gets
// applied to the journal, and its fields end up logged on a malformed or
// rejected write (see apply's callers). A credential must never be able to
// flow through that path by accident, so it only ever travels inside
// Request/Response, which the journal and the logging around Event never
// see.
type Request struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`

	Target string `json:"target,omitempty"`
	EnvKey string `json:"env_key,omitempty"`
	Secret string `json:"secret,omitempty"` // only set for connector.set; never logged, see String
}

const (
	KindCredentialGet   = "credential.get"
	KindConnectorSet    = "connector.set"
	KindConnectorList   = "connector.list"
	KindConnectorRemove = "connector.remove"
)

// String redacts Secret so that even a future fmt.Printf/log.Printf("%v", req)
// mistake cannot print it: Go's fmt package calls String on any operand that
// implements it, for every verb, including %v and %+v.
func (r Request) String() string {
	secret := "<none>"
	if r.Secret != "" {
		secret = "<redacted>"
	}
	return fmt.Sprintf("Request{ID:%s Kind:%s Target:%s EnvKey:%s Secret:%s}",
		r.ID, r.Kind, r.Target, r.EnvKey, secret)
}

// Response answers a Request. Env carries real secret material for
// credential.get and must never be logged; it has the same String
// protection as Request.
type Response struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`

	// credential.get
	Found bool              `json:"found,omitempty"`
	Env   map[string]string `json:"env,omitempty"`

	// connector.list
	Connectors []ConnectorInfo `json:"connectors,omitempty"`
}

// ConnectorInfo is non-secret connector metadata: which env var name a
// target's credential is injected under, never the value itself.
type ConnectorInfo struct {
	Target    string `json:"target"`
	EnvKey    string `json:"env_key"`
	UpdatedAt string `json:"updated_at"`
}

func (r Response) String() string {
	env := "<none>"
	if len(r.Env) > 0 {
		env = "<redacted>"
	}
	return fmt.Sprintf("Response{ID:%s Error:%q Found:%v Env:%s Connectors:%d}",
		r.ID, r.Error, r.Found, env, len(r.Connectors))
}

// SendRequest writes req and reads back its Response on conn. Used by the
// shim to fetch credentials before spawning a downstream, and by connector
// management commands -- never by anything on the fire-and-forget Event
// path. Callers that want a bound on how long this can block should call
// conn.SetDeadline before calling this; SendRequest sets none of its own.
func SendRequest(conn net.Conn, req Request) (Response, error) {
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("sending request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("reading response: %w", err)
	}
	return resp, nil
}
