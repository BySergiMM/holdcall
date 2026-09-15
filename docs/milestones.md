# Milestones

Each one ends in something demonstrable. Nothing is built before the milestone
that needs it.

## Where the work lives

`m1-bootstrap` is the branch to build on. It adopted the merged tree
(`a10e626`) that `integration/trunk` had produced from the two lineages that
grew from M1 in parallel -- the append-only hash-chained journal, the console
and enforcement from one, credentials and daemon lifetime from the other --
and everything since (agent identity, the dashboard, policy in SQLite) landed
on it. `integration/trunk` is frozen history: it is where the merge was done,
and it is not developed on.

The numbering below is the merged one. `M2` means **enforcement**; daemon
lifetime, which the other lineage had called M2, is folded into M2.5. Anything
written before the merge that says otherwise is describing one half.

## Standing decisions

- **Daemon + shim.** One thin relay per configured MCP server, all state in a
  single local daemon. Tool names stay untouched; budgets, journal and approval
  have one writer.
- **SQLite is the source of truth** for configuration, sessions, grants and the
  journal. It is the only thing an authorization decision reads.
- **`config.toml` configures the daemon** — socket path, data directory, which
  servers to supervise.

  **Weakened from M2 to M4, deliberately and temporarily, and restored in M4.**
  M2 read a deny list from `config.toml` so that the enforcement path could be
  exercised end to end against something real, knowing that any process able
  to write the file could empty the list. M4 moved the rules into SQLite,
  behind the daemon, with every change an entry in the chain. A `config.toml`
  that still carries a `[policy]` section is refused rather than read past.
  Nothing an authorization decision depends on goes in the file again.
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
  saying so. M2 refuses an unparseable frame, and a batch that carries a
  `tools/call`; a batch carrying none is still relayed.
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
  standing decision above. Closed in M4: the rules live in SQLite and the
  file is refused if it still carries them.

**Scope, stated as exclusions.** No canonicalization of arguments, no credential
handling, no agent identity, no policy model beyond exact tool names, no human
approval, no control plane, and no change to how the daemon is supervised.

## M2.5 — Daemon lifetime and socket identity

**Status: done.** The daemon starts detached from the shim's process group and
session — `Setsid` on Linux and macOS, `CREATE_BREAKAWAY_FROM_JOB` +
`DETACHED_PROCESS` (with a fallback for job objects that forbid breakaway) on
Windows. Verified end to end on macOS: a `nim serve` run inside its own
session, with the whole session's process group killed, leaves the daemon
running.

This matters more under enforcement than it did under observation. A daemon
that dies with the client session takes enforcement with it, and every shim
still running then denies every call, because that is what an unreachable
daemon means. Detachment is what makes fail-closed a safety net rather than an
outage.

An `flock`-based startup lock serializes the stale-socket reclaim across
racing daemons — the ordinary case of a client spawning several shims at once
after a crash. Reproduced directly: without it, 8 daemons racing on one stale
socket produced 2-5 simultaneous winners; with it, always exactly 1. No
verified equivalent exists on Windows, which keeps only the weaker retry-based
mitigation.

**Socket identity, both ways.** Verification used to run in one direction: the
daemon checked its callers and nothing checked the daemon. Any local process
that bound the socket path first became the daemon for every relay started
afterwards — and the real daemon, finding the path bound and answering,
concluded another was running and exited, so the impostor did not even compete
with it. Reproduced live against the merged binary: as the daemon, a python
script nominated the command a credential is injected into and the relay
spawned it, allowed a call the deny list refuses, and collected the tool name
and argument digest of every call. The shim now verifies before writing a
single byte, so an impostor learns nothing, and a failed check is treated as
no daemon at all — which denies every call.

## M3 — Credentials

**Status: done, with two known limitations (Windows; PATH).** The daemon holds
the downstream servers' credentials and injects them at spawn time. They leave
the client's configuration file for the OS credential store (Keychain / DPAPI /
Secret Service), never SQLite, never argv.

**What authorizes a credential is the connector's registered command:**

```
echo "$GITHUB_TOKEN" | nim connector set github --env GITHUB_TOKEN \
    -- npx -y @modelcontextprotocol/server-github
```

The daemon hands that command back rather than accepting one, and the shim
spawns it. A command given on the command line is ignored when a connector
supplies one.

This corrects a claim an earlier version made. It said peer identity and
per-connection target-binding together closed off a downstream server reaching
another connector's secret. They did not — both passed
`nim serve --connector github -- /bin/sh -c 'echo $GITHUB_TOKEN'`, reproduced
live, which printed the real token. Peer identity asks whether the caller is
this binary, and anything on the machine can be by running it; target binding
asks whether a connection asked for a *second* connector, and this asks for
exactly one. Neither constrained what the answer would be injected into, and a
caller that chooses the process receiving a secret has the secret.

A connector registered before this existed has no authorized command. It fails
closed: the daemon releases nothing and the relay refuses to spawn, with a
message saying to register it again. It is not treated as unrestricted.

