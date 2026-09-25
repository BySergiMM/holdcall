# Architecture

How Nim is put together, layer by layer, and what one tool call goes
through. `README.md` says what Nim guarantees; `docs/security.md` says what
it defends against and what it does not; this file says where each of those
lives in the code. Sizes on 2026-09-16: about 13,600 lines of Go outside
tests and 16,400 in tests, in ten packages under `engine/internal`.

## The shape in one picture

```
 MCP client (Claude Desktop, Claude Code, Cursor)
      │  stdio, JSON-RPC frames                 
      ▼
 nim serve  ── the relay ──────────────────────────► downstream MCP server
  (internal/shim)      forwards every byte unchanged      (the "connector")
      │                except a tools/call it refuses
      │  unix socket, one connection per relay run
      ▼
 nim daemon  (internal/daemon)      one process per user, the only writer
   ├─ peer identity: is the caller this same binary?      (internal/peer)
   ├─ agent identity: which enrolled program spawned it?  (internal/peer)
   ├─ decision: rules, budgets, ask                       (internal/journal)
   ├─ credentials: OS store, bound to a connector's argv  (internal/credential)
   ├─ approvals: calls held for nim approve, memory only  (approval.go)
   └─ journal: SQLite, append-only, hash-chained           (internal/journal)
                     │
       nim status / log / verify / console / policy / agent / connector
                     │                       (cmd/nim, internal/readmodel)
       dashboard/    a static page about the repository, not about runtime
```

Nothing hosted decides anything. The engine works with no network.

## Layer 1: the relay (`internal/shim`, `internal/mcp`)

A client is pointed at `nim serve --connector <name> -- <command>` instead
of at the MCP server directly (`nim init` rewrites the client's config to
do that, `internal/clientconfig`). The relay spawns the server with the
command the daemon registered for that connector, or the one given, and
pumps stdio both ways.

Every frame is parsed by a strict reader (`internal/mcp`): keys are matched
by their exact bytes, an object naming a key twice is refused, and a
`tools/call` whose tool name cannot be read as exactly one string is refused
before anyone is asked. Everything that is not a `tools/call` goes through as
the bytes that arrived; the relay is invisible to a client by test
(`tools/relay-rig`, a real client run direct and through Nim).

A `tools/call` is the exception. The relay asks the daemon over its socket
(`call.request`, with the tool name and a digest of the arguments, never the
arguments) and waits up to two seconds. Only an explicit `allow`, `approved`
or `pending` from the daemon lets the original bytes continue; every other
outcome (deny, no daemon, a slow daemon, a closed socket, an answer to a
different question, a journal write that failed) is a denial, answered to
the client as a tool error in the dialect the session negotiated
(`internal/mcp/deny.go`; both the `initialize` and the `server/discover`
handshake are watched). A refused call never leaves Nim.

The relay also reports `session.start`, `session.end`, `call.outcome` (did
the server answer, how long it took) and `anomaly` (a frame it could not
account for). It verifies that what answered on the socket is Nim before it
sends a byte, and refuses to take decisions from anything else.

## Layer 2: the daemon (`internal/daemon`)

One per user, started on demand by the first relay and detached from any
terminal. It listens on a unix socket whose path is derived from the Nim
home (`~/Library/Application Support/nim` on macOS, `$XDG_RUNTIME_DIR` or
the temp dir elsewhere). Two things happen at accept, before a byte is read:

- **Peer identity** (`internal/peer`): the kernel is asked which executable
  the connecting process is running (dev/ino of its main image on macOS via
  `proc_info`, `/proc/<pid>/exe` on Linux; unsupported on Windows). Anything
  that is not this same binary is refused. An older build of Nim at the same
  path is refused too, but named as such, and `nim daemon restart` is the
  remedy.
- **Agent identity**: the relay's parent process is resolved the same way
  and matched against the enrolments in `nim_agents` (`nim agent add <name>
  <path>`). The result is bound to the session at `session.start` and never
  re-derived. Nothing on the wire names an agent; a client cannot claim one.

A connection commits to one purpose on its first message (events from a
relay, credentials, connectors, agents, policy, approval) and cannot change
it, so a relay's connection can never ask for a credential or approve a
call. A connection may only report on sessions it opened.

The daemon is the only process that touches SQLite. It answers exactly one
kind of report, `call.request`, and only after writing the decision.

## Layer 3: the decision (`internal/journal/rules.go`, `budgets.go`, `daemon/approval.go`)

Policy is data in SQLite, not a language and not `config.toml` (a file that
still carries policy is refused).

- **Rules** (`nim_rules`): effect `deny`, `allow` or `ask`, scoped to a tool
  or `*`, optionally one agent and one connector. `journal.Decide` picks the
  most specific matching rule; at equal specificity deny beats ask beats
  allow; no matching rule is allow. `nim policy explain` runs the same
  function. `docs/decisions/0002` and `0003`.
- **Budgets** (`nim_budgets`): a cap on allowed calls per session, scoped like
  a rule, consulted only once the rules allow. A budget narrows, never
  grants; it counts decisions, not outcomes. `docs/decisions/0004`.
