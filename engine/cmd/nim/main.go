// Command nim is the local engine: a relay a client spawns, and a daemon that
// keeps the record.
//
//	nim serve --target github -- npx -y @modelcontextprotocol/server-github
//	nim daemon
//	nim status
//	echo "$GITHUB_TOKEN" | nim connector set github --env GITHUB_TOKEN
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/shim"
	"github.com/BySergiMM/nim/engine/internal/uuid"
)

var version = "0.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "daemon":
		err = runDaemon()
	case "status":
		err = runStatus()
	case "connector":
		err = runConnector(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("nim", version)
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nim:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `nim - authorization for MCP tool calls

  nim serve --target <name> [--client <name>] -- <command> [args...]
        Relay one MCP server. This is what your client spawns.

  nim daemon
        Run the shared daemon. Started automatically when needed.

  nim status
        Where state lives and how much has been recorded.

  nim connector set <target> --env KEY
        Store a credential for a downstream server, in the OS credential
        store (Keychain / Credential Manager / Secret Service). Never in
        SQLite, never in the client's own configuration. The secret itself
        is never a command-line argument: it is read from stdin (pipeable
        from a script) or, in an interactive terminal, prompted for without
        echoing.

            echo "$GITHUB_TOKEN" | nim connector set github --env GITHUB_TOKEN
            nim connector set github --env GITHUB_TOKEN   # interactive prompt

  nim connector list
        Configured connectors and the env var name each injects. Never the
        secret value.

  nim connector remove <target>
        Forget a connector's credential.

`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	target := fs.String("target", "", "name of the downstream MCP server (required)")
	client := fs.String("client", "", "label for the MCP client, if known")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target == "" {
		return fmt.Errorf("--target is required")
	}
	command := fs.Args()
	if len(command) == 0 {
		return fmt.Errorf("give the downstream server after --, e.g. -- npx -y some-mcp-server")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return shim.Run(shim.Options{
		Target:  *target,
		Client:  *client,
		Command: command,
		Config:  cfg,
	})
}

func runDaemon() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return daemon.Run(cfg)
}

func runStatus() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Println("home    ", config.Home())
	fmt.Println("config  ", config.Path())
	fmt.Println("socket  ", cfg.Daemon.Socket)
	fmt.Println("journal ", cfg.DatabasePath())

	if err := cfg.Validate(); err != nil {
		fmt.Println("config   INVALID:", err)
	}

	// Whether the daemon answers is the question that matters: a relay with no
	// daemon behind it records nothing while looking perfectly healthy.
	if conn, err := net.DialTimeout("unix", cfg.Daemon.Socket, 500*time.Millisecond); err == nil {
		conn.Close()
		fmt.Println("daemon   running")
	} else {
		fmt.Println("daemon   not running")
		if _, statErr := os.Stat(config.LogPath()); statErr == nil {
			fmt.Println("         see", config.LogPath())
		}
	}

	if _, err := os.Stat(cfg.DatabasePath()); os.IsNotExist(err) {
		fmt.Println("calls    (no journal yet)")
		return nil
	}
	j, err := journal.Open(cfg.DatabasePath())
	if err != nil {
		return err
	}
	defer j.Close()
	n, err := j.CountCalls()
	if err != nil {
		return err
	}
	fmt.Println("calls   ", n)
	return nil
}

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

// parseConnectorSetArgs is split out from runConnectorSet so its parsing can
// be tested without a daemon. It does not use flag.FlagSet: the specified
// invocation is "nim connector set <target> --env KEY", target before the
// flag, and the standard flag package stops parsing flags at the first
// positional argument it sees -- it would silently treat --env as a second
// positional and never populate it.
//
// --env takes only the variable name, never a value: the secret itself
// never becomes a command-line argument of this process (visible via ps and
// left in shell history for as long as that history file exists), which is
// exactly what the old "--env KEY=value" form did. See readSecretFromStdin.
func parseConnectorSetArgs(args []string) (target, key string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--env":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--env requires a KEY argument")
			}
			i++
			key = args[i]
		case strings.HasPrefix(a, "--env="):
			key = strings.TrimPrefix(a, "--env=")
		case target == "" && !strings.HasPrefix(a, "-"):
			target = a
		default:
			return "", "", fmt.Errorf("unexpected argument %q; usage: nim connector set <target> --env KEY", a)
		}
	}
	if target == "" {
		return "", "", fmt.Errorf("usage: nim connector set <target> --env KEY")
	}
	if key == "" {
		return "", "", fmt.Errorf("--env is required, e.g. --env GITHUB_TOKEN")
	}
	if strings.Contains(key, "=") {
		return "", "", fmt.Errorf("--env takes just the variable name (e.g. --env GITHUB_TOKEN), not KEY=value -- provide the secret on stdin instead")
	}
	return target, key, nil
}

func runConnectorSet(args []string) error {
	target, key, err := parseConnectorSetArgs(args)
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
		ID: uuid.New(), Kind: daemon.KindConnectorSet,
		Target: target, EnvKey: key, Secret: secret,
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Printf("connector %q set (injects %s)\n", target, key)
	return nil
}

// readSecretFromStdin never puts the secret in argv. When stdin is a
// terminal, it prompts and reads without echoing (like a password prompt);
// when it is piped (a script, or a value substitution), it reads one line,
// which makes "echo "$TOKEN" | nim connector set target --env KEY" and
// similar fully scriptable.
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

	resp, err := daemon.SendRequest(conn, daemon.Request{ID: uuid.New(), Kind: daemon.KindConnectorList})
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
		fmt.Printf("%-20s %-20s %s\n", c.Target, c.EnvKey, c.UpdatedAt)
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

	resp, err := daemon.SendRequest(conn, daemon.Request{ID: uuid.New(), Kind: daemon.KindConnectorRemove, Target: target})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Printf("connector %q removed\n", target)
	return nil
}

// dialConnectorDaemon connects to the daemon for a one-shot request/response,
// starting it if it is not already running. Unlike the shim's dialDaemon,
// a connector command is not part of any relay session and has nothing
// sensible to fail open to: if the daemon truly cannot be reached, the
// command must say so rather than silently pretend to have done nothing.
func dialConnectorDaemon() (net.Conn, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", cfg.Daemon.Socket, 300*time.Millisecond)
	if err == nil {
		return conn, nil
	}
	if !shim.StartDaemon() {
		return nil, fmt.Errorf("could not start the daemon")
	}
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		conn, err = net.DialTimeout("unix", cfg.Daemon.Socket, 300*time.Millisecond)
		if err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("daemon did not become reachable at %s", cfg.Daemon.Socket)
}
