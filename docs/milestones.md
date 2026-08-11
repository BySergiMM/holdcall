# Milestones

Each one ends in something demonstrable. Nothing is built before the milestone
that needs it.

## Standing decisions

- **Daemon + shim.** One thin relay per configured MCP server, all state in a
  single local daemon. Tool names stay untouched; budgets, journal and approval
  have one writer.
- **SQLite is the source of truth** for configuration, sessions, grants and the
  journal. It is the only thing an authorization decision reads.
- **`config.toml` configures the daemon** — socket path, data directory, which
  servers to supervise.

  **Weakened in M2, deliberately and temporarily.** M2 reads a deny list from
  `config.toml` so that the enforcement path can be exercised end to end against
  something real. The original decision was right and this breaks it: any
  process able to write `config.toml` can empty the list, which is not a
  property an authorization input should have. It is scaffolding, kept small on
  purpose — exact tool names, no wildcards, no scopes, no ordering — and real
  policy belongs somewhere the daemon owns. Nothing else an authorization
  decision depends on may go here in the meantime.
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
- **`decision` said `observed`, not `allow`.** Nothing in this milestone
  authorized anything, and the old value claimed a decision that was never made.
  The rest of the vocabulary was reserved in the schema, which is why M2 needed
  no migration; entries from this milestone still read `observed`.
- **`target` is now `connector`**, done while there is no history to migrate.
- **Detectable gaps are counted.** Events are still dropped when the daemon is
  slow or absent — M2 replaces that path for `call.request` only, so it was not
  worth repairing here — but the relay now says so on stderr, and `nim status`
  reconstructs what it can from the journal afterwards.

  **Not all of it.** Reporting is asynchronous and one-way, so a failure on the
  daemon's write path never reaches the shim and leaves nothing in the journal:
  it is indistinguishable from no event having been sent. The same is true of
  events lost at the end of a session that then closes normally. The claim this
  milestone can defend is *"the losses that leave evidence are reported"*, not
  *"all losses are accounted for"* — see `docs/journal-format.md`.
- **Batches and unparseable frames are counted.** A JSON-RPC batch slips past
  envelope parsing entirely, so a `tools/call` inside one reached a server with
  no record at all. In this milestone it still did, and left an `anomaly` entry
  saying so. M2 refuses both instead.
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

## M2 — Minimal enforcement

Nim can stop a `tools/call` from reaching a connector. That is the whole
milestone: one property, demonstrated, with everything it does not yet cover
written down beside it.

The relay now asks the daemon before it forwards a call and waits for the
answer. The daemon decides, writes the decision onto the `call.request` entry,
and only then replies. An allowed call goes on as the exact bytes that arrived;
a refused one never leaves Nim, and the client is answered with a JSON-RPC
response carrying `result.isError`.

**Fail-closed, and not for performance reasons.** No daemon, a slow daemon, a
closed socket, a reply that does not match the question — every one of them is a
denial. The synchronous hop, with the durable write included, costs p99 0.27 ms
for one relay and p99 1.6 ms with sixteen contending. See `docs/benchmarks.md`
for the method, the machine and the commands that reproduce it. So
there was never a performance argument for the alternative; the argument would
have had to be that a call Nim cannot record should proceed anyway, and there
isn't one.

An earlier version of this section quoted 0.157 ms, from a measurement that
left nothing behind to re-run. The benchmark reports about twice that here, and
there is no way to tell whether the original figure was wrong or the hardware
was different — which is why the number now comes with the command that
produces it.

**What M2 guarantees, in one direction only:**

> A `tools/call` that reached a connector is a call the journal recorded, with
> the decision that allowed it.

**The converse does not hold.** An `allow` in the journal does not mean the call
was made. The relay gives up after two seconds, SQLite's busy timeout is five, so
a pathologically contended write can land after the relay has already denied.
Such a call reads as `pending`, which is also what a call still running looks
like. The journal cannot tell them apart and does not pretend to. Closing that
window needs machinery this milestone does not buy.

**What M2 does not guarantee:**

- **A refusal Nim could not record is not in the journal.** When the daemon is
  unreachable the relay denies locally, and the only writer is exactly what
  could not be reached. Those denials are counted as lost events and reported on
  stderr; that is all there is. So `decision = deny` in the journal always means
  a policy refusal, never an inability to decide.
- **An invalid JSON frame may leave a client with no answer.** Nim will not relay
  a frame it cannot parse — Go rejects `NaN` where Python accepts it, which is
  enough to put an unseen `tools/call` in front of a server — and if no id can be
  recovered, nothing is fabricated to answer with. Deliberate: the alternative is
  a call reaching a connector unexamined.
- **A batch carrying a `tools/call` is refused whole.** No element is forwarded,
  no `call.request` is written and no sequence number is spent on it. Batch
  elements are not decided one by one. JSON-RPC batching was removed from MCP in
  2025-06-18, so this closes a bypass rather than dropping a feature.
- **Nothing is claimed about Linux or Windows.** The relay's lifetime behaviour
  was measured on darwin/arm64 only. The connector is still a child of the shim,
  so nothing in M2 depends on the answer; the inversion that would is a later
  milestone, and the spike belongs with it.
- **Anything that can write `config.toml` can empty the deny list.** See the
  standing decision above.

**Scope, stated as exclusions.** No canonicalization of arguments, no credential
handling, no agent identity, no policy model beyond exact tool names, no human
approval, no control plane, and no change to how the daemon is supervised.

## M2.5 — Daemon lifetime

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
