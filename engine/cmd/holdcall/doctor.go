package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/BySergiMM/holdcall/engine/internal/clientconfig"
	"github.com/BySergiMM/holdcall/engine/internal/config"
	"github.com/BySergiMM/holdcall/engine/internal/credential"
	"github.com/BySergiMM/holdcall/engine/internal/daemon"
	"github.com/BySergiMM/holdcall/engine/internal/journal"
	"github.com/BySergiMM/holdcall/engine/internal/readmodel"
	"github.com/BySergiMM/holdcall/engine/internal/shim"
)

// severity is how bad one doctor check turned out to be. Ordered so the
// worst seen across every check decides the process's exit status.
type severity int

const (
	sevOK severity = iota
	sevWarn
	sevFail
)

func (s severity) tag() string {
	switch s {
	case sevWarn:
		return "WARN"
	case sevFail:
		return "FAIL"
	default:
		return "OK  "
	}
}

// dialTimeout bounds how long any one doctor check waits on the daemon.
// Generous next to the p99 numbers in docs/benchmarks.md, but still short
// enough that a hung daemon does not make the whole command hang.
const dialTimeout = 500 * time.Millisecond

// runDoctor prints one line per check and exits 1 only if something FAILed; a
// WARN is not a healthy install, but it is not what "exit 1" means here --
// that is reserved for what the operator must fix before Holdcall can be trusted
// at all, as opposed to a degraded but working state (no daemon yet, a stale
// enrolment, a client not wired up).
//
// Every check that talks to the daemon opens its own connection, through
// shim.DialRunningDaemon rather than shim.DialDaemon: a connection commits to
// one purpose on the daemon side (see internal/daemon/request.go), so sharing
// one across checks that ask different things would trip that rule rather
// than exercise it, and doctor's whole point is to report what is true right
// now -- a health check that starts the daemon to check whether it is
// running could never answer that question.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	worst := sevOK
	report := func(s severity, format string, a ...any) {
		fmt.Printf("[%s] %s\n", s.tag(), fmt.Sprintf(format, a...))
		if s > worst {
			worst = s
		}
	}

	checkBinaryOnPath(report)
	cfg, cfgOK := checkConfig(report)
	checkDaemon(report, cfg, cfgOK)
	checkMachineID(report)
	checkJournal(report, cfg, cfgOK)
	checkCredentialStore(report)
	checkAgents(report, cfg, cfgOK)
	checkRulesAndConnectors(report, cfg, cfgOK)
	checkClientConfigs(report)

	if worst == sevFail {
		os.Exit(1)
	}
	return nil
}

type reportFunc func(severity, string, ...any)

