package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/BySergiMM/nim/engine/internal/credential"
	"github.com/BySergiMM/nim/engine/internal/journal"
)

// requestKinds distinguishes a Request from an Event on the wire: both carry a
// "kind" field, but the two vocabularies never overlap, so a cheap peek at just
// that field routes the rest of the message without an extra discriminator.
var requestKinds = map[string]bool{
	KindCredentialGet:   true,
	KindConnectorSet:    true,
	KindConnectorList:   true,
	KindConnectorRemove: true,
	KindAgentAdd:        true,
	KindAgentList:       true,
	KindAgentRemove:     true,
	KindPolicyDeny:      true,
	KindPolicyAllow:     true,
	KindPolicyRemove:    true,
	KindPolicyList:      true,
	KindPolicyExplain:   true,
	KindBudgetSet:       true,
	KindBudgetRemove:    true,
	KindBudgetList:      true,
}

// requestState is the per-connection authorization state the Request family
// needs, on top of the peer check every connection already passed.
//
// kind and boundTarget are a second layer, independent of peer identity: a
// connection commits to exactly one purpose on its first request -- asking for
// its own target's credential, managing connectors, or managing agents -- and
// to exactly one target if that purpose is credential.get. A real shim only ever does one or
// the other. Anything that mixes them, or pivots to a second target on one
// connection, gets "unauthorized" rather than a more specific reason: specific
// reasons are exactly the oracle an attacker iterating on this protocol wants.
type requestState struct {
	kind        string // "" | "credential" | "connector" | "agent" | "policy"
	boundTarget string // meaningful only once kind == "credential"
}

// serveRequest decodes, dispatches and answers one Request.
func serveRequest(
	conn net.Conn, raw []byte, id, kind string,
	state *requestState, j *journal.Journal, store credential.Store, locks *targetLocks,
) error {
	if len(raw) > MaxRequestBytes {
		return json.NewEncoder(conn).Encode(Response{
			ID: id, Error: fmt.Sprintf("request exceeds %d byte limit", MaxRequestBytes)})
	}
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		// Never log req here: it failed to fully decode, but a partial or
		// garbled connector.set payload could still carry enough of a secret
		// fragment to be worth not printing.
		return json.NewEncoder(conn).Encode(Response{
			ID: id, Error: fmt.Sprintf("malformed %s request", kind)})
	}
	return json.NewEncoder(conn).Encode(handleRequest(req, state, j, store, locks))
}

// handleRequest enforces the purpose binding, then routes. None of the handlers
// below may log req or any secret-bearing value they compute -- Response.Error
// is the only channel back to the caller, and must describe the failure without
// ever including a credential value.
func handleRequest(
	req Request, state *requestState,
	j *journal.Journal, store credential.Store, locks *targetLocks,
) Response {
	var thisKind string
	switch req.Kind {
	case KindCredentialGet:
		thisKind = "credential"
	case KindConnectorSet, KindConnectorList, KindConnectorRemove:
		thisKind = "connector"
	case KindAgentAdd, KindAgentList, KindAgentRemove:
		thisKind = "agent"
	case KindPolicyDeny, KindPolicyAllow, KindPolicyRemove, KindPolicyList, KindPolicyExplain,
		KindBudgetSet, KindBudgetRemove, KindBudgetList:
		thisKind = "policy"
	default:
		return Response{ID: req.ID, Error: fmt.Sprintf("unknown request kind %q", req.Kind)}
	}
	if state.kind == "" {
		state.kind = thisKind
	} else if state.kind != thisKind {
		return Response{ID: req.ID, Error: "unauthorized"}
	}

	switch req.Kind {
	case KindCredentialGet:
		return handleCredentialGet(req, state, j, store, locks)
	case KindConnectorSet:
		return handleConnectorSet(req, j, store, locks)
	case KindConnectorList:
		return handleConnectorList(req, j)
	case KindConnectorRemove:
		return handleConnectorRemove(req, j, store, locks)
	case KindAgentAdd:
		return handleAgentAdd(req, j)
	case KindAgentList:
		return handleAgentList(req, j)
	case KindAgentRemove:
		return handleAgentRemove(req, j)
	case KindPolicyDeny:
		return handlePolicyDeny(req, j)
	case KindPolicyAllow:
		return handlePolicyAllow(req, j)
	case KindPolicyRemove:
		return handlePolicyRemove(req, j)
	case KindPolicyList:
		return handlePolicyList(req, j)
	case KindPolicyExplain:
		return handlePolicyExplain(req, j)
	case KindBudgetSet:
		return handleBudgetSet(req, j)
	case KindBudgetRemove:
		return handleBudgetRemove(req, j)
	case KindBudgetList:
		return handleBudgetList(req, j)
	default:
		panic("unreachable: the switch above is exhaustive for req.Kind")
	}
}

