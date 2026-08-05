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

## M1 — Pass-through (current)

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

## M2 — Daemon lifetime

On Windows the daemon dies with the process tree that spawned it. State survives
in SQLite and the next shim restarts it, so M1 stands, but a daemon shared
across sessions needs proper detachment.

**Why it matters:** budgets, the hash-chained journal and human approval all
need one writer that outlives any single client session.

## M3 — Credentials

The daemon holds the downstream servers' credentials and injects them at spawn
time. They leave the client's configuration file.

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
