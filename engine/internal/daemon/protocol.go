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

	// Command is the downstream server this target's credential may be
	// injected into, as argv. Set only by connector.set: a credential.get
	// never sends one, because a caller does not get to nominate the command
	// that receives a secret -- that is precisely the hole this field exists
	// to close. See handleCredentialGet.
	Command []string `json:"command,omitempty"`

	// AgentName and AgentPath belong to the agent.* kinds. The path is what
	// the operator typed; the daemon resolves it to an executable identity
	// itself rather than accepting one from the caller, for the same reason
	// it returns a connector's command rather than accepting one.
	AgentName string `json:"agent_name,omitempty"`
	AgentPath string `json:"agent_path,omitempty"`
}

const (
	KindCredentialGet   = "credential.get"
	KindConnectorSet    = "connector.set"
	KindConnectorList   = "connector.list"
	KindConnectorRemove = "connector.remove"

	KindAgentAdd    = "agent.add"
	KindAgentList   = "agent.list"
	KindAgentRemove = "agent.remove"
)

// String redacts Secret so that even a future fmt.Printf/log.Printf("%v", req)
// mistake cannot print it: Go's fmt package calls String on any operand that
// implements it, for every verb, including %v and %+v.
func (r Request) String() string {
	secret := "<none>"
	if r.Secret != "" {
		secret = "<redacted>"
	}
	return fmt.Sprintf("Request{ID:%s Kind:%s Target:%s EnvKey:%s Secret:%s Command:%d args}",
		r.ID, r.Kind, r.Target, r.EnvKey, secret, len(r.Command))
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
	// Command is the argv the daemon authorizes this credential to be
	// injected into. The shim spawns this, not whatever was on its own
	// command line: the daemon is the authority on which process may receive
	// a secret, and a shim that took the caller's word for it is the
	// credential oracle this replaces.
	Command []string `json:"command,omitempty"`

	// connector.list
	Connectors []ConnectorInfo `json:"connectors,omitempty"`

	// agent.list
	Agents []AgentInfo `json:"agents,omitempty"`
}

// ConnectorInfo is non-secret connector metadata: which env var name a
// target's credential is injected under, never the value itself.
type ConnectorInfo struct {
	Target    string   `json:"target"`
	EnvKey    string   `json:"env_key"`
	Command   []string `json:"command,omitempty"`
	UpdatedAt string   `json:"updated_at"`
}

// AgentInfo is one enrolment as reported to the CLI. ExecDev and ExecIno are
// the identity; ExecPath is what the operator enrolled and is shown so the
// enrolment can be recognised, never so it can be matched on.
type AgentInfo struct {
	Name       string `json:"name"`
	ExecDev    uint64 `json:"exec_dev"`
	ExecIno    uint64 `json:"exec_ino"`
	ExecPath   string `json:"exec_path"`
	EnrolledAt string `json:"enrolled_at"`
	// Current reports whether the file at ExecPath is still the enrolled one.
	// False is not an error and not a security finding: a device number can
	// change across a reboot and an application that updates itself becomes a
	// different file. It means this enrolment no longer matches anything and
	// should be repeated.
	Current bool `json:"current"`
}

func (r Response) String() string {
	env := "<none>"
	if len(r.Env) > 0 {
		env = "<redacted>"
	}
	return fmt.Sprintf("Response{ID:%s Error:%q Found:%v Env:%s Connectors:%d Agents:%d}",
		r.ID, r.Error, r.Found, env, len(r.Connectors), len(r.Agents))
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
