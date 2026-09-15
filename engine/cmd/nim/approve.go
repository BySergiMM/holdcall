package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/shim"
)

// nim approve and nim reject are the human side of an "ask" rule:
//
//	nim approve                  lists every call currently held
//	nim approve <id>              approves one
//	nim reject <id> [--reason]    refuses one
//
// Both talk to the daemon under its own purpose -- approval.list and
// approval.decide -- exactly as nim policy talks to it under "policy".
// docs/decisions/0005-human-approval.md is the design: what is shown is the
// real arguments a call carried, never a model-generated summary, because a
// summary is something the model that asked for the call could have written
// to make a dangerous call look safe.

func runApprove(args []string) error {
	switch len(args) {
	case 0:
		return listPending()
	case 1:
		return decideApproval(args[0], journal.DecisionApproved, "")
	default:
		return fmt.Errorf("usage: nim approve [<id>]")
	}
}

// runReject parses `nim reject <id> [--reason <text>]` by hand, the same
// reason parsePolicyArgs does rather than the standard flag package: the id
// comes first, and flag.Parse stops at the first non-flag argument it sees
// rather than looking past it for --reason.
func runReject(args []string) error {
	const usage = "usage: nim reject <id> [--reason <text>]"
	var id, reason string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--reason":
			if i+1 >= len(args) {
				return fmt.Errorf("--reason requires text")
			}
			i++
			reason = args[i]
		case strings.HasPrefix(a, "--reason="):
			reason = strings.TrimPrefix(a, "--reason=")
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unexpected flag %q; %s", a, usage)
		case id == "":
			id = a
		default:
			return fmt.Errorf("one id per rejection; %q was a second one. %s", a, usage)
		}
	}
	if id == "" {
		return fmt.Errorf("%s", usage)
	}
	return decideApproval(id, journal.DecisionRejected, reason)
}

func listPending() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	conn, err := shim.DialDaemon(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{ID: config.NewID(), Kind: daemon.KindApprovalList})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	if len(resp.Pending) == 0 {
		fmt.Println("(nothing is held for approval)")
		return nil
	}
	for i, p := range resp.Pending {
		if i > 0 {
			fmt.Println()
		}
		printPending(os.Stdout, p)
	}
	return nil
}

// printPending shows one held call exactly as nim approve promises: the
// real params.arguments, pretty-printed, never a summary and never
// anything a model wrote -- the whole point of docs/decisions/0005-human-approval.md.
func printPending(out io.Writer, p daemon.PendingInfo) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "id\t%s\n", p.ID)
	fmt.Fprintf(w, "age\t%s\n", ageSince(p.StartedAt))
	fmt.Fprintf(w, "agent\t%s\n", orEvery(p.Agent))
	fmt.Fprintf(w, "connector\t%s\n", orEvery(p.Connector))
	fmt.Fprintf(w, "tool\t%s\n", p.Tool)
	w.Flush()
	if !p.ArgumentsKnown {
		// Not the same as a call with no arguments: the relay has not
		// reported them yet, and the daemon refuses to approve until it
		// has. Saying so is what stops a human approving a tool name.
		fmt.Fprintln(out, "arguments: (not received from the relay yet -- run nim approve again in a moment)")
		return
	}
	fmt.Fprintln(out, "arguments:")
	fmt.Fprintln(out, prettyArguments(p.Arguments))
}

// prettyArguments renders a call's real arguments for a human to read, not a
// program: indented JSON, or a plain statement that there were none. A call
// listed in the instant between the daemon holding it and the relay's
// call.arguments reaching it reads the same way a call with no arguments
// does -- both are "nothing here yet" -- and nim approve is meant to be run
// by someone looking at a specific call, not raced against the network.
func prettyArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "  (none)"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "  ", "  "); err != nil {
		// Should not happen -- these bytes were read out of params.arguments
		// by the same strict object reader that decided this was a call at
		// all -- but showing the raw bytes is still showing the real
		// arguments, which is the guarantee that matters here.
		return "  " + visible(string(raw))
	}
	return "  " + visible(buf.String())
}

// visible makes every byte of what a human is about to approve show up as
// itself. The arguments are the model's to write, and a terminal obeys
// escape sequences and bidirectional overrides wherever they appear: an
// ESC in a string could erase the line that names the tool, a U+202E could
// reverse the path that follows it, and the human would approve what they
// saw rather than what was there. JSON keeps control characters escaped,
// so a well-formed argument is unchanged; anything that is not printable
// -- a raw control byte on the fallback path, a format character in an
// otherwise valid string -- is written as its escape instead.
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || unicode.IsPrint(r):
			b.WriteRune(r)
		case r > 0xFFFF:
			fmt.Fprintf(&b, "\\U%08X", r)
		default:
			fmt.Fprintf(&b, "\\u%04X", r)
		}
	}
	return b.String()
}

func ageSince(startedAt string) string {
	t, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return "unknown"
	}
	return time.Since(t).Round(time.Second).String()
}

func decideApproval(id, decision, reason string) error {
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
		ID: config.NewID(), Kind: daemon.KindApprovalDecide,
		ApprovalID: id, ApprovalDecision: decision, ApprovalReason: reason,
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	if len(resp.Pending) != 1 {
		return fmt.Errorf("the daemon answered with %d calls, want 1", len(resp.Pending))
	}
	p := resp.Pending[0]
	verb := "approved"
	if decision == journal.DecisionRejected {
		verb = "rejected"
	}
	fmt.Printf("%s %s for %s on %s (recorded in the journal)\n",
		verb, p.Tool, scopeWord("agent", p.Agent), scopeWord("connector", p.Connector))
	if reason != "" {
		fmt.Printf("reason: %s\n", reason)
	}
	return nil
}
