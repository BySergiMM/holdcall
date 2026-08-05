// Command nim is the local engine: a relay a client spawns, and a daemon that
// keeps the record.
//
//	nim serve --target github -- npx -y @modelcontextprotocol/server-github
//	nim daemon
//	nim status
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/shim"
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
