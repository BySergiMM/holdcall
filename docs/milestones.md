# Milestones

Each one ends in something demonstrable. Nothing is built before the milestone
that needs it.

## Standing decisions

- **Daemon + shim.** One thin relay per configured MCP server, all state in a
  single local daemon. Tool names stay untouched; budgets, journal and approval
  have one writer.
- **SQLite is the source of truth** for configuration, sessions, grants and the
  journal. It is the only thing an authorization decision reads.
- **`config.toml` configures the daemon only** — socket path, data directory,
  which servers to supervise. Nothing an authorization decision depends on.
- **Supabase is a mirror, never a dependency.** It receives a copy of the
  journal for the dashboard. If it is unreachable, nothing changes locally.
- **Every table is prefixed `nim_`**, in SQLite and in Postgres alike, so the
  schema is unambiguous wherever it lands.
- **The relay forwards original bytes.** Messages are parsed only far enough to
  read `method`; the buffer is never re-serialised. Re-encoding JSON changes key
  order and whitespace and breaks clients in ways that are hard to reproduce.

## M1 — Pass-through

The shim spawns one downstream MCP server and relays stdio in both directions,
forwarding every byte untouched. It recognises `tools/call` and reports it to
the daemon, which appends a row to `nim_calls` in SQLite. Nothing is blocked,
no credentials are handled, no policy is evaluated.

Scope is exactly: shim, daemon, unix socket / named pipe, SQLite, `config.toml`.
Anything else is a later milestone.

**Status: done.** A real MCP client was run twice against the same server, once
direct and once through Nim, and the two runs were identical on everything the
client can observe: tool list, JSON schemas, unicode round-trips, a 512 KiB
payload, error propagation and ping. The harness lives in `tools/relay-rig`.

Three findings worth keeping:

- **Unix sockets work on Windows**, so one transport serves all three platforms
  and `go-winio` is not needed.
- **The socket cannot live inside the install directory.** AF_UNIX paths are
  capped near 104 bytes; a deep `NIM_HOME` overflowed it and the daemon died
  with `bind: invalid argument` while the relay carried on, recording nothing
  and reporting no problem. The socket name is now a hash of the home path,
  placed in the temp directory, and an unusable path is rejected with an
  explanation instead of a kernel error.
- **A failed tool call is a successful JSON-RPC response.** MCP reports tool
  errors as `result.isError`, not as a protocol error, so checking only the
  JSON-RPC `error` field recorded every failure as a success.

The second and third were invisible to unit tests and obvious the moment a real
client was involved. That is the argument for keeping the rig.

## M2 — Daemon lifetime (current)

On Windows the daemon dies with the process tree that spawned it. State survives
in SQLite and the next shim restarts it, so M1 stands, but a daemon shared
across sessions needs proper detachment.

**Why it matters:** budgets, the hash-chained journal and human approval all
need one writer that outlives any single client session.

**Status: in progress.** The daemon is now started detached from the shim's
process group and session: `Setsid` on Linux and macOS, `CREATE_BREAKAWAY_FROM_JOB`
+ `DETACHED_PROCESS` (with a fallback for job objects that forbid breakaway) on
Windows. Verified end to end on macOS: a `nim serve` run inside its own session,
with the whole session's process group killed, leaves the daemon running. The
Windows path only cross-compiles here and has not run on real Windows.

Startup is also now safe when several daemons race to reclaim the same stale
socket left by a crash -- the ordinary case of a client spawning several shims
at once with no daemon yet alive. An `flock`-based startup lock on Linux and
macOS serializes the check-and-reclaim sequence, closing a real split-brain
window (reproduced directly: without the lock, 8 daemons racing on one stale
socket produced 2-5 simultaneous "winners"; with it, always exactly 1). No
verified equivalent exists for Windows yet, so that platform keeps only the
weaker retry-based mitigation.

## M3 — Credentials

The daemon holds the downstream servers' credentials and injects them at spawn
time. They leave the client's configuration file.

**Status: done, READY WITH KNOWN LIMITATION (Windows).** Secrets live in the OS
credential store (Keychain / DPAPI / Secret Service), never in SQLite, never in
argv (`nim connector set <target> --env KEY` reads the value from stdin, not a
command-line argument). Two independent authorization layers gate the socket:
peer-process identity (kernel-verified on darwin/linux -- `LOCAL_PEERPID` /
`SO_PEERCRED`, resolved to an executable path and compared to the daemon's own
by file identity, not by name or content) and per-connection target-binding
(a connection may only ever ask for the one target its first `credential.get`
named, closing off a downstream MCP server pivoting to another target's
secret even from a connection that did pass peer verification). Verified live
on macOS with an adversarial suite: raw-socket and Python clients denied
outright, symlinked/copied/renamed binaries still denied (compared by inode,
not path), pivot/reconnect/duplicate-JSON-key tricks all denied, a real
"malicious downstream" (a legitimately-spawned `nim serve` session) denied
when it tried to reach another target's credential directly. Two additional
issues were found and fixed during the same audit: an unbounded per-line
socket read let any local process (pre-authorization) grow the daemon's
memory without limit, and `credential.get`'s metadata+secret read was not
serialized against a concurrent `connector.set`, producing a torn env-key/
secret pairing under load; both are now bounded/locked and covered by
regression tests, including one reproducing the original race live.

Windows has no peer-credential API for AF_UNIX sockets (`afunix.sys` exposes
nothing equivalent to `SO_PEERCRED`/`LOCAL_PEERPID`), investigated again
specifically during this audit with no workaround found -- see
`peer_windows.go`'s comment for what was considered (named pipes, a
capability token, file-ACL checks) and why each either just moves the
problem or requires reversing this document's own M1 "unix sockets on
Windows" standing decision. Target-binding still holds there (it does not
depend on peer identity), but nothing can verify the caller is genuinely this
binary, and `config.go`'s `EnsureDirs` also does not set a real Windows ACL
on the socket's directory (`os.Chmod`/`os.MkdirAll`'s mode argument only
toggles the read-only attribute on Windows, never an ACL) -- confidentiality
there rests on the OS's own default temp-directory permissions, not on
anything Nim verifies. Closing this for real needs a named-pipe transport
with `GetNamedPipeClientProcessId` and an explicit DACL, which is a real
architecture change or none of this has been run on real Windows; only
cross-compilation (`windows/amd64`) has been checked.

## M4 — Grants (Cedar)

Allow / deny per (agent, connector, tool). Preceded by a one-day spike to verify
`cedar-go` is complete enough; if it is not, that decision is reopened with data.

## M5 — Budgets

Per session, decremented at authorization time, not at execution time.

## M6 — Human approval

Out-of-band prompt showing real parameters, never a model-generated summary.

## M7 — Journal

Append-only, hash-chained, with the inputs that produced each decision.

## M8 — Dashboard

Next.js + Supabase. The engine pushes a copy of the journal; the cloud never
decides anything.

### Sync contract

| Never leaves the machine | Synced |
|---|---|
| Credentials and connector secrets | Agent, connector and tool names |
| Call parameters (digest only) | Decision, duration, cost, timestamp |
| Response contents | Journal link hash |
| The decision itself | Aggregate statistics |
