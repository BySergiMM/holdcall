package readmodel

import (
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/peer"
)

// Rule is one denial as a reader sees it. It and journal.Rule agree on
// scope -- an agent and a connector, both nilable meaning "every" -- and on
// effect, which is always deny because there is no other kind yet. See
// docs/decisions/0002-what-an-unknown-agent-may-do.md.
type Rule struct {
	Agent     *string `json:"agent,omitempty"`
	Connector *string `json:"connector,omitempty"`
	Tool      string  `json:"tool"`
	// Effect is deny or allow. Since M4.5 a rule can grant as well as
	// refuse, and a list that did not say which would be worse than no list.
	Effect    string `json:"effect"`
	CreatedAt string `json:"created_at"`
}

// RuleFrom projects one rule. Copies, like EventFrom: nothing here aliases
// the journal's own memory.
func RuleFrom(r journal.Rule) Rule {
	return Rule{
		Agent:     copyString(r.Agent),
		Connector: copyString(r.Connector),
		Tool:      r.Tool,
		Effect:    r.Effect,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// RulesFrom projects every rule, in the order ListRules already reads them
// in (oldest first, by id).
func RulesFrom(rows []journal.Rule) []Rule {
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, RuleFrom(r))
	}
	return out
}

// Agent is one enrolment as a reader sees it. ExecDev and ExecIno stay out:
// they are the kernel identity a decision matches on, and a console has no
// more reason to show them than to show a params_digest's preimage.
type Agent struct {
	Name       string `json:"name"`
	ExecPath   string `json:"exec_path"`
	EnrolledAt string `json:"enrolled_at"`

	// Current reports whether the file at ExecPath is still the one that was
	// enrolled, computed the same way `nim agent list` computes it (see
	// daemon.stillTheEnrolledFile) -- but through internal/peer directly
	// rather than the daemon package, so a read-only viewer with no socket
	// to the daemon can still say it.
	//
	// Three states, not two. nil means this platform cannot answer the
	// question at all (peer.FileIdentitySupported is false on Windows
	// today), which is not the same as having looked and found a mismatch:
	// reporting nil as false would tell an operator their enrolment had gone
	// stale when nothing was actually checked. false is not a security
	// finding either way -- a device number can change across a reboot and a
	// self-updating program becomes a different file. It means this
	// enrolment will match no running process and should be repeated.
	Current *bool `json:"current"`
}

// stillEnrolled reports whether the file now at a.ExecPath is still the one
// identified by a.ExecDev/a.ExecIno at enrolment time, or nil when this
// platform has no way to ask.
func stillEnrolled(a journal.Agent) *bool {
	if !peer.FileIdentitySupported {
		return nil
	}
	img, err := peer.ImageOfFile(a.ExecPath)
	if err != nil {
		current := false
		return &current
	}
	current := img.Equal(peer.NewImage(a.ExecDev, a.ExecIno))
	return &current
}

// AgentFrom projects one enrolment.
func AgentFrom(a journal.Agent) Agent {
	return Agent{
		Name:       a.Name,
		ExecPath:   a.ExecPath,
		EnrolledAt: a.EnrolledAt.UTC().Format(time.RFC3339Nano),
		Current:    stillEnrolled(a),
	}
}

// AgentsFrom projects every enrolment, in the order ListAgents already reads
// them in (by name).
func AgentsFrom(rows []journal.Agent) []Agent {
	out := make([]Agent, 0, len(rows))
	for _, a := range rows {
		out = append(out, AgentFrom(a))
	}
	return out
}

// Connector is non-secret connector metadata as a reader sees it: which env
// var name a target's credential is injected under, and the argv it may be
// injected into. Never the secret -- there is none in the journal to show,
// and this must never become the place one is added.
type Connector struct {
	Target    string   `json:"target"`
	EnvKey    string   `json:"env_key"`
	Command   []string `json:"command,omitempty"`
	UpdatedAt string   `json:"updated_at"`
}

// ConnectorFrom projects one connector. Command is copied, not aliased:
// journal.Connector.Command comes straight out of a driver-owned buffer for
// the row it just scanned, like every other slice this package puts on the
// wire.
func ConnectorFrom(c journal.Connector) Connector {
	return Connector{
		Target:    c.Target,
		EnvKey:    c.EnvKey,
		Command:   append([]string(nil), c.Command...),
		UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// ConnectorsFrom projects every connector, in the order ListConnectors
// already reads them in (by target).
func ConnectorsFrom(rows []journal.Connector) []Connector {
	out := make([]Connector, 0, len(rows))
	for _, c := range rows {
		out = append(out, ConnectorFrom(c))
	}
	return out
}

// Policy is the configuration view: what an agent is, what a connector may
// receive, and what is denied. Distinct from Snapshot, which is the record
// of what happened -- rules only ever deny, an enrolment is as privileged as
// running Nim, and a connector's command is the argv authorized to receive
// its credential, none of which is a fact about a call that was made.
type Policy struct {
	Rules      []Rule      `json:"rules"`
	Agents     []Agent     `json:"agents"`
	Connectors []Connector `json:"connectors"`
}

// TakePolicy reads rules, agents and connectors and projects them together,
// so a page that shows all three -- the console's Policy tab, `nim status`
// -- makes one call instead of three scattered across its caller.
func TakePolicy(src PolicySource) (Policy, error) {
	var p Policy

	rules, err := src.ListRules()
	if err != nil {
		return p, err
	}
	p.Rules = RulesFrom(rules)

	agents, err := src.ListAgents()
	if err != nil {
		return p, err
	}
	p.Agents = AgentsFrom(agents)

	connectors, err := src.ListConnectors()
	if err != nil {
		return p, err
	}
	p.Connectors = ConnectorsFrom(connectors)

	return p, nil
}
