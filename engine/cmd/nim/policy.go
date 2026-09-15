package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/shim"
)

const policyUsage = "nim policy deny|allow|ask <tool> [--agent <name>] [--connector <name>] | " +
	"nim policy default deny|allow|ask [--agent <name>] [--connector <name>] | " +
	"nim policy remove <tool>|--default [--agent <name>] [--connector <name>] | " +
	"nim policy list | nim policy explain <tool> [--agent <name>] [--connector <name>]"

func runPolicy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s", policyUsage)
	}
	switch args[0] {
	case "deny":
		return runPolicyChange(daemon.KindPolicyDeny, args[1:])
	case "allow":
		return runPolicyChange(daemon.KindPolicyAllow, args[1:])
	case "ask":
		return runPolicyChange(daemon.KindPolicyAsk, args[1:])
	case "default":
		return runPolicyDefault(args[1:])
	case "remove":
		return runPolicyRemove(args[1:])
	case "list":
		return runPolicyList(args[1:])
	case "explain":
		return runPolicyExplain(args[1:])
	default:
		return fmt.Errorf("unknown policy subcommand %q; usage: %s", args[0], policyUsage)
	}
}

// parseScopeFlags reads --agent and --connector, in either "--flag value" or
// "--flag=value" form, and returns every other token as rest, in the order it
// arrived. Shared by every policy subcommand that takes a scope, so the two
// forms are recognised the same way everywhere rather than reimplemented per
// command.
func parseScopeFlags(args []string) (agent, connector string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--agent" || a == "--connector":
			if i+1 >= len(args) {
				return "", "", nil, fmt.Errorf("%s requires a name", a)
			}
			i++
			if a == "--agent" {
				agent = args[i]
			} else {
				connector = args[i]
			}
		case strings.HasPrefix(a, "--agent="):
			agent = strings.TrimPrefix(a, "--agent=")
		case strings.HasPrefix(a, "--connector="):
			connector = strings.TrimPrefix(a, "--connector=")
		default:
			rest = append(rest, a)
		}
	}
	return agent, connector, rest, nil
}

// parsePolicyArgs reads one tool and its optional scope, for deny, allow and
// explain. Hand-rolled like parseConnectorSetArgs, for the same reason: the
// tool comes first and the standard flag package stops at the first
// positional it sees.
func parsePolicyArgs(args []string) (tool, agent, connector string, err error) {
	agent, connector, rest, err := parseScopeFlags(args)
	if err != nil {
		return "", "", "", err
	}
	for _, a := range rest {
		switch {
		case strings.HasPrefix(a, "-"):
			return "", "", "", fmt.Errorf("unexpected flag %q; usage: %s", a, policyUsage)
		case tool == "":
			tool = a
		default:
			return "", "", "", fmt.Errorf("one tool per rule; %q was a second one. usage: %s", a, policyUsage)
		}
	}
	if tool == "" {
		return "", "", "", fmt.Errorf("usage: %s", policyUsage)
	}
	return tool, agent, connector, nil
}

func runPolicyChange(kind string, args []string) error {
	tool, agent, connector, err := parsePolicyArgs(args)
	if err != nil {
		return err
	}
	return sendPolicyChange(kind, tool, agent, connector, false)
}

// runPolicyDefault handles `nim policy default deny|allow|ask [--agent]
// [--connector]`. A default has no tool of its own -- RuleDefault carries
// that to the daemon, which is the one place "*" is ever written -- so this
// parses the effect word plus scope, not parsePolicyArgs's tool-plus-scope.
func runPolicyDefault(args []string) error {
	const usage = "usage: nim policy default deny|allow|ask [--agent <name>] [--connector <name>]"
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	var kind string
	switch args[0] {
	case "deny":
		kind = daemon.KindPolicyDeny
	case "allow":
		kind = daemon.KindPolicyAllow
	case "ask":
		kind = daemon.KindPolicyAsk
	default:
		return fmt.Errorf("unknown default effect %q; %s", args[0], usage)
	}
	agent, connector, rest, err := parseScopeFlags(args[1:])
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("%s", usage)
	}
	return sendPolicyChange(kind, "", agent, connector, true)
}

