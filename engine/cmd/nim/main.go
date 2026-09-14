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
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/BySergiMM/nim/engine/internal/config"
	"github.com/BySergiMM/nim/engine/internal/console"
	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/readmodel"
	"github.com/BySergiMM/nim/engine/internal/shim"
)

// Set by the release workflow with -ldflags "-X main.xxx=...". A build made
// any other way -- `go build`, `go run`, a developer's own binary -- is
// exactly the case these defaults describe: it did not come from a tagged
// release and has no commit or build time to report.
var (
	version = "0.0.0-dev"
	commit  = "unknown"
	builtAt = "unknown"
)

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
	case "console":
		err = runConsole(os.Args[2:])
	case "connector":
		err = runConnector(os.Args[2:])
	case "agent":
		err = runAgent(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(versionString())
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

// versionString is what `nim version` prints. It is a function rather than a
// literal Println call so a test can check the shape without spawning the
// binary or depending on the ldflags a particular build was made with.
func versionString() string {
	return fmt.Sprintf("nim %s (%s, built %s, %s/%s, %s)",
		version, commit, builtAt, runtime.GOOS, runtime.GOARCH, runtime.Version())
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

  nim log --follow [--json] [--since <chain_seq>]
        Every journal entry in the order it was written, as it arrives.

  nim verify [--expect-head <hash>]
        Walk the journal's hash chain.

  nim console [--addr 127.0.0.1:7717]
        Serve a local, read-only view of what has been recorded.

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
	// The command may be omitted when the connector has one registered: the
	// daemon then supplies the one it authorized, and that is the documented
	// way to run a connector that holds a credential. Only shim.Run knows
	// whether the daemon answered with one, so the "neither side has a
	// command" case is reported there rather than guessed at here.
	command := fs.Args()

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
	seed, _ := config.ReadMachineID()
	j, err := journal.OpenReadOnly(cfg.DatabasePath(), seed)
	if err != nil {
		return err
	}
	defer j.Close()

	// The same projection the console reads. Both surfaces draw their meaning
	// from one place, so "calls recorded" cannot come to mean one thing here
	// and another in a browser.
	snap, err := readmodel.Take(j)
	if err != nil {
		return err
	}
	renderStatus(os.Stdout, snap)
	return nil
}

// renderStatus writes a snapshot as text. Separate from reading it so a test
// can render the same projection the console serves and compare the two.
func renderStatus(w io.Writer, snap readmodel.Snapshot) {
	// "recorded", not "made": these are the calls that reached the journal, and
	// the gaps below are the only thing with anything to say about the rest.
	fmt.Fprintln(w, "calls    recorded", snap.CallsRecorded)

	fmt.Fprintln(w)
	fmt.Fprintln(w, "journal  schema version", snap.Journal.SchemaVersion)
	fmt.Fprintln(w, "         entries       ", snap.Journal.Entries)
	if snap.Journal.Head == "" {
		fmt.Fprintln(w, "         head           (none)")
	} else {
		fmt.Fprintln(w, "         head          ", snap.Journal.Head)
	}

	switch snap.Journal.Chain {
	case readmodel.ChainEmpty:
		fmt.Fprintln(w, "         chain          nothing recorded yet -- nothing to check")
	case readmodel.ChainBroken:
		fmt.Fprintln(w, "         chain          BROKEN:", snap.Journal.Problem)
	case readmodel.ChainPartial:
		fmt.Fprintln(w, "         chain          self-consistent from entry 2 on")
		fmt.Fprintln(w, "                        entry 1 unchecked:", config.MachineIDPath(), "is missing")
	default:
		fmt.Fprintln(w, "         chain          self-consistent")
	}

	renderGaps(w, snap.Gaps)

	// Said plainly, because the alternative is a user who believes the chain
	// does more than it does. The remedy is one line, so it is worth printing
	// next to the limitation rather than burying it in a document.
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Self-consistent means each entry still hashes to what it claims. It does not")
	fmt.Fprintln(w, "mean nothing was removed, and it does not detect anyone who can write to the")
	fmt.Fprintln(w, "journal file: nothing here is secret, so they can recompute every hash and a")
	fmt.Fprintln(w, "shorter chain checks out just as cleanly. To cover that, record the head above")
	fmt.Fprintln(w, "somewhere else and check it later with:")
	fmt.Fprintln(w)
	if snap.Journal.Head != "" {
		fmt.Fprintln(w, "    nim verify --expect-head", snap.Journal.Head)
	} else {
		fmt.Fprintln(w, "    nim verify --expect-head <hash>")
	}
	if !snap.Journal.VerificationMaterial {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Restore machine-id to check the first entry again. Its absence is why entry 1")
		fmt.Fprintln(w, "is unchecked; it is not evidence that anything was altered.")
	}
}

// renderGaps reports what the journal can tell about its own incompleteness.
func renderGaps(w io.Writer, gaps readmodel.Gaps) {
	if gaps.UnfinishedSessions == 0 && gaps.SessionsWithGaps == 0 && len(gaps.Anomalies) == 0 {
		// Not "no gaps": events dropped on the daemon's write path leave no
		// trace to find, so this can only speak for the ones that do.
		fmt.Fprintln(w, "         gaps           none of the detectable kinds found")
		return
	}
	if gaps.MissingCallEntries > 0 {
		fmt.Fprintf(w, "         gaps           %d call(s) missing across %d session(s): reported but not recorded\n",
			gaps.MissingCallEntries, gaps.SessionsWithGaps)
	}
	if gaps.UnfinishedSessions > 0 {
		fmt.Fprintf(w, "         unfinished     %d session(s) with no end (a session running now looks the same)\n",
			gaps.UnfinishedSessions)
	}
	for _, name := range sortedKeys(gaps.Anomalies) {
		fmt.Fprintf(w, "         anomaly        %-16s %d (relayed without inspection)\n", name, gaps.Anomalies[name])
	}
}

func runLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	limit := fs.Int("n", 50, "how many calls to show")
	asJSON := fs.Bool("json", false, "stream journal entries as one JSON object per line")
	follow := fs.Bool("follow", false, "keep watching for new entries")
	since := fs.Int64("since", 0, "stream entries after this chain_seq")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sinceSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "since" {
			sinceSet = true
		}
	})

	// --json and --follow both mean "the entry stream", which is a different
	// view from the default: every kind of entry, in the journal's own order,
	// oldest first. The plain table stays a table of calls.
	if *asJSON || *follow {
		return streamEntries(*asJSON, *follow, *since, sinceSet, *limit)
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
		// The same derivation the console uses, so a call cannot read as refused
		// in a browser and as unanswered here. A completed call is worth more
		// than the word "completed": it is the only state that has a result.
		state := readmodel.CallStateOf(c.Decision, c.HasOutcome)
		result := string(state)
		if state == readmodel.CallCompleted {
			result = "ok"
			if c.OK != nil && !*c.OK {
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

// pollInterval is how long the stream waits after catching up. Short enough to
// feel live, long enough that watching an idle journal costs nothing.
const pollInterval = 250 * time.Millisecond

// streamEntries reads the journal by cursor and writes what it finds.
//
// The cursor is chain_seq, so this needs nothing the journal does not already
// have: read what came after the last position, remember the new one, ask
// again. Stopping and restarting resumes cleanly, and so will a console.
//
// Without --since, --follow starts at the head and shows only what happens
// next, the way tail -f does; a plain --json dump starts at the beginning.
func streamEntries(asJSON, follow bool, since int64, sinceSet bool, limit int) error {
	j, err := openJournal()
	if err != nil {
		return err
	}
	defer j.Close()

	cursor := since
	if follow && !sinceSet {
		length, _, err := j.Head()
		if err != nil {
			return err
		}
		cursor = length
	}

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	encoder := json.NewEncoder(out)

	for {
		// Drain everything available before waiting: a full page means there is
		// probably more, and a reader that slept between pages would fall
		// steadily further behind a busy journal.
		for {
			page, err := readmodel.Stream(j, cursor, limit)
			if err != nil {
				return err
			}
			for _, ev := range page.Events {
				if asJSON {
					err = encoder.Encode(ev)
				} else {
					_, err = fmt.Fprintln(out, formatEvent(ev))
				}
				if err != nil {
					return nil // the reader went away; that is not our error
				}
			}
			cursor = page.Cursor
			if len(page.Events) < limit {
				break
			}
		}
		if err := out.Flush(); err != nil {
			return nil
		}
		if !follow {
			return nil
		}
		time.Sleep(pollInterval)
	}
}

// formatEvent renders one entry on one line, leading with the position that
// orders it.
func formatEvent(ev readmodel.Event) string {
	line := fmt.Sprintf("%6d  %-14s %s", ev.ChainSeq, ev.Kind, shortID(ev.SessionID))
	add := func(format string, args ...any) { line += "  " + fmt.Sprintf(format, args...) }

	if ev.Seq != nil {
		add("seq=%d", *ev.Seq)
	}
	if ev.Connector != nil {
		add("connector=%s", *ev.Connector)
	}
	if ev.Client != nil {
		add("client=%s", *ev.Client)
	}
	if ev.Tool != nil {
		add("tool=%s", *ev.Tool)
	}
	if ev.Decision != nil {
		add("decision=%s", *ev.Decision)
	}
	if ev.Anomaly != nil {
		add("anomaly=%s", *ev.Anomaly)
	}
	if ev.OK != nil {
		if *ev.OK {
			add("ok")
		} else {
			add("failed")
		}
	}
	if ev.DurationMS != nil {
		add("%dms", *ev.DurationMS)
	}
	if ev.ProtocolVersion != nil {
		add("protocol=%s", *ev.ProtocolVersion)
	}
	return line
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// runConsole serves the read-only view.
//
// A separate process from the daemon on purpose: the daemon is what will decide
// things, and it should not also be a web server. Running apart also means the
// console still works when the daemon does not, which is when someone is most
// likely to want it.
func runConsole(args []string) error {
	fs := flag.NewFlagSet("console", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7717", "loopback address to serve on")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	j, err := openJournal()
	if err != nil {
		return err
	}
	defer j.Close()

	// Belt and braces: openJournal already returns a read-only handle, and this
	// refuses to serve anything else. A console that could write would be a
	// different product.
	if !j.ReadOnly() {
		return fmt.Errorf("refusing to serve: the journal was not opened read-only")
	}

	listener, err := console.Listen(*addr)
	if err != nil {
		return err
	}
	srv := console.New(j, cfg.Daemon.Socket)

	fmt.Println("nim console on http://" + listener.Addr().String())
	fmt.Println("reading", cfg.DatabasePath())
	fmt.Println("read-only: this cannot change anything Nim recorded.")
	return http.Serve(listener, srv.Handler())
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

	// The same four-way reading of the chain the console and `nim status` use.
	state, err := readmodel.Check(j, *expect)
	if err != nil {
		return err
	}
	if state.Chain == readmodel.ChainBroken {
		fmt.Fprintln(os.Stderr, "journal FAILED verification")
		fmt.Fprintln(os.Stderr, " ", state.Problem)
		os.Exit(1)
	}

	// An empty journal is not a verified one. It is indistinguishable from one
	// whose every entry was lost, and saying "verified" would put those two on
	// the same footing.
	if state.Chain == readmodel.ChainEmpty {
		fmt.Println("nothing recorded yet: the journal has no entries, so there was nothing to check")
		return nil
	}

	if state.Chain == readmodel.ChainPartial {
		fmt.Printf("journal checked from entry 2 on: %d entries, head %s\n", state.Entries, state.Head)
		fmt.Println()
		fmt.Println("Entry 1 could not be checked because", config.MachineIDPath(), "is missing.")
		fmt.Println("That file seeds the chain. Its absence is missing verification material, not")
		fmt.Println("evidence of a problem: restore it and the whole chain can be checked again.")
		fmt.Println("Everything from entry 2 onwards is self-consistent.")
		return nil
	}

	fmt.Printf("journal self-consistent: %d entries, head %s\n", state.Entries, state.Head)
	if *expect != "" {
		if state.ExpectedHeadAt == state.Entries {
			fmt.Println("head matches the one you recorded, so nothing before it has been rewritten")
			return nil
		}
		// The journal grew, which is the ordinary case for any head recorded
		// before the agent kept working. Saying so is the difference between a
		// check an operator keeps running and one they learn to ignore.
		fmt.Printf("the head you recorded is entry %d of %d, so the journal has grown by %d entries\n",
			state.ExpectedHeadAt, state.Entries, state.Entries-state.ExpectedHeadAt)
		fmt.Println("nothing at or before it has been rewritten; the entries after it are covered")
		fmt.Println("only by the chain itself, so record the new head to cover them too.")
		return nil
	}
	fmt.Println()
	fmt.Println("This walked the chain and found each entry hashes to what it claims. It does")
	fmt.Println("not show that nothing was removed: anyone able to write to the journal can")
	fmt.Println("recompute the chain, and a shorter one checks out just as cleanly. Pass")
	fmt.Println("--expect-head with a hash you recorded earlier to cover that too.")
	return nil
}

// openJournal opens the journal for reading and nothing else.
//
// Read-only twice over: SQLite refuses writes on the handle, and it never
// creates an install identifier, because a read command that invented one would
// reseed the chain and make the next verification report tampering. Reading a
// record should not be able to change it.
func openJournal() (*journal.Journal, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(cfg.DatabasePath()); os.IsNotExist(err) {
		return nil, fmt.Errorf("no journal at %s yet", cfg.DatabasePath())
	}
	seed, _ := config.ReadMachineID()
	return journal.OpenReadOnly(cfg.DatabasePath(), seed)
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
