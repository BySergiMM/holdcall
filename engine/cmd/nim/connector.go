package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/shim"
)

const connectorSetUsage = "nim connector set <target> --env KEY -- <command> [args...]"

func runConnector(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: nim connector <set|list|remove> ...")
	}
	switch args[0] {
	case "set":
		return runConnectorSet(args[1:])
	case "list":
		return runConnectorList(args[1:])
	case "remove":
		return runConnectorRemove(args[1:])
	default:
		return fmt.Errorf("unknown connector subcommand %q", args[0])
	}
}

// parseConnectorSetArgs is split out so its parsing can be tested without a
// daemon. It does not use flag.FlagSet: the specified invocation puts the
// target before the flag, and the standard flag package stops parsing flags at
// the first positional argument it sees -- it would silently treat --env as a
// second positional and never populate it.
//
// --env takes only the variable name, never a value: the secret itself never
// becomes a command-line argument of this process (visible via ps, and left in
// shell history for as long as that file exists). See readSecretFromStdin.
//
// Everything after -- is the downstream server this credential may be injected
// into. It is required: a stored secret with no statement about what may
// receive it is what let any caller name a target alongside a command of its
// own and be handed the secret. The same -- convention as nim serve, so a
// command with its own flags needs no quoting or escaping.
func parseConnectorSetArgs(args []string) (target, key string, command []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			command = args[i+1:]
			i = len(args)
		case a == "--env":
			if i+1 >= len(args) {
				return "", "", nil, fmt.Errorf("--env requires a KEY argument")
			}
			i++
			key = args[i]
		case strings.HasPrefix(a, "--env="):
			key = strings.TrimPrefix(a, "--env=")
		case target == "" && !strings.HasPrefix(a, "-"):
			target = a
		default:
			return "", "", nil, fmt.Errorf("unexpected argument %q; usage: %s", a, connectorSetUsage)
		}
	}
	if target == "" {
		return "", "", nil, fmt.Errorf("usage: %s", connectorSetUsage)
	}
	if key == "" {
		return "", "", nil, fmt.Errorf("--env is required, e.g. --env GITHUB_TOKEN")
	}
	if strings.Contains(key, "=") {
		return "", "", nil, fmt.Errorf(
			"--env takes just the variable name (e.g. --env GITHUB_TOKEN), not KEY=value -- " +
				"provide the secret on stdin instead")
	}
	if len(command) == 0 {
		return "", "", nil, fmt.Errorf(
			"give the server this credential belongs to after --, so Nim knows what it may be "+
				"injected into.\nusage: %s", connectorSetUsage)
	}
	return target, key, command, nil
}

func runConnectorSet(args []string) error {
	target, key, command, err := parseConnectorSetArgs(args)
	if err != nil {
		return err
	}
	secret, err := readSecretFromStdin()
	if err != nil {
		return err
	}

	conn, err := dialConnectorDaemon()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{
		ID: config.NewID(), Kind: daemon.KindConnectorSet,
		Target: target, EnvKey: key, Secret: secret, Command: command,
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Printf("connector %q set (injects %s into %s)\n", target, key, strings.Join(command, " "))
	return nil
}

// readSecretFromStdin never puts the secret in argv. On a terminal it prompts
// and reads without echoing; when stdin is piped it reads one line, which
// makes `echo "$TOKEN" | nim connector set ...` fully scriptable.
func readSecretFromStdin() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Secret: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("reading secret: %w", err)
		}
		if len(b) == 0 {
			return "", fmt.Errorf("no secret entered")
		}
		return string(b), nil
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 4096), daemon.MaxSecretLen)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("reading secret from stdin: %w", err)
		}
		return "", fmt.Errorf("no secret provided on stdin")
	}
	secret := scanner.Text()
	if secret == "" {
		return "", fmt.Errorf("no secret provided on stdin")
	}
	return secret, nil
}

func runConnectorList(args []string) error {
	fs := flag.NewFlagSet("connector list", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	conn, err := dialConnectorDaemon()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{ID: config.NewID(), Kind: daemon.KindConnectorList})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	if len(resp.Connectors) == 0 {
		fmt.Println("(no connectors configured)")
		return nil
	}
	for _, c := range resp.Connectors {
		// A connector with no command cannot be used at all -- the daemon
		// refuses to release its secret -- so saying so here is the only place
		// a user finds out before an agent hits it at spawn time.
		command := strings.Join(c.Command, " ")
		if command == "" {
			command = "(no authorized command: register it again)"
		}
		fmt.Printf("%-20s %-20s %-24s %s\n", c.Target, c.EnvKey, c.UpdatedAt, command)
	}
	return nil
}

func runConnectorRemove(args []string) error {
	fs := flag.NewFlagSet("connector remove", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	target := fs.Arg(0)
	if target == "" {
		return fmt.Errorf("usage: nim connector remove <target>")
	}

	conn, err := dialConnectorDaemon()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{
		ID: config.NewID(), Kind: daemon.KindConnectorRemove, Target: target})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Printf("connector %q removed\n", target)
	return nil
}

// dialConnectorDaemon connects for a one-shot request/response, starting the
// daemon if it is not already running. Unlike the relay, a management command
// has nothing sensible to fail open to: if the daemon truly cannot be reached
// it must say so rather than silently pretend to have done nothing.
//
// Through shim.DialDaemon, which verifies that what answered is Nim. This
// used to dial the path and trust it, so `nim connector set` handed the
// plaintext secret to whatever had bound the socket first -- the impostor the
// relay had learnt to refuse, still welcome on the one path that carries a
// credential in the clear.
func dialConnectorDaemon() (net.Conn, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return shim.DialDaemon(cfg)
}