// runPolicyRemove handles both nim policy remove <tool> ... and nim policy
// remove --default ...: exactly one of a tool name or --default is required,
// because a scope holds one rule and this is what names it.
func runPolicyRemove(args []string) error {
	const usage = "usage: nim policy remove <tool>|--default [--agent <name>] [--connector <name>]"
	agent, connector, rest, err := parseScopeFlags(args)
	if err != nil {
		return err
	}
	isDefault := false
	var tools []string
	for _, a := range rest {
		switch {
		case a == "--default":
			isDefault = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unexpected flag %q; %s", a, usage)
		default:
			tools = append(tools, a)
		}
	}
	switch {
	case isDefault && len(tools) > 0:
		return fmt.Errorf("--default and a tool name are two ways to name the same rule; use one. %s", usage)
	case !isDefault && len(tools) == 0:
		return fmt.Errorf("%s", usage)
	case !isDefault && len(tools) > 1:
		return fmt.Errorf("one tool per rule; %q was a second one. %s", tools[1], usage)
	}
	tool := ""
	if len(tools) == 1 {
		tool = tools[0]
	}
	return sendPolicyChange(daemon.KindPolicyRemove, tool, agent, connector, isDefault)
}

// sendPolicyChange sends one policy.deny, policy.allow or policy.remove
// request and reports what the daemon did. isDefault sets RuleDefault, in
// which case tool must be "" -- the daemon is what turns that into
// journal.RuleToolDefault, so this command line is the only place a rule's
// tool may ever become "*", and it never types the character itself.
func sendPolicyChange(kind, tool, agent, connector string, isDefault bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	conn, err := shim.DialDaemon(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{
		ID: config.NewID(), Kind: kind,
		RuleTool: tool, RuleAgent: agent, RuleConnector: connector, RuleDefault: isDefault,
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	if len(resp.Rules) != 1 {
		return fmt.Errorf("the daemon answered with %d rules, want 1", len(resp.Rules))
	}
	r := resp.Rules[0]
	name := r.Tool
	if name == journal.RuleToolDefault {
		name = "every tool (the default)"
	}
	fmt.Printf("%s %s for %s on %s (recorded in the journal)\n",
		name, removeVerb(kind, r), scopeWord("agent", r.Agent), scopeWord("connector", r.Connector))
	return nil
}

// removeVerb is the past-tense verb the confirmation line prints. deny,
// allow and ask always print their own name; remove reports what the rule it
// deleted used to do, since a scope's rule could have been any of the three.
func removeVerb(kind string, r daemon.RuleInfo) string {
	switch kind {
	case daemon.KindPolicyDeny:
		return "denied"
	case daemon.KindPolicyAllow:
		return "allowed"
	case daemon.KindPolicyAsk:
		return "set to ask"
	default: // daemon.KindPolicyRemove
		switch r.Effect {
		case journal.DecisionAllow:
			return "no longer allowed"
		case journal.DecisionAsk:
			return "no longer set to ask"
		default: // journal.DecisionDeny
			return "no longer denied"
		}
	}
}

func scopeWord(what, name string) string {
	if name == "" {
		return "every " + what
	}
	return what + " " + name
}

func runPolicyList(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: nim policy list")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	conn, err := shim.DialDaemon(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{ID: config.NewID(), Kind: daemon.KindPolicyList})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	if len(resp.Rules) == 0 {
		fmt.Println("(no rules: every tool is allowed for every agent on every connector)")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "EFFECT\tTOOL\tAGENT\tCONNECTOR\tSINCE")
	for _, r := range resp.Rules {
		tool := r.Tool
		if tool == journal.RuleToolDefault {
			tool = "(default)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Effect, tool, orEvery(r.Agent), orEvery(r.Connector), shortTime(r.CreatedAt))
	}
	w.Flush()
	fmt.Println()
	fmt.Println("No rule matching a call is allow -- the M4 baseline. Among rules that do match,")
	fmt.Println("the most specific wins: an exact tool beats a default, and naming the agent or")
	fmt.Println("the connector beats not naming it; a tie in specificity goes to deny, then ask,")
	fmt.Println("then allow.")
	fmt.Println("nim policy explain <tool> shows which rule decides one particular call, and why.")
	return nil
}

func orEvery(s string) string {
	if s == "" {
		return "(every)"
	}
	return s
}

// runPolicyExplain asks the daemon which rule would decide a call for this
// tool, from this agent, on this connector, and why -- through
// policy.explain, never by reading nim_rules itself.
func runPolicyExplain(args []string) error {
	tool, agent, connector, err := parsePolicyArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	conn, err := shim.DialDaemon(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{
		ID: config.NewID(), Kind: daemon.KindPolicyExplain,
		RuleTool: tool, RuleAgent: agent, RuleConnector: connector,
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	e := resp.Explain
	if e == nil {
		return fmt.Errorf("the daemon answered policy.explain with no explanation")
	}
	fmt.Printf("%s for %s on %s: %s\n", e.Tool, scopeWord("agent", e.Agent), scopeWord("connector", e.Connector), strings.ToUpper(e.Decision))
	fmt.Println(e.Reason)
	if e.Candidates > 1 {
		fmt.Printf("(%d rules matched this scope; the one above is the most specific)\n", e.Candidates)
	}
	return nil
}
