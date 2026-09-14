// Package clientconfig rewrites MCP client configuration files so every
// stdio server they spawn goes through Nim instead.
//
// It is the one place that knows where each client keeps its configuration
// and what an entry inside it looks like. `nim init` uses it to change those
// files, and `nim doctor` uses the same read path to report on them, so the
// two commands can never disagree about what "through Nim" means.
package clientconfig

import (
	"fmt"
	"os"
	"path/filepath"
)

// Client identifiers, used both to select which files Discover looks at and
// as the --client label nim init writes into a rewritten entry's args.
const (
	ClaudeCode    = "claude-code"
	Cursor        = "cursor"
	ClaudeDesktop = "claude-desktop"
)

// AllClients is every client Nim knows how to configure, in the order they
// are checked and reported.
var AllClients = []string{ClaudeCode, Cursor, ClaudeDesktop}

// FileKind selects how mcpServers objects are located inside one file. Most
// clients have exactly one, at the top level; Claude Code also nests one
// under each entry of "projects".
type FileKind int

const (
	KindFlat       FileKind = iota // a top-level "mcpServers" object
	KindClaudeCode                 // top-level "mcpServers" plus "projects.<path>.mcpServers"
)

// File is one JSON file nim init or nim doctor looks at.
type File struct {
	Client string
	Path   string
	Kind   FileKind
}

// Discover lists the files nim init would look at.
//
// clientFilter, when non-empty, restricts this to one client. configOverride
// replaces that one client's primary file and requires clientFilter to be
// set: with more than one client in play, a single override path would not
// say which of them it replaces. This is what lets tests exercise the whole
// command against a fixture file without a real home directory.
//
// cwd is used to find Claude Code's project-local .mcp.json, which lives
// next to the project rather than under the client's own directory; it is
// taken as a parameter rather than read with os.Getwd here so a caller can
// fix it once for a whole run.
func Discover(clientFilter, configOverride, cwd string) ([]File, error) {
	if configOverride != "" && clientFilter == "" {
		return nil, fmt.Errorf("--config only makes sense together with --client, naming the one client it overrides")
	}

	clients := AllClients
	if clientFilter != "" {
		if !contains(AllClients, clientFilter) {
			return nil, fmt.Errorf("unknown client %q; want one of %v", clientFilter, AllClients)
		}
		clients = []string{clientFilter}
	}

	var files []File
	for _, c := range clients {
		override := ""
		if c == clientFilter {
			override = configOverride
		}
		primary, extra, err := clientFiles(c, override, cwd)
		if err != nil {
			return nil, err
		}
		files = append(files, primary)
		files = append(files, extra...)
	}
	return files, nil
}

// clientFiles resolves one client's primary configuration file, plus any
// extra files it also reads (Claude Code's project-local .mcp.json).
func clientFiles(client, override, cwd string) (File, []File, error) {
	switch client {
	case ClaudeCode:
		path := override
		if path == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return File{}, nil, fmt.Errorf("finding the home directory: %w", err)
			}
			path = filepath.Join(home, ".claude.json")
		}
		primary := File{Client: client, Path: path, Kind: KindClaudeCode}

		// The project-local file is only listed when it exists: unlike the
		// three primary files, its absence says nothing about whether Claude
		// Code is even installed, so it is not worth a "not found" line of
		// its own.
		var extra []File
		if cwd != "" {
			local := filepath.Join(cwd, ".mcp.json")
			if _, err := os.Stat(local); err == nil {
				extra = append(extra, File{Client: client, Path: local, Kind: KindFlat})
			}
		}
		return primary, extra, nil

	case Cursor:
		path := override
		if path == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return File{}, nil, fmt.Errorf("finding the home directory: %w", err)
			}
			path = filepath.Join(home, ".cursor", "mcp.json")
		}
		return File{Client: client, Path: path, Kind: KindFlat}, nil, nil

	case ClaudeDesktop:
		path := override
		if path == "" {
			// os.UserConfigDir resolves to exactly the three paths this
			// client needs, per platform: ~/Library/Application Support on
			// darwin, %AppData% on windows, and $XDG_CONFIG_HOME (or
			// ~/.config) elsewhere -- so no runtime.GOOS switch is needed
			// here.
			dir, err := os.UserConfigDir()
			if err != nil {
				return File{}, nil, fmt.Errorf("finding the config directory: %w", err)
			}
			path = filepath.Join(dir, "Claude", "claude_desktop_config.json")
		}
		return File{Client: client, Path: path, Kind: KindFlat}, nil, nil

	default:
		return File{}, nil, fmt.Errorf("unknown client %q", client)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