**PATH is the limitation this does not close.** The registered argv is spawned
through normal PATH resolution, so a caller that already controls PATH can put
its own binary in front of the registered name. Closing it needs the daemon to
spawn the downstream itself and hand the shim a pipe — process inversion,
which is also what would let a credential stop being handed to the connector
at all.

## M4 — Identity, and policy that lives in SQLite

**Status: done, 2026-09-14.** A decision is `(agent, connector, tool)`. The
acceptance test ran with real processes: two client programs, enrolled under
two names, each spawning a relay against one connector and sending the same
call, received different verdicts, and the journal distinguishes them on their
`session.start` entries (`TestTwoRealAgentsAgainstOneConnectorReceiveDifferentVerdicts`).

What landed:

- **Agent identity**, derived by the daemon from the kernel -- the socket
  peer's parent process and the file it executes -- matched against
  enrolments made with `nim agent add`. Nothing on the wire names an agent.
  Recorded on `session.start` at schema_version 2, and shown by `nim log`,
  `nim log --json` and the console.
- **Rules in SQLite.** `nim policy deny <tool> [--agent] [--connector]`, kept
  in `nim_rules`, read by the daemon at decision time. Rules only deny; a tool
  no rule names is allowed, as before. A rule may name only an enrolled agent.
- **Every change in the chain.** A rule and its `rule.add` or `rule.remove`
  entry are one SQLite transaction, so the rules have a history that verifies
  like the calls do. This needed the journal table to be rebuilt once on
  open -- SQLite cannot widen a CHECK constraint in place -- and the rebuild
  copies every row verbatim, which a test holds it to.
- **Policy out of `config.toml`.** The file configures the daemon and nothing
  else; a `[policy]` section, or any key this build does not know, is refused
  with the way forward in the message.
- **D-002 answered** for this model, in
  `docs/decisions/0002-what-an-unknown-agent-may-do.md`: a session no
  enrolment matched meets only the rules that name no agent. Because rules
  only deny, that is the ceiling every session had before agents existed, and
  nobody gains by avoiding enrolment.

What it does not do, stated rather than implied:

- **No allow rules.** "Only Claude Code may touch github" needs a rule that
  grants, a precedence between allow and deny, and a different answer to
  D-002. That is a policy language, and it is M4.5 with the spike in front.
- **Two windows of one program are the same agent.** The identity is the
  executable, not the instance.
- **Enrolment changes are not journaled** (F-018). Re-enrolling a name moves
  every rule scoped to it, and the chain does not say so. The rules are in the
  chain; the thing they are scoped to is not yet.
- **The decision costs one more read.** `docs/benchmarks.md` has the figures:
  p50 moved from 0.075 ms to 0.091 ms and p99 stayed where it was.

**Reordered, with reasons.** M4 was "Grants (Cedar)", starting with a spike on
`cedar-go`. That was the wrong next step, and the merged engine made it obvious
why: **a policy language had nothing to talk about yet.**

Every decision Nim made was per-tool and global. Two agents against the
same connector got the same answer, because there was no way to tell them apart
— the word "agent" appeared in this document and nowhere in the schema. Grants
(M4 as written), budgets (M5) and human approval (M6) all need a subject, and
none of them could be built until one existed. Choosing a policy language before
there is a subject to write policies about is picking the syntax before the
semantics.

Two things belonged here, in this order:

1. **Agent identity.** A decision becomes `(agent, connector, tool)` rather
   than `tool`. The journal records which agent, and the console shows it. The
   acceptance test is concrete: two agents against one connector receive
   different verdicts, and the record distinguishes them.

   The hard part is not the schema, it is what an identity *is* and how it is
   established — a client-supplied label is a claim, not an identity, exactly
   as `--connector` was before the command binding. Expect this to be most of
   the milestone.

2. **Policy in SQLite.** The deny list is scaffolding in `config.toml` and
   says so; anything able to write a file can empty it, and the change leaves
   no journal entry. Moving it restores the standing decision above, and every
   policy change becomes an entry in the chain — which is the property that
   makes an audit of the *rules* possible, not just of the calls.

Cedar is deferred, not rejected. It becomes a real question once there is a
subject, a resource and an action to express, and the spike should happen then
with those in hand.

## M4.5 — Allow rules, and the precedence between them and deny

**Status: done, 2026-09-15.** Not Cedar. The one-day spike on `cedar-go` that
preceded this milestone, and the real policy model available to evaluate it
against once M4 landed, together said the same thing docs/decisions/0003
argues in full: two rules and a precedence between them said everything this
milestone needed to say, and a grammar for conditions nothing here asks for
would have been syntax chosen before there was a semantics to serve.

What landed:

- **`effect` is `deny` or `allow`.** The column's CHECK constraint widened
  from `('deny')` to `('deny','allow')` the way `nim_journal`'s widened for
  `rule.add` in M4: SQLite cannot alter a CHECK in place, so an existing
  `nim_rules` is rebuilt once, on open, row for row --
  `rebuildRulesTable`, tested against a database seeded with the exact DDL
  M4 shipped
  (`TestANimRulesTableFromTheCurrentDDLIsWidenedForAllowRules`).
  `nim_rules` is not part of the hash chain, unlike `nim_journal`, so its
  rebuild needed no view to drop and nothing to re-verify beyond the rows
  themselves.
