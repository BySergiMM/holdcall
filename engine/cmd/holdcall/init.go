package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BySergiMM/holdcall/engine/internal/clientconfig"
)

// resolveNimPath is what holdcall init writes into a rewritten entry's "command",
// and what it compares an already-wrapped entry's command against.
//
// It resolves through symlinks so that a client invoking a symlinked holdcall
// (e.g. one on PATH pointing at an install elsewhere) and one invoking the
// real path are recognised as the same binary, rather than holdcall init offering
// to "repoint" an entry that was already correct.
func resolveNimPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		return resolved, nil
	}
	// A broken symlink or an unusual filesystem: fall back to the
	// unresolved path rather than failing holdcall init outright over something
	// that does not stop the binary from actually running.
	return self, nil
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configOverride := fs.String("config", "", "override the discovered config file path (needs --client; mainly for tests)")
	clientFilter := fs.String("client", "", "only touch this client: claude-code, cursor, or claude-desktop")
	write := fs.Bool("write", false, "apply the changes; without this, holdcall init only prints what it would do")
	repoint := fs.Bool("repoint", false, "repoint entries already wrapped by a holdcall binary at a different path")
	undo := fs.String("undo", "", "restore a backup written by an earlier holdcall init --write, and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *undo != "" {
		restored, err := clientconfig.RestoreBackup(*undo)
		if err != nil {
			return err
		}
		fmt.Printf("restored %s from %s\n", restored, *undo)
		return nil
	}

	nimPath, err := resolveNimPath()
	if err != nil {
		return fmt.Errorf("resolving the running holdcall binary: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	files, err := clientconfig.Discover(*clientFilter, *configOverride, cwd)
	if err != nil {
		return err
	}

	var pending []clientconfig.Result
	for _, f := range files {
		res, err := clientconfig.BuildResult(f, nimPath, f.Client, *repoint)
		if err != nil {
			fmt.Printf("%s (%s): %v\n\n", f.Path, f.Client, err)
			continue
		}
		printResult(os.Stdout, res)
		if res.Found && res.Changed {
			pending = append(pending, res)
		}
	}

	if *write {
		for _, res := range pending {
			backup, err := clientconfig.Apply(res)
			if err != nil {
				return err
			}
			fmt.Printf("wrote %s (backup: %s)\n", res.File.Path, backup)
		}
	} else if len(pending) > 0 {
		fmt.Println("Dry run: nothing was written. Re-run with --write to apply.")
	}

	fmt.Println()
	printManualInstructions(os.Stdout)
	return nil
}

// printResult renders one file's classification and, for anything that would
// change, a before/after block -- unified-diff-like without the full
// machinery of an actual diff algorithm, which nothing here needs: an
// mcpServers entry is small, and showing the whole thing before and after is
// clearer than a line-by-line patch would be for something this size.
func printResult(w io.Writer, res clientconfig.Result) {
	fmt.Fprintf(w, "%s (%s)\n", res.File.Path, res.File.Client)
	if !res.Found {
		fmt.Fprintln(w, "  not found")
		fmt.Fprintln(w)
		return
	}

	any := false
	for _, g := range res.Groups {
		if len(g.Entries) == 0 {
			continue
		}
		any = true
		label := g.Label
		if label == "" {
			label = "mcpServers"
		}
		fmt.Fprintf(w, "  %s:\n", label)
		for _, e := range g.Entries {
			printEntry(w, e)
		}
	}
	if !any {
		fmt.Fprintln(w, "  no mcpServers found")
	}
	fmt.Fprintln(w)
}

func printEntry(w io.Writer, e clientconfig.EntryPlan) {
	switch e.Status {
	case clientconfig.StatusWrapped:
		fmt.Fprintf(w, "    %s: route through Holdcall\n", e.Key)
		printDiff(w, e.Before, e.After)
	case clientconfig.StatusRepointed:
		fmt.Fprintf(w, "    %s: %s\n", e.Key, e.Detail)
		printDiff(w, e.Before, e.After)
	case clientconfig.StatusAlreadyNim:
		fmt.Fprintf(w, "    %s: already through Holdcall\n", e.Key)
	case clientconfig.StatusStalePath:
		fmt.Fprintf(w, "    %s: %s\n", e.Key, e.Detail)
	case clientconfig.StatusHTTPSkipped:
		fmt.Fprintf(w, "    %s: HTTP/SSE server, left alone\n", e.Key)
	case clientconfig.StatusInvalidName, clientconfig.StatusUnrecognized:
		fmt.Fprintf(w, "    %s: %s\n", e.Key, e.Detail)
	}
}

func printDiff(w io.Writer, before, after string) {
	for _, line := range strings.Split(strings.TrimRight(before, "\n"), "\n") {
		fmt.Fprintf(w, "      - %s\n", line)
	}
	for _, line := range strings.Split(strings.TrimRight(after, "\n"), "\n") {
		fmt.Fprintf(w, "      + %s\n", line)
	}
}

// printManualInstructions is shown every run, dry or not: an operator on a
// client holdcall init does not know about has no other way to find out how to do
// this by hand.
func printManualInstructions(w io.Writer) {
	fmt.Fprintln(w, "For any other MCP client, edit its config by hand: change a stdio server's")
	fmt.Fprintln(w, `"command" to the absolute path of the holdcall binary shown above, and its "args" to`)
	fmt.Fprintln(w, `  ["serve", "--connector", "<server-name>", "--client", "<your-client-name>",`)
	fmt.Fprintln(w, `   "--", <original command>, <original args...>]`)
	fmt.Fprintln(w, `Leave "env" exactly as it was.`)
}
