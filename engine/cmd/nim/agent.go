package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
)

const agentAddUsage = "nim agent add <name> <path-to-executable>"

func runAgent(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: nim agent <add|list|remove> ...")
	}
	switch args[0] {
	case "add":
		return runAgentAdd(args[1:])
	case "list":
		return runAgentList(args[1:])
	case "remove":
		return runAgentRemove(args[1:])
	default:
		return fmt.Errorf("unknown agent subcommand %q", args[0])
	}
}

// parseAgentAddArgs is split out so it can be tested without a daemon.
//
// Two positionals rather than a flag and a `--`: an agent is identified by one
// executable file, not by a command line, and borrowing nim serve's `--`
// convention would suggest arguments that are never used.
//
// The path is made absolute here, against the directory the operator is
// standing in, because that is the only place where a relative path means what
// they think it means. The daemon runs somewhere else and refuses anything
// relative.
func parseAgentAddArgs(args []string, abs func(string) (string, error)) (name, path string, err error) {
	var positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return "", "", fmt.Errorf("unexpected flag %q; usage: %s", a, agentAddUsage)
		}
		positional = append(positional, a)
	}
	if len(positional) != 2 {
		return "", "", fmt.Errorf("usage: %s", agentAddUsage)
	}
	name = positional[0]
	path, err = abs(positional[1])
	if err != nil {
		return "", "", fmt.Errorf("resolving %s: %w", positional[1], err)
	}
	return name, path, nil
}

func runAgentAdd(args []string) error {
	name, path, err := parseAgentAddArgs(args, filepath.Abs)
	if err != nil {
		return err
	}

	conn, err := dialConnectorDaemon()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{
		ID: config.NewID(), Kind: daemon.KindAgentAdd,
		AgentName: name, AgentPath: path,
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Printf("agent %q enrolled (%s)\n", name, path)
	fmt.Println("Nothing is decided by this yet: enrolment records which program an agent is,")
	fmt.Println("and grants are a later milestone.")
	return nil
}

func runAgentList(args []string) error {
	fs := flag.NewFlagSet("agent list", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	conn, err := dialConnectorDaemon()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{ID: config.NewID(), Kind: daemon.KindAgentList})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	if len(resp.Agents) == 0 {
		fmt.Println("(no agents enrolled)")
		return nil
	}
	stale := 0
	for _, a := range resp.Agents {
		state := "ok"
		if !a.Current {
			state = "STALE"
			stale++
		}
		fmt.Printf("%-20s %-6s %-24s %s\n", a.Name, state, a.EnrolledAt, a.ExecPath)
	}
	if stale > 0 {
		fmt.Println()
		fmt.Printf("%d enrolment(s) marked STALE: the file at that path is no longer the one\n", stale)
		fmt.Println("that was enrolled, so nothing will match them. That is not a sign of tampering --")
		fmt.Println("an application that updates itself becomes a different file, and a device number")
		fmt.Println("can change across a reboot. Run `nim agent add` again with the same name to repair.")
	}
	return nil
}

func runAgentRemove(args []string) error {
	fs := flag.NewFlagSet("agent remove", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := fs.Arg(0)
	if name == "" {
		return fmt.Errorf("usage: nim agent remove <name>")
	}

	conn, err := dialConnectorDaemon()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{
		ID: config.NewID(), Kind: daemon.KindAgentRemove, AgentName: name})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Printf("agent %q removed\n", name)
	return nil
}