// handleCredentialGet is the shim's spawn-time lookup.
//
// Found=false with no error is the ordinary case for a target with no
// connector configured: not a failure, and the shim spawns with no env
// changes. Found=true with a non-empty Error is the fail-closed case -- a
// connector IS configured but its credential cannot be released, and the shim
// must refuse to spawn rather than start the downstream under a partial
// configuration. docs/decisions/0001-failure-behaviour.md sets out why those
// two are different questions.
//
// The answer carries the command the credential may be injected into, and the
// shim spawns that rather than whatever was on its own command line. That is
// the authorization boundary, and it exists because the checks around it are
// not one.
//
// Peer verification asks "is the caller this binary". Any local process
// satisfies that by executing the binary, so on its own it authorizes nothing:
// reproduced live, where
//
//	nim serve --target github -- /bin/sh -c 'echo $GITHUB_TOKEN'
//
// printed the real secret. Both layers passed -- the caller was genuinely this
// binary, and the connection genuinely asked for one target and only one.
// Neither constrains which target may be asked for, or what receives the
// answer. A caller that chooses the process receiving a secret has the secret.
//
// So the command is registered with the connector, in SQLite, by whoever
// stored the credential, and the daemon hands it back rather than accepting
// one. An attacker naming another target now gets that target's real
// downstream server spawned, which is not a way to read the secret.
//
// The metadata lookup and the secret lookup are two reads, not one atomic
// operation, so this takes the same per-target lock connector.set/remove use:
// without it a concurrent set renaming a target's env_key can interleave
// between them and hand back one generation's env_key paired with another
// generation's secret. Reproduced live -- thousands of torn reads out of 20000
// iterations under -race.
//
// What this does not close, stated rather than glossed: the registered argv is
// spawned through the usual PATH resolution, so a caller that already controls
// PATH can put its own binary in front of the registered name. Closing that
// needs the daemon to spawn the downstream itself, which is a larger change.
func handleCredentialGet(
	req Request, state *requestState,
	j *journal.Journal, store credential.Store, locks *targetLocks,
) Response {
	if err := validateTarget(req.Target); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if state.boundTarget == "" {
		state.boundTarget = req.Target
	} else if state.boundTarget != req.Target {
		return Response{ID: req.ID, Error: "unauthorized"}
	}

	unlock := locks.Lock(req.Target)
	defer unlock()

	info, found, err := j.ConnectorInfo(req.Target)
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("looking up connector metadata: %v", err)}
	}
	if !found {
		return Response{ID: req.ID, Found: false}
	}
	// A connector with no registered command is not one without a restriction:
	// it is one whose authorized command is unknown, which is the state every
	// connector stored before commands existed is in. There is nothing safe to
	// inject the secret into, so nothing is injected.
	if len(info.Command) == 0 {
		return Response{ID: req.ID, Found: true, Error: fmt.Sprintf(
			"connector %q has no authorized command: it was registered before Nim bound credentials "+
				"to a command. Register it again with the server it belongs to, e.g. "+
				"nim connector set %s --env %s -- <command> [args...]",
			req.Target, req.Target, info.EnvKey)}
	}
	if store == nil {
		return Response{ID: req.ID, Found: true, Error: "credential store unavailable"}
	}
	secret, err := store.Get(req.Target)
	if err != nil {
		return Response{ID: req.ID, Found: true,
			Error: fmt.Sprintf("retrieving secret for connector %q: %v", req.Target, err)}
	}
	return Response{
		ID:      req.ID,
		Found:   true,
		Env:     map[string]string{info.EnvKey: secret},
		Command: info.Command,
	}
}

func handleConnectorSet(req Request, j *journal.Journal, store credential.Store, locks *targetLocks) Response {
	if err := validateTarget(req.Target); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if err := validateEnvKey(req.EnvKey); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if err := validateSecret(req.Secret); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if err := validateCommand(req.Command); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if store == nil {
		return Response{ID: req.ID, Error: "credential store unavailable"}
	}

	unlock := locks.Lock(req.Target)
	defer unlock()

	if err := store.Set(req.Target, req.Secret); err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("storing secret: %v", err)}
	}
	if err := j.SetConnector(req.Target, req.EnvKey, req.Command, time.Now()); err != nil {
		// The store write succeeded but the metadata write did not: undo it so
		// the two do not silently drift apart. An orphaned store entry with no
		// matching metadata would be invisible to connector.list but still
		// retrievable by anyone who guesses the target name.
		_ = store.Delete(req.Target)
		return Response{ID: req.ID, Error: fmt.Sprintf("storing connector metadata: %v", err)}
	}
	return Response{ID: req.ID}
}

func handleConnectorList(req Request, j *journal.Journal) Response {
	list, err := j.ListConnectors()
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("listing connectors: %v", err)}
	}
	infos := make([]ConnectorInfo, len(list))
	for i, c := range list {
		infos[i] = ConnectorInfo{
			Target:    c.Target,
			EnvKey:    c.EnvKey,
			Command:   c.Command,
			UpdatedAt: c.UpdatedAt.Format(time.RFC3339Nano),
		}
	}
	return Response{ID: req.ID, Connectors: infos}
}

// The secret is removed before the metadata, deliberately the mirror image of
// handleConnectorSet's ordering: if the secret delete fails partway through,
// leaving the metadata in place means the connector still shows up as
// configured and the next credential.get correctly fails closed on it, rather
// than the secret quietly surviving in the store with nothing pointing at it.
func handleConnectorRemove(req Request, j *journal.Journal, store credential.Store, locks *targetLocks) Response {
	if err := validateTarget(req.Target); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}

	unlock := locks.Lock(req.Target)
	defer unlock()

	if store != nil {
		if err := store.Delete(req.Target); err != nil && !errors.Is(err, credential.ErrNotFound) {
			return Response{ID: req.ID, Error: fmt.Sprintf("removing secret: %v", err)}
		}
	}
	if err := j.DeleteConnector(req.Target); err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("removing connector metadata: %v", err)}
	}
	return Response{ID: req.ID}
}
