package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/shim"
)

const policyUsage = "nim policy deny|allow <tool> [--agent <name>] [--connector <name>] | " +
	"nim policy default deny|allow [--agent <name>] [--connector <name>] | " +
	"nim policy remove <tool>|--default [--agent <name>] [--connector <name>] | " +
	"nim policy list | nim policy explain <tool> [--agent <name>] [--connector <name>] | " +
	"nim policy budget <n> --tool <tool>|--all-tools [--agent <name>] [--connector <name>] | " +
	"nim policy budget remove --tool <tool>|--all-tools [--agent <name>] [--connector <name>]"

func runPolicy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s", policyUsage)
	}
	switch args[0] {
	case "deny":
		return runPolicyChange(daemon.KindPolicyDeny, args[1:])
	case "allow":
		return runPolicyChange(daemon.KindPolicyAllow, args[1:])
	case "default":
		return runPolicyDefault(args[1:])
	case "remove":
		return runPolicyRemove(args[1:])
	case "list":
		return runPolicyList(args[1:])
	case "explain":
		return runPolicyExplain(args[1:])
	case "budget":
		return runPolicyBudget(args[1:])
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

// runPolicyDefault handles `nim policy default deny|allow [--agent]
// [--connector]`. A default has no tool of its own -- RuleDefault carries
// that to the daemon, which is the one place "*" is ever written -- so this
// parses the effect word plus scope, not parsePolicyArgs's tool-plus-scope.
func runPolicyDefault(args []string) error {
	const usage = "usage: nim policy default deny|allow [--agent <name>] [--connector <name>]"
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	var kind string
	switch args[0] {
	case "deny":
		kind = daemon.KindPolicyDeny
	case "allow":
		kind = daemon.KindPolicyAllow
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

// removeVerb is the past-tense verb the confirmation line prints. deny and
// allow always print their own name; remove reports what the rule it deleted
// used to do, since a scope's rule could have been either.
func removeVerb(kind string, r daemon.RuleInfo) string {
	switch kind {
	case daemon.KindPolicyDeny:
		return "denied"
	case daemon.KindPolicyAllow:
		return "allowed"
	default: // daemon.KindPolicyRemove
		if r.Effect == journal.DecisionAllow {
			return "no longer allowed"
		}
		return "no longer denied"
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
	} else {
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
		fmt.Println("the connector beats not naming it; a tie in specificity goes to deny.")
		fmt.Println("nim policy explain <tool> shows which rule decides one particular call, and why.")
	}

	// Budgets are policy too -- the same connection may ask for them, since
	// a connection commits to one purpose ("policy") rather than one kind.
	budgetsResp, err := daemon.SendRequest(conn, daemon.Request{ID: config.NewID(), Kind: daemon.KindBudgetList})
	if err != nil {
		return err
	}
	if budgetsResp.Error != "" {
		return fmt.Errorf("%s", budgetsResp.Error)
	}
	fmt.Println()
	if len(budgetsResp.Budgets) == 0 {
		fmt.Println("(no budgets: no session has a call cap)")
		return nil
	}
	fmt.Println("BUDGETS")
	bw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(bw, "CALLS\tTOOL\tAGENT\tCONNECTOR\tSINCE")
	for _, b := range budgetsResp.Budgets {
		tool := b.Tool
		if tool == journal.BudgetToolAll {
			tool = "(every tool)"
		}
		fmt.Fprintf(bw, "%d\t%s\t%s\t%s\t%s\n", b.Calls, tool, orEvery(b.Agent), orEvery(b.Connector), shortTime(b.CreatedAt))
	}
	bw.Flush()
	fmt.Println()
	fmt.Println("Per session: session.start to session.end, decremented at authorization time.")
	fmt.Println("A budget never grants -- it only lowers what the rules above already allow.")
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
	// Kept simple, deliberately: this names the budgets that would apply and
	// their caps, never how much of one this call's session has used --
	// explain has no session to count against, only a scope.
	if len(e.Budgets) > 0 {
		fmt.Println()
		fmt.Println("Budgets that would apply to a call shaped like this:")
		for _, b := range e.Budgets {
			tool := b.Tool
			if tool == journal.BudgetToolAll {
				tool = "every tool"
			}
			fmt.Printf("  %d calls for %s, for %s on %s\n",
				b.Calls, tool, scopeWord("agent", b.Agent), scopeWord("connector", b.Connector))
		}
	}
	return nil
}

// runPolicyBudget handles `nim policy budget <n> --tool <tool>|--all-tools
// [--agent <name>] [--connector <name>]` and `nim policy budget remove
// --tool <tool>|--all-tools [--agent <name>] [--connector <name>]`.
func runPolicyBudget(args []string) error {
	if len(args) > 0 && args[0] == "remove" {
		return runPolicyBudgetChange(daemon.KindBudgetRemove, 0, args[1:])
	}
	const usage = "usage: nim policy budget <n> --tool <tool>|--all-tools " +
		"[--agent <name>] [--connector <name>]"
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("%q is not a whole number of calls; %s", args[0], usage)
	}
	return runPolicyBudgetChange(daemon.KindBudgetSet, n, args[1:])
}

// parseBudgetScopeArgs reads --tool/--all-tools plus the --agent/--connector
// scope flags shared with rules, for both nim policy budget <n> and nim
// policy budget remove.
func parseBudgetScopeArgs(args []string) (tool string, allTools bool, agent, connector string, err error) {
	agent, connector, rest, err := parseScopeFlags(args)
	if err != nil {
		return "", false, "", "", err
	}
	const usage = "usage: nim policy budget <n>|remove --tool <tool>|--all-tools " +
		"[--agent <name>] [--connector <name>]"
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--all-tools":
			allTools = true
		case a == "--tool":
			if i+1 >= len(rest) {
				return "", false, "", "", fmt.Errorf("--tool requires a name")
			}
			i++
			tool = rest[i]
		case strings.HasPrefix(a, "--tool="):
			tool = strings.TrimPrefix(a, "--tool=")
		default:
			return "", false, "", "", fmt.Errorf("unexpected argument %q; %s", a, usage)
		}
	}
	switch {
	case allTools && tool != "":
		return "", false, "", "", fmt.Errorf("--tool and --all-tools are two ways to name the same scope; use one")
	case !allTools && tool == "":
		return "", false, "", "", fmt.Errorf("a budget needs --tool <tool> or --all-tools; %s", usage)
	}
	return tool, allTools, agent, connector, nil
}

// runPolicyBudgetChange sends one budget.set or budget.remove request and
// reports what the daemon did -- sendPolicyChange's counterpart for
// budgets, which carry a cap rather than an effect.
func runPolicyBudgetChange(kind string, n int, args []string) error {
	tool, allTools, agent, connector, err := parseBudgetScopeArgs(args)
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
		ID: config.NewID(), Kind: kind,
		BudgetTool: tool, BudgetAllTools: allTools,
		BudgetAgent: agent, BudgetConnector: connector, BudgetCalls: n,
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	if len(resp.Budgets) != 1 {
		return fmt.Errorf("the daemon answered with %d budgets, want 1", len(resp.Budgets))
	}
	b := resp.Budgets[0]
	name := b.Tool
	if name == journal.BudgetToolAll {
		name = "every tool"
	}
	verb := "set"
	if kind == daemon.KindBudgetRemove {
		verb = "removed"
	}
	fmt.Printf("budget of %d calls for %s, for %s on %s, %s (recorded in the journal)\n",
		b.Calls, name, scopeWord("agent", b.Agent), scopeWord("connector", b.Connector), verb)
	return nil
}