- **Ask** (M6): the daemon answers `pending`, keeps the call in memory with
  the real arguments the relay then sends once, and a human decides with
  `nim approve <id>` or `nim reject <id>` after seeing those arguments, every
  byte shown as itself. The decision is journaled before the relay is told;
  a session ending, the connection dropping or the timer
  (`[daemon] approval_timeout`, default 2m) all reject. An approve is refused
  until the arguments have arrived. `docs/decisions/0005`.

Every change to rules, budgets or enrolments is one SQLite transaction with
its own journal entry (`rule.add`, `budget.remove`, `agent.add`, ...), so
policy has the same verifiable history as calls.

## Layer 4: the journal (`internal/journal`)

`nim_journal` is append-only (triggers refuse update and delete) and
hash-chained: each entry's hash covers a canonical encoding of its fields
plus the previous hash, from a genesis derived from the machine id.
`docs/journal-format.md` is normative and versioned (schema 1 to 4); a second
implementation can reproduce the bytes from it alone, and the tests do.
Kinds: `session.start`, `call.request`, `call.outcome`, `session.end`,
`anomaly`, plus the policy kinds. What is never in it: credentials, call
arguments (a digest only), response bodies.

`nim verify` walks the chain; `--expect-head` compares it with a head you
recorded elsewhere, which is the one check that covers rewriting, because
the chain is unkeyed (F-003, accepted; `docs/decisions/0006`). Tables an
older build created are rebuilt on open with every row copied verbatim.

`internal/readmodel` is the one projection every reader uses: `nim status`,
`nim log`, the console and the sessions view all draw their meaning from it,
so a count cannot mean one thing in the terminal and another in a browser.

## Layer 5: credentials (`internal/credential`)

`nim connector set <name> --env KEY -- <command>` reads a secret from stdin
and stores it in the OS store: the macOS Keychain through `security`, the
Secret Service through `secret-tool` on Linux, DPAPI on Windows. The daemon
records which env var name and which argv may receive it. A relay asks for
the credential at spawn time over its own short-lived connection; the daemon
decides what gets spawned, not the caller, and injects the value into the
connector's environment only. The value is never in argv, never in SQLite,
never in the console.

## Layer 6: the operator's surfaces (`cmd/nim`, `internal/console`)

- `nim init` and `nim doctor`: point a client's config at Nim (backups,
  atomic writes, env values never printed) and check the result.
- `nim status`, `nim log`, `nim verify`: the journal from the terminal.
- `nim policy`, `nim agent`, `nim connector`, `nim approve`, `nim reject`,
  `nim daemon restart`: policy and lifecycle, all through the daemon's
  verified socket.
- `nim console`: a read-only page on loopback only (`Host` must be a
  loopback name), with `/api/snapshot`, `/api/events`, `/api/sessions/<id>`,
  `/api/policy`, `/api/explain?tool=&agent=&connector=` (the daemon's own
  `journal.Decide`, applied to the rules as they stand) and `/api/pending`
  (what the daemon holds for approval, read over the verified socket; it
  says so when the daemon cannot be asked). Views are URL fragments, and
  `#journal/<chain_seq>` or `#sessions/<id>` open one entry or session.
  There is no write route: approving stays on the CLI.

## Layer 7: the repository's own claims (`dashboard/`, `.github/`)

`dashboard/` is a static page about this repository, built from
`data/state.json`. Its generator refuses to build a claim without evidence:
every cited test must exist in the tree, a milestone cannot be done while
listing pending work, a decision cannot be resolved without a date, no
secret or local path may appear in prose, and the published output is
scanned byte by byte. Deployed publicly by decision (D-003) with `noindex`
(D-005), by hand, never on push.

CI (`.github/workflows/ci.yml`) runs the suite with `-race -shuffle -v` on
three platforms, an evidence script that refuses a cited test that skipped,
cross-compiles, govulncheck and the relay rig; `tools/ci-local.sh` runs the
same jobs on a laptop. A release is gated on a human pushing a tag, and
`install.sh` verifies checksums.

## One call, end to end

1. The client writes a `tools/call` frame to the relay's stdin.
2. The relay's strict reader extracts one tool name and a digest of the
   arguments, assigns the call the next sequence number in the session, and
   sends `call.request` to the daemon.
3. The daemon looks up the rules for (agent, connector, tool). Deny: writes
   `call.request` with `deny`, answers deny. Allow: checks matching budgets,
   writes `allow` (or `deny` if a cap is reached), answers. Ask: holds the
   call, answers `pending`; the relay sends the arguments once and waits; a
   human decides; the daemon writes `approved` or `rejected`, then answers.
4. On allow or approved the relay forwards the original bytes untouched. On
   anything else it answers the client itself with a tool error whose text
   says which kind of refusal it was, and the server never sees the call.
5. The server's response goes back byte for byte; the relay reports
   `call.outcome` with success and duration. On `session.end` the negotiated
   protocol version is recorded.

Cost on this machine (`docs/benchmarks.md`): about 0.1 ms per decision, 0.3
ms with a budget configured, against a 2 s timeout.

## What is outside

Not built: conditions on a call's arguments or on time, notifications for
held calls, a hosted mirror (M8, planned as "what this machine reported"), a
second OS principal for the daemon (which is what F-006 and a keyed journal
both need). Never executed: anything on Windows, and Linux only in CI, which
is what `docs/security.md`'s gaps table and the dashboard's platform columns
say.
