// Command nim is the local engine: a relay a client spawns, and a daemon that
// keeps the record.
//
//	nim serve --connector github -- npx -y @modelcontextprotocol/server-github
//	nim daemon
//	nim status
//	nim log
//	nim verify
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"text/tabwriter"
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
	case "log":
		err = runLog(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
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
	fmt.Fprint(os.Stderr, `nim - a record of what agents did through MCP

  nim serve --connector <name> [--client <name>] -- <command> [args...]
        Relay one MCP server. This is what your client spawns.

  nim daemon
        Run the shared daemon. Started automatically when needed.

  nim status
        Where state lives, and what the record says about itself.

  nim log [-n <count>]
        The calls that have been seen, newest first.

  nim verify [--expect-head <hash>]
        Walk the journal's hash chain.

`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	connector := fs.String("connector", "", "name of the downstream MCP server (required)")
	client := fs.String("client", "", "label for the MCP client, if known")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *connector == "" {
		return fmt.Errorf("--connector is required")
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
		Connector: *connector,
		Client:    *client,
		Command:   command,
		Config:    cfg,
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
	seed, seedKnown := config.ReadMachineID()
	j, err := journal.Open(cfg.DatabasePath(), seed)
	if err != nil {
		return err
	}
	defer j.Close()

	calls, err := j.CountCalls()
	if err != nil {
		return err
	}
	// "recorded", not "made": these are the calls that reached the journal, and
	// §gaps below is the only thing that has anything to say about the rest.
	fmt.Println("calls    recorded", calls)

	length, head, err := j.Head()
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("journal  schema version", journal.SchemaVersion1)
	fmt.Println("         entries       ", length)
	if head == "" {
		fmt.Println("         head           (none)")
	} else {
		fmt.Println("         head          ", head)
	}

	report, err := j.Verify("")
	if err != nil {
		return err
	}
	switch {
	case report.Empty:
		fmt.Println("         chain          nothing recorded yet -- nothing to check")
	case !report.OK:
		fmt.Println("         chain          BROKEN:", report.Problem)
	case report.Partial:
		fmt.Println("         chain          self-consistent from entry 2 on")
		fmt.Println("                        entry 1 unchecked:", config.MachineIDPath(), "is missing")
	default:
		fmt.Println("         chain          self-consistent")
	}

	if err := printGaps(j); err != nil {
		return err
	}

	// Said plainly, because the alternative is a user who believes the chain
	// does more than it does. The remedy is one line, so it is worth printing
	// next to the limitation rather than burying it in a document.
	fmt.Println()
	fmt.Println("Self-consistent means each entry still hashes to what it claims. It does not")
	fmt.Println("mean nothing was removed, and it does not detect anyone who can write to the")
	fmt.Println("journal file: nothing here is secret, so they can recompute every hash and a")
	fmt.Println("shorter chain checks out just as cleanly. To cover that, record the head above")
	fmt.Println("somewhere else and check it later with:")
	fmt.Println()
	if head != "" {
		fmt.Println("    nim verify --expect-head", head)
	} else {
		fmt.Println("    nim verify --expect-head <hash>")
	}
	if !seedKnown {
		fmt.Println()
		fmt.Println("Restore machine-id to check the first entry again. Its absence is why entry 1")
		fmt.Println("is unchecked; it is not evidence that anything was altered.")
	}
	return nil
}

// printGaps reports what the journal can tell about its own incompleteness.
func printGaps(j *journal.Journal) error {
	loss, err := j.Loss()
	if err != nil {
		return err
	}
	anomalies, err := j.Anomalies()
	if err != nil {
		return err
	}
	if loss.UnfinishedSessions == 0 && loss.SessionsWithGaps == 0 && len(anomalies) == 0 {
		// Not "no gaps": events dropped on the daemon's write path leave no
		// trace to find, so this can only speak for the ones that do.
		fmt.Println("         gaps           none of the detectable kinds found")
		return nil
	}
	if loss.MissingCallEntries > 0 {
		fmt.Printf("         gaps           %d call(s) missing across %d session(s): reported but not recorded\n",
			loss.MissingCallEntries, loss.SessionsWithGaps)
	}
	if loss.UnfinishedSessions > 0 {
		fmt.Printf("         unfinished     %d session(s) with no end (a session running now looks the same)\n",
			loss.UnfinishedSessions)
	}
	for _, name := range sortedKeys(anomalies) {
		fmt.Printf("         anomaly        %-16s %d (relayed without inspection)\n", name, anomalies[name])
	}
	return nil
}

func runLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	limit := fs.Int("n", 50, "how many calls to show")
	if err := fs.Parse(args); err != nil {
		return err
	}

	j, err := openJournal()
	if err != nil {
		return err
	}
	defer j.Close()

	calls, err := j.RecentCalls(*limit)
	if err != nil {
		return err
	}
	if len(calls) == 0 {
		fmt.Println("no calls recorded yet")
		return nil
	}

	// Newest first by chain_seq, the order the journal was written in. The
	// timestamp is shown because it is useful, but it does not decide the
	// order: it is a value an entry carries, and a value can be wrong.
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tWHEN\tCONNECTOR\tTOOL\tDECISION\tRESULT\tMS")
	for _, c := range calls {
		result := "(no response)"
		if c.OK != nil {
			result = "ok"
			if !*c.OK {
				result = "failed"
			}
		}
		ms := ""
		if c.DurationMS != nil {
			ms = fmt.Sprintf("%d", *c.DurationMS)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			c.ChainSeq, shortTime(c.OccurredAt), c.Connector, c.Tool, c.Decision, result, ms)
	}
	w.Flush()

	totals, err := j.ToolTotals()
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("%d tool(s) seen across the whole journal:\n", len(totals))
	for _, tool := range sortedKeys(totals) {
		fmt.Printf("  %-32s %d\n", tool, totals[tool])
	}
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	expect := fs.String("expect-head", "", "the head hash you recorded earlier")
	if err := fs.Parse(args); err != nil {
		return err
	}

	j, err := openJournal()
	if err != nil {
		return err
	}
	defer j.Close()

	report, err := j.Verify(*expect)
	if err != nil {
		return err
	}
	// Keyed on Problem rather than on OK: an empty journal is not OK either,
	// but it is not a failure, and reporting it as one would say something went
	// wrong when the truth is that nothing has happened yet.
	if report.Problem != "" {
		fmt.Fprintln(os.Stderr, "journal FAILED verification")
		fmt.Fprintln(os.Stderr, " ", report.Problem)
		os.Exit(1)
	}

	// An empty journal is not a verified one. It is indistinguishable from one
	// whose every entry was lost, and saying "verified" would put those two on
	// the same footing.
	if report.Empty {
		fmt.Println("nothing recorded yet: the journal has no entries, so there was nothing to check")
		return nil
	}

	if report.Partial {
		fmt.Printf("journal checked from entry 2 on: %d entries, head %s\n", report.Entries, report.Head)
		fmt.Println()
		fmt.Println("Entry 1 could not be checked because", config.MachineIDPath(), "is missing.")
		fmt.Println("That file seeds the chain. Its absence is missing verification material, not")
		fmt.Println("evidence of a problem: restore it and the whole chain can be checked again.")
		fmt.Println("Everything from entry 2 onwards is self-consistent.")
		return nil
	}

	fmt.Printf("journal self-consistent: %d entries, head %s\n", report.Entries, report.Head)
	if *expect != "" {
		fmt.Println("head matches the one you recorded, so nothing before it has been rewritten")
		return nil
	}
	fmt.Println()
	fmt.Println("This walked the chain and found each entry hashes to what it claims. It does")
	fmt.Println("not show that nothing was removed: anyone able to write to the journal can")
	fmt.Println("recompute the chain, and a shorter one checks out just as cleanly. Pass")
	fmt.Println("--expect-head with a hash you recorded earlier to cover that too.")
	return nil
}

// openJournal opens the journal for reading. It never creates an install
// identifier: a read command that invented one would reseed the chain and make
// the next verification report tampering.
func openJournal() (*journal.Journal, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(cfg.DatabasePath()); os.IsNotExist(err) {
		return nil, fmt.Errorf("no journal at %s yet", cfg.DatabasePath())
	}
	seed, _ := config.ReadMachineID()
	return journal.Open(cfg.DatabasePath(), seed)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func shortTime(s string) string {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Local().Format("2006-01-02 15:04:05")
	}
	return s
}
