package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/shim"
)

const policyUsage = "nim policy deny|remove <tool> [--agent <name>] [--connector <name>] | nim policy list"

func runPolicy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s", policyUsage)
	}
	switch args[0] {
	case "deny":
		return runPolicyChange(daemon.KindPolicyDeny, args[1:])
	case "remove":
		return runPolicyChange(daemon.KindPolicyRemove, args[1:])
	case "list":
		return runPolicyList(args[1:])
	default:
		return fmt.Errorf("unknown policy subcommand %q; usage: %s", args[0], policyUsage)
	}
}

// parsePolicyArgs reads one tool and its optional scope. Hand-rolled like
// parseConnectorSetArgs, for the same reason: the tool comes first and the
// standard flag package stops at the first positional it sees.
func parsePolicyArgs(args []string) (tool, agent, connector string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--agent" || a == "--connector":
			if i+1 >= len(args) {
				return "", "", "", fmt.Errorf("%s requires a name", a)
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
		RuleTool: tool, RuleAgent: agent, RuleConnector: connector,
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
	verb := "denied"
	if kind == daemon.KindPolicyRemove {
		verb = "no longer denied"
	}
	fmt.Printf("%s %s for %s on %s (recorded in the journal)\n", r.Tool, verb, scopeWord("agent", r.Agent), scopeWord("connector", r.Connector))
	return nil
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
	fmt.Fprintln(w, "TOOL\tAGENT\tCONNECTOR\tSINCE")
	for _, r := range resp.Rules {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Tool, orEvery(r.Agent), orEvery(r.Connector), shortTime(r.CreatedAt))
	}
	w.Flush()
	fmt.Println()
	fmt.Println("Rules only deny: a tool no rule names is allowed. A rule for an agent applies")
	fmt.Println("to sessions the daemon derived as that agent, and to no other -- including")
	fmt.Println("sessions it could not derive an agent for at all.")
	return nil
}

func orEvery(s string) string {
	if s == "" {
		return "(every)"
	}
	return s
}
