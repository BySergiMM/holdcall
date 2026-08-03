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

**Done when:** Claude Code lists exactly the same tools with and without Nim, a
real working session is indistinguishable, and the file shows the calls.

**Why first:** it validates the highest technical risk in the project — that we
can sit in the middle of a protocol we do not control without breaking it. If
this fails there is no product, and it is better to know in week one.

## M2 — Daemon

State moves out of the shim. The shim becomes a byte relay to a local daemon
over a unix socket / named pipe. The daemon supervises downstream servers and
owns all shared state.

**Why:** budgets, the hash-chained journal and human approval all need one
writer. A client spawns one shim per configured server; only a daemon gives
them a single view.

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
