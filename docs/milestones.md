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

## M1.5 — Trustworthy record

The journal became evidence rather than a log. Three ways it could quietly stop
telling the truth are closed, and the one that stays open is written down.

**Status: done.** The rig still reports `RESULT: IDENTICAL`, so none of it cost
invisibility.

- **Append-only in fact.** A call was one row that got updated when the response
  came back, which a hash chain cannot cover. It is now two immutable entries,
  `call.request` and `call.outcome`, joined by `(session_id, seq)` when read.
  `nim_calls` and `nim_sessions` are views over the entries.
- **Hash-chained**, with the encoding defined in `docs/journal-format.md` rather
  than left to whatever a serialiser happens to emit. Each entry carries the
  `schema_version` it was written under, so M2 can change the fields without
  invalidating what is already on disk.
- **`decision` says `observed`, not `allow`.** Nothing here authorizes anything,
  and the old value claimed a decision that was never made. The rest of the
  vocabulary is reserved in the schema so it need not change later.
- **`target` is now `connector`**, done while there is no history to migrate.
- **Detectable gaps are counted.** Events are still dropped when the daemon is
  slow or absent — that path is replaced in M2, so it was not worth repairing —
  but the relay now says so on stderr, and `nim status` reconstructs what it can
  from the journal afterwards.

  **Not all of it.** Reporting is asynchronous and one-way, so a failure on the
  daemon's write path never reaches the shim and leaves nothing in the journal:
  it is indistinguishable from no event having been sent. The same is true of
  events lost at the end of a session that then closes normally. The claim this
  milestone can defend is *"the losses that leave evidence are reported"*, not
  *"all losses are accounted for"* — see `docs/journal-format.md`.
- **Batches and unparseable frames are counted.** A JSON-RPC batch slips past
  envelope parsing entirely, so a `tools/call` inside one reached a server with
  no record at all. It still does; now it leaves an `anomaly` entry. Rejecting
  it needs the strict reader that comes with enforcement.
- **`nim log`**, **`nim verify`**, and a `nim status` that prints the chain head.

**What the chain does not do.** It detects corruption and edits that did not
recompute it. It does not detect anyone who can write to `nim.db`: nothing in it
is secret, so they can recompute every hash and it verifies cleanly. Recording
the head elsewhere (`nim verify --expect-head`) closes that for everything
before it; owning the file as another OS user closes it properly. A test asserts
the limitation rather than the reverse, so it will notice if this ever changes.

It also says nothing about whether entries make sense *together*: an outcome
with no request, or an end with no start, is cryptographically fine. The unique
`(session_id, seq, kind)` index rules out exact duplicates — which gap detection
depends on — but nothing checks the set as a whole.

**A note on the standing decision this milestone had to weaken.** The intent was
that every loss be accounted for and visible. That cannot hold while reporting
is one-way: the shim cannot learn what the daemon failed to write. The version
that does hold here is *"losses that leave evidence in the journal are reported,
and the record never claims more than that"*. The stronger form needs the
reply-carrying protocol, and belongs with it.

Two findings worth keeping:

- **Clients kill the relay, they do not close its stdin**, so a session was
  never marked as ended. Since "never ended" is one of the two shapes used to
  report lost events, that would have made the report meaningless. The daemon
  now closes a session when the shim's connection drops — which leaves the case
  worth keeping: if the *daemon* dies, nothing is written and the session stays
  open, which is exactly the period during which events were lost.
- **A daemon outlives the shim and holds the journal open.** Deleting the data
  directory under a running daemon leaves it writing to an unlinked file while
  `nim status` reports it as running. A preview of M2's process-lifetime work.

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