- **`tool` may be `*`, meaning every tool, only through `nim policy default
  deny|allow`.** Everywhere else -- `nim policy deny`, `allow`, the match at
  decision time -- it is refused as an ordinary tool name, so there remains
  exactly one way to write a rule that matches more than one tool. It is not
  a wildcard or a prefix. A client that genuinely calls a tool named `*` is
  not specially guarded against; docs/decisions/0003 says why that is
  honest rather than an oversight.
- **A stated precedence, in one function.** `journal.Decide`: higher
  specificity wins (exact tool over default, naming the agent or the
  connector over not naming it), deny beats allow at equal specificity, and
  no matching rule at all is left to the caller as the M4 baseline -- allow.
  Pure, with no database in it, and table-driven tested exhaustively
  (`TestDecideAppliesSpecificityThenDenyOverAllow`,
  `internal/journal/decide_test.go`) rather than only through the daemon.
- **D-002 answered again, for this model**, in docs/decisions/0003 and in
  0002's last section: `nim policy default deny` plus `nim policy allow
  <tool> --agent <name>` is the allow-list M4 could not express, and an
  unenrolled program is denied by the default because no rule naming no
  agent grants it anything. Proven at the answer level
  (`TestDefaultDenyDeniesAnUnknownAgentWhileAnAgentScopedAllowAdmitsAnEnrolledOne`)
  and end to end with real processes
  (`TestADefaultDenyClosesEverythingAndAnAgentScopedAllowReopensOneToolForOneAgent`),
  the second following the shape
  `TestTwoRealAgentsAgainstOneConnectorReceiveDifferentVerdicts` set for M4.
- **`nim policy explain <tool> [--agent] [--connector]`**, through a new
  `policy.explain` daemon request, names the rule that would decide a call
  shaped like that and why -- computed by calling the same `journal.Decide`
  the decision path uses, so it can never say something a real call would
  not do. The CLI still never reads `nim_rules` itself.
- **Every allow and default entry verifies like a deny always did.**
  `rule.add`/`rule.remove` carry the effect in `decision` and `*` in `tool`
  exactly as stored, and the chain checks out across them
  (`TestAllowAndDefaultRuleEntriesCarryTheEffectAndVerify`).
- **The decision cost the one query it already cost in M4.** `MatchingRules`
  replaces the single-row lookup with a small `select` -- at most eight
  candidate rows for any one call, bounded by the same unique index -- and
  `docs/benchmarks.md`'s method reproduces p50 at essentially the same
  0.09 ms M4 measured.

What it does not do, stated rather than implied:

- **No conditions on arguments.** A rule is still keyed on `(agent,
  connector, tool)` alone; nothing here reads `params.arguments`.
- **No time bounds and no budgets.** Both need a clock or a counter to name,
  which a precedence between two rules has no reason to grow on its own.
- **No human approval.** Still M6.
- **Enrolment changes are still not journaled (F-018).** This milestone did
  not touch it: a rule scoped to a name is only as trustworthy as the record
  of what that name pointed at, and that record still does not exist. It
  matters more now than it did in M4, because a name can grant as well as
  restrict -- re-enrolling `claude-code` against a different executable
  moves an allow rule's meaning with it, silently. Still open, still worth
  closing before this model is trusted for anything that matters.

**What would still need a language, if one of these is ever asked for:**
conditions on a call's arguments (a real policy grammar, not a
precedence between two effects), time-of-day or session-age bounds, and
budgets that decrement across calls rather than deciding each in isolation.
None of the three needed anything this milestone built; each would need its
own subject to talk about, the same argument M4's reordering made about
agent identity before a policy language had anything to say.

## M5 — Budgets

Per session, decremented at authorization time, not at execution time.

## M6 — Human approval

Out-of-band prompt showing real parameters, never a model-generated summary.

## M7 — Journal

**Delivered early, as M1.5.** Append-only and hash-chained, with the decision
recorded on the `call.request` entry. What remains under this heading is the
part M1.5 did not claim: the *inputs* that produced each decision, which only
becomes meaningful once there is a policy richer than a list of names to
record inputs for. Folded into M4.

## M8 — Dashboard

**Blocked on something more basic than itself.** Syncing a record whose
authenticity rests on an unkeyed chain exports a liability rather than
evidence: anything able to write `nim.db` can rewrite history and the mirror
would faithfully copy it. Either the journal gains a key the agent cannot
reach, or the dashboard has to present what it shows as "what this machine
reported", which is a much weaker claim than the sync contract below implies.
Decide that before building the viewer.


Next.js + Supabase. The engine pushes a copy of the journal; the cloud never
decides anything.

### Sync contract

| Never leaves the machine | Synced |
|---|---|
| Credentials and connector secrets | Agent, connector and tool names |
| Call parameters (digest only) | Decision, duration, cost, timestamp |
| Response contents | Journal link hash |
| The decision itself | Aggregate statistics |