// 1. The binary answering to "holdcall" on PATH should be the one running. Not a
// FAIL either way: holdcall init writes an absolute path into every client config
// it rewrites, so a client spawning holdcall never consults PATH at all. This only
// matters to someone typing `holdcall` themselves at a shell.
func checkBinaryOnPath(report reportFunc) {
	self, err := os.Executable()
	if err != nil {
		report(sevWarn, "holdcall binary -- could not determine the running executable: %v", err)
		return
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	found, err := exec.LookPath("holdcall")
	if err != nil {
		report(sevWarn, "holdcall binary -- not on PATH -- add its directory to PATH so a shell can spawn it by name")
		return
	}
	if resolved, err := filepath.EvalSymlinks(found); err == nil {
		found = resolved
	}

	if found != self {
		report(sevWarn, "holdcall binary -- PATH resolves to %s, but this process is %s -- harmless for clients, which holdcall init points at an absolute path; only a shell typing `holdcall` sees the difference", found, self)
		return
	}
	report(sevOK, "holdcall binary -- on PATH and matches the running executable (%s)", self)
}

// 2. config.toml loads and validates. A stale [policy] section is the error
// Load returns, surfaced verbatim rather than summarised: it already says
// where policy went and how to move it.
func checkConfig(report reportFunc) (config.Config, bool) {
	cfg, err := config.Load()
	if err != nil {
		report(sevFail, "config.toml -- %v", err)
		return cfg, false
	}
	if err := cfg.Validate(); err != nil {
		report(sevFail, "config.toml -- %v", err)
		return cfg, false
	}
	report(sevOK, "config.toml -- loads and validates (%s)", config.Path())
	return cfg, true
}

// 3. The daemon socket answers, the peer on the other end is genuine, and it
// is the same build as this binary -- F-001: replacing the binary while a
// daemon runs makes the two refuse each other, and an operator reading a
// bare "something other than Holdcall is on that socket" cannot tell that apart
// from a real impostor. shim.ErrDaemonOlderBuild is the specific case;
// everything else keeps the older, more general FAIL.
func checkDaemon(report reportFunc, cfg config.Config, cfgOK bool) {
	if !cfgOK {
		report(sevWarn, "daemon -- skipped: config.toml did not load, see above")
		return
	}
	conn, err := shim.DialRunningDaemon(cfg, dialTimeout)
	if err != nil {
		switch {
		case errors.Is(err, shim.ErrDaemonNotReachable):
			report(sevWarn, "daemon -- not running at %s -- it starts on demand when a client spawns `holdcall serve`, or run `holdcall daemon`", cfg.Daemon.Socket)
		case errors.Is(err, shim.ErrDaemonOlderBuild):
			report(sevFail, "daemon build -- %v", err)
		default:
			report(sevFail, "daemon -- %v -- something other than Holdcall is on that socket; stop it and let a client restart the daemon", err)
		}
		return
	}
	defer conn.Close()
	report(sevOK, "daemon build -- matches this binary")

	resp, err := daemon.SendRequest(conn, daemon.Request{ID: config.NewID(), Kind: daemon.KindAgentList})
	if err != nil {
		report(sevFail, "daemon -- listening at %s but did not answer cleanly: %v -- restart the daemon", cfg.Daemon.Socket, err)
		return
	}
	if resp.Error != "" {
		report(sevFail, "daemon -- listening at %s but returned an error: %s -- restart the daemon", cfg.Daemon.Socket, resp.Error)
		return
	}
	report(sevOK, "daemon -- reachable and genuine (%s)", cfg.Daemon.Socket)
}

// 4. machine-id exists, which is what lets the journal's first entry be
// checked at all.
func checkMachineID(report reportFunc) {
	if _, ok := config.ReadMachineID(); !ok {
		report(sevWarn, "machine-id -- missing (%s) -- entry 1 of the journal cannot be checked until it is restored; the daemon will not recreate one on top of an existing journal", config.MachineIDPath())
		return
	}
	report(sevOK, "machine-id -- present (%s)", config.MachineIDPath())
}

// 5. The journal opens read-only and its chain verifies.
func checkJournal(report reportFunc, cfg config.Config, cfgOK bool) {
	if !cfgOK {
		report(sevWarn, "journal -- skipped: config.toml did not load, see above")
		return
	}
	if _, err := os.Stat(cfg.DatabasePath()); os.IsNotExist(err) {
		report(sevWarn, "journal -- no journal yet at %s -- created the first time a call is recorded", cfg.DatabasePath())
		return
	}

	seed, _ := config.ReadMachineID()
	j, err := journal.OpenReadOnly(cfg.DatabasePath(), seed)
	if err != nil {
		report(sevFail, "journal -- could not open %s: %v -- check the daemon's own log at %s", cfg.DatabasePath(), err, config.LogPath())
		return
	}
	defer j.Close()

	state, err := readmodel.Check(j, "")
	if err != nil {
		report(sevFail, "journal -- could not verify %s: %v -- check the daemon's own log at %s", cfg.DatabasePath(), err, config.LogPath())
		return
	}
	switch state.Chain {
	case readmodel.ChainBroken:
		report(sevFail, "journal -- BROKEN: %s -- see docs/journal-format.md", state.Problem)
	case readmodel.ChainEmpty:
		report(sevWarn, "journal -- empty -- nothing recorded yet, so there is nothing to verify")
	case readmodel.ChainPartial:
		report(sevWarn, "journal -- self-consistent from entry 2 on (%d entries) -- restore %s to check entry 1 too", state.Entries, config.MachineIDPath())
	default:
		report(sevOK, "journal -- self-consistent, %d entries, head %s", state.Entries, state.Head)
	}
}

// 6. The OS credential store is available.
func checkCredentialStore(report reportFunc) {
	if _, err := credential.New(); err != nil {
		report(sevWarn, "credential store -- unavailable: %v", err)
		return
	}
	report(sevOK, "credential store -- available")
}

// 7. Every enrolled agent whose enrolled file no longer matches gets its own
// WARN, naming the fix.
func checkAgents(report reportFunc, cfg config.Config, cfgOK bool) {
	if !cfgOK {
		report(sevWarn, "enrolments -- skipped: config.toml did not load, see above")
		return
	}
	conn, err := shim.DialRunningDaemon(cfg, dialTimeout)
	if err != nil {
		report(sevWarn, "enrolments -- skipped: no daemon to ask")
		return
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{ID: config.NewID(), Kind: daemon.KindAgentList})
	if err != nil {
		report(sevFail, "enrolments -- could not list agents: %v -- restart the daemon", err)
		return
	}
	if resp.Error != "" {
		report(sevFail, "enrolments -- %s", resp.Error)
		return
	}

	stale := 0
	for _, a := range resp.Agents {
		if !a.Current {
			stale++
			report(sevWarn, "enrolment %q -- STALE, %s no longer matches what was enrolled -- run `holdcall agent add %s %s` to re-enrol", a.Name, a.ExecPath, a.Name, a.ExecPath)
		}
	}
	if stale == 0 {
		report(sevOK, "enrolments -- %d enrolled, all current", len(resp.Agents))
	}
}

// 8. Rules and connectors, each counted on its own connection: policy.list
// and connector.list commit a connection to a different purpose (see
// internal/daemon/request.go's requestState), so one connection cannot ask
// both.
func checkRulesAndConnectors(report reportFunc, cfg config.Config, cfgOK bool) {
	if !cfgOK {
		report(sevWarn, "rules -- skipped: config.toml did not load, see above")
		report(sevWarn, "connectors -- skipped: config.toml did not load, see above")
		return
	}

	if conn, err := shim.DialRunningDaemon(cfg, dialTimeout); err != nil {
		report(sevWarn, "rules -- skipped: no daemon to ask")
	} else {
		resp, err := daemon.SendRequest(conn, daemon.Request{ID: config.NewID(), Kind: daemon.KindPolicyList})
		conn.Close()
		switch {
		case err != nil:
			report(sevFail, "rules -- could not list: %v", err)
		case resp.Error != "":
			report(sevFail, "rules -- %s", resp.Error)
		default:
			report(sevOK, "rules -- %d denial rule(s) (holdcall policy list)", len(resp.Rules))
		}
	}

	conn, err := shim.DialRunningDaemon(cfg, dialTimeout)
	if err != nil {
		report(sevWarn, "connectors -- skipped: no daemon to ask")
		return
	}
	defer conn.Close()

	resp, err := daemon.SendRequest(conn, daemon.Request{ID: config.NewID(), Kind: daemon.KindConnectorList})
	if err != nil {
		report(sevFail, "connectors -- could not list: %v", err)
		return
	}
	if resp.Error != "" {
		report(sevFail, "connectors -- %s", resp.Error)
		return
	}

	withoutCommand := 0
	for _, c := range resp.Connectors {
		if len(c.Command) == 0 {
			withoutCommand++
		}
	}
	if withoutCommand > 0 {
		report(sevWarn, "connectors -- %d configured, %d with no authorized command -- register them again with `holdcall connector set`", len(resp.Connectors), withoutCommand)
		return
	}
	report(sevOK, "connectors -- %d configured", len(resp.Connectors))
}

// 9. Client configs: the same detection holdcall init uses, read-only -- this
// never calls clientconfig.Apply, so it changes nothing on disk.
func checkClientConfigs(report reportFunc) {
	cwd, err := os.Getwd()
	if err != nil {
		report(sevWarn, "client configs -- could not determine the working directory: %v", err)
		return
	}
	nimPath, err := resolveNimPath()
	if err != nil {
		report(sevWarn, "client configs -- could not resolve the running holdcall binary: %v", err)
		return
	}
	files, err := clientconfig.Discover("", "", cwd)
	if err != nil {
		report(sevWarn, "client configs -- %v", err)
		return
	}

	for _, f := range files {
		res, err := clientconfig.BuildResult(f, nimPath, f.Client, false)
		if err != nil {
			report(sevWarn, "%s (%s) -- %v", f.Path, f.Client, err)
			continue
		}
		if !res.Found {
			report(sevOK, "%s (%s) -- not found", f.Path, f.Client)
			continue
		}
		through, notThrough := tallyClientEntries(res)
		if notThrough > 0 {
			report(sevWarn, "%s (%s) -- %d server(s) through Holdcall, %d not yet -- run `holdcall init` to fix", f.Path, f.Client, through, notThrough)
			continue
		}
		report(sevOK, "%s (%s) -- %d server(s) through Holdcall", f.Path, f.Client, through)
	}
}

// tallyClientEntries counts entries clientconfig has already classified, the
// same classification holdcall init itself would act on -- so this can never
// report a different count than a `holdcall init` dry run would show.
func tallyClientEntries(res clientconfig.Result) (through, notThrough int) {
	for _, g := range res.Groups {
		for _, e := range g.Entries {
			switch e.Status {
			case clientconfig.StatusAlreadyNim, clientconfig.StatusRepointed, clientconfig.StatusStalePath:
				through++
			case clientconfig.StatusWrapped:
				notThrough++
			}
		}
	}
	return
}
