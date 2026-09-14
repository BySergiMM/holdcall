# Nim

A local process that sits between an MCP client (Claude Code, Cursor) and the
MCP servers it uses. It holds the credentials, decides whether each
`tools/call` is allowed, and records every decision in a hash-chained journal.

Nothing else in the protocol is touched: everything that is not a `tools/call`
is forwarded byte for byte.

## Status

Pre-alpha, and honest about it. What works:

- **A relay a real client cannot distinguish from a direct connection.** Tool
  list, JSON schemas, unicode, a 512 KiB payload, error propagation and ping
  all pass through unchanged. The harness is `tools/relay-rig`, and it was
  last run before enforcement and credentials existed (F-007 on the dashboard);
  byte-exactness since then rests on the unit tests.
- **Refusal.** A `tools/call` is decided before it is forwarded, and the
  decision is written to the journal before the relay acts on it. A refused
  call never leaves Nim.
- **An append-only, hash-chained record.** `nim log` shows what happened,
  `nim verify` walks the chain, `nim console` serves a read-only local view.
- **Credentials out of the client's config file**, into the OS credential
  store, injected only into the one server each is registered for.

- **Agent identity, derived rather than declared.** An enrolled client program
  is identified by the executable it runs, as the kernel reports it for the
  process that spawned the relay; nothing on the wire names an agent.
- **Policy in SQLite, per agent and per connector.** `nim policy deny` refuses
  a tool for every session, or for one enrolled agent, or on one connector.
  Rules only deny. Every change is an entry in the journal, and `config.toml`
  carries no policy at all — a file that still does is refused.

What does not exist yet: allow rules and any precedence between allow and deny
(a policy language, M4.5), budgets, human approval, and the hosted journal
viewer (M8). `docs/milestones.md` is the order those come in.

## Failing closed

Every way of not getting a decision is a denial: no daemon, a slow daemon, a
closed socket, an answer to a different question, a journal write that failed.
There is no path on which a `tools/call` reaches a server without a decision
behind it.

The one thing that is deliberately *not* a denial is the absence of
configuration. A connector nobody set up is not a failure, and those servers
keep working untouched. `docs/decisions/0001-failure-behaviour.md` sets out
why those are different questions.

## Policy

A rule refuses one tool. It applies to every session, or to the sessions of
one enrolled agent, or to the sessions started for one connector:

```bash
nim policy deny delete_repository                       # for everyone
nim policy deny delete_repository --agent cursor        # for one client program
nim policy deny force_push --connector github           # on one server
nim policy list
```

An agent is a client program, enrolled by the executable it runs:

```bash
nim agent add cursor /Applications/Cursor.app/Contents/MacOS/Cursor
```

The daemon derives which agent a session belongs to from the process that
spawned the relay; the relay never says. A session no enrolment matched meets
only the rules that name no agent, which — because rules only deny — is the
same ceiling every session had before agents existed.
`docs/decisions/0002-what-an-unknown-agent-may-do.md` is the argument.

Every rule added or removed is a `rule.add` or `rule.remove` entry in the
chain, written in the same transaction as the rule, so the rules have a history
that verifies like the calls do.

## Credentials

A connector names the one downstream server its secret may be injected into.
That binding is what authorizes the credential, so it is required:

```bash
echo "$GITHUB_TOKEN" | nim connector set github --env GITHUB_TOKEN \
    -- npx -y @modelcontextprotocol/server-github

nim serve --connector github        # runs the registered server, with the token
```

The secret is never a command-line argument and never reaches SQLite. A command
given on the command line is ignored when a connector supplies one: the daemon
decides what receives a credential, not the caller.

Both ends of the daemon socket verify each other by peer identity, so neither
a process pretending to be a shim nor one pretending to be the daemon gets in.

## What the record is, and is not

The journal is append-only and hash-chained, and `docs/journal-format.md` is
the normative definition of the encoding, the chain, and — importantly — what
the chain does not protect against.

The short version: it detects corruption, partial writes and edits made by
anything that does not know the chain exists. It does **not** detect a local
attacker who can write to `nim.db`, because nothing in the construction is
secret and the whole chain can be recomputed. Only a head recorded elsewhere
(`nim verify --expect-head`) covers that.

Do not describe it as tamper-proof, tamper-evident, or an immutable audit log.

## Shape

```
engine/      Go. The daemon, the shim, the journal and the console.
dashboard/   A static page of what is true about this repository, built from it.
docs/        Design, security posture, decisions, and the journal's format.
supabase/    The schema a journal mirror would land in. Nothing writes to it.
tools/       The relay rig: a real MCP client, run direct and through Nim.
```

The engine keeps working with no network. `dashboard/` is deployed publicly
(D-003) and shows no runtime state; the hosted journal viewer is M8 and does
not exist.

## Building

```bash
cd engine && go build -o bin/nim ./cmd/nim
```

Go 1.25 or newer. No C toolchain: the SQLite driver is pure Go, so the engine
cross-compiles for darwin, linux and windows with `CGO_ENABLED=0`.

Everything has been run on darwin/arm64. Linux is exercised in CI; **no code
in this project has ever been run on Windows**, only cross-compiled, and two
of its security properties are known not to hold there — see the *Known gaps*
table in `docs/security.md`.

## Performance

`docs/benchmarks.md`, with the commands that reproduce every figure. The
decision a call waits for costs p99 0.27 ms including the durable write.

## Where the project actually stands

`dashboard/` builds a page listing every guarantee alongside what it does *not*
guarantee, every attack anyone has tried against Nim including the ones that
worked, and every known weakness. It is generated from this repository and
refuses to build if a claim cites a test that does not exist — which is how it
avoids becoming another document that drifts from the code. `docs/dashboard.md`
explains the design and, more usefully, what it deliberately cannot show.

    cd dashboard && npm install && npm run check && npm run dev

It shows no runtime state and never will: the journal, sessions, enrolled
agents and rules live on the machine running Nim. `nim console` serves the
journal and its sessions over loopback; `nim agent list` and `nim policy list`
show the rest.

## Licence

MIT.
